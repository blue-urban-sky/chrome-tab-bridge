package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

var chromeDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}()

type chromeProfile struct {
	Dir   string
	Name  string
	Email string
	How   string
}

type profileCacheEntry struct {
	Name     string `json:"name"`
	UserName string `json:"user_name"`
	GaiaID   string `json:"gaia_id"`
}

func readProfileCache() map[string]profileCacheEntry {
	raw, err := os.ReadFile(filepath.Join(chromeDir, "Local State"))
	if err != nil {
		return nil
	}
	var state struct {
		Profile struct {
			InfoCache map[string]profileCacheEntry `json:"info_cache"`
		} `json:"profile"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return nil
	}
	return state.Profile.InfoCache
}

func identify(info ProfileInfo) (chromeProfile, bool) {
	cache := readProfileCache()
	for dir, e := range cache {
		if (info.GaiaID != "" && e.GaiaID == info.GaiaID) || (info.Email != "" && strings.EqualFold(e.UserName, info.Email)) {
			return chromeProfile{Dir: dir, Name: e.Name, Email: e.UserName, How: "signed-in account"}, true
		}
	}
	if info.ExtensionID == "" || info.ID == "" {
		return chromeProfile{}, false
	}
	stores, _ := filepath.Glob(filepath.Join(chromeDir, "*", "Local Extension Settings", info.ExtensionID))
	for _, store := range stores {
		files, _ := filepath.Glob(filepath.Join(store, "*"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil || !bytes.Contains(data, []byte(info.ID)) {
				continue
			}
			dir := filepath.Base(filepath.Dir(filepath.Dir(store)))
			e := cache[dir]
			return chromeProfile{Dir: dir, Name: e.Name, Email: e.UserName, How: "extension storage"}, true
		}
	}
	return chromeProfile{}, false
}
