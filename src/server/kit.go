package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/release"
)

// The helper kit (user): one file per helper, made by the server alone, holding a one-time
// credential, the client program and the admin's settings. The clients are the official
// ones of this server's own version, downloaded from its signed release (update.url, the
// same place and checks as updates), or taken from a folder (--clients, e.g. the output of
// tools/release.sh dist). No source code and no Go are needed.

//go:embed kit/kit-unix.sh
var kitUnixTemplate string

//go:embed kit/kit-windows.bat
var kitWindowsTemplate string

const kitDefaultSettings = `[resources]
cores = 1
memory_per_core = 1GiB

[schedule]
windows =
pause_on_battery = true

[limits]
cpu_temperature_limit = 0
end_date = never`

// kitUnixTargets: the clients a Linux/macOS kit carries, and the name its script looks for
// (uname -s and uname -m, aarch64 read as arm64)
var kitUnixTargets = [][3]string{
	{"linux", "amd64", "linux-x86_64"}, {"linux", "arm64", "linux-arm64"}, {"linux", "riscv64", "linux-riscv64"},
	{"darwin", "arm64", "darwin-arm64"}, {"darwin", "amd64", "darwin-x86_64"},
}

func cmdKit(cfgPath string, args []string) error {
	var pos []string
	uses, lang, settings, only, clients := 1, "en", kitDefaultSettings, "", ""
	for i := 0; i < len(args); i++ {
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", args[i])
			}
			i++
			return args[i], nil
		}
		var err error
		var v string
		switch args[i] {
		case "--uses":
			if v, err = next(); err == nil {
				uses, err = strconv.Atoi(v)
			}
		case "--language":
			lang, err = next()
		case "--settings":
			if v, err = next(); err == nil {
				var b []byte
				if b, err = os.ReadFile(v); err == nil {
					settings = strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
				}
			}
		case "--only":
			only, err = next()
		case "--clients":
			clients, err = next()
		default:
			pos = append(pos, args[i])
		}
		if err != nil {
			return err
		}
	}
	if len(pos) != 2 {
		return errors.New("usage: kit <helper name> <folder> [--language en] [--settings client-settings.conf] [--uses N] [--only unix|windows] [--clients folder]")
	}
	name, out := pos[0], pos[1]
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(name) {
		return errors.New("helper name: letters, digits, '_' and '-' only")
	}
	switch lang { // pasted into the scripts: only these
	case "", "en", "es", "fr", "pt", "ar":
	default:
		return fmt.Errorf("--language %q: en, es, fr, pt, ar or empty", lang)
	}
	if strings.ContainsAny(settings, `'"`) {
		return errors.New("settings: write them without quotes (they are pasted inside the scripts)")
	}
	if only != "" && only != "unix" && only != "windows" {
		return errors.New("--only: unix or windows")
	}
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "matriline-kit-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// the programs first, the credential last: a failed download leaves no issued
	// credential on the server
	get, err := kitClientSource(cfg, clients, work)
	if err != nil {
		return err
	}
	var unixBins [][]byte
	var winBin []byte
	if only != "windows" {
		for _, t := range kitUnixTargets {
			b, err := get(t[0], t[1])
			if err != nil {
				return err
			}
			unixBins = append(unixBins, b)
		}
	}
	if only != "unix" {
		if winBin, err = get("windows", "amd64"); err != nil {
			return err
		}
	}
	credPath := filepath.Join(work, name+".cred")
	issue := []string{"keys", "issue", name, credPath}
	if uses > 1 {
		issue = append(issue, "--uses", strconv.Itoa(uses))
	}
	o, err := call(cfgPath, issue)
	if err != nil {
		return fmt.Errorf("issuing the credential: %v", err)
	}
	fmt.Println(strings.TrimSpace(o))
	cred, err := os.ReadFile(credPath)
	if err != nil {
		return err
	}
	fill := func(t string) string {
		t = strings.ReplaceAll(t, "@@SETTINGS@@", settings)
		t = strings.ReplaceAll(t, "@@NAME@@", name)
		t = strings.ReplaceAll(t, "@@LANGUAGE@@", lang)
		return strings.Replace(t, "@@CRED@@", base64.StdEncoding.EncodeToString(cred), 1)
	}
	// the files hold a live credential: only this user may read them
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	if only != "windows" {
		var tgz bytes.Buffer
		gz := gzip.NewWriter(&tgz)
		tw := tar.NewWriter(gz)
		for i, t := range kitUnixTargets {
			if err := tw.WriteHeader(&tar.Header{Name: "matriline-client-" + t[2], Mode: 0o755, Size: int64(len(unixBins[i]))}); err != nil {
				return err
			}
			if _, err := tw.Write(unixBins[i]); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		f := filepath.Join(out, name+"-setup.sh")
		if err := writeKitFile(f, []byte(fill(kitUnixTemplate)+kitWrap(tgz.Bytes(), "\n")), 0o700); err != nil {
			return err
		}
		kitReport(f, "Linux and macOS")
	}
	if only != "unix" {
		// cmd.exe wants Windows line ends
		text := strings.ReplaceAll(strings.ReplaceAll(fill(kitWindowsTemplate), "\r\n", "\n"), "\n", "\r\n")
		f := filepath.Join(out, name+"-setup.bat")
		if err := writeKitFile(f, []byte(text+kitWrap(winBin, "\r\n")), 0o600); err != nil {
			return err
		}
		kitReport(f, "Windows")
	}
	fmt.Printf("Send the file for the helper's system to %s, privately: it holds a credential that whoever runs\nit first can use. Its settings are at the top of the file.\n", name)
	return nil
}

