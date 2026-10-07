package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/agaloya/matriline/common/orca"
)

// Multi-core jobs: ORCA runs its parallel modules through OpenMPI's mpirun (ORCA manual,
// "Calling the program with multiple processes"; ORCA 6.1 for Linux is built against
// OpenMPI 4.1.x). The OpenMPI installation is fingerprinted like ORCA, its programs are
// the only extra ones the process audit accepts, and it runs in the job's sandbox, talking
// over loopback and shared memory inside the work directory.

type mpiInstall struct {
	dir     string // holds bin/mpirun (OpenMPI) or mpiexec.exe and smpd.exe (MS-MPI)
	version string // "4.1.8", or "msmpi-10.1.12498" for MS-MPI
	kind    string // "openmpi" or "msmpi"
	fp      *orca.Fingerprint
}

// MS-MPI (Windows): ORCA for Windows is built against Microsoft MPI 10
// (https://github.com/microsoft/Microsoft-MPI); its runtime installs mpiexec.exe and
// smpd.exe in one folder (MSMPI_BIN) and msmpi.dll in System32.
var reMSMPIVersion = regexp.MustCompile(`Version ([0-9]+\.[0-9]+\.[0-9]+)`)

func findMSMPI(cfg *Config) (*mpiInstall, error) {
	var cands []string
	if cfg.MPIPath != "" && cfg.MPIPath != "auto" {
		cands = []string{cfg.MPIPath}
	} else {
		if d := os.Getenv("MSMPI_BIN"); d != "" {
			cands = append(cands, filepath.Clean(d))
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			cands = append(cands, filepath.Join(pf, "Microsoft MPI", "Bin"))
		}
	}
	var why []string
	for _, d := range cands {
		run := filepath.Join(d, "mpiexec.exe")
		if _, err := os.Stat(run); err != nil {
			why = append(why, d+": no mpiexec.exe")
			continue
		}
		out, _ := exec.Command(run).CombinedOutput() // prints "Microsoft MPI Startup Program [Version 10.1.12498.18]"
		m := reMSMPIVersion.FindSubmatch(out)
		if m == nil || !strings.Contains(string(out), "Microsoft MPI") {
			why = append(why, d+": mpiexec.exe is not Microsoft MPI")
			continue
		}
		if !strings.HasPrefix(string(m[1]), "10.") {
			why = append(why, d+": Microsoft MPI "+string(m[1])+", ORCA 6.1 needs 10.x")
			continue
		}
		fp, err := orca.FingerprintTree(d, filepath.Join(cfg.StateDir, "fpcache-msmpi.json"), false)
		if err != nil {
			why = append(why, d+": "+err.Error())
			continue
		}
		return &mpiInstall{dir: d, version: "msmpi-" + string(m[1]), kind: "msmpi", fp: fp}, nil
	}
	if len(why) == 0 {
		return nil, errors.New("no Microsoft MPI found (install its runtime, msmpisetup.exe, or set orca.mpi_path)")
	}
	return nil, fmt.Errorf("no usable Microsoft MPI (%s)", strings.Join(why, "; "))
}

var reOMPIVersion = regexp.MustCompile(`Open MPI\)?\s+([0-9]+\.[0-9]+\.[0-9]+)`)

