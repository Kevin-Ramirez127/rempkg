package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const version = "0.1.0"

// tagList collects repeated --tag / -t flags.
type tagList []string

func (t *tagList) String() string { return strings.Join(*t, ",") }

func (t *tagList) Set(v string) error {
	*t = append(*t, v)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "rempkg: %v\n", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	var (
		tags    tagList
		file    string
		dryRun  bool
		list    bool
		forget  bool
		showVer bool
		showHelp bool
		jsonOut bool
		filterTag string
	)

	fs := flag.NewFlagSet("rempkg", flag.ContinueOnError)
	fs.Var(&tags, "tag", "tag to attach to tracked packages (repeatable)")
	fs.Var(&tags, "t", "shorthand for --tag")
	fs.StringVar(&file, "file", "", "path to JSONC tracking file (overrides $REMPKG_FILE)")
	fs.StringVar(&file, "f", "", "shorthand for --file")
	fs.BoolVar(&dryRun, "dry-run", false, "print the manager command without executing it")
	fs.BoolVar(&list, "list", false, "list tracked packages and exit")
	fs.BoolVar(&list, "l", false, "shorthand for --list")
	fs.BoolVar(&forget, "forget", false, "untrack packages without uninstalling (use with package names)")
	fs.BoolVar(&showVer, "version", false, "print version and exit")
	fs.BoolVar(&showVer, "v", false, "shorthand for --version")
	fs.BoolVar(&showHelp, "help", false, "print help and exit")
	fs.BoolVar(&showHelp, "h", false, "shorthand for --help")
	fs.BoolVar(&jsonOut, "json", false, "with --list: output raw JSON")
	fs.StringVar(&filterTag, "filter-tag", "", "with --list: only show entries with this tag")

	// Split rempkg flags from the manager command at the first
	// non-flag positional or a "--" separator.
	manager, managerArgs, rempkgArgs := splitCommand(argv)
	_ = managerArgs // parsed below from full positionals

	if err := fs.Parse(rempkgArgs); err != nil {
		if err == flag.ErrHelp {
			usage()
			return nil
		}
		return err
	}

	if showHelp {
		usage()
		return nil
	}
	if showVer {
		fmt.Printf("rempkg %s\n", version)
		return nil
	}

	storePath := resolveStorePath(file)
	store := Store{Path: storePath}

	if list {
		return cmdList(store, jsonOut, filterTag)
	}

	rest := fs.Args()
	if forget {
		if len(rest) == 0 && manager == "" {
			return fmt.Errorf("usage: rempkg --forget <package> [...]")
		}
		// `rempkg --forget foo bar` (no manager) or `rempkg --forget pacman foo`?
		// If a manager name leaked into positionals, treat everything as names.
		names := rest
		if manager != "" {
			names = append([]string{manager}, rest...)
			// managerArgs belong to a real command, not forget mode; but
			// --forget never runs a manager, so include them as names only
			// if the user clearly passed them. We use splitCommand's tail:
			names = append(names, managerArgs...)
		}
		removed, err := store.Forget(names)
		if err != nil {
			return err
		}
		fmt.Printf("forgot %d package(s) [%s]\n", len(removed), storePath)
		return nil
	}

	if manager == "" {
		if len(rest) > 0 {
			manager = rest[0]
			managerArgs = rest[1:]
		} else {
			usage()
			return fmt.Errorf("missing package manager command")
		}
	}

	managerBase := filepath.Base(manager)
	rawCmd := managerBase
	if len(managerArgs) > 0 {
		rawCmd += " " + strings.Join(managerArgs, " ")
	}

	if dryRun {
		fmt.Printf("+ %s\n", rawCmd)
		op, pkgs := ParseManagerCommand(managerBase, managerArgs)
		fmt.Printf("(dry-run) op=%s packages=%v tags=%v file=%s\n", op, pkgs, []string(tags), storePath)
		return nil
	}

	managerPath, err := resolveManager(manager)
	if err != nil {
		return err
	}

	cmd := exec.Command(managerPath, managerArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}

	op, pkgs := ParseManagerCommand(managerBase, managerArgs)
	switch op {
	case OpInstall:
		added, err := store.Upsert(pkgs, managerBase, rawCmd, tags, time.Now())
		if err != nil {
			return fmt.Errorf("command succeeded but tracking failed: %w", err)
		}
		fmt.Printf("rempkg: tracked %d new, %d total in %s\n", added, len(pkgs), storePath)
	case OpRemove:
		removed, err := store.Remove(pkgs)
		if err != nil {
			return fmt.Errorf("command succeeded but untracking failed: %w", err)
		}
		if len(removed) > 0 {
			fmt.Printf("rempkg: untracked %d package(s) in %s\n", len(removed), storePath)
		}
	default:
		// query / upgrade / search: nothing to track
	}

	return nil
}