// writeKitFile writes a kit file only its owner can read, also over an older kit (whose
// mode os.WriteFile would keep; code review): it holds a credential.
func writeKitFile(f string, data []byte, mode os.FileMode) error {
	if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
		return err
	}
	fh, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = fh.Write(data)
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	return err
}

// kitClientSource returns a function that gives the client program for a system: from a
// folder with tools/release.sh's file names, or from this server version's signed release.
func kitClientSource(cfg *Config, dir, work string) (func(goos, goarch string) ([]byte, error), error) {
	if dir != "" {
		return func(goos, goarch string) ([]byte, error) {
			p := filepath.Join(dir, release.AssetName("client", goos, goarch))
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("%v (the folder must hold tools/release.sh's files)", err)
			}
			return b, nil
		}, nil
	}
	if !release.Enabled() {
		return nil, errors.New("this server was built without a release key, so it cannot check downloaded clients: give a folder of clients with --clients")
	}
	version := release.VersionOf(agentVersion)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	m, err := release.FetchVersion(ctx, cfg.UpdateURL, version)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("the signed release of this version (%s) from %s: %v\n(no published release yet, or no internet: build the clients with tools/release.sh dist and give that folder with --clients)", version, cfg.UpdateURL, err)
	}
	fmt.Printf("clients: the signed release %s (commit %s)\n", m.Version, m.Commit)
	return func(goos, goarch string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		p, err := release.Download(ctx, cfg.UpdateURL, m, release.AssetName("client", goos, goarch), work)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(p)
	}, nil
}

// kitWrap is base64 in lines of 76 characters.
func kitWrap(b []byte, eol string) string {
	s := base64.StdEncoding.EncodeToString(b)
	var sb strings.Builder
	for len(s) > 76 {
		sb.WriteString(s[:76] + eol)
		s = s[76:]
	}
	sb.WriteString(s + eol)
	return sb.String()
}

func kitReport(f, what string) {
	size := ""
	if st, err := os.Stat(f); err == nil {
		size = fmt.Sprintf(" (%d MB)", st.Size()>>20)
	}
	fmt.Printf("wrote %s%s: for %s\n", f, size, what)
}
