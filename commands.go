package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type refusal struct {
	ID      int      `json:"id"`
	Reasons []string `json:"reasons"`
}

type failure struct {
	ID     int    `json:"id"`
	Reason string `json:"reason"`
}

type outcome struct {
	Refused []refusal `json:"refused"`
	Missing []int     `json:"missing"`
	Failed  []failure `json:"failed"`
}

type exitStatus struct{ errored, partial bool }

func (e *exitStatus) fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	e.errored = true
}

func (e *exitStatus) note(o outcome, who string) {
	for _, r := range o.Refused {
		fmt.Printf("%s: refused %d (%s), pass --force to override\n", who, r.ID, strings.Join(r.Reasons, ", "))
		e.partial = true
	}
	for _, f := range o.Failed {
		fmt.Printf("%s: failed %d (%s)\n", who, f.ID, f.Reason)
		e.partial = true
	}
	for _, id := range o.Missing {
		fmt.Printf("%s: no tab %d\n", who, id)
		e.partial = true
	}
}

func (e *exitStatus) code() int {
	switch {
	case e.errored:
		return 1
	case e.partial:
		return 2
	}
	return 0
}

func parseIDs(args []string) ([]int, error) {
	var fields []string
	for _, a := range args {
		if a != "-" {
			fields = append(fields, a)
			continue
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		fields = append(fields, strings.Fields(string(b))...)
	}
	ids := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("not a tab ID: %q", f)
		}
		ids = append(ids, n)
	}
	if len(ids) == 0 {
		return nil, errors.New("no tab IDs given")
	}
	return ids, nil
}

func eachOwner(ps []*profile, ids []int, st *exitStatus, fn func(p *profile, ids []int)) {
	by := map[*profile][]int{}
	for _, id := range ids {
		owner := tabOwner(ps, id)
		if owner == nil {
			fmt.Printf("no tab %d\n", id)
			st.partial = true
			continue
		}
		by[owner] = append(by[owner], id)
	}
	for _, p := range ps {
		if len(by[p]) > 0 {
			fn(p, by[p])
		}
	}
}

func cmdStatus(args []string) int {
	fs := newFlags("status")
	reveal := fs.Bool("reveal", false, "show hidden active-tab titles")
	parseArgs(fs, args)
	for _, p := range connectAll() {
		desc := "Chrome profile not identified"
		if cp, ok := identify(p.Profile); ok {
			email := cp.Email
			if email == "" {
				email = "not signed in"
			}
			desc = fmt.Sprintf("Chrome profile %q (%s), %s, matched by %s", cp.Name, cp.Dir, email, cp.How)
		}
		fmt.Printf("%s: %s, bridge id %s\n", p.Name(), desc, p.Profile.ID)
		for _, w := range p.Windows {
			tabs, active := 0, ""
			groups := map[int]bool{}
			for _, t := range p.Tabs {
				if t.WindowID != w.ID {
					continue
				}
				tabs++
				if t.GroupID != -1 {
					groups[t.GroupID] = true
				}
				if t.Active {
					_, active = shown(t.URL, t.Title, *reveal)
				}
			}
			focused := ""
			if w.Focused {
				focused = ", focused"
			}
			fmt.Printf("  window %d (%s%s): %d tabs, %d groups, %dx%d at %d,%d, showing %q\n",
				w.ID, w.State, focused, tabs, len(groups), w.Width, w.Height, w.Left, w.Top, truncate(active, 60))
		}
	}
	return 0
}

func cmdReload(args []string) int {
	parseArgs(newFlags("reload"), args)
	var st exitStatus
	for _, p := range connectAll() {
		var r struct {
			Version string `json:"version"`
		}
		if err := p.Call("reload", nil, &r); err != nil {
			st.fail("%s: %v", p.Name(), err)
			continue
		}
		fmt.Printf("%s: reloading extension (was %s)\n", p.Name(), r.Version)
	}
	return st.code()
}

