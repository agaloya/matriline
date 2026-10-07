package main

import (
	"errors"
	"fmt"
	"github.com/agaloya/matriline/common/build"
	"github.com/agaloya/matriline/common/i18n"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/conf"
	"github.com/agaloya/matriline/common/release"
	"github.com/agaloya/matriline/common/wire"
)

// Config is the parsed server configuration. Every network policy lives here and only here
// (D11): clients learn it during the handshake and cannot change it.
type Config struct {
	Root      string   // spool root (directory holding input/, output/, ...)
	KeepEmpty []string // spool directories whose empty sub-directories stay

	Listen      []string // e.g. ":44100"
	Advertise   string   // address written into issued credentials
	Mode        wire.Mode
	Connection  string // "direct" or "relay"
	RelayAddr   string
	Enrollment  string // issued, register, open
	BlockedIPs  []string
	MaxSessions int

	BanGarbageAfter    int
	BanGarbageWindow   time.Duration
	BanGarbageDuration time.Duration
	BanAuthAfter       int
	BanAuthWindow      time.Duration
	BanAuthDuration    time.Duration
	BanTrustKnown      bool
	EnrollTTL          time.Duration
	ReuseCheckpoints   bool

	HeartbeatSec      int
	TaskTimeout       time.Duration
	MaxJobTime        time.Duration // longest run of one job on one computer; then errors/ (0 = no limit)
	ScanInterval      time.Duration
	SettleTime        time.Duration
	Order             string // alphanumeric, random
	Scheduler         string // order, learned
	Parallel          string // downgrade, honor: inputs that ask for several MPI processes
	MaxProcs          int    // largest number of processes honoured
	LearnedMinSamples int
	InputExt          []string
	MaxReplicas       int
	DuplicateIdle     bool
	OrcaFailHosts     int // distinct hosts that must fail with an ORCA error before errors/
	MaxAttempts       int
	OrcaVersion       string // required version, e.g. "6.1.1"
	OrcaCheck         string // fingerprint (programs vs the reference builds) or version (outputs only)
	OtherVersions     string // refuse, separate, errors: clients without the campaign's ORCA version
	AcceptNoSecurity  bool   // give tasks to clients built without sandbox and audit
	OrcaPaths         []string
	OrcaTreeHashes    []string
	OrcaFingerprints  []string // JSON files from 'matriline-client fingerprint' // extra accepted fingerprints

	ReturnFiles    []string
	ReturnExclude  []string
	MaxResultBytes int64
	CompressLevel  int
	MetaFields     []string
	Percentiles    []int
	SampleSec      int

	// verification (D44)
	VerifyFingerprints      bool
	VerifyConsistency       bool
	VerifyTiming            bool
	VerifySCF               float64 // fraction of results re-checked with a 1-iteration SCF
	VerifyGradient          float64
	VerifyHessian           float64
	CanaryRate              float64
	ReplicationRate         float64
	ProbationResults        int
	ProbationBoost          float64
	Reputation              bool
	TrustedAfter            int
	TrustedMultiplier       float64
	RecentFailureWindow     time.Duration
	RecentFailureMultiplier float64
	ProbationReplication    float64
	FailurePolicy           string // weird, quarantine
	EnergyTolerance         float64
	CheckMaxSCFIter         int
	HessTolerance           float64
	SecondOpinion           bool
	NoSecondOpinion         string // weird | accept: when no independent host can give it
	RetryWeird              bool
	RescueWeird             bool
	QuarantineAfter         int
	SameHost                bool // checks may run on the computer that produced the result (one computer: Nacomline)
	QuarantineWindow        time.Duration
	QuarantineRecheck       int
	QuarantineEscalate      float64
	QuarantineAutoRelease   bool
	ErrorAlertAfter         int
	ErrorPauseAfter         int
	ErrorWindow             time.Duration

	StorageLimit   int64 // -1 unlimited
	StorageWarnPct int
	StorageMinFree int64

	MailEnabled  bool
	MailServer   string
	MailUser     string
	MailPassEnv  string
	MailPassFile string // a file holding only the SMTP password (works for a service)
	// alerts by Telegram: a bot's token (in a file) and the chat to write to
	TelegramEnabled   bool
	TelegramTokenFile string
	TelegramChat      string
	MailFrom          string
	MailTo            []string
	// AlertCommand is run for each alert with the event and the message as arguments
	AlertCommand string
	// alerts: each kind off, now or summary; the summary's schedule (alertkinds.go)
	AlertModes       map[string]string
	AlertNowLimit    time.Duration
	AlertSummary     *schedule
	AlertSummarySpec string
	SummaryQuiet     bool // send the summary even when nothing happened
	alertWarn        []string
	HookResult       string // program run for each result (hooks.go)
	// the web page's colours: a palette and any of its five colours replaced ([web])
	WebPalette    string
	MolRotation   time.Duration // the 3D view turns once per this; 0 = still
	MolBackground string        // auto (black in a dark palette, white in a light one) or a colour
	WebColors     map[string]string
	HookDone      string // program run when the queue drains
	// updates (D66): off, alert or auto; how often; where the signed releases are
	UpdateMode     string
	UpdateInterval time.Duration
	UpdateURL      string
	UpdateWait     time.Duration // clients slower than this to update: the admin is told

	LogLevel string
	Language string // general.language: en, es, ... (empty: this computer's)
}

