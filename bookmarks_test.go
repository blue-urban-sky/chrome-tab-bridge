package main

import (
	"os"
	"path/filepath"
	"testing"
)

func boolp(b bool) *bool { return &b }

func sampleBookmarks() *bookmarkSet {
	return newBookmarkSet([]Bookmark{
		{ID: "0", Folder: true},
		{ID: "1", ParentID: "0", Title: "Bookmarks bar", Folder: true, Syncing: boolp(false)},
		{ID: "2", ParentID: "0", Title: "Other bookmarks", Folder: true},
		{ID: "9", ParentID: "0", Title: "Bookmarks bar", Folder: true, Syncing: boolp(true)},
		{ID: "10", ParentID: "1", Index: 0, Title: "Tools", Folder: true},
		{ID: "11", ParentID: "10", Index: 0, Title: "a", URL: "https://a/x", DateAdded: 200},
		{ID: "12", ParentID: "2", Index: 0, Title: "a again", URL: "https://a/x#top", DateAdded: 100},
		{ID: "13", ParentID: "9", Index: 0, Title: "b", URL: "https://b/"},
	})
}

func TestBookmarkPathsNameTwinRoots(t *testing.T) {
	s := sampleBookmarks()
	cases := map[string]string{
		"10": "Bookmarks bar [device]/Tools",
		"2":  "Other bookmarks",
		"9":  "Bookmarks bar [account]",
	}
	for id, want := range cases {
		if got := s.path(id); got != want {
			t.Errorf("path(%s) = %q, want %q", id, got, want)
		}
	}
	if n := len(s.bookmarks()); n != 3 {
		t.Fatalf("want 3 bookmarks, folders excluded, got %d", n)
	}
}

func TestBookmarkDupesKeepTheOldest(t *testing.T) {
	extra := bookmarkDupes(sampleBookmarks().bookmarks())
	if !extra["11"] || extra["12"] || extra["13"] {
		t.Fatalf("want only 11 marked, the newer copy, got %v", extra)
	}
}

func TestSplitRef(t *testing.T) {
	work := &profile{Snapshot: Snapshot{Profile: ProfileInfo{ID: "aaaa1111", Label: "work"}}}
	home := &profile{Snapshot: Snapshot{Profile: ProfileInfo{ID: "bbbb2222", Label: "personal"}}}
	ps := []*profile{work, home}
	if p, id, err := splitRef(ps, "personal:42", ""); err != nil || p != home || id != "42" {
		t.Fatalf("personal:42 -> %v %q %v", p, id, err)
	}
	if p, id, err := splitRef(ps, "42", "work"); err != nil || p != work || id != "42" {
		t.Fatalf("42 with --profile work -> %v %q %v", p, id, err)
	}
	if _, _, err := splitRef(ps, "42", ""); err == nil {
		t.Fatal("a bare ID with two profiles connected must be refused, IDs repeat across profiles")
	}
}

func TestIdentifyByAccountThenByExtensionStorage(t *testing.T) {
	dir := t.TempDir()
	old := chromeDir
	chromeDir = dir
	defer func() { chromeDir = old }()

	localState := `{"profile":{"info_cache":{
		"Default":{"name":"Work","user_name":"me@work.example","gaia_id":"111"},
		"Profile 1":{"name":"Home","user_name":"","gaia_id":""}}}}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(localState), 0o600); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "Profile 1", "Local Extension Settings", "extid")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "000003.log"), []byte("junk\x00profileId\x00\"cafe1234\"junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	cp, ok := identify(ProfileInfo{ID: "x", GaiaID: "111"})
	if !ok || cp.Dir != "Default" || cp.Name != "Work" || cp.How != "signed-in account" {
		t.Fatalf("by account: %+v %v", cp, ok)
	}
	cp, ok = identify(ProfileInfo{ID: "cafe1234", ExtensionID: "extid"})
	if !ok || cp.Dir != "Profile 1" || cp.Name != "Home" || cp.How != "extension storage" {
		t.Fatalf("by storage: %+v %v", cp, ok)
	}
	if _, ok := identify(ProfileInfo{ID: "nothere", ExtensionID: "extid"}); ok {
		t.Fatal("an unknown profile must not match")
	}
}
