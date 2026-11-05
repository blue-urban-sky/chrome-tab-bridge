package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

type Bookmark struct {
	ID           string  `json:"id"`
	ParentID     string  `json:"parentId"`
	Index        int     `json:"index"`
	Title        string  `json:"title"`
	URL          string  `json:"url"`
	Folder       bool    `json:"folder"`
	DateAdded    float64 `json:"dateAdded"`
	DateLastUsed float64 `json:"dateLastUsed"`
	FolderType   string  `json:"folderType"`
	Syncing      *bool   `json:"syncing"`
	Unmodifiable string  `json:"unmodifiable"`
}

type bookmarkSet struct {
	nodes    []Bookmark
	byID     map[string]Bookmark
	children map[string][]Bookmark
	rootName map[string]string
}

func newBookmarkSet(nodes []Bookmark) *bookmarkSet {
	s := &bookmarkSet{nodes: nodes, byID: map[string]Bookmark{}, children: map[string][]Bookmark{}, rootName: map[string]string{}}
	for _, n := range nodes {
		s.byID[n.ID] = n
		if n.ParentID != "" {
			s.children[n.ParentID] = append(s.children[n.ParentID], n)
		}
	}
	for _, kids := range s.children {
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].Index < kids[j].Index })
	}
	titles := map[string]int{}
	for _, n := range nodes {
		if n.ParentID == "0" {
			titles[n.Title]++
		}
	}
	for _, n := range nodes {
		if n.ParentID != "0" {
			continue
		}
		name := n.Title
		if titles[n.Title] > 1 && n.Syncing != nil {
			if *n.Syncing {
				name += " [account]"
			} else {
				name += " [device]"
			}
		}
		s.rootName[n.ID] = name
	}
	return s
}

func (s *bookmarkSet) path(folderID string) string {
	var parts []string
	for id := folderID; id != "" && id != "0"; id = s.byID[id].ParentID {
		if name, ok := s.rootName[id]; ok {
			parts = append(parts, name)
			break
		}
		parts = append(parts, s.byID[id].Title)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "/")
}

func (s *bookmarkSet) bookmarks() []Bookmark {
	var out []Bookmark
	var walk func(id string)
	walk = func(id string) {
		for _, c := range s.children[id] {
			if c.Folder {
				walk(c.ID)
			} else {
				out = append(out, c)
			}
		}
	}
	walk("0")
	return out
}

func bookmarkDupes(marks []Bookmark) map[string]bool {
	keep := map[string]Bookmark{}
	for _, b := range marks {
		k := withoutFragment(b.URL)
		if cur, ok := keep[k]; !ok || (b.DateAdded != 0 && b.DateAdded < cur.DateAdded) {
			keep[k] = b
		}
	}
	extra := map[string]bool{}
	for _, b := range marks {
		if keep[withoutFragment(b.URL)].ID != b.ID {
			extra[b.ID] = true
		}
	}
	return extra
}

func loadBookmarks(p *profile) (*bookmarkSet, error) {
	var nodes []Bookmark
	if err := p.Call("bookmarks", nil, &nodes); err != nil {
		return nil, err
	}
	return newBookmarkSet(nodes), nil
}

func ref(p *profile, id string) string { return p.Name() + ":" + id }

func splitRef(ps []*profile, r, profileFlag string) (*profile, string, error) {
	name, id := profileFlag, r
	if i := strings.LastIndexByte(r, ':'); i >= 0 {
		name, id = r[:i], r[i+1:]
	}
	if name == "" {
		if len(ps) != 1 {
			return nil, "", fmt.Errorf("%s: bookmark IDs repeat across profiles, write it as profile:%s or pass --profile", r, r)
		}
		return ps[0], id, nil
	}
	p := findProfile(ps, name)
	if p == nil {
		return nil, "", fmt.Errorf("no profile %q", name)
	}
	return p, id, nil
}

