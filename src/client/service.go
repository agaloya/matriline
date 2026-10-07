package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// 'matriline-client service install|remove': start the client by itself, without the
// user keeping a terminal open (a client started from a terminal or an SSH session ends
// with it; seen on Windows). Linux: a systemd user unit; Windows: a Task Scheduler task at
// logon, without a console window (conhost --headless); macOS: a launchd LaunchAgent.

const serviceName = "matriline-client"

// serviceUnit names this installation's service: one per configuration file, so that
// several clients on one computer each have their own (with one fixed name, installing a
// second one replaced the first; seen on Windows 11). A short hash of the configuration's
// absolute path keeps the name stable and valid for systemd and Task Scheduler.
func serviceUnit(cfgPath string) string {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		abs = cfgPath
	}
	sum := sha256.Sum256([]byte(abs))
	return serviceName + "-" + hex.EncodeToString(sum[:4])
}

func cmdService(cfgPath string, args []string) error {
	if len(args) == 0 || (args[0] != "install" && args[0] != "remove") {
		return errors.New("usage: service install | service remove")
	}
	if _, _, err := loadConfig(cfgPath); err != nil {
		return err
	}
	self := selfPath()
	if abs, err := filepath.Abs(cfgPath); err == nil { // the service does not start here
		cfgPath = abs
	}
	switch runtime.GOOS {
	case "linux":
		return serviceSystemd(args[0], self, cfgPath)
	case "windows":
		return serviceWindows(args[0], self, cfgPath)
	case "darwin":
		return serviceLaunchd(args[0], self, cfgPath)
	}
	return fmt.Errorf("not available on %s yet: start it with '%s -c %s run' (see INSTALL.md)", runtime.GOOS, self, cfgPath)
}

func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// legacyUnit is the unit installed under the old fixed name, if it runs this configuration.
func legacyUnit(home, cfgPath string) (string, bool) {
	old := filepath.Join(home, ".config", "systemd", "user", serviceName+".service")
	b, err := os.ReadFile(old)
	return old, err == nil && strings.Contains(string(b), cfgPath)
}

func serviceSystemd(action, self, cfgPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	name := serviceUnit(cfgPath)
	unit := filepath.Join(home, ".config", "systemd", "user", name+".service")
	if action == "remove" {
		if _, err := os.Stat(unit); os.IsNotExist(err) {
			// installed before names were per configuration: the old fixed name, but only if
			// it runs THIS configuration (it removed another server's service: lab services
			// test, the real server's unit)
			old, ok := legacyUnit(home, cfgPath)
			if !ok {
				fmt.Println("no service is installed for " + cfgPath)
				return nil
			}
			name, unit = serviceName, old
		}
		runCmd("systemctl", "--user", "disable", "--now", name)
		if err := os.Remove(unit); err != nil && !os.IsNotExist(err) {
			return err
		}
		runCmd("systemctl", "--user", "daemon-reload")
		fmt.Println("removed the service; the client no longer starts by itself")
		return nil
	}
	text := fmt.Sprintf(`[Unit]
Description=Matriline client (lends this computer's CPU to ORCA calculations)
After=network-online.target

[Service]
WorkingDirectory=%s
ExecStart=%q -c %q run
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`, filepath.Dir(cfgPath), self, cfgPath)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unit, []byte(text), 0o644); err != nil {
		return err
	}
	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := runCmd("systemctl", "--user", "enable", "--now", name); err != nil {
		return err
	}
	fmt.Printf("installed and started %s (systemd user service %s)\n", unit, name)
	fmt.Println("it starts when you log in; to keep it running while you are logged out: loginctl enable-linger $USER")
	fmt.Println("stop it: systemctl --user stop " + name + "   remove it: matriline-client service remove")
	return nil
}

func serviceWindows(action, self, cfgPath string) error {
	task := serviceUnit(cfgPath)
	if action == "remove" {
		if runCmd("schtasks", "/Query", "/TN", task) != nil {
			// installed before names were per configuration: the old fixed name, but only if
			// it runs THIS configuration
			out, err := exec.Command("schtasks", "/Query", "/TN", "Matriline client", "/XML").CombinedOutput()
			if err != nil || !strings.Contains(strings.ToLower(string(out)), strings.ToLower(cfgPath)) {
				fmt.Println("no service is installed for " + cfgPath)
				return nil
			}
			task = "Matriline client"
		}
		runCmd("schtasks", "/End", "/TN", task)
		if err := runCmd("schtasks", "/Delete", "/TN", task, "/F"); err != nil {
			return err
		}
		fmt.Println("removed the scheduled task; the client no longer starts by itself")
		return nil
	}
	// At boot, as this user, without a logon and without storing a password (/NP: Task
	// Scheduler's "S4U" logon), like the server: a computer lent full time computes after
	// a power cut or an update reboot with nobody logged in (lab: the at-logon task did
	// not come back). Creating it needs an administrator once; otherwise at logon.
	u, err := user.Current()
	if err != nil {
		return err
	}
	when := "when the computer starts (nobody needs to log in)"
	tr := fmt.Sprintf(`"%s" -c "%s" run`, self, cfgPath)
	if err := runCmd("schtasks", "/Create", "/TN", task, "/TR", tr, "/SC", "ONSTART", "/RU", u.Username, "/NP", "/RL", "LIMITED", "/F"); err != nil {
		// conhost --headless runs the console program without a window (Windows 10 1809
		// and later); the task starts at logon with the user's normal rights
		fmt.Printf("note: a task at start-up needs an administrator (%v);\n  this one starts when you log in. For start-up, run this command again in an administrator PowerShell.\n", err)
		when = "when you log in"
		tr = fmt.Sprintf(`conhost.exe --headless "%s" -c "%s" run`, self, cfgPath)
		if err := runCmd("schtasks", "/Create", "/TN", task, "/TR", tr, "/SC", "ONLOGON", "/RL", "LIMITED", "/F"); err != nil {
			return err
		}
	}
	if err := runCmd("schtasks", "/Run", "/TN", task); err != nil {
		return err
	}
	fmt.Printf("installed and started the scheduled task %q: the client starts by itself %s\n", task, when)
	fmt.Println("stop it: schtasks /End /TN \"" + task + "\"   remove it: matriline-client service remove")
	return nil
}
