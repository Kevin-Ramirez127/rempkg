package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Op is the detected package-manager operation.
type Op int

const (
	OpNone Op = iota
	OpInstall
	OpRemove
)

func (o Op) String() string {
	switch o {
	case OpInstall:
		return "install"
	case OpRemove:
		return "remove"
	default:
		return "none"
	}
}

// ParseManagerCommand inspects <manager> <args...> and decides whether
// packages are being installed, removed, or neither (query/update/search).
// It returns the operation and the extracted package names.
func ParseManagerCommand(manager string, args []string) (Op, []string) {
	base := strings.ToLower(filepath.Base(manager))

	switch base {
	case "pacman", "yay", "paru", "pikaur", "trizen", "aura", "pamac", "garuda-update":
		return parsePacmanFamily(args)
	case "apt", "apt-get", "aptitude":
		return parseVerbCommand(args,
			map[string]bool{"install": true, "reinstall": true, "build-dep": true},
			map[string]bool{"remove": true, "purge": true, "autoremove": true, "autopurge": true},
		)
	case "dnf", "yum", "microdnf", "tdnf":
		return parseVerbCommand(args,
			map[string]bool{"install": true, "reinstall": true, "groupinstall": true, "localinstall": true},
			map[string]bool{"remove": true, "erase": true, "autoremove": true, "groupremove": true},
		)
	case "zypper":
		return parseVerbCommand(args,
			map[string]bool{"install": true, "in": true, "add": true},
			map[string]bool{"remove": true, "rm": true, "erase": true},
		)
	case "apk":
		return parseVerbCommand(args,
			map[string]bool{"add": true},
			map[string]bool{"del": true, "delete": true, "remove": true, "rm": true},
		)
	case "brew":
		return parseVerbCommand(args,
			map[string]bool{"install": true, "reinstall": true},
			map[string]bool{"uninstall": true, "remove": true, "rm": true},
		)
	case "flatpak":
		return parseVerbCommand(args,
			map[string]bool{"install": true},
			map[string]bool{"uninstall": true, "remove": true},
		)
	case "snap":
		return parseVerbCommand(args,
			map[string]bool{"install": true},
			map[string]bool{"remove": true, "uninstall": true},
		)
	case "pacstrap":
		// arch-install-scripts: pacstrap /mnt pkg...
		pkgs := nonFlagArgs(args)
		// first non-flag arg is usually the target dir (/mnt); drop it
		if len(pkgs) > 0 {
			pkgs = pkgs[1:]
		}
		if len(pkgs) == 0 {
			return OpNone, nil
		}
		return OpInstall, pkgs
	default:
		// Generic fallback: look for well-known verbs.
		return parseVerbCommand(args,
			map[string]bool{"install": true},
			map[string]bool{"remove": true, "uninstall": true, "erase": true, "purge": true, "delete": true},
		)
	}
}

// parsePacmanFamily handles pacman-style flags: -S (sync/install),
// -R (remove), -U (local upgrade/install), plus long options.
func parsePacmanFamily(args []string) (Op, []string) {
	var (
		hasSync   bool
		hasRemove bool
		hasLocal  bool // -U
		hasQuery  bool // -Q
		hasFiles  bool // -F
		hasDb     bool // -D
	)

	for _, a := range args {
		switch {
		case a == "--sync" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "S")):
			// short op char S inside combined flags, e.g. -S, -Syu, -Sy
			hasSync = true
		case a == "--remove" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "R")):
			hasRemove = true
		case a == "--upgrade" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "U")):
			hasLocal = true
		case a == "--query" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "Q")):
			hasQuery = true
		case a == "--files" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "F")):
			hasFiles = true
		case a == "--database" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "D")):
			hasDb = true
		}
	}

	// Query/files/db operations never change the tracked set.
	if hasQuery || hasFiles || hasDb {
		return OpNone, nil
	}

	// -U installs local package files.
	if hasLocal {
		pkgs := pacmanTargets(args, true)
		if len(pkgs) == 0 {
			return OpNone, nil
		}
		return OpInstall, normalizeLocalFiles(pkgs)
	}

	if hasRemove {
		pkgs := pacmanTargets(args, false)
		if len(pkgs) == 0 {
			return OpNone, nil
		}
		return OpRemove, pkgs
	}

	if hasSync {
		// -Ss/--search, -Si/--info, -Sg/--groups, -Sl/--list,
		// -Sp/--print, -Sc/--clean are read-only.
		for _, a := range args {
			switch a {
			case "--search", "--info", "--groups", "--list",
				"--print", "--changelog", "--clean", "--deps", "--check":
				return OpNone, nil
			}
			if len(a) > 2 && a[0] == '-' && a[1] == 'S' {
				// combined short flags, e.g. -Ss, -Ssi, -Sc, -Sg
				for _, c := range a[2:] {
					switch c {
					case 's', 'i', 'g', 'l', 'p', 'c':
						return OpNone, nil
					}
				}
			}
			if a == "-s" && hasSync {
				// `pacman -S -s foo` style search
				return OpNone, nil
			}
		}
		pkgs := pacmanTargets(args, false)
		if len(pkgs) == 0 {
			// pure `pacman -Syu` / `pacman -Sy`: system upgrade, nothing new to track
			return OpNone, nil
		}
		return OpInstall, pkgs
	}

	return OpNone, nil
}

