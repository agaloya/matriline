package main

import (
	"strings"
	"testing"
)

// pickTree nests the suggestions under their folders (added when missing), folders first.
func TestPickTree(t *testing.T) {
	top := pickTree([]string{"input/b.inp", "input/set1/", "input/set1/x/deep.inp", "input/a.inp", "errors/"})
	var show func(ns []*pickNode, depth int) string
	show = func(ns []*pickNode, depth int) string {
		var b strings.Builder
		for _, n := range ns {
			b.WriteString(strings.Repeat(" ", depth) + n.Path)
			if n.Dir {
				b.WriteString("/")
			}
			b.WriteString("\n" + show(n.Kids, depth+1))
		}
		return b.String()
	}
	want := "input/\n set1/\n  set1/x/\n   input/set1/x/deep.inp\n input/a.inp\n input/b.inp\nerrors/\n"
	want = strings.NewReplacer(" set1/\n", " input/set1/\n", "  set1/x/", "  input/set1/x/").Replace(want)
	if got := show(top, 0); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if !top[0].Open || top[0].Kids[0].Open {
		t.Error("only the top level starts open")
	}
}
