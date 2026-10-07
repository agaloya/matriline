package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The 3D view of a running job's molecule (user): 3Dmol.js (BSD-3-Clause, web3d/), served
// by this page itself, so nothing is fetched from elsewhere and no molecule leaves the
// computer. The only page with scripts: its own files, no inline code.

//go:embed web3d/3Dmol-min.js
var lib3Dmol []byte

//go:embed web3d/LICENSE-3Dmol.txt
var license3Dmol []byte

const viewerJS = `(function(){
var el=document.getElementById('mol3d'); if(!el||!window.$3Dmol)return;
var v=$3Dmol.createViewer(el,{backgroundColor:el.getAttribute('data-bg')||'white'});
v.addModel(el.getAttribute('data-xyz'),'xyz');
v.setStyle({},{stick:{radius:0.15},sphere:{scale:0.28}});
v.zoomTo();v.render();
var t=parseFloat(el.getAttribute('data-rotate'));
if(t>0){var step=50;setInterval(function(){v.rotate(360*step/(t*1000),'y');v.render();},step);}
})();`

// liveJob is one running job in the 3D page's list.
type liveJob struct {
	N            int
	Client, Task string
}

// molXYZ is the geometry of live job n's input as an XYZ file (admin command "molxyz").
func (s *Server) molXYZ(a []string) (string, error) {
	n := 1
	if len(a) > 0 {
		n, _ = strconv.Atoi(a[0])
	}
	rows := s.liveRows()
	if n < 1 || n > len(rows) {
		return "", fmt.Errorf("no running job %d", n)
	}
	in, err := s.taskInput(rows[n-1].task)
	if err != nil {
		return "", err
	}
	atoms := parseXYZ(string(in))
	if len(atoms) == 0 {
		return "", fmt.Errorf("the input of %s has no * xyz block", rows[n-1].label)
	}
	faceOn(atoms)
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n%s\n", len(atoms), rows[n-1].label)
	for _, at := range atoms {
		fmt.Fprintf(&b, "%s %.6f %.6f %.6f\n", at.el, at.x, at.y, at.z)
	}
	return b.String(), nil
}

func (w *webUI) molPage(rw http.ResponseWriter, r *http.Request) {
	if !offered("live") {
		http.NotFound(rw, r)
		return
	}
	p := w.page("Live 3D")
	p.Tab = "Live"
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	p.LiveSel = max(n, 1)
	p.MolRotate, p.MolBg = w.molRotate, w.molBg
	if out, err := call(w.cfgPath, []string{"live", "--json"}); err == nil {
		var lv struct {
			Running []struct {
				N            int
				Client, Task string
			}
		}
		if json.Unmarshal([]byte(out), &lv) == nil {
			for _, j := range lv.Running {
				p.Jobs = append(p.Jobs, liveJob{j.N, j.Client, j.Task})
			}
		}
	}
	xyz, err := call(w.cfgPath, []string{"molxyz", strconv.Itoa(p.LiveSel)})
	if err != nil {
		p.Err = err.Error()
	} else {
		p.XYZ = xyz
		p.XYZName = strings.SplitN(xyz, "\n", 3)[1]
	}
	// this page only: its own scripts (3Dmol.js and the few lines that start it)
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; form-action 'self'; frame-ancestors 'none'")
	w.render(rw, p)
}

func serveJS(body []byte) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		rw.Header().Set("Cache-Control", "private, max-age=86400")
		rw.Write(body)
	}
}

// faceOn turns a molecule so that it faces the viewer: its two largest principal axes along
// x and y, the smallest (a flat molecule's normal) along z, the viewing direction. Seen
// edge-on, a flat molecule such as melatonin was a line of overlapping atoms.
func faceOn(atoms []atom) {
	if len(atoms) < 3 {
		return
	}
	var c [3]float64
	for _, a := range atoms {
		c[0], c[1], c[2] = c[0]+a.x, c[1]+a.y, c[2]+a.z
	}
	n := float64(len(atoms))
	var cov [3][3]float64
	for _, a := range atoms {
		p := [3]float64{a.x - c[0]/n, a.y - c[1]/n, a.z - c[2]/n}
		for r := 0; r < 3; r++ {
			for k := 0; k < 3; k++ {
				cov[r][k] += p[r] * p[k]
			}
		}
	}
	vals, vecs := jacobi3(cov)
	idx := []int{0, 1, 2}
	sort.Slice(idx, func(i, k int) bool { return vals[idx[i]] > vals[idx[k]] })
	for i, a := range atoms {
		p := [3]float64{a.x - c[0]/n, a.y - c[1]/n, a.z - c[2]/n}
		var q [3]float64
		for d := 0; d < 3; d++ {
			v := vecs[idx[d]]
			q[d] = p[0]*v[0] + p[1]*v[1] + p[2]*v[2]
		}
		atoms[i].x, atoms[i].y, atoms[i].z = q[0], q[1], q[2]
	}
}