func splitRefs(ps []*profile, refs []string, profileFlag string) (*profile, []string, error) {
	var owner *profile
	var ids []string
	for _, r := range refs {
		p, id, err := splitRef(ps, r, profileFlag)
		if err != nil {
			return nil, nil, err
		}
		if owner != nil && p != owner {
			return nil, nil, fmt.Errorf("bookmarks are in more than one profile, run once per profile")
		}
		owner = p
		ids = append(ids, id)
	}
	if owner == nil {
		return nil, nil, fmt.Errorf("no bookmark IDs given")
	}
	return owner, ids, nil
}

func expandStdin(args []string) []string {
	var out []string
	for _, a := range args {
		if a != "-" {
			out = append(out, a)
			continue
		}
		b, _ := io.ReadAll(os.Stdin)
		out = append(out, strings.Fields(string(b))...)
	}
	return out
}

const bookmarksUsage = `tabs bookmarks [list] [filters] [output]     bookmarks, by folder
tabs bookmarks tree [--profile P]             folders with counts
tabs bookmarks open REF... [--window W]       open bookmarks, or a folder's bookmarks, as tabs
tabs bookmarks mv REF... --to FOLDER [--index N]
tabs bookmarks rm REF... [--force]            --force also removes non-empty folders
tabs bookmarks mkdir PARENT NAME
tabs bookmarks rename REF NAME

REF is profile:id as printed, e.g. work:123. A bare id needs --profile.
list filters: --profile --folder --host --match --dupes --open --unused DAYS
list output:  --ids --urls --json --reveal
`

func cmdBookmarks(args []string) int {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		return bookmarksList(args)
	case "tree":
		return bookmarksTree(args)
	case "open":
		return bookmarksOpen(args)
	case "mv":
		return bookmarksMove(args)
	case "rm":
		return bookmarksRemove(args)
	case "mkdir":
		return bookmarksMkdir(args)
	case "rename":
		return bookmarksRename(args)
	}
	fmt.Fprint(os.Stderr, bookmarksUsage)
	return 1
}

type bookmarkRow struct {
	p      *profile
	b      Bookmark
	folder string
	url    string
	title  string
}

