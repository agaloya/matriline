package main

import (
	"errors"
	"fmt"
	"github.com/agaloya/matriline/common/build"
	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/release"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/conf"
)

// Config holds the client's LOCAL options only. Network policy (encryption, enrollment,
// which files to return, ...) comes from the server (D11); conversely the server can never
// make the client exceed the limits below.
type Config struct {
	Credential    string
	StateDir      string
	ScratchDir    string
	OrcaPaths     []string
	Cores         int // 0 = automatic (physical cores - 1)
	SMT           bool
	MemPerCoreMB  int
	MemTotalMB    int    // memory pool shared by the running jobs (0 = fixed memory_per_core)
	MaxCoresJob   int    // largest multi-core (MPI) job accepted; 1 = single-core jobs only
	MPIPath       string // OpenMPI installation for multi-core jobs; "auto" = find it
	DiskLimit     int64  // all jobs together; -1 unlimited
	DiskPerJob    int64  // one job; -1 unlimited
	DiskReserve   int64  // always left free on the scratch disk for the system (-1 = auto)
	BandwidthBPS  int64  // bits/s, 0 = unlimited
	Schedule      []window
	PauseBattery  bool
	KeepAwake     bool    // no sleep by itself while calculations run on mains power
	MinBattery    float64 // percent; 0 = off
	BatteryResume float64 // percent above MinBattery needed to resume
	BusyPercent   int     // pause when other processes use more than this % of CPU (0 = off)
	MaxTaskTime   time.Duration
	EndDate       time.Time
	TempLimitC    float64
	TempFile      string
	Sandbox       bool
	UpdateAllow   bool   // install signed releases the server offers (D66)
	UpdateURL     string // where this client downloads them (never the server's choice)
	DeepCheck     time.Duration
	MaxFileMB     int64 // 0 = unlimited
}