func cmdFocus(args []string) int {
	pos := parseArgs(newFlags("focus"), args)
	if len(pos) != 1 {
		return fail("usage: tabs focus WINDOW")
	}
	wid, err := strconv.Atoi(pos[0])
	if err != nil {
		return fail("not a window ID: %q", pos[0])
	}
	p := windowOwner(connectAll(), wid)
	if p == nil {
		return fail("no window %d", wid)
	}
	if err := p.Call("focus", map[string]any{"windowId": wid}, nil); err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	fmt.Printf("%s: brought window %d to the front\n", p.Name(), wid)
	return 0
}

func cmdLabel(args []string) int {
	pos := parseArgs(newFlags("label"), args)
	if len(pos) != 2 {
		return fail("usage: tabs label PROFILE NAME")
	}
	p := findProfile(connectAll(), pos[0])
	if p == nil {
		return fail("no profile %q", pos[0])
	}
	if err := p.Call("label", map[string]any{"label": pos[1]}, nil); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%s is now %s\n", p.Profile.ID, pos[1])
	return 0
}

type listRow struct {
	p     *profile
	t     Tab
	url   string
	title string
	group string
}

func cmdList(args []string) int {
	fs := newFlags("list")
	only := fs.String("profile", "", "only this profile, by name or id")
	window := fs.Int("window", 0, "only this window")
	host := fs.String("host", "", "only sites whose host contains this")
	group := fs.String("group", "", "only groups whose title contains this, or none for ungrouped tabs")
	match := fs.String("match", "", "regexp against the shown URL and title")
	stale := fs.Float64("stale", 0, "only tabs not viewed for this many days")
	dupes := fs.Bool("dupes", false, "only extra copies of a URL already open, fragment ignored")
	idsOnly := fs.Bool("ids", false, "print tab IDs only")
	urls := fs.Bool("urls", false, "show URLs instead of hosts")
	asJSON := fs.Bool("json", false, "print JSON")
	reveal := fs.Bool("reveal", false, "show hidden titles and URLs")
	parseArgs(fs, args)

	var re *regexp.Regexp
	if *match != "" {
		var err error
		if re, err = regexp.Compile(*match); err != nil {
			return fail("bad --match: %v", err)
		}
	}
	staleBefore := time.Now().Add(-time.Duration(*stale * float64(24*time.Hour)))

	ps := connectAll()
	var rows []listRow
	for _, p := range ps {
		if *only != "" && !p.Is(*only) {
			continue
		}
		extra := map[int]bool{}
		if *dupes {
			extra = duplicates(p.Tabs)
		}
		for _, t := range p.Tabs {
			u, title := shown(t.URL, t.Title, *reveal)
			g, _ := p.group(t.GroupID)
			switch {
			case *window != 0 && t.WindowID != *window,
				*host != "" && !strings.Contains(hostOf(t.URL), *host),
				*group == "none" && t.GroupID != -1,
				*group != "" && *group != "none" && (t.GroupID == -1 || !strings.Contains(strings.ToLower(g.Title), strings.ToLower(*group))),
				re != nil && !re.MatchString(u+" "+title),
				*stale > 0 && (t.LastAccessed == 0 || time.UnixMilli(int64(t.LastAccessed)).After(staleBefore)),
				*dupes && !extra[t.ID]:
				continue
			}
			rows = append(rows, listRow{p, t, u, title, g.Title})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.p != b.p {
			return a.p.Name() < b.p.Name()
		}
		if a.t.WindowID != b.t.WindowID {
			return a.t.WindowID < b.t.WindowID
		}
		return a.t.Index < b.t.Index
	})

	switch {
	case *idsOnly:
		for _, r := range rows {
			fmt.Println(r.t.ID)
		}
	case *asJSON:
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{
				"profile": r.p.Name(), "windowId": r.t.WindowID, "index": r.t.Index, "id": r.t.ID,
				"groupId": r.t.GroupID, "group": r.group, "pinned": r.t.Pinned, "active": r.t.Active,
				"audible": r.t.Audible, "discarded": r.t.Discarded, "lastAccessed": r.t.LastAccessed,
				"url": r.url, "title": r.title,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", " ")
		_ = enc.Encode(out)
	default:
		printRows(rows, *urls)
	}
	return 0
}

func printRows(rows []listRow, urls bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	var lastP *profile
	lastW := 0
	for _, r := range rows {
		if r.p != lastP || r.t.WindowID != lastW {
			w.Flush()
			total, focused := 0, ""
			for _, t := range r.p.Tabs {
				if t.WindowID == r.t.WindowID {
					total++
				}
			}
			for _, win := range r.p.Windows {
				if win.ID == r.t.WindowID && win.Focused {
					focused = ", focused"
				}
			}
			fmt.Printf("\n%s window %d (%d tabs%s)\n", r.p.Name(), r.t.WindowID, total, focused)
			lastP, lastW = r.p, r.t.WindowID
		}
		place := hostOf(r.url)
		if urls {
			place = truncate(r.url, 100)
		}
		group := "-"
		if r.t.GroupID != -1 {
			group = truncate(r.group, 16)
			if group == "" {
				group = strconv.Itoa(r.t.GroupID)
			}
		}
		fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\t%s\n", r.t.ID, flags(r.t), lastSeen(r.t), group, place, truncate(r.title, 80))
	}
	w.Flush()
	fmt.Printf("\n%d tabs\n", len(rows))
}

func cmdGroups(args []string) int {
	parseArgs(newFlags("groups"), args)
	for _, p := range connectAll() {
		for _, win := range p.Windows {
			first := map[int]int{}
			count := map[int]int{}
			for _, t := range p.Tabs {
				if t.WindowID != win.ID || t.GroupID == -1 {
					continue
				}
				count[t.GroupID]++
				if i, ok := first[t.GroupID]; !ok || t.Index < i {
					first[t.GroupID] = t.Index
				}
			}
			if len(count) == 0 {
				continue
			}
			ids := make([]int, 0, len(first))
			for id := range first {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return first[ids[i]] < first[ids[j]] })
			fmt.Printf("\n%s window %d\n", p.Name(), win.ID)
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			for _, id := range ids {
				g, _ := p.group(id)
				state := ""
				if g.Collapsed {
					state = "collapsed"
				}
				fmt.Fprintf(w, "  %d\t%s\t%s\t%d tabs\t%s\n", id, g.Title, g.Color, count[id], state)
			}
			w.Flush()
		}
	}
	return 0
}