// findMPI locates and fingerprints OpenMPI: orca.mpi_path, or "auto" (PATH, /opt/openmpi-*).
func findMPI(cfg *Config) (*mpiInstall, error) {
	if runtime.GOOS == "windows" {
		return findMSMPI(cfg)
	}
	var cands []string
	if cfg.MPIPath != "" && cfg.MPIPath != "auto" {
		cands = []string{cfg.MPIPath}
	} else {
		if p, err := exec.LookPath("mpirun"); err == nil {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				cands = append(cands, filepath.Dir(filepath.Dir(r)))
			}
		}
		opt, _ := filepath.Glob("/opt/openmpi-*")
		cands = append(cands, opt...)
	}
	var why []string
	for _, d := range cands {
		run := filepath.Join(d, "bin", "mpirun")
		if _, err := os.Stat(run); err != nil {
			why = append(why, d+": no bin/mpirun")
			continue
		}
		vc := exec.Command(run, "--version") // finds its own libraries only like this
		vc.Env = []string{"PATH=" + filepath.Join(d, "bin"), "LD_LIBRARY_PATH=" + filepath.Join(d, "lib"), "OPAL_PREFIX=" + d}
		out, _ := vc.CombinedOutput()
		m := reOMPIVersion.FindSubmatch(out)
		if m == nil {
			// e.g. "error while loading shared libraries: libhwloc.so.15" (an OpenMPI built
			// against the system hwloc/libevent of another computer)
			first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
			why = append(why, d+": mpirun --version: "+first)
			continue
		}
		v := string(m[1])
		if !strings.HasPrefix(v, "4.") {
			// ORCA 6.1's Linux binaries are linked against OpenMPI 4.1 (libmpi.so.40 of
			// 5.x is not compatible with them)
			why = append(why, d+": OpenMPI "+v+", ORCA 6.1 needs 4.1.x")
			continue
		}
		fp, err := orca.FingerprintTree(d, filepath.Join(cfg.StateDir, "fpcache-mpi-"+filepath.Base(d)+".json"), false)
		if err != nil {
			why = append(why, d+": "+err.Error())
			continue
		}
		return &mpiInstall{dir: d, version: v, kind: "openmpi", fp: fp}, nil
	}
	if len(why) == 0 {
		return nil, errors.New("no OpenMPI found (set orca.mpi_path)")
	}
	return nil, fmt.Errorf("no usable OpenMPI (%s)", strings.Join(why, "; "))
}

// mpiEnv adds OpenMPI to a job's clean environment. Everything OpenMPI writes (session
// directory, shared-memory segments) goes to the work directory, the only writable place in
// the sandbox; only the local launcher and loopback are used (no ssh, no network).
// Variables: OpenMPI 4.1 MCA parameters (ompi_info --all).
func mpiEnv(env []string, m *mpiInstall, work string) []string {
	if m.kind == "msmpi" { // ORCA finds mpiexec through PATH; everything else is MS-MPI's own
		out := make([]string, 0, len(env))
		for _, e := range env {
			if len(e) > 5 && strings.EqualFold(e[:5], "PATH=") {
				e += string(filepath.ListSeparator) + m.dir
			}
			out = append(out, e)
		}
		return out
	}
	out := make([]string, 0, len(env)+10)
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "PATH="):
			e += string(filepath.ListSeparator) + filepath.Join(m.dir, "bin")
		case strings.HasPrefix(e, "LD_LIBRARY_PATH="):
			e += string(filepath.ListSeparator) + filepath.Join(m.dir, "lib")
		}
		out = append(out, e)
	}
	return append(out,
		"OPAL_PREFIX="+m.dir,
		"OMPI_MCA_plm=isolated",   // start processes locally only, never through ssh/rsh
		"OMPI_MCA_btl=self,vader", // shared memory between the processes of the job
		"OMPI_MCA_btl_vader_backing_directory="+work,
		"OMPI_MCA_osc_sm_backing_directory="+work, // MPI_Win_allocate_shared (ORCA uses it)
		"OMPI_MCA_osc_rdma_backing_directory="+work,
		"OMPI_MCA_shmem_mmap_relocate_backing_file=1", // ... and every other shared segment
		"OMPI_MCA_shmem_mmap_backing_file_base_dir="+work,
		"OMPI_MCA_orte_tmpdir_base="+work, // session directory
		"OMPI_MCA_oob_tcp_if_include=lo",  // the launcher's own control channel
		"OMPI_MCA_rmaps_base_oversubscribe=0",
		"OMPI_MCA_hwloc_base_binding_policy=none", // the OS places processes; other jobs share the CPU
	)
}
