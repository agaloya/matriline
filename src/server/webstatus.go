package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/i18n"
)

// The web page's Status and Live tabs, drawn as HTML from 'status --json' and 'live --json'
// (user: not a copy of the terminal; the same information, presented for a browser).

type statView struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Lost    int `json:"lost"`
	Results struct {
		Output        int `json:"output"`
		Weird         int `json:"weird"`
		Errors        int `json:"errors"`
		Outdated      int `json:"outdated"`
		OtherVersions int `json:"other_versions"`
	} `json:"results"`
	Inputs struct {
		Completed int `json:"completed"`
		Paused    int `json:"paused"`
		Cancelled int `json:"cancelled"`
	} `json:"inputs"`
	Clients []struct {
		Name          string `json:"name"`
		Slots         int    `json:"slots"`
		Running       int    `json:"running"`
		Accepting     bool   `json:"accepting"`
		LastHeartbeat string `json:"last_heartbeat"`
		Power         string `json:"power"`
		Seen          string `json:"-"` // "12s ago"
		Bar           int    `json:"-"` // % of its slots in use
	} `json:"clients"`
	Projects []projView `json:"projects"`

	// computed for the page
	Waiting, Total, Done, Pct, Slots, Busy int
	All                                    projView
}

type projView struct {
	Name          string `json:"name"`
	Queued        int    `json:"queued"`
	Running       int    `json:"running"`
	Output        int    `json:"output"`
	Weird         int    `json:"weird"`
	Errors        int    `json:"errors"`
	Paused        int    `json:"paused"`
	Cancelled     int    `json:"cancelled"`
	OtherVersions int    `json:"other_versions"`
	Chart         webChart
	Bar           []barSeg
	Total, Pct    int
}

type barSeg struct {
	Name, Color string
	N           int
	X, W        float64 // start and width in % (SVG: the page allows no inline styles)
}

// finish computes a project's total, its share done, its pie and its progress bar.
func (p *projView) finish() {
	vals := map[string]int{"queued": p.Queued, "running": p.Running, "output": p.Output, "weird": p.Weird,
		"errors": p.Errors, "other-versions": p.OtherVersions, "paused": p.Paused, "cancelled": p.Cancelled}
	p.Chart = makeChart(p.Name, vals)
	p.Total = p.Chart.Total
	if p.Total > 0 {
		p.Pct = 100 * (p.Output + p.OtherVersions) / p.Total
	}
	p.Bar = nil
	x := 0.0
	for _, k := range chartOrder {
		if vals[k] > 0 && p.Total > 0 {
			w := 100 * float64(vals[k]) / float64(p.Total)
			p.Bar = append(p.Bar, barSeg{Name: k, Color: chartColors[k], N: vals[k], X: x, W: w})
			x += w
		}
	}
}

func parseStatus(js string) (*statView, error) {
	v := &statView{}
	if err := json.Unmarshal([]byte(js), v); err != nil {
		return nil, err
	}
	v.Waiting = max(v.Queued-v.Running, 0)
	v.All = projView{Name: "all", Queued: v.Waiting, Running: v.Running, Output: v.Results.Output, Weird: v.Results.Weird,
		Errors: v.Results.Errors, Paused: v.Inputs.Paused, Cancelled: v.Inputs.Cancelled, OtherVersions: v.Results.OtherVersions}
	v.All.finish()
	v.Total, v.Done, v.Pct = v.All.Total, v.Results.Output, v.All.Pct
	for i := range v.Projects {
		v.Projects[i].finish()
	}
	if len(v.Projects) == 1 { // one folder: it is the whole project, shown once
		v.Projects = nil
	}
	for i := range v.Clients {
		c := &v.Clients[i]
		v.Slots += c.Slots
		v.Busy += c.Running
		if c.Slots > 0 {
			c.Bar = min(100, 100*c.Running/c.Slots)
		}
		c.Seen = "-"
		if t, err := time.Parse(time.RFC3339, c.LastHeartbeat); err == nil {
			c.Seen = time.Since(t).Round(time.Second).String()
		}
		c.Power = strings.TrimSpace(c.Power)
		if c.Power == "" {
			c.Power = "-"
		}
	}
	return v, nil
}

type liveView struct {
	Rows []struct {
		N           int    `json:"n"`
		Client      string `json:"client"`
		Task        string `json:"task"`
		Kind        string `json:"kind"`
		Phase       string `json:"phase"`
		Seconds     int    `json:"seconds"`
		OutputBytes int64  `json:"output_bytes"`
		Tail        string `json:"tail"`
		Time        string `json:"-"`
		Size        string `json:"-"`
	} `json:"running"`
	Phases map[string]int // how many starting, computing, uploading
}

func parseLive(js string) (*liveView, error) {
	v := &liveView{Phases: map[string]int{}}
	if err := json.Unmarshal([]byte(js), v); err != nil {
		return nil, err
	}
	for i := range v.Rows {
		r := &v.Rows[i]
		r.Time = (time.Duration(r.Seconds) * time.Second).String()
		r.Size = humanBytes(r.OutputBytes)
		v.Phases[r.Phase]++
	}
	return v, nil
}

// the words the pages translate from data (state and phase names)
var _ = []string{i18n.N("queued"), i18n.N("running"), i18n.N("output"), i18n.N("weird"), i18n.N("errors"),
	i18n.N("other-versions"), i18n.N("paused"), i18n.N("cancelled"), i18n.N("starting"), i18n.N("computing"),
	i18n.N("uploading"), i18n.N("lost"), i18n.N("job")}