// autoDiskReserve: 10 GiB kept free, but at most 20 % of the disk (a 10 GB disk would
// otherwise never have room for a job: found by a fresh-install test on a small VM).
func autoDiskReserve(dir string) int64 {
	r := int64(10 << 30)
	for d := dir; d != ""; d = filepath.Dir(d) {
		if size := diskSize(d); size > 0 {
			return min(r, size/5)
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	return r
}

type window struct {
	days       map[time.Weekday]bool
	start, end int // minutes after midnight; end may be < start (overnight)
}

func loadConfig(path string) (*Config, []string, error) {
	cf, err := conf.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, errors.New(i18n.Tf("%s not found: create this computer's client with 'matriline-client init <empty folder>' and run the commands inside that folder (or add -c <folder>/client.conf)", path))
	}
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	dir := filepath.Dir(path)
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	c := &Config{
		Credential:    abs(cf.String("client.credential", "credential.conf")),
		StateDir:      abs(cf.String("client.state_dir", "state")),
		ScratchDir:    abs(cf.String("client.scratch_dir", "")),
		OrcaPaths:     cf.List("orca.paths", []string{"/opt/orca-6.1.1"}),
		Cores:         cf.Int("resources.cores", 0, &errs),
		SMT:           cf.Bool("resources.smt", false, &errs),
		MemPerCoreMB:  int(cf.Size("resources.memory_per_core", 1<<30, &errs) >> 20),
		MaxCoresJob:   cf.Int("resources.max_cores_per_job", 1, &errs),
		MemTotalMB:    int(cf.Size("resources.memory_total", 0, &errs) >> 20),
		MPIPath:       cf.String("orca.mpi_path", "auto"),
		DiskLimit:     cf.Size("resources.disk_limit", -1, &errs),
		DiskPerJob:    cf.Size("resources.disk_per_job", -1, &errs),
		DiskReserve:   -1, // auto, unless set below
		PauseBattery:  cf.Bool("schedule.pause_on_battery", true, &errs),
		KeepAwake:     cf.Bool("schedule.keep_awake", true, &errs),
		MinBattery:    cf.Float("schedule.min_battery_percent", 0, &errs),
		BatteryResume: cf.Float("schedule.battery_resume_margin", 5, &errs),
		BusyPercent:   cf.Int("schedule.pause_when_busy_percent", 0, &errs),
		MaxTaskTime:   cf.Duration("limits.max_task_time", 0, &errs),
		MaxFileMB:     sizeMB(cf.Size("limits.max_file_size", -1, &errs)),
		TempLimitC:    cf.Float("limits.cpu_temperature_limit", 0, &errs),
		TempFile:      cf.String("limits.cpu_temperature_file", ""),
		Sandbox:       cf.Bool("security.sandbox", true, &errs) && !build.NoSecurity,
		DeepCheck:     cf.Duration("security.orca_deep_check_interval", 24*time.Hour, &errs),
		UpdateAllow:   cf.Bool("security.updates", true, &errs),
		UpdateURL:     cf.String("security.update_url", release.DefaultURL),
	}
	if c.ScratchDir == "" {
		c.ScratchDir = filepath.Join(c.StateDir, "jobs")
	}
	bw := cf.String("resources.bandwidth_limit", "10Mbit")
	if c.BandwidthBPS, err = parseBits(bw); err != nil {
		errs = append(errs, fmt.Errorf("resources.bandwidth_limit: %v", err))
	}
	for _, w := range cf.List("schedule.windows", nil) {
		win, err := parseWindow(w)
		if err != nil {
			errs = append(errs, fmt.Errorf("schedule.windows: %v", err))
			continue
		}
		c.Schedule = append(c.Schedule, win)
	}
	if v := cf.String("resources.disk_reserve", "auto"); v != "auto" && v != "" {
		c.DiskReserve = cf.Size("resources.disk_reserve", 0, &errs)
	} else {
		c.DiskReserve = autoDiskReserve(c.ScratchDir)
	}
	if d := cf.String("limits.end_date", "never"); d != "never" && d != "" {
		t, err := time.ParseInLocation("2006-01-02", d, time.Local)
		if err != nil {
			errs = append(errs, fmt.Errorf("limits.end_date: use YYYY-MM-DD or never"))
		}
		c.EndDate = t
	}
	var warn []string
	if runtime.GOOS == "darwin" && c.MaxCoresJob > 1 {
		warn = append(warn, fmt.Sprintf("resources.max_cores_per_job = %d: macOS runs every job on one core (its sandbox cannot keep OpenMPI's connections on this computer); using 1", c.MaxCoresJob))
		c.MaxCoresJob = 1
	}
	for _, e := range errs {
		warn = append(warn, "ERROR: "+e.Error())
	}
	if l := cf.String("general.language", ""); l != "" && !slices.Contains(i18n.Available(), i18n.Normalize(l)) {
		warn = append(warn, fmt.Sprintf("general.language = %s: no translation, English is used (available: %s)", l, strings.Join(i18n.Available(), ", ")))
	}
	for _, u := range cf.Unused() {
		warn = append(warn, "unknown option (typo?): "+u)
	}
	if len(errs) > 0 {
		return c, warn, fmt.Errorf("invalid configuration: %s", warn[0])
	}
	return c, warn, nil
}

// slots computes the number of concurrent single-core jobs (D33): n-1 logical cores by
// default; a 1-core machine uses its only core but warns.
func (c *Config) slots() (int, string) {
	// one calculation per PHYSICAL core by default: in the lab, two ORCA jobs on the two
	// hardware threads of one core gave only 11 % more throughput while each ran 80 %
	// slower (and hotter); resources.smt = true uses every logical CPU instead
	n := physicalCores()
	if c.SMT || n <= 0 {
		n = runtime.NumCPU()
	}
	if c.Cores > 0 {
		if c.Cores > n {
			return n, fmt.Sprintf("resources.cores = %d but only %d cores are available (resources.smt = %v); using %d", c.Cores, n, c.SMT, n)
		}
		return c.Cores, ""
	}
	if n <= 1 {
		return 1, "this machine has a single logical CPU: Matriline will use it fully, which may make the computer slow while a job runs (set resources.cores to choose)"
	}
	return n - 1, ""
}

func parseBits(s string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" || t == "0" || t == "unlimited" {
		return 0, nil
	}
	mult := map[string]float64{"kbit": 1e3, "mbit": 1e6, "gbit": 1e9, "bit": 1}
	for _, suf := range []string{"kbit", "mbit", "gbit", "bit"} {
		if strings.HasSuffix(t, suf) {
			var v float64
			if _, err := fmt.Sscan(strings.TrimSuffix(t, suf), &v); err != nil || v < 0 {
				return 0, fmt.Errorf("bad value %q", s)
			}
			return int64(v * mult[suf]), nil
		}
	}
	return 0, fmt.Errorf("use units like 10Mbit, 500kbit or unlimited (got %q)", s)
}