func cmdClose(args []string) int {
	fs := newFlags("close")
	force := fs.Bool("force", false, "also close pinned tabs, tabs playing audio and Meet calls")
	ids, err := parseIDs(parseArgs(fs, args))
	if err != nil {
		return fail("%v", err)
	}
	var st exitStatus
	eachOwner(connectAll(), ids, &st, func(p *profile, ids []int) {
		var r struct {
			Closed []int `json:"closed"`
			outcome
		}
		if err := p.Call("close", map[string]any{"ids": ids, "force": *force}, &r); err != nil {
			st.fail("%s: %v", p.Name(), err)
			return
		}
		fmt.Printf("%s: closed %d\n", p.Name(), len(r.Closed))
		st.note(r.outcome, p.Name())
	})
	return st.code()
}

func cmdMove(args []string) int {
	fs := newFlags("move")
	window := fs.Int("window", 0, "target window")
	index := fs.Int("index", -1, "position in the target window, -1 for the end")
	reopen := fs.Bool("reopen", false, "for tabs in another profile, open the URL there and close the original")
	force := fs.Bool("force", false, "with --reopen, also take pinned tabs, tabs playing audio and Meet calls")
	ids, err := parseIDs(parseArgs(fs, args))
	if err != nil {
		return fail("%v", err)
	}
	if *window == 0 {
		return fail("--window is required")
	}
	ps := connectAll()
	target := windowOwner(ps, *window)
	if target == nil {
		return fail("no window %d", *window)
	}
	var st exitStatus
	eachOwner(ps, ids, &st, func(p *profile, ids []int) {
		if p == target {
			var r struct {
				Moved []Tab `json:"moved"`
			}
			if err := p.Call("move", map[string]any{"ids": ids, "windowId": *window, "index": *index}, &r); err != nil {
				st.fail("%s: %v", p.Name(), err)
				return
			}
			fmt.Printf("%s: moved %d to window %d\n", p.Name(), len(r.Moved), *window)
			return
		}
		if !*reopen {
			st.fail("%d tabs are in profile %s and window %d is in %s. Chrome can't move tabs between profiles. "+
				"--reopen opens them there and closes the originals, which reloads them and drops their back history.",
				len(ids), p.Name(), *window, target.Name())
			return
		}
		var opened []int
		for _, id := range ids {
			t, _ := p.tab(id)
			if r := risks(t); len(r) > 0 && !*force {
				fmt.Printf("%s: refused %d (%s), pass --force to override\n", p.Name(), id, strings.Join(r, ", "))
				st.partial = true
				continue
			}
			if err := target.Call("open", map[string]any{"url": t.URL, "windowId": *window}, nil); err != nil {
				st.fail("%s: opening %d: %v", target.Name(), id, err)
				continue
			}
			opened = append(opened, id)
		}
		if len(opened) == 0 {
			return
		}
		var r struct {
			Closed []int `json:"closed"`
			outcome
		}
		if err := p.Call("close", map[string]any{"ids": opened, "force": true}, &r); err != nil {
			st.fail("%s: reopened %d in %s but closing the originals failed: %v", p.Name(), len(opened), target.Name(), err)
			return
		}
		fmt.Printf("reopened %d in %s window %d, closed %d in %s\n", len(opened), target.Name(), *window, len(r.Closed), p.Name())
		st.note(r.outcome, p.Name())
	})
	return st.code()
}