func loadConfig(path string) (*Config, []string, error) {
	cf, err := conf.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, errors.New(i18n.Tf("%s not found: create the project with 'matriline-server init <empty folder>' and run the commands inside that folder (or add -c <folder>/server.conf)", path))
	}
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	c := &Config{}
	c.Root = cf.String("spool.root", ".")
	c.KeepEmpty = cf.List("spool.keep_empty_dirs", []string{dInput})
	c.Listen = cf.List("network.listen", []string{":44100"})
	for i, l := range c.Listen {
		if _, err := strconv.Atoi(l); err == nil {
			c.Listen[i] = ":" + l // a bare port: every address of this computer (user)
		}
	}
	c.Advertise = cf.String("network.advertise", "")
	if c.Mode, err = wire.ParseMode(cf.String("network.security", "tls")); err != nil {
		errs = append(errs, err)
	}
	c.Connection = strings.ToLower(cf.String("network.connection", "direct"))
	c.RelayAddr = cf.String("network.relay_address", "")
	c.Enrollment = strings.ToLower(cf.String("network.enrollment", "issued"))
	c.BlockedIPs = cf.List("network.blocked_ips", nil)
	c.MaxSessions = cf.Int("network.max_sessions", 256, &errs)
	c.BanGarbageAfter = cf.Int("network.ban_garbage_after", 10, &errs)
	c.BanGarbageWindow = cf.Duration("network.ban_garbage_window", time.Hour, &errs)
	c.BanGarbageDuration = cf.Duration("network.ban_garbage_duration", 7*24*time.Hour, &errs)
	c.BanAuthAfter = cf.Int("network.ban_auth_after", 20, &errs)
	c.BanAuthWindow = cf.Duration("network.ban_auth_window", 24*time.Hour, &errs)
	c.BanAuthDuration = cf.Duration("network.ban_auth_duration", 24*time.Hour, &errs)
	c.BanTrustKnown = cf.Bool("network.ban_trust_known", true, &errs)
	c.EnrollTTL = cf.Duration("network.credential_valid", 48*time.Hour, &errs)
	c.ReuseCheckpoints = cf.Bool("tasks.reuse_checkpoints", true, &errs)

	c.HeartbeatSec = cf.Int("tasks.heartbeat_seconds", 60, &errs)
	c.TaskTimeout = cf.Duration("tasks.task_timeout", time.Hour, &errs)
	c.MaxJobTime = cf.Duration("tasks.max_job_time", 24*time.Hour, &errs)
	c.ScanInterval = cf.Duration("tasks.scan_interval", 5*time.Second, &errs)
	c.SettleTime = cf.Duration("tasks.settle_time", 2*time.Second, &errs)
	c.Order = strings.ToLower(cf.String("tasks.order", "alphanumeric"))
	c.InputExt = cf.List("tasks.input_extensions", []string{".inp"})
	c.MaxReplicas = cf.Int("tasks.max_replicas", 2, &errs)
	c.DuplicateIdle = cf.Bool("tasks.duplicate_when_idle", true, &errs)
	c.OrcaFailHosts = cf.Int("tasks.orca_error_hosts", 2, &errs)
	c.MaxAttempts = cf.Int("tasks.max_attempts", 6, &errs)
	c.Scheduler = strings.ToLower(cf.String("tasks.scheduler", "order"))
	c.Parallel = strings.ToLower(cf.String("tasks.parallel", "downgrade"))
	c.MaxProcs = cf.Int("tasks.max_procs", 8, &errs)
	c.LearnedMinSamples = cf.Int("tasks.learned_min_samples", 30, &errs)
	c.OrcaVersion = cf.String("orca.version", "6.1.1")
	c.OrcaCheck = strings.ToLower(cf.String("orca.check", "fingerprint"))
	c.OtherVersions = strings.ToLower(cf.String("orca.other_versions", "refuse"))
	c.AcceptNoSecurity = cf.Bool("network.accept_nosecurity_clients", false, &errs)
	c.OrcaPaths = cf.List("orca.reference_paths", []string{"/opt/orca-6.1.1"})
	c.OrcaTreeHashes = cf.List("orca.accepted_tree_hashes", nil)
	c.OrcaFingerprints = cf.List("orca.accepted_fingerprints", nil)

	c.ReturnFiles = cf.List("results.include", []string{"*"})
	c.ReturnExclude = cf.List("results.exclude", []string{"*.tmp", "*.tmp.*"})
	c.MaxResultBytes = cf.Size("results.max_bytes", 2<<30, &errs)
	c.CompressLevel = cf.Int("results.compress_level", 6, &errs)
	c.MetaFields = cf.List("report.fields", []string{"*"})
	for _, p := range cf.List("report.percentiles", []string{"50", "90", "99"}) {
		var v int
		if _, err := fmt.Sscan(p, &v); err != nil || v <= 0 || v >= 100 {
			errs = append(errs, fmt.Errorf("report.percentiles: bad value %q", p))
			continue
		}
		c.Percentiles = append(c.Percentiles, v)
	}
	c.SampleSec = cf.Int("report.sample_seconds", 5, &errs)
	_ = cf.String("report.client_log_level", "") // obsolete (the clients never applied it); accepted in old files

	_ = cf.Bool("verify.signatures", true, &errs) // obsolete (results are always signed); accepted in old files
	c.VerifyFingerprints = cf.Bool("verify.fingerprints", true, &errs) && c.OrcaCheck != "version"
	c.VerifyConsistency = cf.Bool("verify.output_consistency", true, &errs)
	c.VerifyTiming = cf.Bool("verify.timing_plausibility", true, &errs)
	c.VerifySCF = cf.Float("verify.scf_recheck_fraction", 0.05, &errs)
	c.VerifyGradient = cf.Float("verify.gradient_check_fraction", 0.05, &errs)
	c.VerifyHessian = cf.Float("verify.hessian_probe_fraction", 0.05, &errs)
	c.CanaryRate = cf.Float("verify.canary_rate", 0, &errs)
	c.ReplicationRate = cf.Float("verify.replication_rate", 0, &errs)
	c.ProbationResults = cf.Int("verify.probation_results", 5, &errs)
	c.ProbationReplication = cf.Float("verify.probation_replication", 0.2, &errs)
	c.ProbationBoost = cf.Float("verify.probation_multiplier", 3, &errs)
	c.Reputation = cf.Bool("verify.reputation", true, &errs)
	c.TrustedAfter = cf.Int("verify.trusted_after", 50, &errs)
	c.TrustedMultiplier = cf.Float("verify.trusted_multiplier", 0.5, &errs)
	c.RecentFailureWindow = cf.Duration("verify.recent_failure_window", 720*time.Hour, &errs)
	c.RecentFailureMultiplier = cf.Float("verify.recent_failure_multiplier", 2, &errs)
	c.FailurePolicy = strings.ToLower(cf.String("verify.on_failure", "weird"))
	c.EnergyTolerance = cf.Float("verify.energy_tolerance", 1e-5, &errs)
	c.CheckMaxSCFIter = cf.Int("verify.check_max_scf_iterations", 10, &errs)
	c.HessTolerance = cf.Float("verify.hessian_tolerance", 0.005, &errs)
	c.SecondOpinion = cf.Bool("verify.second_opinion", true, &errs)
	c.NoSecondOpinion = strings.ToLower(cf.String("verify.second_opinion_unavailable", "weird"))
	if c.NoSecondOpinion != "weird" && c.NoSecondOpinion != "accept" {
		errs = append(errs, fmt.Errorf("verify.second_opinion_unavailable must be weird or accept, not %q", c.NoSecondOpinion))
	}
	c.RetryWeird = cf.Bool("verify.retry_weird", true, &errs)
	c.RescueWeird = cf.Bool("verify.rescue_weird", true, &errs)
	c.QuarantineAfter = cf.Int("verify.quarantine_after", 3, &errs)
	c.SameHost = cf.Bool("verify.same_host", false, &errs)
	c.QuarantineWindow = cf.Duration("verify.quarantine_window", 24*time.Hour, &errs)
	c.QuarantineRecheck = cf.Int("verify.quarantine_recheck", 10, &errs)
	c.QuarantineEscalate = cf.Float("verify.quarantine_escalate_fraction", 0.25, &errs)
	c.QuarantineAutoRelease = cf.Bool("verify.quarantine_auto_release", true, &errs)
	c.ErrorAlertAfter = cf.Int("tasks.error_alert_after", 3, &errs)
	c.ErrorPauseAfter = cf.Int("tasks.error_pause_after", 10, &errs)
	c.ErrorWindow = cf.Duration("tasks.error_window", 24*time.Hour, &errs)
	// master switch: false turns every integrity layer off at once (trusted machines);
	// true applies the individual options below
	if !cf.Bool("verify.enabled", true, &errs) || build.NoSecurity {
		c.VerifyFingerprints, c.VerifyConsistency, c.VerifyTiming = false, false, false
		c.VerifySCF, c.VerifyGradient, c.VerifyHessian, c.CanaryRate, c.ReplicationRate = 0, 0, 0, 0, 0
		c.SecondOpinion = false
		c.ProbationReplication = 0 // new clients' first results were still replicated (load test)
	}

	c.StorageLimit = cf.Size("storage.limit", -1, &errs)
	c.StorageWarnPct = cf.Int("storage.warn_percent", 90, &errs)
	if v := strings.TrimSpace(cf.String("storage.min_free", "auto")); strings.EqualFold(v, "auto") {
		c.StorageMinFree = minFreeAuto
	} else {
		c.StorageMinFree = cf.Size("storage.min_free", 0, &errs)
	}

	c.MailEnabled = cf.Bool("alerts.email_enabled", false, &errs)
	c.MailServer = cf.String("alerts.smtp_server", "")
	c.MailUser = cf.String("alerts.smtp_user", "")
	c.MailPassEnv = cf.String("alerts.smtp_password_env", "MATRILINE_SMTP_PASSWORD")
	c.MailPassFile = cf.String("alerts.smtp_password_file", "")
	c.TelegramEnabled = cf.Bool("alerts.telegram_enabled", false, &errs)
	c.TelegramTokenFile = cf.String("alerts.telegram_token_file", "")
	c.TelegramChat = strings.TrimSpace(cf.String("alerts.telegram_chat", ""))
	c.MailFrom = cf.String("alerts.from", "")
	c.MailTo = cf.List("alerts.to", nil)
	c.AlertCommand = cf.String("alerts.command", "")
	c.alertWarn = alertConfig(c, cf.Has, cf.String, func(k string, d time.Duration) time.Duration { return cf.Duration(k, d, &errs) })
	c.SummaryQuiet = cf.Bool("alerts.summary_when_quiet", false, &errs)
	c.HookResult = cf.String("hooks.result", "")
	c.WebPalette = strings.ToUpper(cf.String("web.palette", "A"))
	c.MolRotation = cf.Duration("web.mol_rotation", 20*time.Second, &errs)
	c.MolBackground = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cf.String("web.mol_background", "auto"))), "#")
	c.WebColors = map[string]string{}
	for _, k := range paletteKeys {
		if v := strings.TrimPrefix(strings.TrimSpace(cf.String("web."+k, "")), "#"); v != "" {
			c.WebColors[k] = v
		}
	}
	c.HookDone = cf.String("hooks.done", "")
	c.UpdateMode = strings.ToLower(cf.String("update.mode", "off"))
	c.UpdateInterval = cf.Duration("update.check_interval", 30*24*time.Hour, &errs)
	c.UpdateURL = cf.String("update.url", release.DefaultURL)
	c.UpdateWait = cf.Duration("update.client_wait", 24*time.Hour, &errs)

	c.LogLevel = cf.String("log.level", "info")
	c.Language = cf.String("general.language", "")

	var warn []string
	warn = append(warn, c.validate()...)
	for _, e := range errs {
		warn = append(warn, "ERROR: "+e.Error())
	}
	if l := i18n.Normalize(c.Language); c.Language != "" && !slices.Contains(i18n.Available(), l) {
		warn = append(warn, fmt.Sprintf("general.language = %s: no translation, English is used (available: %s)", c.Language, strings.Join(i18n.Available(), ", ")))
	}
	for _, u := range cf.Unused() {
		warn = append(warn, "unknown option (typo?): "+u)
	}
	for _, w := range warn {
		if strings.HasPrefix(w, "ERROR") {
			return c, warn, fmt.Errorf("invalid configuration: %s", w)
		}
	}
	return c, warn, nil
}

