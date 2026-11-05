package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const usage = `tabs manages Chrome tabs through the Tab bridge extension.

  tabs status                                 profiles, which Chrome profile each is, their windows
  tabs focus WINDOW                           bring a window to the front
  tabs show ID                                switch to a tab and bring its window to the front
  tabs autoplay [on|off] [--profile P]        YouTube autoplay before you touch the page, off by default
  tabs label PROFILE NAME                     name a profile, shown on the extension badge
  tabs list [filters] [output]                tabs, one per line
  tabs groups                                 tab groups per window
  tabs close ID... [--force]
  tabs move ID... --window W [--index N] [--reopen] [--force]
  tabs group ID... [--title T] [--color C] [--into GROUP]
  tabs group-edit GROUP [--title T] [--color C] [--collapse | --expand]
  tabs ungroup ID...
  tabs sort WINDOW [--order GROUP,GROUP...]   groups first, then loose tabs, each by site
  tabs pin ID... / tabs unpin ID...
  tabs mute ID... / tabs unmute ID...         flags in list: * active, P pinned, A sound, M muted, Z asleep
  tabs discard ID... [--force]                sleep tabs, they stay open
  tabs park ID... [--name NAME] [--force]     bookmark, then close
  tabs closed [--profile P] [-n N]            recently closed
  tabs restore SESSION [--profile P]
  tabs bookmarks ...                          list, tree, open, mv, rm, mkdir, rename. tabs bookmarks help
  tabs reload                                 reload the extension in every profile after changing it
  tabs install --extension DIR                register the bridge with Chrome

ID can be - to read IDs from stdin.
list filters: --profile --window --host --group (or none) --match --stale DAYS --dupes
list output:  --ids --urls --json --reveal
Titles and URLs on hosts in ~/.config/tabs/hidden-hosts (default: Gmail) print as [hidden] unless --reveal.
Pinned tabs, tabs playing audio and Meet calls are refused unless --force.
`

var commands = map[string]func([]string) int{
	"status":     cmdStatus,
	"focus":      cmdFocus,
	"show":       cmdShow,
	"autoplay":   cmdAutoplay,
	"reload":     cmdReload,
	"bookmarks":  cmdBookmarks,
	"label":      cmdLabel,
	"list":       cmdList,
	"groups":     cmdGroups,
	"close":      cmdClose,
	"move":       cmdMove,
	"group":      cmdGroup,
	"group-edit": cmdGroupEdit,
	"ungroup":    cmdUngroup,
	"sort":       cmdSort,
	"pin":        func(a []string) int { return cmdPin(a, true) },
	"unpin":      func(a []string) int { return cmdPin(a, false) },
	"mute":       func(a []string) int { return cmdMute(a, true) },
	"unmute":     func(a []string) int { return cmdMute(a, false) },
	"discard":    cmdDiscard,
	"park":       cmdPark,
	"closed":     cmdClosed,
	"restore":    cmdRestore,
	"install":    cmdInstall,
}

func main() {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "chrome-extension://") {
		if err := runHost(); err != nil {
			fmt.Fprintln(os.Stderr, "tabs host:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(1)
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	os.Exit(cmd(os.Args[2:]))
}

func newFlags(name string) *flag.FlagSet {
	return flag.NewFlagSet("tabs "+name, flag.ExitOnError)
}

func parseArgs(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args)
		rest := fs.Args()
		if len(rest) == 0 {
			return pos
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func setFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	return 1
}