func cmdGroup(args []string) int {
	fs := newFlags("group")
	title := fs.String("title", "", "group title")
	color := fs.String("color", "", strings.Join(groupColors, ", "))
	into := fs.Int("into", -1, "add to this existing group")
	ids, err := parseIDs(parseArgs(fs, args))
	if err != nil {
		return fail("%v", err)
	}
	if *color != "" && !slices.Contains(groupColors, *color) {
		return fail("--color must be one of %s", strings.Join(groupColors, ", "))
	}
	set := setFlags(fs)
	ps := connectAll()
	var owner *profile
	for _, id := range ids {
		p := tabOwner(ps, id)
		if p == nil {
			return fail("no tab %d", id)
		}
		if owner != nil && p != owner {
			return fail("tabs are in more than one profile, a group lives in one profile")
		}
		owner = p
	}
	a := map[string]any{"ids": ids}
	if set["title"] {
		a["title"] = *title
	}
	if *color != "" {
		a["color"] = *color
	}
	if *into >= 0 {
		a["groupId"] = *into
	}
	var g Group
	if err := owner.Call("group", a, &g); err != nil {
		return fail("%s: %v", owner.Name(), err)
	}
	fmt.Printf("%s: group %d %q (%s) in window %d, added %d tabs\n", owner.Name(), g.ID, g.Title, g.Color, g.WindowID, len(ids))
	return 0
}

func cmdGroupEdit(args []string) int {
	fs := newFlags("group-edit")
	title := fs.String("title", "", "group title")
	color := fs.String("color", "", strings.Join(groupColors, ", "))
	collapse := fs.Bool("collapse", false, "collapse the group")
	expand := fs.Bool("expand", false, "expand the group")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		return fail("usage: tabs group-edit GROUP [--title T] [--color C] [--collapse | --expand]")
	}
	gid, err := strconv.Atoi(pos[0])
	if err != nil {
		return fail("not a group ID: %q", pos[0])
	}
	if *color != "" && !slices.Contains(groupColors, *color) {
		return fail("--color must be one of %s", strings.Join(groupColors, ", "))
	}
	p := groupOwner(connectAll(), gid)
	if p == nil {
		return fail("no group %d", gid)
	}
	set := setFlags(fs)
	a := map[string]any{"groupId": gid}
	if set["title"] {
		a["title"] = *title
	}
	if *color != "" {
		a["color"] = *color
	}
	if *collapse || *expand {
		a["collapsed"] = *collapse
	}
	var g Group
	if err := p.Call("updateGroup", a, &g); err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	fmt.Printf("%s: group %d %q (%s)\n", p.Name(), g.ID, g.Title, g.Color)
	return 0
}