// parseWindow parses "mon-fri 20:00-07:00" or "sat,sun 00:00-24:00" or "22:00-06:00".
func parseWindow(s string) (window, error) {
	w := window{days: map[time.Weekday]bool{}}
	f := strings.Fields(s)
	names := map[string]time.Weekday{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	timePart := f[len(f)-1]
	if len(f) == 2 {
		for _, part := range strings.Split(f[0], ",") {
			if a, b, ok := strings.Cut(part, "-"); ok {
				da, oka := names[a]
				db, okb := names[b]
				if !oka || !okb {
					return w, fmt.Errorf("bad days %q", f[0])
				}
				for d := da; ; d = (d + 1) % 7 {
					w.days[d] = true
					if d == db {
						break
					}
				}
			} else if d, ok := names[part]; ok {
				w.days[d] = true
			} else {
				return w, fmt.Errorf("bad day %q", part)
			}
		}
	} else if len(f) != 1 {
		return w, fmt.Errorf("bad window %q", s)
	} else {
		for d := time.Weekday(0); d < 7; d++ {
			w.days[d] = true
		}
	}
	a, b, ok := strings.Cut(timePart, "-")
	if !ok {
		return w, fmt.Errorf("bad time range %q", timePart)
	}
	var err error
	if w.start, err = hhmm(a); err != nil {
		return w, err
	}
	if w.end, err = hhmm(b); err != nil {
		return w, err
	}
	return w, nil
}

func hhmm(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return h*60 + m, nil
}

// inSchedule reports whether t falls inside any window (no windows = always).
func (c *Config) inSchedule(t time.Time) bool {
	if len(c.Schedule) == 0 {
		return true
	}
	min := t.Hour()*60 + t.Minute()
	for _, w := range c.Schedule {
		if w.start <= w.end {
			if w.days[t.Weekday()] && min >= w.start && min < w.end {
				return true
			}
		} else { // overnight: the part after midnight belongs to the previous day's window
			if w.days[t.Weekday()] && min >= w.start {
				return true
			}
			if w.days[(t.Weekday()+6)%7] && min < w.end {
				return true
			}
		}
	}
	return false
}

const defaultConfig = `# =============================================================================
# Matriline client configuration (LOCAL options of this computer)
# =============================================================================
# Network policy (encryption, how to join, which files are returned, ...) is decided by
# the server and received automatically: you do not configure it here, and the server
# cannot make this computer exceed the limits you set below.


[general]
# en: language of the menus, help and messages: "en", "es", "fr", "pt", "ar" (empty: this computer's)
# es: idioma de los menús, la ayuda y los mensajes: "en", "es", "fr", "pt", "ar" (vacío: el de esta computadora)
# fr: langue des menus, de l'aide et des messages : "en", "es", "fr", "pt", "ar" (vide : celle de cet ordinateur)
# pt: idioma dos menus, da ajuda e das mensagens: "en", "es", "fr", "pt", "ar" (vazio: o deste computador)
# ar: لغة القوائم والمساعدة والرسائل: "en", "es", "fr", "pt", "ar" (فارغ: لغة هذا الحاسوب؛ الترجمة العربية آلية ولم تُراجَع بعد)
language =


[client]
# Credential file given to you by the server admin (or created by "matriline-client join").
credential = credential.conf

# Where the client keeps its state (jobs in progress survive power cuts here).
state_dir = state

# Only one client can run per state_dir (a second one refuses to start). To lend this
# computer to another project too, run a second client with its own directory.
# Where ORCA runs. Empty = <state_dir>/jobs. Use a fast local disk.
scratch_dir =


[orca]
# ORCA installations available on this computer (the server says which version to use).
paths = /opt/orca-6.1.1

# OpenMPI installation for multi-core jobs (resources.max_cores_per_job > 1): the directory
# that holds bin/mpirun. "auto" looks in PATH and /opt/openmpi-*. ORCA 6.1 for Linux needs
# OpenMPI 4.1.x. It is fingerprinted like ORCA and run inside the same sandbox.
mpi_path = auto


[resources]
# CPU cores lent to Matriline (one single-core calculation per core).
# 0 = automatic: all physical cores minus one (a single-core computer uses its only core
# and warns).
cores = 0

# smt: also count the second hardware thread of each core (Hyper-Threading/SMT) as a core.
# Off by default: measured with ORCA, a second job on the same core adds only ~11 %
# throughput and makes every job ~80 % slower and the CPU hotter.
smt = false

# Memory per core. The client sets ORCA's %maxcore to 75 % of this (ORCA can exceed
# %maxcore) and warns and lends fewer cores if the computer does not have enough RAM.
memory_per_core = 1GiB

# Or a total for all jobs together (e.g. 12GiB; 0 = off): each job gets an equal share
# (memory_total / cores) by default, but a job known to need more memory per core (the
# server learns it from ORCA's own errors) or a multi-core job may take more while the
# rest is free. When set, memory_per_core is not used.
memory_total = 0

# Largest multi-core calculation accepted (an input with "%pal nprocs N" or "! PALN", when
# the server allows them). A job of N cores takes N of the cores above and N times the
# memory per core. 1 = single-core jobs only (default: several independent jobs use the
# cores better; measured, 2 cores make one job only ~1.25x faster). Needs OpenMPI, see
# orca.mpi_path.
# macOS: always 1. Its sandbox cannot keep OpenMPI's connections on this computer only,
# so a multi-core job would have network access (docs/PORTING.md).
max_cores_per_job = 1

# Scratch disk for calculations in progress, like cores and memory: a total for all jobs
# together (disk_limit; no new tasks while it is used up), and/or a fixed amount per job
# (disk_per_job; a job that needs more is stopped and goes to another computer). Both
# "unlimited" by default: the client learns how much each kind of job needs and only takes
# a job that fits.
disk_limit = unlimited

disk_per_job = unlimited

# Always left free on the scratch disk for the system and the user, whatever the limits
# above say; a running job is stopped if free space falls below half of it.
# "auto" = 10 GiB, but at most 20 % of the disk (e.g. 2 GiB on a 10 GB disk); or a size.
disk_reserve = auto

# Upload speed limit for results (10Mbit by default; "unlimited" to disable).
bandwidth_limit = 10Mbit


[schedule]
# Only accept new tasks inside these windows (running ones always finish). Empty = always.
# Examples: windows = mon-fri 20:00-07:00, sat-sun 00:00-24:00
windows =

# Do not accept new tasks while running on battery.
pause_on_battery = true

# While calculations run on mains power, keep this computer from going to sleep by itself
# (the screen may still turn off; closing a laptop's lid still suspends it). On battery or
# with nothing to compute it sleeps as usual. Its power settings are not changed.
keep_awake = true

# When pause_on_battery = false: stop accepting new tasks while on battery below this
# charge (percent, 0 = off); running tasks finish. Taking tasks resumes on mains power or
# once the charge is battery_resume_margin points above the limit (no flapping around it).
# Machines without a system battery (desktops, servers, VMs) are never paused by this.
min_battery_percent = 0

battery_resume_margin = 5

# Do not accept new tasks while other programs use more than this % of the CPU (0 = off).
pause_when_busy_percent = 0


[limits]
# Stop a calculation that runs longer than this (0 = no limit). The task goes back to the
# server for another computer.
max_task_time = 0

# Largest single file a calculation may write (e.g. 50G), "unlimited" by default. ORCA's
# temporary files can be several GB (DLPNO); the scratch-disk guard protects the disk.
max_file_size = unlimited

# Stop accepting new tasks while the CPU is hotter than this (degrees Celsius, 0 = off,
# the default). Running calculations continue; new ones resume once it cools down.
cpu_temperature_limit = 0

# Where the temperature is read from. Empty = this computer's CPU sensor. A file holding a
# number in degrees Celsius is useful in a virtual machine (which has no sensor) when its
# host writes its own CPU temperature there, or for any custom sensor.
cpu_temperature_file =

# After this date (YYYY-MM-DD) the client stops and disables itself. "never" by default.
end_date = never


[security]
# Run ORCA isolated: no network access, no access to your files outside the job directory,
# and no programs other than the ORCA installation. Strongly recommended.
sandbox = true

# How often the whole ORCA installation is re-hashed ignoring the cache.
orca_deep_check_interval = 24h

# Install new Matriline versions when the server offers them. Only releases signed by the
# Matriline maintainer's key (built into this program) are installed, whoever sends them;
# it happens between jobs, and the previous program stays as <program>.previous.
updates = true

# Where new versions are downloaded from (the server only says which version; it never
# chooses the address).
update_url = https://github.com/agaloya/matriline/releases`

// sizeMB converts a size in bytes (-1 = unlimited) to whole MB, 0 meaning unlimited.
func sizeMB(b int64) int64 {
	if b <= 0 {
		return 0
	}
	return (b + 1<<20 - 1) >> 20
}

// initLanguage: init's --language (empty: this computer's).
var initLanguage string

// initSettings is init's --settings file: client.conf-format settings written over the
// defaults and the credential's preset (the admin's helper kit, src/kit).
var initSettings string

// initConfigText is the template with general.language set for init.
func initConfigText() (string, error) {
	lang := initLanguage
	if lang == "" {
		lang = i18n.System()
	}
	l := i18n.Normalize(lang)
	if l != "en" && i18n.Set(lang) != nil {
		if initLanguage != "" {
			return "", fmt.Errorf("language %q: no translation (available: %s)", initLanguage, strings.Join(i18n.Available(), ", "))
		}
		l = "en" // this computer's language has no translation yet
	}
	return languageLine.ReplaceAllString(i18n.Template("client.conf", defaultConfig), "${1}language = "+l+"\n"), nil
}

var languageLine = regexp.MustCompile(`(?m)^(\[general\][^\[]*?)language =[ \t]*\n`)
