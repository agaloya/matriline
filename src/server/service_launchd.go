package main

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agaloya/matriline/common/lock"
)

// macOS: a per-user LaunchAgent (~/Library/LaunchAgents/<label>.plist, launchd.plist(5)),
// the same as the client's (client/service_launchd.go, tested on a Mac). It starts when
// the user logs in and is started again 30 s after a failure (KeepAlive with SuccessfulExit
// = false, like systemd's Restart=on-failure); a server that ends normally ('stop',
// SIGTERM) stays stopped until the next login. The label holds the hash of the
// configuration's path: one service per project.

// launchdLabel is the LaunchAgent's label (and plist file name) for a configuration.
func launchdLabel(cfgPath string) string { return "org.matriline." + serviceUnit(cfgPath) }

// launchdPlist is the LaunchAgent's property list.
func launchdPlist(label, self, cfgPath, logPath string) string {
	x := html.EscapeString // XML text: & < > " '
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + x(label) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + x(self) + `</string>
		<string>-c</string>
		<string>` + x(cfgPath) + `</string>
		<string>run</string>
	</array>
	<key>WorkingDirectory</key>
	<string>` + x(filepath.Dir(cfgPath)) + `</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>StandardOutPath</key>
	<string>` + x(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + x(logPath) + `</string>
</dict>
</plist>
`
}

// launchdDomain is where the agent is loaded: the login session (gui/<uid>) when there is
// one, otherwise the user's background domain (user/<uid>, e.g. over SSH).
func launchdDomain() string {
	uid := strconv.Itoa(os.Getuid())
	if exec.Command("launchctl", "print", "gui/"+uid).Run() == nil {
		return "gui/" + uid
	}
	return "user/" + uid
}

func serviceLaunchd(action, self, cfgPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	stateDir := filepath.Join(cfg.Root, dState)
	label := launchdLabel(cfgPath)
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	domain := launchdDomain()
	if action == "remove" {
		if _, err := os.Stat(plist); os.IsNotExist(err) {
			fmt.Println("no service is installed for " + cfgPath)
			return nil
		}
		runCmd("launchctl", "bootout", domain+"/"+label) // stops the server (SIGTERM)
		if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("removed the service; the server no longer starts by itself")
		return nil
	}
	// a server already running for this project (from a terminal) holds the lock: the
	// agent's server would stop at once and launchd would start it again every 30 s
	// (once the agent is installed, the server running is its own: installing again is fine)
	if _, err := os.Stat(plist); err != nil && lock.Held(stateDir, "server.lock") {
		return errors.New("a server is already running with " + cfgPath + ": stop it first (Ctrl+C in its terminal, or 'matriline-server stop'), then install the service")
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(stateDir, "service.log")
	if err := os.WriteFile(plist, []byte(launchdPlist(label, self, cfgPath, logPath)), 0o644); err != nil {
		return err
	}
	runCmd("launchctl", "bootout", domain+"/"+label) // an older version of this agent
	if err := runCmd("launchctl", "bootstrap", domain, plist); err != nil {
		if strings.HasPrefix(domain, "user/") {
			return fmt.Errorf("%v (log in to this Mac once, or run the command in its Terminal)", err)
		}
		return err
	}
	fmt.Printf("installed and started %s (launchd agent %s in %s)\n", plist, label, domain)
	fmt.Println("it starts when you log in; its output goes to " + logPath)
	fmt.Println("it runs while you are logged in to this Mac (System Settings > Users & Groups: automatic login keeps it running after a restart)")
	fmt.Println("stop it: launchctl bootout " + domain + "/" + label + "   remove it: matriline-server service remove")
	return nil
}