func cmdUngroup(args []string) int {
	ids, err := parseIDs(parseArgs(newFlags("ungroup"), args))
	if err != nil {
		return fail("%v", err)
	}
	var st exitStatus
	eachOwner(connectAll(), ids, &st, func(p *profile, ids []int) {
		if err := p.Call("ungroup", map[string]any{"ids": ids}, nil); err != nil {
			st.fail("%s: %v", p.Name(), err)
			return
		}
		fmt.Printf("%s: ungrouped %d\n", p.Name(), len(ids))
	})
	return st.code()
}

func cmdSort(args []string) int {
	fs := newFlags("sort")
	order := fs.String("order", "", "group IDs, comma-separated, first to last")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		return fail("usage: tabs sort WINDOW [--order GROUP,GROUP...]")
	}
	wid, err := strconv.Atoi(pos[0])
	if err != nil {
		return fail("not a window ID: %q", pos[0])
	}
	var groupOrder []int
	for _, s := range strings.FieldsFunc(*order, func(r rune) bool { return r == ',' }) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fail("not a group ID: %q", s)
		}
		groupOrder = append(groupOrder, n)
	}
	p := windowOwner(connectAll(), wid)
	if p == nil {
		return fail("no window %d", wid)
	}
	a := map[string]any{"windowId": wid}
	if len(groupOrder) > 0 {
		a["groupOrder"] = groupOrder
	}
	var r struct {
		Groups int `json:"groups"`
		Loose  int `json:"loose"`
	}
	if err := p.Call("sort", a, &r); err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	fmt.Printf("%s: sorted window %d, %d groups then %d ungrouped tabs, each by site\n", p.Name(), wid, r.Groups, r.Loose)
	return 0
}

func cmdPin(args []string, pinned bool) int {
	name := map[bool]string{true: "pin", false: "unpin"}[pinned]
	ids, err := parseIDs(parseArgs(newFlags(name), args))
	if err != nil {
		return fail("%v", err)
	}
	var st exitStatus
	eachOwner(connectAll(), ids, &st, func(p *profile, ids []int) {
		if err := p.Call("pin", map[string]any{"ids": ids, "pinned": pinned}, nil); err != nil {
			st.fail("%s: %v", p.Name(), err)
			return
		}
		fmt.Printf("%s: %sned %d\n", p.Name(), name, len(ids))
	})
	return st.code()
}

func cmdDiscard(args []string) int {
	fs := newFlags("discard")
	force := fs.Bool("force", false, "also sleep pinned tabs, tabs playing audio and Meet calls")
	ids, err := parseIDs(parseArgs(fs, args))
	if err != nil {
		return fail("%v", err)
	}
	var st exitStatus
	eachOwner(connectAll(), ids, &st, func(p *profile, ids []int) {
		var r struct {
			Discarded []struct {
				ID    int `json:"id"`
				NewID int `json:"newId"`
			} `json:"discarded"`
			outcome
		}
		if err := p.Call("discard", map[string]any{"ids": ids, "force": *force}, &r); err != nil {
			st.fail("%s: %v", p.Name(), err)
			return
		}
		fmt.Printf("%s: put %d to sleep\n", p.Name(), len(r.Discarded))
		for _, d := range r.Discarded {
			if d.NewID != d.ID {
				fmt.Printf("  tab %d is now %d\n", d.ID, d.NewID)
			}
		}
		st.note(r.outcome, p.Name())
	})
	return st.code()
}

func cmdPark(args []string) int {
	fs := newFlags("park")
	name := fs.String("name", "", "bookmark folder name, after the date")
	force := fs.Bool("force", false, "also park pinned tabs, tabs playing audio and Meet calls")
	ids, err := parseIDs(parseArgs(fs, args))
	if err != nil {
		return fail("%v", err)
	}
	var st exitStatus
	eachOwner(connectAll(), ids, &st, func(p *profile, ids []int) {
		var r struct {
			Folder string `json:"folder"`
			Parked []int  `json:"parked"`
			outcome
		}
		if err := p.Call("park", map[string]any{"ids": ids, "name": *name, "force": *force}, &r); err != nil {
			st.fail("%s: %v", p.Name(), err)
			return
		}
		if len(r.Parked) > 0 {
			fmt.Printf("%s: parked %d in Other bookmarks/%s\n", p.Name(), len(r.Parked), r.Folder)
		}
		st.note(r.outcome, p.Name())
	})
	return st.code()
}

