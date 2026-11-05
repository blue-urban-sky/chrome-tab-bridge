package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	meetCall     = regexp.MustCompile(`^https://meet\.google\.com/[a-z]{3}-[a-z]{4}-[a-z]{3}`)
	groupColors  = []string{"grey", "blue", "red", "yellow", "green", "pink", "purple", "cyan", "orange"}
	hiddenMarker = "[hidden]"
	rules        = loadHideRules()
)

const defaultHideRules = `^mail\.google\.com$`

type hideRules struct {
	hide []*regexp.Regexp
	show map[string]bool
	all  bool
}

func hideRulesPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tabs", "hidden-hosts")
}

func parseHideRules(text string) (hideRules, error) {
	r := hideRules{show: map[string]bool{}}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "!"):
			r.show[strings.TrimSpace(line[1:])] = true
		default:
			re, err := regexp.Compile(line)
			if err != nil {
				return hideRules{}, fmt.Errorf("line %d: %v", i+1, err)
			}
			r.hide = append(r.hide, re)
		}
	}
	return r, nil
}

func loadHideRules() hideRules {
	text := defaultHideRules
	path := hideRulesPath()
	if b, err := os.ReadFile(path); err == nil {
		text = string(b)
	}
	r, err := parseHideRules(text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v. Hiding every title and URL until it's fixed.\n", path, err)
		return hideRules{all: true}
	}
	return r
}

func (r hideRules) hides(host string) bool {
	if r.all {
		return true
	}
	if r.show[host] {
		return false
	}
	for _, re := range r.hide {
		if re.MatchString(host) {
			return true
		}
	}
	return false
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func hidden(raw string) bool {
	return rules.hides(hostOf(raw))
}

func shown(rawURL, title string, reveal bool) (string, string) {
	if reveal || !hidden(rawURL) {
		return rawURL, title
	}
	u, _ := url.Parse(rawURL)
	return u.Scheme + "://" + u.Host + "/" + hiddenMarker, hiddenMarker
}

func risks(t Tab) []string {
	var r []string
	if t.Pinned {
		r = append(r, "pinned")
	}
	if t.Audible {
		r = append(r, "playing audio")
	}
	if meetCall.MatchString(t.URL) {
		r = append(r, "meet call")
	}
	return r
}

func withoutFragment(raw string) string {
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func duplicates(tabs []Tab) map[int]bool {
	ordered := append([]Tab(nil), tabs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].WindowID != ordered[j].WindowID {
			return ordered[i].WindowID < ordered[j].WindowID
		}
		return ordered[i].Index < ordered[j].Index
	})
	keep := map[string]Tab{}
	for _, t := range ordered {
		k := withoutFragment(t.URL)
		if cur, ok := keep[k]; !ok || (t.Active && !cur.Active) {
			keep[k] = t
		}
	}
	extra := map[int]bool{}
	for _, t := range tabs {
		if keep[withoutFragment(t.URL)].ID != t.ID {
			extra[t.ID] = true
		}
	}
	return extra
}

func flags(t Tab) string {
	var b strings.Builder
	if t.Active {
		b.WriteByte('*')
	}
	if t.Pinned {
		b.WriteByte('P')
	}
	if t.Audible {
		b.WriteByte('A')
	}
	if t.Discarded {
		b.WriteByte('Z')
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return "now"
	}
}

func lastSeen(t Tab) string {
	if t.LastAccessed == 0 {
		return "-"
	}
	return age(time.UnixMilli(int64(t.LastAccessed)))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