// splitCommand separates rempkg's own flags from the manager invocation.
// Returns (manager, managerArgs, rempkgArgs). The manager is the first
// positional arg before any "--"; everything after it belongs to the manager.
func splitCommand(argv []string) (string, []string, []string) {
	for i, a := range argv {
		if a == "--" {
			// Everything before is rempkg flags, everything after is the command.
			rempkgArgs := argv[:i]
			rest := argv[i+1:]
			if len(rest) == 0 {
				return "", nil, rempkgArgs
			}
			return rest[0], rest[1:], rempkgArgs
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		// First positional: this is the manager. But flags like
		// --tag consume the next arg, so make sure this positional is not
		// a value of a preceding --tag/--file/-t/-f.
		if isFlagValue(argv, i) {
			continue
		}
		return a, argv[i+1:], argv[:i]
	}
	return "", nil, argv
}

// isFlagValue reports whether argv[i] is the value of a preceding
// flag that takes an argument.
func isFlagValue(argv []string, i int) bool {
	if i == 0 {
		return false
	}
	prev := argv[i-1]
	if prev == "--tag" || prev == "-t" || prev == "--file" || prev == "-f" ||
		prev == "--filter-tag" {
		return true
	}
	return false
}

// resolveStorePath picks the JSONC file location.
// Priority: --file flag > $REMPKG_FILE env > ~/rempkg/packages.jsonc.
// When run via sudo, the invoking user's home is used (via $SUDO_USER),
// so `sudo rempkg ...` still tracks to /home/<user>/rempkg/packages.jsonc.
func resolveStorePath(flagFile string) string {
	if flagFile != "" {
		return flagFile
	}
	if env := os.Getenv("REMPKG_FILE"); env != "" {
		return env
	}
	return filepath.Join(invokingUserHome(), "rempkg", "packages.jsonc")
}

// invokingUserHome returns the home directory of the user who invoked
// rempkg, even when running elevated via sudo (where $HOME is /root).
func invokingUserHome() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		if u, err := user.Lookup(sudoUser); err == nil && u.HomeDir != "" {
			return u.HomeDir
		}
		// Fallback for systems without nsswitch/user lookup.
		return filepath.Join("/home", sudoUser)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return filepath.Join("/home", os.Getenv("USER"))
}

func cmdList(store Store, jsonOut bool, filterTag string) error {
	entries, err := store.Load()
	if err != nil {
		return err
	}
	if filterTag != "" {
		var kept []Entry
		for _, e := range entries {
			for _, t := range e.Tags {
				if t == filterTag {
					kept = append(kept, e)
					break
				}
			}
		}
		entries = kept
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].Manager < entries[j].Manager
		}
		return entries[i].Name < entries[j].Name
	})
	if jsonOut {
		out, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	if len(entries) == 0 {
		fmt.Printf("no tracked packages (%s)\n", store.Path)
		return nil
	}
	fmt.Printf("%-28s %-10s %-20s %s\n", "NAME", "MANAGER", "TAGS", "INSTALLED_AT")
	for _, e := range entries {
		fmt.Printf("%-28s %-10s %-20s %s\n",
			e.Name, e.Manager, strings.Join(e.Tags, ","), e.InstalledAt)
	}
	return nil
}

func usage() {
	fmt.Print(`rempkg - track installs across package managers

Usage:
  sudo rempkg [--tag <tag>...] [--file <path>] <manager> [manager-args...]
  sudo rempkg --tag delete-later --tag tool pacman -S opencode
  rempkg --list [--filter-tag <tag>]
  rempkg --forget <package> [...]

Options:
  --tag, -t <tag>     tag to attach (repeatable)
  --file, -f <path>   tracking file (overrides $REMPKG_FILE)
  --dry-run           print the command without running it
  --list, -l          list tracked packages
  --filter-tag <tag>  with --list: filter by tag
  --forget            untrack packages without uninstalling
  --help, -h          show this help
  --version, -v       show version

Env:
  REMPKG_FILE         path to packages.jsonc
                      default: ~/rempkg/packages.jsonc
                      (/home/<user>/rempkg/packages.jsonc, even under sudo)

Examples:
  sudo rempkg pacman -S opencode
  sudo rempkg --tag delete-later --tag tool pacman -S opencode
  sudo rempkg yay -S google-chrome
  sudo rempkg pacman -Rns opencode
  REMPKG_FILE=./packages.jsonc rempkg --dry-run --tag tool pacman -S foo bar
`)
}