func cmdClosed(args []string) int {
	fs := newFlags("closed")
	only := fs.String("profile", "", "only this profile")
	n := fs.Int("n", 25, "how many, at most 25")
	reveal := fs.Bool("reveal", false, "show hidden titles and URLs")
	parseArgs(fs, args)
	for _, p := range connectAll() {
		if *only != "" && !p.Is(*only) {
			continue
		}
		var entries []struct {
			Kind      string  `json:"kind"`
			ClosedAt  float64 `json:"closedAt"`
			SessionID string  `json:"sessionId"`
			URL       string  `json:"url"`
			Title     string  `json:"title"`
		}
		if err := p.Call("closed", map[string]any{"max": *n}, &entries); err != nil {
			return fail("%s: %v", p.Name(), err)
		}
		fmt.Printf("\n%s\n", p.Name())
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, e := range entries {
			u, title := e.URL, e.Title
			if e.Kind == "tab" {
				u, title = shown(e.URL, e.Title, *reveal)
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", e.SessionID, e.Kind, age(time.Unix(int64(e.ClosedAt), 0)), hostOf(u), truncate(title, 80))
		}
		w.Flush()
	}
	return 0
}

func cmdRestore(args []string) int {
	fs := newFlags("restore")
	only := fs.String("profile", "", "profile the session belongs to")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		return fail("usage: tabs restore SESSION [--profile P]")
	}
	ps := connectAll()
	var p *profile
	switch {
	case *only != "":
		p = findProfile(ps, *only)
	case len(ps) == 1:
		p = ps[0]
	default:
		return fail("more than one profile is connected, say which with --profile")
	}
	if p == nil {
		return fail("no profile %q", *only)
	}
	var r struct {
		Kind     string `json:"kind"`
		Tab      *Tab   `json:"tab"`
		WindowID int    `json:"windowId"`
	}
	if err := p.Call("restore", map[string]any{"sessionId": pos[0]}, &r); err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	if r.Tab != nil {
		fmt.Printf("%s: restored tab %d in window %d\n", p.Name(), r.Tab.ID, r.Tab.WindowID)
	} else {
		fmt.Printf("%s: restored %s\n", p.Name(), r.Kind)
	}
	return 0
}

func extensionID(publicKeyDER []byte) string {
	const alphabet = "abcdefghijklmnop"
	sum := sha256.Sum256(publicKeyDER)
	id := make([]byte, 0, 32)
	for _, b := range sum[:16] {
		id = append(id, alphabet[b>>4], alphabet[b&0xf])
	}
	return string(id)
}

func cmdInstall(args []string) int {
	fs := newFlags("install")
	ext := fs.String("extension", "", "path to the extension folder")
	parseArgs(fs, args)
	if *ext == "" {
		return fail("--extension is required")
	}
	raw, err := os.ReadFile(filepath.Join(*ext, "manifest.json"))
	if err != nil {
		return fail("%v", err)
	}
	var m struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return fail("manifest.json: %v", err)
	}
	der, err := base64.StdEncoding.DecodeString(m.Key)
	if err != nil || len(der) == 0 {
		return fail("manifest.json has no usable key")
	}
	id := extensionID(der)

	exe, err := os.Executable()
	if err != nil {
		return fail("%v", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return fail("%v", err)
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail("%v", err)
	}
	data, _ := json.MarshalIndent(map[string]any{
		"name":            hostName,
		"description":     "Tab bridge for the tabs CLI",
		"path":            exe,
		"type":            "stdio",
		"allowed_origins": []string{"chrome-extension://" + id + "/"},
	}, "", "  ")
	target := filepath.Join(dir, hostName+".json")
	if err := os.WriteFile(target, append(data, '\n'), 0o644); err != nil {
		return fail("%v", err)
	}
	abs, _ := filepath.Abs(*ext)
	fmt.Printf("registered %s\nextension id %s\nnext: in each Chrome profile, open chrome://extensions, turn on Developer mode, Load unpacked, pick %s\n", target, id, abs)
	return 0
}