// validate returns warnings; entries starting with "ERROR" are fatal.
func (c *Config) validate() []string {
	w := append([]string(nil), c.alertWarn...)
	if c.SameHost {
		w = append(w, "NOTE: verify.same_host = true: checks may run on the computer that produced the result, only while a single client is active (one computer, e.g. Nacomline); with several clients it is ignored")
	}
	if c.MailEnabled {
		// e-mail on but incomplete: said at start, config validate/reload and doctor, not
		// only when the first alert fails (user)
		var miss []string
		if c.MailServer == "" {
			miss = append(miss, "alerts.smtp_server")
		}
		if c.MailFrom == "" {
			miss = append(miss, "alerts.from")
		}
		if len(c.MailTo) == 0 {
			miss = append(miss, "alerts.to")
		}
		if len(miss) > 0 {
			w = append(w, "WARNING: alerts.email_enabled = true but "+strings.Join(miss, ", ")+" not set: no alert e-mail can be sent (test with 'matriline-server test-alert')")
		}
	}
	if c.TelegramEnabled && (c.TelegramTokenFile == "" || c.TelegramChat == "") {
		w = append(w, "WARNING: alerts.telegram_enabled = true but telegram_token_file or telegram_chat is not set: no alert can go by Telegram (test with 'matriline-server test-alert')")
	}
	switch c.Mode {
	case wire.ModeAuth:
		w = append(w, "WARNING: network.security = auth: traffic is authenticated but NOT encrypted; anyone on the path can read inputs and results.")
	case wire.ModeNone:
		w = append(w, "WARNING: network.security = none: traffic is neither encrypted nor protected against tampering after the handshake. Use only on a trusted, isolated network.")
	}
	switch c.Enrollment {
	case "issued", "register":
	case "open":
		w = append(w, "WARNING: network.enrollment = open: ANY machine that knows the address and server key can join and receive your inputs.")
	default:
		w = append(w, "ERROR: network.enrollment must be issued, register or open")
	}
	switch c.Connection {
	case "direct":
	case "relay":
		if c.RelayAddr == "" {
			w = append(w, "ERROR: network.connection = relay requires network.relay_address")
		}
	default:
		w = append(w, "ERROR: network.connection must be direct or relay")
	}
	if c.OtherVersions != "refuse" && c.OtherVersions != "separate" && c.OtherVersions != "errors" {
		w = append(w, "ERROR: orca.other_versions must be refuse, separate or errors")
	}
	if c.OrcaCheck != "fingerprint" && c.OrcaCheck != "version" {
		w = append(w, "ERROR: orca.check must be fingerprint or version")
	}
	if c.Parallel != "downgrade" && c.Parallel != "honor" {
		w = append(w, "ERROR: tasks.parallel must be downgrade or honor")
	}
	if c.MaxProcs < 1 {
		w = append(w, "ERROR: tasks.max_procs must be >= 1")
	}
	switch c.UpdateMode {
	case "off", "alert", "auto":
	default:
		w = append(w, "ERROR: update.mode must be off, alert or auto")
	}
	if err := release.URLOK(c.UpdateURL); err != nil {
		w = append(w, "ERROR: update.url: "+err.Error())
	}
	if c.Scheduler != "order" && c.Scheduler != "learned" {
		w = append(w, "ERROR: tasks.scheduler must be order or learned")
	}
	if c.Order != "alphanumeric" && c.Order != "random" {
		w = append(w, "ERROR: tasks.order must be alphanumeric or random")
	}
	if c.FailurePolicy != "weird" && c.FailurePolicy != "quarantine" {
		w = append(w, "ERROR: verify.on_failure must be weird or quarantine")
	}
	if _, ok := palettes[c.WebPalette]; !ok {
		w = append(w, "ERROR: web.palette must be one of "+strings.Join(paletteNames(), ", "))
	}
	if c.MolBackground != "auto" && !hexColor(c.MolBackground) {
		w = append(w, "ERROR: web.mol_background must be auto or a colour like ffffff")
	}
	for k, v := range c.WebColors {
		if !hexColor(v) {
			w = append(w, "ERROR: web."+k+" must be a colour like f0f7f4 (six hexadecimal digits)")
		}
	}
	for k, v := range map[string]string{"hooks.result": c.HookResult, "hooks.done": c.HookDone} {
		if v != "" && !filepath.IsAbs(v) {
			w = append(w, "ERROR: "+k+" must be an absolute path")
		}
	}
	if c.AlertCommand != "" && !filepath.IsAbs(c.AlertCommand) {
		w = append(w, "ERROR: alerts.command must be an absolute path")
	}
	if c.HeartbeatSec < 5 {
		w = append(w, "ERROR: tasks.heartbeat_seconds must be >= 5")
	}
	if c.TaskTimeout < time.Duration(3*c.HeartbeatSec)*time.Second {
		w = append(w, "WARNING: tasks.task_timeout shorter than 3 heartbeats; tasks may be reassigned during brief network hiccups.")
	}
	if c.CompressLevel < 0 || c.CompressLevel > 9 {
		w = append(w, "ERROR: results.compress_level must be 0..9")
	}
	for _, f := range []float64{c.VerifySCF, c.VerifyGradient, c.VerifyHessian, c.CanaryRate, c.ReplicationRate} {
		if f < 0 || f > 1 {
			w = append(w, "ERROR: verification fractions/rates must be between 0 and 1")
			break
		}
	}
	return w
}

