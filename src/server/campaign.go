package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/ledger"
)

// The campaign's ORCA version is set by the admin (orca.version) before the first inputs.
// state/campaign.json remembers it: a later change is allowed (a new ORCA release) but
// warned about loudly, because output/ then holds results of two versions that should not
// be mixed in one analysis.

type campaignState struct {
	OrcaVersion string    `json:"orca_version"`
	Since       time.Time `json:"since"`
	History     []string  `json:"history,omitempty"` // "6.1.0 until 2026-10-05T10:00:00Z (123 results)"
}

// noteOrcaVersion records the version in force and returns a warning when it changed.
func (s *Server) noteOrcaVersion() string {
	cfg := s.conf()
	p := filepath.Join(cfg.Root, dState, "campaign.json")
	var st campaignState
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &st)
	}
	if st.OrcaVersion == cfg.OrcaVersion {
		return ""
	}
	var warn string
	if st.OrcaVersion != "" {
		n := s.spoolCounts("")[dOutput]
		f := i18n.N("WARNING: the campaign's ORCA version changed from %s (since %s) to %s: %d results in output/ were computed with %s; new results use %s. Do not mix them in one analysis (move or rename the old outputs, or set orca.version back).")
		a := []any{st.OrcaVersion, st.Since.Format("2006-01-02"), cfg.OrcaVersion, n, st.OrcaVersion, cfg.OrcaVersion}
		warn = fmt.Sprintf(f, a...)
		st.History = append(st.History, fmt.Sprintf("%s until %s (%d results)", st.OrcaVersion, time.Now().UTC().Format(time.RFC3339), n))
		s.log.Warnf("%s", warn)
		s.alertf("orca_version", f, a...)
		s.ledger.Append(ledger.Entry{Kind: "config", Note: fmt.Sprintf("campaign ORCA version %s -> %s", st.OrcaVersion, cfg.OrcaVersion)})
	}
	st.OrcaVersion, st.Since = cfg.OrcaVersion, time.Now().UTC()
	if b, err := json.MarshalIndent(st, "", " "); err == nil {
		ident.WriteFileAtomic(p, b, 0o600)
	}
	return warn
}
