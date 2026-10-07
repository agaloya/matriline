// Package projects remembers, per user, the server projects and client folders made on this
// computer, so the programs find them from any folder (user): with one, commands use it;
// with several, they ask which one. The lists are plain text files, one absolute path to a
// server.conf or client.conf per line, in the user's configuration folder:
// ~/.config/matriline (Linux), ~/Library/Application Support/matriline (macOS),
// %AppData%\matriline (Windows).
package projects

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// file is the list of one kind ("server" or "client").
func file(kind string) (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "matriline", kind+"s.txt"), nil
}

// Add records a configuration file (made by init). Programs that embed Matriline keep
// their own projects out of the list (MATRILINE_NO_REGISTRY, Nacomline).
func Add(kind, cfgPath string) error {
	if os.Getenv("MATRILINE_NO_REGISTRY") != "" {
		return nil
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	if strings.Contains(filepath.ToSlash(abs), "/.nacomline/") {
		return nil
	}
	list := List(kind)
	for _, p := range list {
		if p == abs {
			return nil
		}
	}
	return write(kind, append(list, abs))
}

// gone: the configuration file surely no longer exists (deleted or moved project). A
// folder that cannot be read right now (no permission, a disk not mounted yet) is not
// gone, and its project stays on the list (code review).
func gone(cfgPath string) bool {
	_, err := os.Stat(cfgPath)
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	_, perr := os.Stat(filepath.Dir(filepath.Dir(cfgPath)))
	return perr == nil
}

// List returns the recorded configuration files, without the ones whose folder was
// deleted or moved (those drop off the file the next time a project is added).
func List(kind string) []string {
	f, err := file(kind)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l == "" || gone(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// available: the recorded files that can be used right now (List keeps the ones that are
// only unreadable at the moment).
func available(kind string) []string {
	var out []string
	for _, p := range List(kind) {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func write(kind string, list []string) error {
	f, err := file(kind)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	text := strings.Join(list, "\n")
	if text != "" {
		text += "\n"
	}
	// a temporary file of its own: two inits at once must not write the same one
	tmp, err := os.CreateTemp(filepath.Dir(f), filepath.Base(f)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	return os.Rename(tmp.Name(), f)
}

// Resolve picks the configuration file a command uses when none was given (-c or the
// environment) and the current folder has none: the only recorded one, or, with several,
// the one the person chooses (ask is non-nil only when someone types at a terminal).
// It returns def unchanged when there is nothing to choose from.
func Resolve(kind, def string, ask io.Reader, out io.Writer, texts Texts) (string, error) {
	if _, err := os.Stat(def); err == nil {
		Add(kind, def) // a project made before the list existed joins it when used
		return def, nil
	}
	list := available(kind)
	switch {
	case len(list) == 0:
		return def, nil
	case len(list) == 1:
		return list[0], nil
	case ask == nil:
		return "", errors.New(texts.Several + "\n  " + strings.Join(list, "\n  "))
	}
	fmt.Fprintln(out, texts.Which)
	for i, p := range list {
		fmt.Fprintf(out, "  %d  %s\n", i+1, filepath.Dir(p))
	}
	fmt.Fprint(out, "> ")
	line, _ := bufio.NewReader(ask).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(list) {
		return "", errors.New(texts.Several + "\n  " + strings.Join(list, "\n  "))
	}
	return list[n-1], nil
}

// Texts are the messages of Resolve, in the program's language.
type Texts struct {
	Which   string // "Which project? (number and Enter)"
	Several string // "several projects on this computer: choose one with -c ..."
}
