// Package logx is a tiny leveled logger writing to a file and stderr.
package logx

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// Logger writes leveled lines.
type Logger struct {
	l     *log.Logger
	level int
}

var levels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// MaxSize: a log file reaching it is renamed to <path>.1 (replacing the previous one) and
// a new one begins, so a log takes at most twice this (a project of 100000 molecules wrote
// about 56 MB of technical log; the full record of what happened is the ledger).
const MaxSize = 10 << 20

// rotating appends to a file and rotates it at MaxSize.
type rotating struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func (r *rotating) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(b)) > MaxSize {
		r.f.Close()
		os.Rename(r.path, r.path+".1")
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		r.f, r.size = f, 0
	}
	n, err := r.f.Write(b)
	r.size += int64(n)
	return n, err
}

// New opens (appends to) path; an empty path logs to stderr only.
func New(path, level string) (*Logger, error) {
	var w io.Writer = os.Stderr
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		st, _ := f.Stat()
		var size int64
		if st != nil {
			size = st.Size()
		}
		w = io.MultiWriter(&rotating{path: path, f: f, size: size}, os.Stderr)
	}
	lv, ok := levels[strings.ToLower(level)]
	if !ok {
		lv = 1
	}
	return &Logger{l: log.New(w, "", log.LstdFlags|log.Lmicroseconds), level: lv}, nil
}

// SetLevel changes the level ("debug", "info", "warn", "error").
func (l *Logger) SetLevel(level string) {
	if lv, ok := levels[strings.ToLower(level)]; ok {
		l.level = lv
	}
}

func (l *Logger) out(lv int, tag, f string, a ...any) {
	if lv < l.level {
		return
	}
	l.l.Printf("%s %s", tag, fmt.Sprintf(f, a...))
}

func (l *Logger) Debugf(f string, a ...any) { l.out(0, "DEBUG", f, a...) }
func (l *Logger) Infof(f string, a ...any)  { l.out(1, "INFO ", f, a...) }
func (l *Logger) Warnf(f string, a ...any)  { l.out(2, "WARN ", f, a...) }
func (l *Logger) Errorf(f string, a ...any) { l.out(3, "ERROR", f, a...) }
