# chrome-tab-bridge

Chrome's AppleScript support can list and close tabs but can't group them, sort them, sleep them, or move one between windows without losing it. This gives the terminal Chrome's full tab API through a small extension and a native messaging host, so Claude Code (or you) can tidy tabs with a `tabs` command.

## Parts

| Part | What it does |
|---|---|
| `extension/` | Chrome extension. Tab and bookmark metadata plus the profile's signed-in account. No access to page contents. Its toolbar badge shows the profile name, blue when the bridge is connected and red when not. |
| `tabs` (Go, built from this folder) | Chrome launches it as the native host, one process per profile, each listening on a socket in `~/Library/Application Support/chrome-tab-bridge/run/`. Run by hand it is the CLI. |

## Install

macOS only, needs Go.

1. `./install.sh` builds `~/.local/bin/tabs` and registers it with Chrome.
2. In each Chrome profile open `chrome://extensions`, turn on Developer mode, Load unpacked, pick `extension/`.
3. `tabs status` lists each connected profile. Name them with `tabs label <id> work`.

The extension ID is fixed by the `key` in `manifest.json`, so the registration in step 1 works before the extension is loaded. Rebuild with `./install.sh` after changing the Go code. Run `tabs reload` after changing `extension/`.

`tabs status` names the Chrome profile behind each bridge profile. It matches the signed-in account against Chrome's `Local State`, and falls back to finding the bridge ID in the extension's storage folder when a profile isn't signed in.

## Use

`tabs` with no arguments prints every command. The common ones:

```sh
tabs list --stale 14                     # not viewed for two weeks
tabs list --dupes --ids | tabs close -   # close extra copies
tabs park $(tabs list --host amazon --ids) --name shopping
tabs group 12 13 14 --title CI --color blue
tabs sort 435803385                      # groups first, then loose tabs, each by site
tabs closed                              # recently closed, then tabs restore <session>
tabs bookmarks tree --profile work       # folders with counts
tabs bookmarks --open                    # bookmarks whose page is already open
tabs bookmarks --dupes --ids | tabs bookmarks rm -
tabs bookmarks open personal:812         # a parked folder back as tabs
```

Bookmark IDs repeat across profiles, so they print as `profile:id`.

## Rules it holds

- Pinned tabs, tabs playing audio and Meet calls are refused by close, park and discard unless `--force`.
- `bookmarks rm` refuses a folder that still holds bookmarks unless `--force`. The CLI has no undo for removals.
- Park writes the bookmarks to `Other bookmarks/Parked tabs/<date> <name>` and only closes the tabs once every bookmark exists.
- Titles and URLs print as `[hidden]` unless `--reveal` for hosts listed in `~/.config/tabs/hidden-hosts`, so pages carrying other people's details stay out of an agent's context. One regexp per line matched against the host, `!host` to always show one host, `#` for comments. With no file, Gmail is hidden. A file that fails to parse hides everything.
- Tabs can't move between profiles. `tabs move --reopen` opens the URL in the other profile and closes the original, which reloads it and drops its back history.
- Profile IDs are random and stored in the extension, so they survive restarts.
- Chrome may give a discarded tab a new ID. `tabs discard` prints any that change.
