// Package orca knows the ORCA-specific details: parsing output files, checking and
// normalizing inputs, and fingerprinting installations.
//
// Output markers were taken from real ORCA 6.1.0/6.1.1 outputs produced in this project and
// cross-checked against the parsers of the ORCA Python Interface (OPI,
// https://github.com/faccts/opi, src/opi/output/), which reads the same banners
// ("ORCA TERMINATED NORMALLY", "FINAL SINGLE POINT ENERGY"). No OPI code is copied.
package orca

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// OutputInfo is what Matriline extracts from an ORCA main output file.
type OutputInfo struct {
	Version        string    // "6.1.1"
	Git            string    // "487d211c"
	Terminated     bool      // ****ORCA TERMINATED NORMALLY****
	ErrorTerm      bool      // error termination detected
	ErrorText      string    // first error line
	MaxcoreShortMB float64   // "Please increase MaxCore - by at least (X MB)": the job needed more memory
	MaxcoreAsked   bool      // any "increase MaxCore" request, with or without an amount
	MemNeededMB    float64   // "MemNeeded X MB > MemAvailable Y MB" (DLPNO triples): X
	MemAvailMB     float64   // ... and Y
	InputName      string    // NAME = x.inp
	EchoedInput    []string  // input lines as echoed by ORCA (right-trimmed)
	EchoGap        bool      // the echoed line numbers are not 1, 2, 3, ... (edited output)
	FinalEnergies  []float64 // every FINAL SINGLE POINT ENERGY (one per SP/opt step/job)
	Modules        []string  // module banners in order of appearance (deduplicated runs)
	SCFConverged   int       // count of "SCF CONVERGED AFTER"
	SCFFailed      int       // count of "SCF NOT CONVERGED"
	OptConverged   bool      // THE OPTIMIZATION HAS CONVERGED
	OptSteps       int       // GEOMETRY OPTIMIZATION CYCLE count
	HasFreq        bool      // VIBRATIONAL FREQUENCIES section
	ImagFreqs      int       // negative frequencies ("***imaginary mode***")
	Jobs           int       // number of jobs ($new_job + 1), from "JOB NUMBER" banners
	TotalRunSec    float64   // TOTAL RUN TIME
	HostName       string    // "* Host name:"
	StartTimeText  string    // "* Starting time:"
	Warnings       int       // lines starting with "WARNING"
}

// reMaxcoreShort reads e.g. "Please increase MaxCore - by at least (  100.5 MB)" (ORCA 6 MDCI).
var reMaxcoreShort = regexp.MustCompile(`(?i)increase MaxCore.*?\(\s*([0-9.]+)\s*MB`)

// reMemNeeded reads "MemNeeded  2974.2 MB > MemAvailable  1096.9 MB", the line after
// "not enough memory for Triples evaluation" (ORCA 6 DLPNO open shell).
var reMemNeeded = regexp.MustCompile(`MemNeeded\s+([0-9.]+)\s*MB\s*>\s*MemAvailable\s+([0-9.]+)\s*MB`)

var (
	reVersion = regexp.MustCompile(`Program Version\s+(\S+)\s+-\s+(\S+)`)
	reGit     = regexp.MustCompile(`\(GIT:\s*\$([0-9a-fA-F]+)\$\)`)
	reEcho    = regexp.MustCompile(`^\|\s*(\d+)>\s?(.*)$`)
	reEnergy  = regexp.MustCompile(`FINAL SINGLE POINT ENERGY\s+(-?\d+\.\d+)`)
	reModule  = regexp.MustCompile(`^\s{4,}(ORCA [A-Z0-9][A-Z0-9 ,/()\-]+?)\s*$`)
	reRunTime = regexp.MustCompile(`TOTAL RUN TIME:\s+(\d+) days (\d+) hours (\d+) minutes (\d+) seconds (\d+) msec`)
)

// ParseOutputFile parses an ORCA output file.
func ParseOutputFile(path string) (*OutputInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseOutput(f)
}

