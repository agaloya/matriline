package main

import (
	"strings"
)

// The web page's settings form: every "key = value" of the configuration file as a field,
// with its explanation (the comment lines just above it, or its ">> key" paragraph in the
// EXPLANATIONS part of server.conf). It works for any file in this format, so a program
// that embeds Matriline (Nacomline) gets a form for its own file.

type setting struct {
	Section, Key, Value string
	Help                string // the comment above it (short), or the explanation
	More                string // the explanation, when Help is the short comment
	Restart             bool   // "[restart]" in its explanation: applied when the server restarts
	Bool                bool   // true/false: a yes/no choice
	ReadOnly            bool   // runs a program or names a secret file: changed only in the file
}

// settingField is the form field name of a setting.
func (s setting) Field() string { return "f." + s.Section + "." + s.Key }

// protectedSettings run a program as the server, or say which file holds a secret: the
// web page shows them but they are changed only in the file (code review).
var protectedSettings = map[string]bool{"hooks.result": true, "hooks.done": true, "alerts.command": true,
	"alerts.smtp_password_file": true, "alerts.smtp_password_env": true, "alerts.telegram_token_file": true,
	"update.url": true, "spool.root": true}

type settingGroup struct {
	Section string
	Items   []setting
}

func parseSettings(text string) []settingGroup {
	explain := explanations(text)
	var groups []settingGroup
	idx := map[string]int{}
	section := ""
	var comment []string
	inExplanations := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.Contains(t, "EXPLANATIONS ="):
			inExplanations = true
		case t == "":
			comment = nil
		case strings.HasPrefix(t, "#"):
			c := strings.TrimSpace(strings.TrimPrefix(t, "#"))
			if !strings.HasPrefix(c, "===") && !strings.HasPrefix(c, "---") {
				comment = append(comment, c)
			} else {
				comment = nil
			}
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			section, comment = strings.Trim(t, "[]"), nil
		case inExplanations:
		default:
			k, v, ok := strings.Cut(t, "=")
			if !ok || section == "" {
				comment = nil
				continue
			}
			s := setting{Section: section, Key: strings.TrimSpace(k)}
			v, inline, _ := strings.Cut(v, "#")
			s.Value = strings.TrimSpace(v)
			help := strings.Join(comment, " ")
			if inline = strings.TrimSpace(inline); inline != "" {
				help = strings.TrimSpace(help + " " + inline)
			}
			if e := explain[s.Key]; e != "" {
				if help == "" {
					help = e
				} else if e != help {
					s.More = e
				}
			}
			s.Restart = strings.Contains(help+" "+s.More, "[restart]")
			s.Help = strings.TrimSpace(strings.ReplaceAll(help, "[restart]", ""))
			s.More = strings.TrimSpace(strings.ReplaceAll(s.More, "[restart]", ""))
			s.Bool = s.Value == "true" || s.Value == "false"
			s.ReadOnly = protectedSettings[s.Section+"."+s.Key]
			i, ok := idx[section]
			if !ok {
				i = len(groups)
				idx[section] = i
				groups = append(groups, settingGroup{Section: section})
			}
			groups[i].Items = append(groups[i].Items, s)
			comment = nil
		}
	}
	return groups
}

// explanations maps each key to its ">> key" paragraph (a paragraph may name several keys:
// ">> email_enabled, smtp_server, ...").
func explanations(text string) map[string]string {
	out := map[string]string{}
	var keys []string
	var para []string
	flush := func() {
		for _, k := range keys {
			out[k] = strings.Join(para, " ")
		}
		keys, para = nil, nil
	}
	in := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.Contains(t, "EXPLANATIONS =") {
			in = true
			continue
		}
		if !in || !strings.HasPrefix(t, "#") {
			continue
		}
		c := strings.TrimSpace(strings.TrimPrefix(t, "#"))
		switch {
		case strings.HasPrefix(c, ">>"):
			flush()
			for _, k := range strings.Split(strings.TrimPrefix(c, ">>"), ",") {
				if k = strings.TrimSpace(k); k != "" {
					keys = append(keys, k)
				}
			}
		case strings.HasPrefix(c, "---"):
			flush()
		case len(keys) > 0 && c != "":
			para = append(para, c)
		}
	}
	flush()
	return out
}
