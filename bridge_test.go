package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func fakeChrome(r io.Reader, w io.Writer) {
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return
		}
		body := make([]byte, binary.NativeEndian.Uint32(header))
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var msg struct {
			ID   string `json:"id"`
			Cmd  string `json:"cmd"`
			Args struct {
				IDs []int `json:"ids"`
			} `json:"args"`
		}
		_ = json.Unmarshal(body, &msg)
		out := map[string]any{"id": msg.ID, "ok": false, "error": "unknown command: " + msg.Cmd}
		switch msg.Cmd {
		case "list":
			out = map[string]any{"id": msg.ID, "ok": true, "result": map[string]any{
				"profile": map[string]any{"profileId": "abcd1234", "label": "work"},
				"windows": []any{map[string]any{"id": 1, "focused": true}},
				"groups":  []any{},
				"tabs":    []any{map[string]any{"id": 435803385, "windowId": 1, "groupId": -1, "url": "https://github.com/x", "title": "x"}},
			}}
		case "close":
			out = map[string]any{"id": msg.ID, "ok": true, "result": map[string]any{"closed": msg.Args.IDs}}
		}
		data, _ := json.Marshal(out)
		_, _ = w.Write(binary.NativeEndian.AppendUint32(nil, uint32(len(data))))
		_, _ = w.Write(data)
	}
}

func TestHostRelaysBetweenCLIAndChrome(t *testing.T) {
	dir, err := os.MkdirTemp("", "tb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	defer fromHostR.Close()
	done := make(chan error, 1)
	go func() { done <- serveHost(dir, toHostR, fromHostW) }()
	go fakeChrome(fromHostR, toHostW)

	sock := filepath.Join(dir, strconv.Itoa(os.Getpid())+".sock")
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var snap Snapshot
	if err := call(sock, "list", nil, 5*time.Second, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Profile.Label != "work" || len(snap.Tabs) != 1 || snap.Tabs[0].ID != 435803385 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	var closed struct {
		Closed []int `json:"closed"`
	}
	if err := call(sock, "close", map[string]any{"ids": []int{435803385}}, 5*time.Second, &closed); err != nil {
		t.Fatal(err)
	}
	if len(closed.Closed) != 1 || closed.Closed[0] != 435803385 {
		t.Fatalf("large tab IDs must survive the relay intact, got %v", closed.Closed)
	}

	var be bridgeError
	if err := call(sock, "nope", nil, 5*time.Second, nil); !errors.As(err, &be) || string(be) != "unknown command: nope" {
		t.Fatalf("want the extension's error passed through, got %v", err)
	}

	toHostW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host did not exit when Chrome closed the pipe")
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket left behind after the host exited")
	}
}

func TestHiddenHosts(t *testing.T) {
	old := rules
	defer func() { rules = old }()
	var err error
	rules, err = parseHideRules("# work admin\n(^|\\.)corp\\.example$\n^mail\\.google\\.com$\n\n!blog.corp.example\n")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		url        string
		wantHidden bool
	}{
		{"https://admin.corp.example/policies/123?email=a@b.c", true},
		{"https://app-qa.env.dev.corp.example/quote", true},
		{"https://mail.google.com/mail/u/0/#inbox/abc", true},
		{"https://blog.corp.example/post", false},
		{"https://corp.atlassian.net/browse/X-1", false},
		{"https://github.com/someone/repo/pull/1", false},
		{"https://notcorp.example/", false},
	}
	for _, c := range cases {
		u, title := shown(c.url, "Jane Customer", false)
		gotHidden := title == hiddenMarker
		if gotHidden != c.wantHidden {
			t.Errorf("%s: hidden=%v, want %v", c.url, gotHidden, c.wantHidden)
		}
		if gotHidden && (u == c.url || hostOf(u) != hostOf(c.url)) {
			t.Errorf("%s: hidden URL should keep only the host, got %s", c.url, u)
		}
		if u, title := shown(c.url, "Jane Customer", true); u != c.url || title != "Jane Customer" {
			t.Errorf("%s: --reveal should show everything", c.url)
		}
	}
}

func TestHideRulesDefaultAndBadConfig(t *testing.T) {
	r, err := parseHideRules(defaultHideRules)
	if err != nil || !r.hides("mail.google.com") || r.hides("github.com") {
		t.Fatalf("default should hide Gmail only: %+v %v", r, err)
	}
	if _, err := parseHideRules("ok\n(unclosed\n"); err == nil {
		t.Fatal("a bad regexp must be an error, the caller then hides everything")
	}
	if !(hideRules{all: true}).hides("github.com") {
		t.Fatal("the fail-safe must hide every host")
	}
}

func TestDuplicatesKeepTheActiveCopy(t *testing.T) {
	tabs := []Tab{
		{ID: 1, WindowID: 1, Index: 0, URL: "https://a/x"},
		{ID: 2, WindowID: 1, Index: 1, URL: "https://a/x#_", Active: true},
		{ID: 3, WindowID: 1, Index: 2, URL: "https://a/x"},
		{ID: 4, WindowID: 1, Index: 3, URL: "https://a/y"},
	}
	extra := duplicates(tabs)
	if !extra[1] || extra[2] || !extra[3] || extra[4] {
		t.Fatalf("want 1 and 3 marked as extra copies, got %v", extra)
	}
}

func TestRisks(t *testing.T) {
	if r := risks(Tab{URL: "https://meet.google.com/abc-defg-hij"}); len(r) != 1 {
		t.Fatalf("a Meet call must be refused, got %v", r)
	}
	if r := risks(Tab{URL: "https://meet.google.com/landing"}); len(r) != 0 {
		t.Fatalf("the Meet landing page is not a call, got %v", r)
	}
	if r := risks(Tab{Pinned: true, Audible: true}); len(r) != 2 {
		t.Fatalf("pinned and audible are both risks, got %v", r)
	}
}