func bookmarksList(args []string) int {
	fs := newFlags("bookmarks list")
	only := fs.String("profile", "", "only this profile")
	folder := fs.String("folder", "", "only folders whose path contains this")
	host := fs.String("host", "", "only sites whose host contains this")
	match := fs.String("match", "", "regexp against the shown URL and title")
	dupes := fs.Bool("dupes", false, "only extra copies of a URL bookmarked more than once, oldest kept")
	open := fs.Bool("open", false, "only bookmarks whose page is open as a tab in the same profile")
	unused := fs.Float64("unused", 0, "only bookmarks not used, or added if never used, for this many days")
	idsOnly := fs.Bool("ids", false, "print refs only")
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
	cutoff := time.Now().Add(-time.Duration(*unused * float64(24*time.Hour)))

	var rows []bookmarkRow
	for _, p := range connectAll() {
		if *only != "" && !p.Is(*only) {
			continue
		}
		s, err := loadBookmarks(p)
		if err != nil {
			return fail("%s: %v", p.Name(), err)
		}
		marks := s.bookmarks()
		extra := map[string]bool{}
		if *dupes {
			extra = bookmarkDupes(marks)
		}
		openURLs := map[string]bool{}
		for _, t := range p.Tabs {
			openURLs[withoutFragment(t.URL)] = true
		}
		for _, b := range marks {
			path := s.path(b.ParentID)
			u, title := shown(b.URL, b.Title, *reveal)
			last := b.DateLastUsed
			if last == 0 {
				last = b.DateAdded
			}
			switch {
			case *folder != "" && !strings.Contains(strings.ToLower(path), strings.ToLower(*folder)),
				*host != "" && !strings.Contains(hostOf(b.URL), *host),
				re != nil && !re.MatchString(u+" "+title),
				*dupes && !extra[b.ID],
				*open && !openURLs[withoutFragment(b.URL)],
				*unused > 0 && (last == 0 || time.UnixMilli(int64(last)).After(cutoff)):
				continue
			}
			rows = append(rows, bookmarkRow{p, b, path, u, title})
		}
	}

	switch {
	case *idsOnly:
		for _, r := range rows {
			fmt.Println(ref(r.p, r.b.ID))
		}
	case *asJSON:
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{
				"ref": ref(r.p, r.b.ID), "profile": r.p.Name(), "folder": r.folder, "title": r.title, "url": r.url,
				"dateAdded": r.b.DateAdded, "dateLastUsed": r.b.DateLastUsed,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", " ")
		_ = enc.Encode(out)
	default:
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		var lastP *profile
		lastFolder := "\x00"
		for _, r := range rows {
			if r.p != lastP || r.folder != lastFolder {
				w.Flush()
				fmt.Printf("\n%s %s\n", r.p.Name(), r.folder)
				lastP, lastFolder = r.p, r.folder
			}
			place := hostOf(r.url)
			if *urls {
				place = truncate(r.url, 100)
			}
			added, used := "-", "never"
			if r.b.DateAdded != 0 {
				added = time.UnixMilli(int64(r.b.DateAdded)).Format("2006-01-02")
			}
			if r.b.DateLastUsed != 0 {
				used = age(time.UnixMilli(int64(r.b.DateLastUsed)))
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", ref(r.p, r.b.ID), added, used, place, truncate(r.title, 80))
		}
		w.Flush()
		fmt.Printf("\n%d bookmarks\n", len(rows))
	}
	return 0
}

func bookmarksTree(args []string) int {
	fs := newFlags("bookmarks tree")
	only := fs.String("profile", "", "only this profile")
	parseArgs(fs, args)
	for _, p := range connectAll() {
		if *only != "" && !p.Is(*only) {
			continue
		}
		s, err := loadBookmarks(p)
		if err != nil {
			return fail("%s: %v", p.Name(), err)
		}
		fmt.Printf("\n%s\n", p.Name())
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		var total func(id string) int
		total = func(id string) int {
			n := 0
			for _, c := range s.children[id] {
				if c.Folder {
					n += total(c.ID)
				} else {
					n++
				}
			}
			return n
		}
		var walk func(id string, depth int)
		walk = func(id string, depth int) {
			for _, c := range s.children[id] {
				if !c.Folder {
					continue
				}
				direct := 0
				for _, k := range s.children[c.ID] {
					if !k.Folder {
						direct++
					}
				}
				name := c.Title
				if rn, ok := s.rootName[c.ID]; ok {
					name = rn
				}
				fmt.Fprintf(w, "  %s%s\t%d here\t%d in all\t%s\n", strings.Repeat("  ", depth), name, direct, total(c.ID), ref(p, c.ID))
				walk(c.ID, depth+1)
			}
		}
		walk("0", 0)
		w.Flush()
	}
	return 0
}

func bookmarksOpen(args []string) int {
	fs := newFlags("bookmarks open")
	only := fs.String("profile", "", "profile for bare IDs")
	window := fs.Int("window", 0, "window to open in, default the profile's focused or first window")
	ps := connectAll()
	p, ids, err := splitRefs(ps, expandStdin(parseArgs(fs, args)), *only)
	if err != nil {
		return fail("%v", err)
	}
	s, err := loadBookmarks(p)
	if err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	var urls []string
	for _, id := range ids {
		b, ok := s.byID[id]
		switch {
		case !ok:
			return fail("no bookmark %s", ref(p, id))
		case b.Folder:
			for _, c := range s.children[id] {
				if !c.Folder {
					urls = append(urls, c.URL)
				}
			}
		default:
			urls = append(urls, b.URL)
		}
	}
	wid := *window
	if wid == 0 {
		for _, w := range p.Windows {
			if wid == 0 || w.Focused {
				wid = w.ID
			}
		}
	} else if !p.hasWindow(wid) {
		return fail("window %d is not in profile %s", wid, p.Name())
	}
	var st exitStatus
	opened := 0
	for _, u := range urls {
		if err := p.Call("open", map[string]any{"url": u, "windowId": wid}, nil); err != nil {
			st.fail("%s: %v", p.Name(), err)
			continue
		}
		opened++
	}
	fmt.Printf("%s: opened %d in window %d\n", p.Name(), opened, wid)
	return st.code()
}

func bookmarksMove(args []string) int {
	fs := newFlags("bookmarks mv")
	only := fs.String("profile", "", "profile for bare IDs")
	to := fs.String("to", "", "target folder ref")
	index := fs.Int("index", -1, "position in the folder, -1 for the end")
	ps := connectAll()
	p, ids, err := splitRefs(ps, expandStdin(parseArgs(fs, args)), *only)
	if err != nil {
		return fail("%v", err)
	}
	if *to == "" {
		return fail("--to is required")
	}
	tp, folder, err := splitRef(ps, *to, p.Name())
	if err != nil {
		return fail("%v", err)
	}
	if tp != p {
		return fail("bookmarks can't move between profiles")
	}
	var r struct {
		Moved []string `json:"moved"`
	}
	if err := p.Call("bookmarkMove", map[string]any{"ids": ids, "parentId": folder, "index": *index}, &r); err != nil {
		return fail("%s: %v. Bookmarks before the failing one may have moved, check with tabs bookmarks list --folder.", p.Name(), err)
	}
	fmt.Printf("%s: moved %d to %s\n", p.Name(), len(r.Moved), ref(p, folder))
	return 0
}

func bookmarksRemove(args []string) int {
	fs := newFlags("bookmarks rm")
	only := fs.String("profile", "", "profile for bare IDs")
	force := fs.Bool("force", false, "also remove folders that still hold bookmarks")
	ps := connectAll()
	p, ids, err := splitRefs(ps, expandStdin(parseArgs(fs, args)), *only)
	if err != nil {
		return fail("%v", err)
	}
	var r struct {
		Removed []string `json:"removed"`
		Refused []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"refused"`
		Failed []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"failed"`
	}
	if err := p.Call("bookmarkRemove", map[string]any{"ids": ids, "force": *force}, &r); err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	fmt.Printf("%s: removed %d\n", p.Name(), len(r.Removed))
	var st exitStatus
	for _, x := range r.Refused {
		fmt.Printf("%s: refused %s (%s), pass --force to override\n", p.Name(), ref(p, x.ID), x.Reason)
		st.partial = true
	}
	for _, x := range r.Failed {
		fmt.Printf("%s: failed %s (%s)\n", p.Name(), ref(p, x.ID), x.Reason)
		st.partial = true
	}
	return st.code()
}

func bookmarksMkdir(args []string) int {
	fs := newFlags("bookmarks mkdir")
	only := fs.String("profile", "", "profile for a bare parent ID")
	pos := parseArgs(fs, args)
	if len(pos) != 2 {
		return fail("usage: tabs bookmarks mkdir PARENT NAME")
	}
	p, parent, err := splitRef(connectAll(), pos[0], *only)
	if err != nil {
		return fail("%v", err)
	}
	var b Bookmark
	if err := p.Call("bookmarkCreate", map[string]any{"parentId": parent, "title": pos[1]}, &b); err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	fmt.Printf("%s: created folder %q as %s\n", p.Name(), b.Title, ref(p, b.ID))
	return 0
}

func bookmarksRename(args []string) int {
	fs := newFlags("bookmarks rename")
	only := fs.String("profile", "", "profile for a bare ID")
	pos := parseArgs(fs, args)
	if len(pos) != 2 {
		return fail("usage: tabs bookmarks rename REF NAME")
	}
	p, id, err := splitRef(connectAll(), pos[0], *only)
	if err != nil {
		return fail("%v", err)
	}
	var b Bookmark
	if err := p.Call("bookmarkRename", map[string]any{"id": id, "title": pos[1]}, &b); err != nil {
		return fail("%s: %v", p.Name(), err)
	}
	fmt.Printf("%s: %s is now %q\n", p.Name(), ref(p, b.ID), b.Title)
	return 0
}