// ParseOutput parses an ORCA output stream.
func ParseOutput(r io.Reader) (*OutputInfo, error) {
	info := &OutputInfo{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 256<<10), 16<<20)
	inEcho := false
	lastModule := ""
	inputErr := 0 // lines still to take after "INPUT ERROR" (what is wrong, and with what)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		switch {
		case inEcho:
			if strings.Contains(t, "****END OF INPUT****") {
				inEcho = false
				continue
			}
			if m := reEcho.FindStringSubmatch(line); m != nil {
				// ORCA numbers the echoed lines 1, 2, 3...: a gap means a line was removed
				// from the output by hand (seen in the lab's "loose" attack)
				if n, _ := strconv.Atoi(m[1]); n != len(info.EchoedInput)+1 {
					info.EchoGap = true
				}
				info.EchoedInput = append(info.EchoedInput, strings.TrimRight(m[2], " \t\r"))
			}
			continue
		case t == "INPUT FILE":
			inEcho = info.EchoedInput == nil // only the first job's echo block
			continue
		case strings.HasPrefix(t, "NAME = ") && info.InputName == "":
			info.InputName = strings.TrimPrefix(t, "NAME = ")
		}
		if info.Version == "" {
			if m := reVersion.FindStringSubmatch(line); m != nil {
				info.Version = m[1]
			}
		}
		if info.Git == "" {
			if m := reGit.FindStringSubmatch(line); m != nil {
				info.Git = strings.ToLower(m[1])
			}
		}
		if m := reEnergy.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				info.FinalEnergies = append(info.FinalEnergies, v)
			}
		}
		if m := reModule.FindStringSubmatch(line); m != nil && m[1] != lastModule {
			info.Modules = append(info.Modules, m[1])
			lastModule = m[1]
		}
		if inputErr > 0 && t != "" && !strings.HasPrefix(t, "!!!") && !strings.HasPrefix(t, "[file ") {
			info.ErrorText += map[bool]string{true: ": ", false: " "}[inputErr == 2] + t
			inputErr--
		}
		switch {
		case t == "INPUT ERROR" && !info.ErrorTerm:
			// e.g. "UNRECOGNIZED OR DUPLICATED KEYWORD(S) IN SIMPLE INPUT LINE" and the
			// keyword on the next line; ORCA prints no other error line for it
			info.ErrorText, info.ErrorTerm, inputErr = t, true, 2
		case strings.HasPrefix(t, "ERROR: ") && !info.ErrorTerm:
			// the input parser: "ERROR: expect a '$', '!', '%', '*' or '[' in the input"
			// and, next line, where ("Line 4 of x.inp (C)"); ORCA exits with 126 then
			info.ErrorText, info.ErrorTerm, inputErr = t, true, 1
		case strings.Contains(t, "****ORCA TERMINATED NORMALLY****"):
			info.Terminated = true
		case strings.Contains(t, "ORCA finished by error termination"),
			strings.Contains(t, "aborting the run"),
			strings.HasPrefix(t, "ERROR") && strings.Contains(t, "!!!"):
			if !info.ErrorTerm {
				info.ErrorText = t
			}
			info.ErrorTerm = true
		case strings.Contains(t, "increase MaxCore") || strings.Contains(t, "increase maxcore"):
			info.MaxcoreAsked = true
			if m := reMaxcoreShort.FindStringSubmatch(t); m != nil {
				info.MaxcoreShortMB, _ = strconv.ParseFloat(m[1], 64)
			}
		case strings.HasPrefix(t, "MemNeeded"):
			if m := reMemNeeded.FindStringSubmatch(t); m != nil {
				info.MemNeededMB, _ = strconv.ParseFloat(m[1], 64)
				info.MemAvailMB, _ = strconv.ParseFloat(m[2], 64)
			}
		case strings.HasPrefix(t, "SCF CONVERGED AFTER"):
			info.SCFConverged++
		case strings.Contains(t, "SCF NOT CONVERGED"):
			info.SCFFailed++
		case strings.Contains(t, "THE OPTIMIZATION HAS CONVERGED"):
			info.OptConverged = true
		case strings.Contains(t, "GEOMETRY OPTIMIZATION CYCLE"):
			info.OptSteps++
		case t == "VIBRATIONAL FREQUENCIES":
			info.HasFreq = true
		case strings.Contains(t, "***imaginary mode***"):
			info.ImagFreqs++
		case strings.HasPrefix(t, "JOB NUMBER"):
			info.Jobs++
		case strings.HasPrefix(t, "WARNING"):
			info.Warnings++
		case strings.HasPrefix(t, "* Host name:"):
			info.HostName = strings.TrimSpace(strings.TrimPrefix(t, "* Host name:"))
		case strings.HasPrefix(t, "* Starting time:"):
			info.StartTimeText = strings.TrimSpace(strings.TrimPrefix(t, "* Starting time:"))
		}
		if m := reRunTime.FindStringSubmatch(line); m != nil {
			var v [5]float64
			for i := range v {
				v[i], _ = strconv.ParseFloat(m[i+1], 64)
			}
			info.TotalRunSec = v[0]*86400 + v[1]*3600 + v[2]*60 + v[3] + v[4]/1000
		}
	}
	if info.Jobs == 0 && info.Version != "" {
		info.Jobs = 1
	}
	return info, sc.Err()
}