// defaultConfig is written by "matriline-server init". It documents every option.
const defaultConfig = `# =============================================================================
# Matriline server configuration
# =============================================================================
# This file has three parts:
#   BASIC         the few settings most servers change (start here)
#   ADVANCED      everything else, by section; the defaults suit most projects
#   EXPLANATIONS  what every option does, in the same order as ADVANCED
# Sections:  [general] language         [spool] working directory      [network] address, security, enrollment, bans
#            [tasks] queue, retries, scheduler, multi-core jobs
#            [orca] campaign version, checks of the clients' ORCA
#            [results] files returned        [report] what clients report while running
#            [verify] integrity checks, reputation, quarantine
#            [storage] disk limits          [alerts] e-mail or a program on events
#            [update] new Matriline versions [log] log level
# A section may appear in more than one part (its settings simply add up).
# Syntax: [section] headers, "key = value", comments start with '#'. Sizes accept
# KB/MB/GB/KiB/MiB/GiB or "unlimited"; durations 90s, 30m, 1h. All network policy is set
# here only: clients receive it when they connect and cannot override it.
# Most options apply while the server runs: "matriline-server config edit" checks the file
# and applies it (options marked [restart] need a restart).


# ================================ BASIC ======================================
[general]
# en: language of the menus, help, web page and alerts: "en", "es", "fr", "pt", "ar" (empty: this computer's)
# es: idioma de los menús, la ayuda, la página web y las alertas: "en", "es", "fr", "pt", "ar" (vacío: el de esta computadora)
# fr: langue des menus, de l'aide, de la page web et des alertes : "en", "es", "fr", "pt", "ar" (vide : celle de cet ordinateur)
# pt: idioma dos menus, da ajuda, da página web e dos alertas: "en", "es", "fr", "pt", "ar" (vazio: o deste computador)
# ar: لغة القوائم والمساعدة وصفحة الويب والتنبيهات: "en", "es", "fr", "pt", "ar" (فارغ: لغة هذا الحاسوب؛ الترجمة العربية آلية ولم تُراجَع بعد)
language =


[spool]
# working directory (holds input/ output/ ... state/); "." = this file's directory [restart]
root = .


[network]
# Port this server waits on for helpers. Open it in the firewall and, behind a router,
# forward it (port forwarding) to this computer [restart]. Example: 44100
listen = 44100

# Address helpers dial to reach this server: your public IP (or a domain name) and the
# port above. It is written into every credential: fill it in before "keys issue". With
# connection = relay it may stay empty. Examples: 203.0.113.7:44100   lab.example.org:44100
advertise =

# How the connection is protected: "tls" (recommended, everything encrypted), "auth" (not
# encrypted) or "none" (test networks only)
security = tls

# How they meet: "direct" (helpers reach this server on the port above) or "relay" (when you
# cannot open any port: both connect to a relay). Example: connection = "relay"
connection = direct

# Relay address:port, only with connection = relay. Example: relay.example.org:443
relay_address =

# Who may join: "issued" (you make one credential file per helper; recommended), "register"
# (the helper asks to join with a token and you approve) or "open" (DANGER: anyone who has
# the address)
enrollment = issued


[tasks]
# "order" (queue order) or "learned" (long jobs to fast clients)
scheduler = order

# Longest a calculation may run on one computer. When it is reached, the job stops and
# goes to errors/ with its orbitals so far, and 'retry' continues from them. 0 = no limit.
# Examples: 12h   3d
max_job_time = 1d


[orca]
# ORCA version of this campaign: every client must use it
version = 6.1.1

# "fingerprint" (clients' ORCA must match the references below) or "version" (trust them)
check = fingerprint

# a client with another ORCA version: "refuse", "separate" or "errors"
other_versions = refuse

# ORCA installation(s) on this computer, comma separated (init fills it in)
reference_paths = /opt/orca-6.1.1

# "builtin" = the official builds of this version (Linux, Windows, macOS); plus JSON files from "matriline-client fingerprint"
accepted_fingerprints = builtin


[verify]
# integrity checks of results (the options are under ADVANCED [verify])
enabled = true


[storage]
# disk space the project may use (unlimited, or e.g. 500G)
limit = unlimited


[alerts]
# Alerts reach you by e-mail, by Telegram, or both. Step by step for each: at the end of
# this file, [alerts]. Then try it: matriline-server test-alert
# --- by e-mail
email_enabled = false

# the mail server and port, e.g. smtp.gmail.com:587
smtp_server =

# the sending account (the one where you created the app password)
smtp_user =

# the file with that account's app password (full path; never the password here)
smtp_password_file =

# the sender shown (usually the same address)
from =

# who receives the alerts (comma separated)
to =

# --- by Telegram (first send /start to your bot from your Telegram)
telegram_enabled = false

# the file with the bot's token (full path; never the token here)
telegram_token_file =

# your chat id (a number; @userinfobot tells it)
telegram_chat =

# --- or a program of your own, run for each alert (event, message); empty = none
command =


[update]
# new Matriline versions: "off", "alert" (tell me; 'update apply' installs) or "auto".
# EXPERIMENTAL: not recommended for a project in progress (see EXPLANATIONS)
mode = off


[log]
# "debug", "info", "warn" or "error"
level = info


# =============================== ADVANCED ====================================
# The defaults suit most projects; each option is explained at the end of the file.


[spool]
keep_empty_dirs = input


[network]
accept_nosecurity_clients = false

credential_valid = 48h

blocked_ips =

max_sessions = 256

ban_garbage_after = 10

ban_garbage_window = 1h

ban_garbage_duration = 168h

ban_auth_after = 20

ban_auth_window = 24h

ban_auth_duration = 24h

ban_trust_known = true


[tasks]
reuse_checkpoints = true

heartbeat_seconds = 60

task_timeout = 1h

scan_interval = 5s

settle_time = 2s

order = alphanumeric

input_extensions = .inp

duplicate_when_idle = true

max_replicas = 2

orca_error_hosts = 2

max_attempts = 6

error_alert_after = 3

error_pause_after = 10

error_window = 24h

learned_min_samples = 30

parallel = downgrade

max_procs = 8


[orca]
accepted_tree_hashes =


[results]
include = *

exclude = *.tmp, *.tmp.*

max_bytes = 2G

compress_level = 6


[report]
fields = *

percentiles = 50, 90, 99

sample_seconds = 5


[verify]
fingerprints = true

output_consistency = true

timing_plausibility = true

scf_recheck_fraction = 0.05

gradient_check_fraction = 0.05

hessian_probe_fraction = 0.05

canary_rate = 0

replication_rate = 0

probation_results = 5

probation_replication = 0.2

probation_multiplier = 3

on_failure = weird

energy_tolerance = 1e-5

check_max_scf_iterations = 10

hessian_tolerance = 0.005

reputation = true

trusted_after = 50

trusted_multiplier = 0.5

recent_failure_window = 720h

recent_failure_multiplier = 2

second_opinion = true

second_opinion_unavailable = weird

retry_weird = true

rescue_weird = true

quarantine_after = 3

quarantine_window = 24h

quarantine_recheck = 10

quarantine_escalate_fraction = 0.25

quarantine_auto_release = true


[storage]
warn_percent = 90

min_free = auto


[alerts]
smtp_password_env = MATRILINE_SMTP_PASSWORD

# each alert: off, now (at once) or summary (in the scheduled summary)
# the disk is nearly full, or results are refused for lack of space
storage = now

# a result failed a check (it went to weird/)
verify_failed = now

# a client was quarantined (every result of it is checked)
quarantine = now

# a client stopped reporting the calculations it was running
client_lost = now

# a task was lost or duplicated ('check')
tasks = now

# every input is done: nothing queued, running or being checked
campaign_done = now

# a new Matriline version, or a problem installing it
update = now

# a task went to errors/ (ORCA failed), or a client was paused after errors
errors = summary

# a client with another ORCA version than this campaign's
orca_version = summary

# a new client joined with its credential
enrolled = summary

# an address was banned (port scanners, failed logins)
ban = summary

# the same alert sent "now" at most once per this (more go into the summary)
now_limit = 1h

# when the summary is sent: off, hourly, every 6h, daily 08:00, daily 08:00 20:00,
# weekdays 08:00, weekends 10:00, mon,thu 09:00, mon-fri 18:30 ...
summary = daily 08:00

# send the summary also when nothing happened ("all is well", with the project's state)
summary_when_quiet = false


[hooks]
result =

done =


[web]
palette = A

mol_rotation = 20s

mol_background = auto

white =

black =

gray =

dark =

light =


[update]
check_interval = 720h

url = https://github.com/agaloya/matriline/releases

client_wait = 24h


# ============================= EXPLANATIONS ==================================
#
# ---------------- [general] ----------------
# >> language
# en: The language of what people read: console menus, 'help', the web page, the alerts.
#   Empty: this computer's language (LANG, or the Windows display language); English when
#   there is no translation. Logs and error messages stay in English.
# es: El idioma de lo que se lee: menús de la consola, 'help', la página web, las alertas.
#   Vacío: el idioma de esta computadora; inglés si no hay traducción. El registro y los
#   mensajes de error quedan en inglés.
# fr: La langue de ce qu'on lit : menus de la console, 'help', la page web, les alertes.
#   Vide : la langue de cet ordinateur ; l'anglais s'il n'y a pas de traduction. Le journal
#   et les messages d'erreur restent en anglais.
# pt: O idioma do que se lê: menus do console, 'help', a página web, os alertas. Vazio: o
#   idioma deste computador; inglês se não houver tradução. O log e as mensagens de erro
#   ficam em inglês.
#
# ---------------- [spool] ----------------
# >> root
# Working directory. Holds input/ output/ completed/ cancelled/ paused/ weird/ errors/
# plus the internal state/ directory. Relative paths are relative to this file.
#
# >> keep_empty_dirs
# Spool directories whose empty sub-directories are kept (comma separated). Elsewhere a
# sub-directory left empty when its last task moves on is removed. input = your own
# structure for new batches stays; e.g. "input, output" keeps output/'s too.
# ---------------- [network] ----------------
# >> accept_nosecurity_clients
# accept_nosecurity_clients: give tasks to clients built with "-tags nosecurity" (no
# sandbox, no process audit; meant for one's own trusted machines). Off: they are told so
# and get no task.
# >> listen
# TCP addresses to listen on [restart]. Default port 44100 (unassigned by IANA).
# A bare port listens on every address of this computer. To listen on one address only
# (a computer with several networks), write address:port, e.g. 192.168.1.10:44100.
# Several entries are separated by commas, e.g. 44100, 443.
# In the most restrictive networks only outbound TCP/443 is allowed; adding "443"
# (needs privileges on most systems) lets such clients reach a directly reachable server.
# >> advertise
# Address (host:port) written into credential files issued to clients. Leave empty to
# be asked when issuing keys.
# >> security
# Security mode for ALL connections:
#   tls  - (default, recommended) TLS 1.3, both sides authenticated, everything encrypted.
#   auth - every message authenticated and protected against modification and replay,
#          but NOT encrypted: anyone on the network path can read inputs and results.
#   none - no protection at all after the initial identity check. DANGER: an attacker on
#          the path can read and alter everything. Only for isolated test networks.
# >> connection, relay_address
# How clients reach this server:
#   direct - clients connect to this machine. The server needs ONE reachable TCP port
#            (forward it on your router if the server is behind NAT). Clients never
#            need port forwarding.
#   relay  - the server and the clients both connect OUT to a relay with a public IP
#            (matriline-relay). Nobody needs port forwarding. The relay only forwards the
#            end-to-end protected stream and cannot read it (it can read nothing useful
#            with security = tls; with auth/none it CAN read the plaintext).
# >> enrollment
# Who may join:
#   issued   - (default) the admin creates a credential file per user with
#              "matriline-server keys issue <name>" and hands it over. The file is a
#              one-time ticket: on its first connection the client creates its own device
#              key (it never leaves that computer) and the file stops working, so a copy
#              stolen later gives no access. Revocable anytime.
#   register - users generate their own key and join with a one-time token
#              ("matriline-server keys token"); the admin approves them
#              ("matriline-server clients approve <id>").
#   open     - DANGER: any machine knowing the address and server key may join and will
#              receive your input files.
# >> credential_valid
# How long a credential from "keys issue" can be used for its single enrollment.
# >> blocked_ips
# IP addresses refused before any handshake (comma separated), permanently.
# >> max_sessions
# Most clients connected at once; further connections are refused until one ends (a
# limit against a flood of connections; raise it for a campaign with more helpers).
# >> ban_garbage_after, ban_garbage_window, ban_garbage_duration
# Automatic bans (list them with "matriline-server bans", lift one with
# "matriline-server bans lift <address>"). 0 in an *_after option disables that rule.
# Connections that never speak the Matriline protocol (port scanners, other protocols,
# garbage, empty connections): this many within the window ban the address.
# >> ban_auth_after, ban_auth_window, ban_auth_duration
# Connections that speak the protocol but are not admitted (unknown, wrong or revoked key,
# bad token). Honest clients can hit this occasionally (an old credential, a link dropping
# mid-handshake), so the allowance is larger and the ban temporary. Reconnections of
# admitted clients never count.
# >> ban_trust_known
# Never ban automatically an address from which a registered client connected within the
# last 24 h: on a shared NAT (an office, a hotel, a mobile carrier) an attacker could otherwise
# get a legitimate helper locked out. Its failed attempts are still refused and alerted.
#
# ---------------- [tasks] ----------------
# >> reuse_checkpoints
# Privacy of inputs: clients see the molecule and the method of what they compute (they must,
# to run ORCA); real file names, sub-directories and who submitted them are never sent.
# Not implemented, a suggestion for whoever extends this code: an option to keep sensitive
# inputs (e.g. a sub-directory such as input/private/) only for chosen clients, and, on
# hardware that offers it (AMD SEV-SNP, Intel TDX), attested confidential VMs whose memory
# the computer's owner cannot read (docs/DECISIONS.md D44).
# reuse_checkpoints: when a host fails a task (machine failure or timeout, not an ORCA
#   error), keep the orbitals (.gbw) it reached and give them to the next host: ORCA starts
#   from them by itself (AutoStart reads <input name>.gbw), so a long SCF does not start
#   over and the input is not changed. Only from trusted hosts (active, past probation, no
#   failed check): a forged .gbw cannot change the method but could steer the SCF to
#   another self-consistent solution (e.g. a broken-symmetry state of a radical).
# >> heartbeat_seconds
# Clients send a heartbeat this often (seconds).
# >> task_timeout
# A running task that is not reported by its client for this long is given to another
# host (the first valid result still wins if the original client comes back later).
# >> scan_interval, settle_time
# How often input/ is rescanned for new files, and how long a new file must stay
# unchanged before it is queued (protects against half-copied files).
# >> order, input_extensions
# Order in which inputs are served: alphanumeric (natural sort of sub-directories and
# files, so 1list, 2list, ... 10list are followed) or random. Explicit priorities set with
# "matriline-server priority" always win.
# >> duplicate_when_idle, max_replicas
# When no queued task is left, running tasks may be duplicated on idle hosts so a very
# slow machine does not delay the end of a campaign. First verified result wins.
# >> orca_error_hosts
# A task goes to errors/ after ORCA itself fails on this many DIFFERENT hosts.
# Machine problems (power cut, out of memory, disconnect) do not count.
# >> max_attempts
# Absolute limit of attempts per task (any cause) before errors/.
# >> max_job_time
# Longest run of one job on one computer (jobs sent from then on). The helper stops the
# job when it is reached and sends back what it has; the task goes to errors/ with its
# orbitals (.gbw) and a note (verdict) saying how to continue: raise the limit, then
# 'matriline-server retry errors/<task>', and the next computer starts from those
# orbitals. A helper's own limits.max_task_time, when shorter, only passes the task on.
# >> error_alert_after, error_pause_after, error_window
# Errors of one client (ORCA errors or machine failures, not cheating):
# error_alert_after: alert the admin after this many errors in a row (0 = off).
# error_pause_after: pause the client (no new tasks until "clients release <name>") after
#   this many errors within error_window, e.g. a computer that cannot run some kind of job.
# >> scheduler, learned_min_samples
# scheduler: which queued task a client gets, within the highest priority level:
#   order   - (default) the order above.
#   learned - the server learns from its accepted results how long each kind of job
#             takes (method, basis, size, Opt/Freq, open shell) and how fast each client
#             is, then gives the longest predicted jobs to the fastest clients and the
#             shortest to the slowest (shorter campaigns on mixed hardware). It needs
#             learned_min_samples results first; until then, and for clients or inputs it
#             cannot rate, it falls back to "order". See "matriline-server model".
# >> parallel, max_procs
# parallel: inputs that ask for several cores ("%pal nprocs N", "! PALN"):
#   downgrade - (default) they run on one core, like every other job (several independent
#               jobs use the cores better: measured, 2 cores make one job ~1.25x faster).
#   honor     - they run with N MPI processes (at most max_procs) on a client that accepts
#               jobs that big (its resources.max_cores_per_job) and has N cores free; until
#               one has, the queue serves the next tasks.
#
# ---------------- [orca] ----------------
# >> version
# ORCA version of this campaign: every client must use it. Set it before the first inputs;
# if it changes later the server warns (log, alert, ledger), since output/ then mixes
# results of two versions.
# >> check
# check: how a client's ORCA is checked.
#   fingerprint - (default) every program a result ran must belong to one of the reference
#                 builds below: put here the ORCA of every operating system and architecture
#                 your clients use (Linux x86-64, Linux arm64, Windows, macOS), unpacked;
#                 the server only hashes their files, it does not run the foreign ones.
#   version     - trust the clients' installations: only the version and build that ORCA
#                 writes in each output (and the manifest) must be this campaign's.
# >> other_versions
# other_versions: what to do with a client that does not have this campaign's version.
#   refuse   - (default) no tasks for it; it is told which version to install, and the
#              admin gets an alert (orca_version).
#   separate - it computes with its own version; its results count as done but are kept
#              apart, in other-versions/<version>/output|weird|errors, never in output/.
#              They are NOT cross-checked (a check by another version would compare two
#              programs), so a client could claim an old version to skip the checks: use
#              it only with clients you trust.
#   errors   - it computes with its own version, its results go to
#              errors/other-versions/<version>/ for reference, and the input stays queued
#              for a client with this campaign's version.
# >> reference_paths
# Reference installation(s) on this machine used to compute the expected fingerprints.
# Several paths are allowed, e.g. the x86-64 and the arm64 builds of the same version
# (unpacked here; a build for another architecture is fingerprinted but cannot be run).
# >> accepted_fingerprints
# Accepted installations by their full fingerprint, comma separated. "builtin" = the
# official ORCA builds of orca.version known to this program (6.1.1: Linux x86-64 and
# arm64, Windows x86-64, macOS x86-64 and arm64): hashes only, so this server needs no
# ORCA at all (reference_paths may stay empty; the clients compute every check). Other
# builds: JSON files made on a client with "matriline-client fingerprint". Better than accepted_tree_hashes: the
# server can then check every program a result ran, also for builds it cannot run here or
# that print no GIT hash (the official arm64 ORCA 6.1.1 build).
# >> accepted_tree_hashes
# Extra accepted installation fingerprints (tree hashes), comma separated, e.g. when the
# server machine has no ORCA installed.
#
# ---------------- [results] ----------------
# >> include, exclude
# Which files produced by ORCA are sent back (glob patterns). Default: everything except
# ORCA's scratch files (*.tmp and *.tmp.N: integrals and intermediates, often GBs, useless
# afterwards; a failed DLPNO job left 6.7 GB of *.tmp.0 in the lab). Clients delete what
# is not returned. To receive them anyway, leave exclude empty.
# Example to save bandwidth: include = *.out, *.property.txt, *.xyz
# >> max_bytes
# Refuse results larger than this (protects your disk from malicious clients). Large
#   enough for ordinary results (.gbw, .hess, trajectories); ORCA scratch is excluded above.
# >> compress_level
# Compression of transferred files: 0 = none ... 9 = smallest (more CPU). 6 is balanced.
#
# ---------------- [report] ----------------
# >> fields
# Fields of the per-run metadata report (matriline.report) collected from clients.
# "*" = all. Prefixes ending with '*' select groups, e.g.: time.*, cpu.*, mem.*, net.*
# >> percentiles
# Percentiles reported for sampled series (temperatures, memory, RTT, ...).
# >> sample_seconds
# Sampling period of client telemetry while a job runs (seconds).
#
# ---------------- [verify] ----------------
# >> enabled
# Not implemented, a suggestion for whoever extends this code: hardware attestation. On
# CPUs with confidential computing (AMD SEV-SNP, Intel TDX) a client could run ORCA in an
# attested confidential VM whose memory the computer's owner cannot read or change, and
# send a hardware-signed report that exactly that image ran; results from such clients
# could then need fewer checks below, and their inputs stay private (docs/DECISIONS.md D44).
# Integrity verification. Every result is always signed by its client and recorded in the
# server's hash-chained ledger (that identifies who produced it). The defaults turn on
# every layer that costs no computation and check a 5 % sample with the cheap active
# checks: in the lab attack rounds this caught every forgery for well under 1 % of extra
# compute (docs/SECURITY_TESTS.md). A quarantined client keeps computing, but EVERY result
# it returns is checked.
# Every other layer costs extra computation; fractions are 0..1 (1 = every result).
#
# enabled: master switch. false = no integrity layer at all, whatever the options below
#   say (for computers you fully trust); results stay signed. Results are still
#   checked for ORCA errors and unconverged optimizations. true = apply the options below.
# >> fingerprints
#
# fingerprints: the client hashes the whole ORCA installation and every program it
#   actually ran. Detects wrong versions and corrupted installs. A deliberately modified
#   client can lie, so this mainly catches honest mistakes.
# >> output_consistency
# output_consistency: parse the ORCA output: version and GIT hash, normal termination,
#   echoed input identical to the task input, expected modules. Free, recommended.
# >> timing_plausibility
# timing_plausibility: the run time claimed by the client and printed by ORCA cannot exceed
#   the time since the task was assigned (+2 min). Flags outputs copied from elsewhere. Free.
# >> scf_recheck_fraction
# scf_recheck_fraction: recompute the energy from the returned orbitals (.gbw) with ONE
#   SCF iteration on another host: a genuine converged result reproduces the energy and
#   converges immediately. Cost ~5% of an SCF. Requires .gbw files to be returned.
# >> gradient_check_fraction
# gradient_check_fraction: for optimizations, compute the gradient at the final geometry
#   on another host; it must be ~0. Cost ~one gradient.
# >> hessian_probe_fraction
# hessian_probe_fraction: for frequencies, compare H*v from the returned Hessian with a
#   finite difference of two gradients along a secret random direction v.
# >> canary_rate
# canary_rate: per real task assigned, probability of also sending that client a task whose
#   answer is known (a verified past result, randomly rotated, translated and reordered so
#   it cannot be recognized). Cost: canary_rate extra single points per task.
# >> replication_rate
# replication_rate: fraction of tasks also run on a second, different host and compared.
# >> probation_results
# New clients get "probation_multiplier" times more checks for their first N results.
# >> probation_replication, probation_multiplier
# probation_replication: during probation, also recompute this fraction of the client's
#   results on another host (the generic check for job types the cheap checks cannot rebuild).
# >> on_failure
# What happens when a check fails: weird (result goes to weird/, client keeps working)
# or quarantine (also stop sending tasks to that client and re-check its past results).
# >> energy_tolerance
# Maximum energy difference (Hartree) accepted when comparing two computations (replicas).
# Honest hosts differ by up to ~2e-6 Eh (UKS + RIJCOSX seen in the lab); 1e-5 Eh is
# 0.006 kcal/mol, far below any chemically useful forgery (2e-4 Eh was caught).
# >> check_max_scf_iterations
# check_max_scf_iterations: SCF iteration cap of the scf/gradient checks, which restart from
#   the returned orbitals (genuine ones converge in a few cycles; 4 were seen for a phenol).
#   A check that does not converge within the cap is INCONCLUSIVE, not a proof of cheating:
#   the result goes to errors/ with a verdict naming this option, so you can raise it and
#   use "retry". Higher = fewer inconclusive checks, slightly more compute per check.
# >> hessian_tolerance
# hessian_tolerance: largest relative mismatch ||H v - finite difference|| accepted by the
#   Hessian probe. Honest runs measured in the lab: HF 0.00004, PBE 0.0006, B3LYP/RIJCOSX
#   0.0018 (numerical grids). A Hessian inflated by x % gives about x/100, so 0.005 catches
#   frequencies shifted by more than ~0.25 % (0.02 let a 2 % forgery through).
# >> reputation, trusted_after, trusted_multiplier, recent_failure_window, recent_failure_multiplier, second_opinion
# second_opinion: when every sub-check of a result ran on ONE host, repeat the cheapest
#   one (SCF, gradient or replica) on another independent host before accepting it, and
#   treat verifiers that disagree as a dispute (weird/, nobody penalized). Defends
#   against two colluding clients approving each other's forged results (lab test: 6 of
#   12 forgeries passed without it). Cost: one cheap extra check for single-host checks.
# Reputation (after probation): a client with trusted_after checks passed and no failed
#   check within recent_failure_window gets its check fractions multiplied by
#   trusted_multiplier (never zero: sampling stays unpredictable); a client with a failed
#   check within that window gets recent_failure_multiplier instead.
# >> second_opinion_unavailable
# second_opinion_unavailable: what to do with a result checked on a single host when no
#   other independent host can give the second opinion (none connected, or it expired):
#   weird (default, chosen by the user) sends it to weird/ for a tie-break or a
#   recomputation; accept keeps it (it did pass one check). On a server with fewer than
#   three clients there is never a third host: there, "weird" sends almost every checked
#   result to weird/ (a warning is logged), so small setups may prefer accept.
# >> retry_weird
# retry_weird: when a result ends in weird/, put its input back at the END of the queue
#   (once per task), excluding the client that produced it; the suspicious result stays in
#   weird/ for review. false = leave the task undone (an alert says so).
# >> rescue_weird
# rescue_weird: a result in weird/ WITHOUT proof of cheating (verifiers disagree, or a
#   failed check nobody could confirm) first gets one cheap tie-break check (SCF re-check,
#   or a replica without orbitals) on a host that took no part; if it passes, the result
#   goes back to output/ and the job is not recomputed. Otherwise retry_weird applies.
#   Confirmed forgeries (two hosts against the producer) are never rescued.
# >> same_host
# Checks may run on the computer that produced the result. Only for a single computer
# (Nacomline): there they can catch a faulty machine (bad memory, overheating), never a
# cheater. Default false: a result is always checked on another computer.
# >> quarantine_after, quarantine_window, quarantine_recheck
# quarantine_after: a client with this many failed verifications within quarantine_window
#   is quarantined automatically: it keeps computing, every result it returns is checked,
#   and the admin is alerted ("clients drain" stops its tasks altogether). Isolated
#   failures are tolerated. 0 = never automatically.
# quarantine_recheck: on any quarantine (automatic or by the admin), re-verify this many of
#   the client's most recent accepted results on independent hosts (0 = none).
# >> quarantine_escalate_fraction
# quarantine_escalate_fraction: if a re-checked result is bad, re-check this fraction of ALL
#   the client's accepted results (a random sample; 1 = every one, 0 = no escalation).
# >> quarantine_auto_release
# quarantine_auto_release: release the client automatically when its re-checks are all
#   correct (detection can be wrong; nobody has to unblock clients by hand). Bad results
#   stay in weird/ and their tasks are retried either way.
#
# ---------------- [storage] ----------------
# >> limit, warn_percent
# Maximum disk space for the spool (output/ etc.). unlimited by default. New results are
# refused (clients keep them and retry later) when the limit is reached.
# >> min_free
# Keep at least this much free on the disk holding the project: a result that would leave
# less (counting the uploads in progress) is refused and the client retries later. Stops
# anyone with a key, or a run-away job, from filling the disk. auto = 5 % of the disk, at
# most 5G (a fixed 5G refused every result on a small 8 GB disk in the lab); 0 = off.
#
# ---------------- [alerts] ----------------
# >> email_enabled, smtp_server, smtp_user, smtp_password_file, from, to, smtp_password_env
# Alerts by e-mail, sent by SMTP (any mail account works; no Google Cloud project or OAuth
# is needed). Never write the password in this file: it goes in smtp_password_file (a file
# with only the password, readable only by you). smtp_password_env names an environment
# variable instead (a service does not see the variables of your terminal: use the file).
#
# Step by step (Gmail; other providers are alike, see their "SMTP" help page):
#   1. Use an account for the alerts (a new one is a good idea) and turn on its 2-Step
#      Verification: https://myaccount.google.com/security
#   2. Go straight to https://myaccount.google.com/apppasswords (the menus hide it), write
#      "Matriline" and press Create. Google shows 16 letters once.
#   3. Save them in a file only you can read, without them appearing on screen:
#        Linux/macOS: umask 077; read -rs -p "App password: " p; printf '%s' "$p" > ~/.keys/matriline-mail; unset p
#        Windows (PowerShell): $p = Read-Host -AsSecureString "App password";
#          [Net.NetworkCredential]::new('', $p).Password | Set-Content -NoNewline $HOME\matriline-mail
#   4. In [alerts] at the top: email_enabled = true, smtp_server = smtp.gmail.com:587,
#      smtp_user = the address of the account where you created the app password (another
#      account's address is refused: "Username and Password not accepted"),
#      smtp_password_file = the full path of the file
#      (e.g. /home/<you>/.keys/matriline-mail), from = that address, to = who receives
#      the alerts.
#   5. matriline-server config reload, then matriline-server test-alert: a test e-mail must
#      arrive (look in the spam folder the first time).
# If Google says the option "is not available for your account": 2-Step Verification is
# off, or the account belongs to an institution that blocks app passwords (use another
# account, or Telegram). Other providers: Yahoo smtp.mail.yahoo.com:587 (app password), a
# company or institution mail server: ask its IT. The connection is encrypted (STARTTLS).
# >> telegram_enabled, telegram_token_file, telegram_chat
# Alerts by Telegram, from a bot of yours (the easiest way). Step by step:
#   1. In Telegram, open @BotFather, send /newbot, give the bot a name (e.g. "My Matriline
#      alerts") and a user name that ends in "bot". BotFather answers with a token.
#   2. Save the token in a file only you can read (as in step 3 of e-mail above), e.g.
#        umask 077; read -rs -p "Bot token: " t; printf '%s' "$t" > ~/.keys/matriline-telegram; unset t
#   3. Open your new bot in Telegram (search its user name) and send it /start. Without
#      this, a bot cannot write to you.
#   4. Your chat id: open @userinfobot in Telegram and send /start; it answers with your
#      Id (a number).
#   5. In [alerts] at the top: telegram_enabled = true, telegram_token_file = the full path
#      of the token file, telegram_chat = your Id.
#   6. matriline-server config reload, then matriline-server test-alert: the bot writes to
#      you. For a group: add the bot to the group, and use the group's id (negative).
# >> command
# test-alert (or 'alerts test') sends a test alert at once through every channel that is
# on, and says which worked. Alert program: run for each alert (same events and limit) with two arguments, the event
# and the message, and no shell; e.g. a script that sends it by Telegram or ntfy. (Telegram
# is built in: telegram_enabled.)
# An absolute path; empty = off. It may run 30 s at most.
# >> storage, verify_failed, quarantine, client_lost, tasks, campaign_done, update, errors, orca_version, enrolled, ban
# Every alert, each off, now or summary. now: e-mailed (and given to the alert command) at
# once; the same alert at most once per now_limit, the rest go into the summary. summary:
# kept and sent together on the summary's schedule. off: only in state/events.log. Files
# written before these options ("events = ..." and "digest = ...") keep their meaning.
# >> now_limit
# The same alert sent "now" at most once per this interval (default 1h).
# >> summary
# When the summary is sent, in plain words: off; hourly; every 30m, every 6h (from
# midnight); daily 08:00 (several times: daily 08:00 20:00); weekdays 08:00; weekends
# 10:00; mon,thu 09:00; mon-fri 18:30; weekly mon 09:00. On this computer's clock. Alerts
# kept when the server stops are sent then.
# >> summary_when_quiet
# true: the summary is sent even when no alert happened, with the project's state (a
# daily "all is well"); false: only when there is something to report.
#
# ---------------- [hooks] ----------------
# Programs run when something happens, to move, copy, upload or chain results (an absolute
# path; empty = none). They run one at a time, without a shell, at most 10 minutes each;
# a failure is logged and noted in state/events.log.
# >> result
# Run for each result that arrives in, or moves to, output/, weird/ or errors/, with the
# arguments: result <task> <output|weird|errors> <result folder> (also in the environment:
# MATRILINE_EVENT, MATRILINE_TASK, MATRILINE_VERDICT, MATRILINE_RESULT_DIR). Example: a
# script that copies output/ results to a cloud folder, or writes the next input of a chain
# (e.g. a frequency job from an optimized geometry) into input/.
# >> done
# Run when the queue empties (every input done, nothing running): done <summary>.
#
# ---------------- [web] ----------------
# >> palette
# Colours of the web page: A (default) or B, or their dark versions A-dark and B-dark.
# Each palette has five colours.
# >> mol_rotation
# The 3D view of a molecule turns by itself once per this time (default 20s); 0 = it
# stays still (it can always be turned with the mouse).
# >> mol_background
# Background of the 3D view: auto (black with a dark palette, white with a light one) or
# a colour (six hexadecimal digits).
# >> white, black, gray, dark, light
# Replace one colour of the palette (six hexadecimal digits, e.g. f0f7f4; empty = the
# palette's): white is the background, black the text on it (text on black is white),
# gray lines and secondary text, dark the selected tab, the chosen option and the
# buttons that can be used, light the options not chosen and what cannot be used.
# Applied when 'matriline-server web' starts.
#
# ---------------- [update] ----------------
# >> mode
# EXPERIMENTAL, not recommended for a project in progress: there is no migration of the
# configuration or of the folders yet, so a version that changed their format could
# disorganize the project. Safer: update by hand between projects (INSTALL.md).
# off: never looks. alert: once per check_interval the server looks for a new release and
# tells you (the 'update' alert of the alerts section); 'matriline-server update
# apply' installs it. auto: it also installs it by itself. Installing happens only if every
# client can update (each one says so when it connects; 'update status' lists those that
# cannot), clients first (each between jobs), then this server, which restarts. Only
# releases signed by the Matriline maintainer's key, built into the programs, are
# installed: not even someone controlling the release page or this server can make a
# client install anything else. The previous program stays as <program>.previous.
# >> check_interval
# How often to look (default 720h, 30 days).
# >> url
# Where the signed releases are: GitHub's releases page (or a mirror with its layout:
# latest/download/release.json, download/v<version>/<program>). Clients download from
# their own update_url, not from here.
# >> client_wait
# A client still on the old version after this long (it waits for its jobs to end) is
# reported; the server keeps waiting for it before installing its own.
#
# ---------------- [log] ----------------
# >> level
# debug, info, warn, error
`