// pacmanTargets extracts package operands from pacman-style args,
// dropping flags (starting with -) and their inline values (--foo=bar counts
// as a flag). A lone "--" ends flag parsing; everything after it is a target.
func pacmanTargets(args []string, allowPaths bool) []string {
	var out []string
	endOfFlags := false
	for _, a := range args {
		if !endOfFlags {
			if a == "--" {
				endOfFlags = true
				continue
			}
			if len(a) > 0 && a[0] == '-' {
				continue
			}
		}
		if a == "" {
			continue
		}
		// Skip stdin marker used by some wrappers.
		if a == "-" {
			continue
		}
		if !allowPaths {
			// Ignore explicit file paths for sync/remove ops.
			if strings.Contains(a, "/") && (strings.HasSuffix(a, ".pkg.tar.zst") ||
				strings.HasSuffix(a, ".pkg.tar.xz") || strings.HasPrefix(a, "/") ||
				strings.HasPrefix(a, "./") || strings.HasPrefix(a, "../")) {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// normalizeLocalFiles turns `pacman -U ./foo.pkg.tar.zst` into "foo"
// (best effort) so the tracker stores a readable name.
func normalizeLocalFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		base := filepath.Base(p)
		for _, suffix := range []string{".pkg.tar.zst", ".pkg.tar.xz", ".pkg.tar.gz", ".pkg.tar"} {
			base = strings.TrimSuffix(base, suffix)
		}
		// Strip arch/version tail? Keep it simple: keep full basename.
		out = append(out, base)
	}
	return out
}

// parseVerbCommand handles managers with subcommands:
// e.g. `apt install foo`, `dnf remove foo`.
func parseVerbCommand(args []string, installVerbs, removeVerbs map[string]bool) (Op, []string) {
	verb := ""
	verbIdx := -1
	for i, a := range args {
		if strings.HasPrefix(a, "-") || a == "" {
			continue
		}
		low := strings.ToLower(a)
		if installVerbs[low] || removeVerbs[low] {
			verb = low
			verbIdx = i
			break
		}
		// Stop at first positional that is not a known verb?
		// Keep scanning so global flags before the verb are tolerated.
		_ = i
	}
	if verbIdx < 0 {
		// No install/remove verb found. Heuristic: `apk add` style already
		// covered; `zypper in` covered. Otherwise nothing to track.
		// Also catch bare `apt foo`? No, that is not an install.
		return OpNone, nil
	}
	op := OpInstall
	if removeVerbs[verb] {
		op = OpRemove
	}
	var pkgs []string
	for _, a := range args[verbIdx+1:] {
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") || a == "" {
			continue
		}
		// Skip field assignments some managers accept (e.g. VAR=value is rare here).
		pkgs = append(pkgs, stripVersionPin(a))
	}
	if len(pkgs) == 0 {
		return OpNone, nil
	}
	return op, pkgs
}

// nonFlagArgs returns positional args (not starting with -).
func nonFlagArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") || a == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// stripVersionPin turns "foo=1.2.3" (apt) into "foo" for cleaner tracking,
// leaving other names untouched.
func stripVersionPin(pkg string) string {
	if i := strings.Index(pkg, "="); i > 0 && !strings.Contains(pkg, "/") && !strings.Contains(pkg, ":") {
		return pkg[:i]
	}
	return pkg
}

// resolveManager ensures the manager binary exists (via PATH lookup when the
// user gave a bare name) and returns a path suitable for exec.
func resolveManager(manager string) (string, error) {
	if strings.Contains(manager, "/") {
		if _, err := os.Stat(manager); err != nil {
			return "", fmt.Errorf("package manager %q not found: %w", manager, err)
		}
		return manager, nil
	}
	path, err := exec.LookPath(manager)
	if err != nil {
		return "", fmt.Errorf("package manager %q not found in PATH", manager)
	}
	return path, nil
}
