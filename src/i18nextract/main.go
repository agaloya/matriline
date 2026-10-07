// i18nextract lists the texts to translate into common/i18n/lang/strings.txt:
//
//	cd src && go run ./i18nextract            (writes common/i18n/lang/strings.txt)
//	go run ./i18nextract -check es            (lists texts lang/es.txt lacks or no longer uses,
//	                                           and whether server.conf.es / client.conf.es are current)
//
// It also writes the English configuration templates to lang/server.conf and
// lang/client.conf: their translations are whole files, lang/server.conf.<code>, whose
// first line is "# source <sum>" of the English template they translate.
//
// A text is to be translated when the code passes it to i18n.T / i18n.Tf / i18n.N (or T/Tf), as the format of alertf, when it
// is the client's usage text, a Help, More or Group field of a command or option description, a value of
// commandDocs or the groups list, or a {{T "..."}} in a web template.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/agaloya/matriline/common/i18n"
)

var dirs = []string{"server", "client", "common/release", "common/orcafind", "common/pager"}

var tmplT = regexp.MustCompile(`\{\{\s*Tf?\s+"((?:[^"\\]|\\.)*)"`) // {{T "..."}} and {{Tf "..." args}}

type found struct {
	text, where string
}

func main() {
	check := flag.String("check", "", "compare lang/<code>.txt with the texts of the code")
	flag.Parse()
	var all []found
	seen := map[string]bool{}
	add := func(s, where string) {
		if strings.TrimSpace(s) == "" || seen[s] {
			return
		}
		seen[s] = true
		all = append(all, found{s, where})
	}
	fset := token.NewFileSet()
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.go"))
		sort.Strings(files)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				fail(err)
			}
			lit := func(e ast.Expr) (string, bool) {
				b, ok := e.(*ast.BasicLit)
				if !ok || b.Kind != token.STRING {
					return "", false
				}
				s, err := strconv.Unquote(b.Value)
				return s, err == nil
			}
			pos := func(n ast.Node) string {
				p := fset.Position(n.Pos())
				return fmt.Sprintf("%s:%d", filepath.ToSlash(p.Filename), p.Line)
			}
			ast.Inspect(af, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					name := ""
					switch fn := x.Fun.(type) {
					case *ast.Ident:
						name = fn.Name
					case *ast.SelectorExpr:
						if p, ok := fn.X.(*ast.Ident); ok && p.Name == "i18n" || fn.Sel.Name == "alertf" {
							name = fn.Sel.Name
						}
					}
					if name == "alertf" && len(x.Args) > 1 { // alertf(kind, format, ...)
						if s, ok := lit(x.Args[1]); ok {
							add(s, pos(x))
						}
					}
					if (name == "T" || name == "Tf" || name == "N" || name == "pathArg") && len(x.Args) > 0 {
						if s, ok := lit(x.Args[0]); ok {
							add(s, pos(x))
						}
					}
				case *ast.KeyValueExpr:
					if k, ok := x.Key.(*ast.Ident); ok && (k.Name == "Help" || k.Name == "More" || k.Name == "Group") {
						if s, ok := lit(x.Value); ok {
							add(s, pos(x))
						}
					}
				case *ast.ValueSpec:
					for i, id := range x.Names {
						if id.Name == "usage" && i < len(x.Values) { // the client's help
							if s, ok := lit(x.Values[i]); ok {
								add(s, pos(x))
							}
						}
						if (id.Name == "groups" || id.Name == "commandDocs") && i < len(x.Values) {
							if cl, ok := x.Values[i].(*ast.CompositeLit); ok {
								for _, el := range cl.Elts {
									if kv, ok := el.(*ast.KeyValueExpr); ok {
										el = kv.Value
									}
									if s, ok := lit(el); ok {
										add(s, pos(el))
									}
								}
							}
						}
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING && strings.Contains(x.Value, "{{") {
						s, _ := lit(x)
						for _, m := range tmplT.FindAllStringSubmatch(s, -1) {
							if t, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
								add(t, pos(x))
							}
						}
					}
				}
				return true
			})
		}
	}
	// the configuration templates, translated as whole files: their English text is
	// written next to the catalogs, so a translator sees (and git shows) what changed
	tmpl := map[string]string{}
	for _, p := range []string{"server", "client"} {
		af, err := parser.ParseFile(fset, p+"/config.go", nil, 0)
		if err != nil {
			fail(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == "defaultConfig" && len(vs.Values) == 1 {
				if b, ok := vs.Values[0].(*ast.BasicLit); ok {
					if s, err := strconv.Unquote(b.Value); err == nil {
						tmpl[p+".conf"] = s
					}
				}
			}
			return true
		})
		if tmpl[p+".conf"] == "" {
			fail(fmt.Errorf("%s/config.go: no defaultConfig", p))
		}
	}
	if *check != "" {
		for name, en := range tmpl {
			tr, ok := i18n.Translations(name)[*check]
			switch first, _, _ := strings.Cut(tr, "\n"); {
			case !ok:
				fmt.Printf("%s.%s: missing (a translation of lang/%s, first line %q)\n", name, *check, name, i18n.TemplateSource(en))
			case first != i18n.TemplateSource(en):
				fmt.Printf("%s.%s: translates an older %s (%s; now %s): 'git log -p common/i18n/lang/%s' shows what changed\n", name, *check, name, first, i18n.TemplateSource(en), name)
			default:
				fmt.Printf("%s.%s: current\n", name, *check)
			}
		}
		b, err := os.ReadFile("common/i18n/lang/" + *check + ".txt")
		if err != nil {
			fail(err)
		}
		es, err := i18n.Parse(string(b), *check)
		if err != nil {
			fail(err)
		}
		have := map[string]bool{}
		for _, e := range es {
			if e.Tr != "" {
				have[e.En] = true
			}
			if !seen[e.En] {
				fmt.Printf("no longer used: %q\n", e.En)
			}
		}
		n := 0
		for _, f := range all {
			if !have[f.text] {
				fmt.Printf("missing (%s): %q\n", f.where, f.text)
				n++
			}
		}
		fmt.Printf("%d texts, %d without a translation\n", len(all), n)
		return
	}
	var b strings.Builder
	b.WriteString(`# Texts to translate (generated by: cd src && go run ./i18nextract; do not edit by hand).
# Copy this file to <code>.txt (es.txt) and write each translation after "es:".
# Rules: keep %s %d %q %v %% and the like exactly, in the same order; keep command words,
# file and option names, and anything inside 'quotes' or <angle brackets> in English;
# a text of several lines continues on lines starting with "  |" (keep the line breaks
# roughly where the English has them). The comment above each entry says where it is used.

`)
	for _, f := range all {
		lines := strings.Split(f.text, "\n")
		fmt.Fprintf(&b, "# %s\nen: %s\n", f.where, lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(&b, "  |%s\n", l)
		}
		b.WriteString("es:\n\n")
	}
	if err := os.WriteFile("common/i18n/lang/strings.txt", []byte(b.String()), 0o644); err != nil {
		fail(err)
	}
	for name, en := range tmpl {
		if err := os.WriteFile("common/i18n/lang/"+name, []byte(en), 0o644); err != nil {
			fail(err)
		}
	}
	fmt.Printf("%d texts written to common/i18n/lang/strings.txt\n", len(all))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "i18nextract:", err)
	os.Exit(1)
}
