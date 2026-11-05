package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Tab struct {
	ID           int     `json:"id"`
	WindowID     int     `json:"windowId"`
	Index        int     `json:"index"`
	GroupID      int     `json:"groupId"`
	Pinned       bool    `json:"pinned"`
	Active       bool    `json:"active"`
	Audible      bool    `json:"audible"`
	Discarded    bool    `json:"discarded"`
	LastAccessed float64 `json:"lastAccessed"`
	URL          string  `json:"url"`
	Title        string  `json:"title"`
}

type Group struct {
	ID        int    `json:"id"`
	WindowID  int    `json:"windowId"`
	Title     string `json:"title"`
	Color     string `json:"color"`
	Collapsed bool   `json:"collapsed"`
}

type Window struct {
	ID        int    `json:"id"`
	Focused   bool   `json:"focused"`
	Incognito bool   `json:"incognito"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Left      int    `json:"left"`
	Top       int    `json:"top"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type ProfileInfo struct {
	ID          string `json:"profileId"`
	Label       string `json:"label"`
	Email       string `json:"email"`
	GaiaID      string `json:"gaiaId"`
	ExtensionID string `json:"extensionId"`
}

type Snapshot struct {
	Profile ProfileInfo `json:"profile"`
	Windows []Window    `json:"windows"`
	Groups  []Group     `json:"groups"`
	Tabs    []Tab       `json:"tabs"`
}

type profile struct {
	Sock string
	Snapshot
}

func (p *profile) Name() string {
	if p.Profile.Label != "" {
		return p.Profile.Label
	}
	return p.Profile.ID
}

func (p *profile) Is(s string) bool {
	return s == p.Profile.ID || strings.EqualFold(s, p.Profile.Label)
}

func (p *profile) Call(cmd string, args, out any) error {
	return call(p.Sock, cmd, args, time.Minute, out)
}

func (p *profile) tab(id int) (Tab, bool) {
	for _, t := range p.Tabs {
		if t.ID == id {
			return t, true
		}
	}
	return Tab{}, false
}

func (p *profile) group(id int) (Group, bool) {
	for _, g := range p.Groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

func (p *profile) hasWindow(id int) bool {
	for _, w := range p.Windows {
		if w.ID == id {
			return true
		}
	}
	return false
}

type clientRequest struct {
	Cmd     string `json:"cmd"`
	Args    any    `json:"args,omitempty"`
	Timeout int    `json:"timeout"`
}

type bridgeError string

func (e bridgeError) Error() string { return string(e) }

func call(sock, cmd string, args any, timeout time.Duration, out any) error {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout + 5*time.Second))
	if err := json.NewEncoder(c).Encode(clientRequest{Cmd: cmd, Args: args, Timeout: int(timeout / time.Second)}); err != nil {
		return err
	}
	var r reply
	if err := json.NewDecoder(c).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return bridgeError(r.Error)
	}
	if out == nil || len(r.Result) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

func connectAll() []*profile {
	socks, _ := filepath.Glob(filepath.Join(runDir(), "*.sock"))
	modTime := func(path string) time.Time {
		info, err := os.Stat(path)
		if err != nil {
			return time.Time{}
		}
		return info.ModTime()
	}
	sort.Slice(socks, func(i, j int) bool { return modTime(socks[i]).After(modTime(socks[j])) })

	var out []*profile
	seen := map[string]bool{}
	for _, s := range socks {
		p := &profile{Sock: s}
		if err := call(s, "list", nil, 15*time.Second, &p.Snapshot); err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist) {
				_ = os.Remove(s)
			} else {
				fmt.Fprintf(os.Stderr, "skipping %s: %v\n", filepath.Base(s), err)
			}
			continue
		}
		if seen[p.Profile.ID] {
			continue
		}
		seen[p.Profile.ID] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		fmt.Fprintln(os.Stderr, "no Chrome profiles connected. Load the extension in each profile, see README.md.")
		os.Exit(1)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func findProfile(ps []*profile, name string) *profile {
	for _, p := range ps {
		if p.Is(name) {
			return p
		}
	}
	return nil
}

func tabOwner(ps []*profile, id int) *profile {
	for _, p := range ps {
		if _, ok := p.tab(id); ok {
			return p
		}
	}
	return nil
}

func windowOwner(ps []*profile, id int) *profile {
	for _, p := range ps {
		if p.hasWindow(id) {
			return p
		}
	}
	return nil
}

func groupOwner(ps []*profile, id int) *profile {
	for _, p := range ps {
		if _, ok := p.group(id); ok {
			return p
		}
	}
	return nil
}
