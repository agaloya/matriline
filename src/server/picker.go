package main

import (
	_ "embed"
	"net/http"
	"sort"
	"strings"
)

// The path pickers of the command forms: under a path field, the spool folders it can
// name as a scrollable tree (folders open and close, <details>); picker.js puts a clicked
// name in the field and marks it. The field can still be typed in.

//go:embed picker.js
var pickerJS []byte

type pickNode struct {
	Name, Path string
	Dir, Open  bool
	Kids       []*pickNode
}

// pickTree turns suggestions (a folder ends in "/") into a tree: their parent folders are
// added, and the top level starts open, in the order given.
func pickTree(paths []string) []*pickNode {
	var top []*pickNode
	byPath := map[string]*pickNode{}
	var add func(p string, dir bool) *pickNode
	add = func(p string, dir bool) *pickNode {
		if n := byPath[p]; n != nil {
			n.Dir = n.Dir || dir
			return n
		}
		n := &pickNode{Name: p[strings.LastIndex(p, "/")+1:], Path: p, Dir: dir}
		byPath[p] = n
		if i := strings.LastIndex(p, "/"); i > 0 {
			parent := add(p[:i], true)
			parent.Kids = append(parent.Kids, n)
		} else {
			n.Open = true
			top = append(top, n)
		}
		return n
	}
	for _, p := range paths {
		dir := strings.HasSuffix(p, "/")
		if p = strings.Trim(p, "/"); p != "" {
			add(p, dir)
		}
	}
	var sortAll func([]*pickNode)
	sortAll = func(ns []*pickNode) { // folders first, then by name
		sort.SliceStable(ns, func(i, j int) bool {
			if ns[i].Dir != ns[j].Dir {
				return ns[i].Dir
			}
			return ns[i].Name < ns[j].Name
		})
		for _, n := range ns {
			sortAll(n.Kids)
		}
	}
	for _, n := range top { // the top level keeps the command's order (input/ before errors/)
		sortAll(n.Kids)
	}
	return top
}

// pickerCSP lets a command form run picker.js (the default policy runs no script).
func pickerCSP(rw http.ResponseWriter) {
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
}
