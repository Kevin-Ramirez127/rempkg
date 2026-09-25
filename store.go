package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one tracked package.
type Entry struct {
	Name        string   `json:"name"`
	Manager     string   `json:"manager"`
	Tags        []string `json:"tags,omitempty"`
	InstalledAt string   `json:"installed_at"` // RFC3339
	Command     string   `json:"command,omitempty"`
}

// Store persists entries in a JSONC file (JSON with comments).
type Store struct {
	Path string
}

const storeHeader = `// rempkg tracked packages.
// This file is JSONC: JSON with comments. Lines starting with // and
// /* ... */ blocks are ignored when reading. Managed by rempkg - edit carefully.
`

// Load reads the JSONC file. A missing file yields an empty set.
func (s Store) Load() ([]Entry, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	stripped := stripJSONComments(string(data))
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal([]byte(stripped), &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path, err)
	}
	return entries, nil
}

// Save writes entries as pretty JSON with a header comment.
func (s Store) Save(entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].Manager < entries[j].Manager
		}
		return entries[i].Name < entries[j].Name
	})
	payload, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	content := storeHeader + string(payload) + "\n"
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return err
	}
	chownToInvoker(s.Path)
	return nil
}

// chownToInvoker hands ownership of path (and its parent dir) to the
// user who invoked rempkg via sudo, so ~/rempkg/packages.jsonc written
// as root stays usable without sudo afterwards. No-op when not root
// or when $SUDO_USER is unset.
func chownToInvoker(path string) {
	if os.Geteuid() != 0 {
		return
	}
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" || sudoUser == "root" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return
	}
	_ = os.Chown(filepath.Dir(path), uid, gid)
	_ = os.Chown(path, uid, gid)
}

// Upsert adds packages (or merges tags when name+manager already tracked).
// Returns the number of newly added entries.
func (s Store) Upsert(pkgs []string, manager, rawCmd string, tags []string, now time.Time) (int, error) {
	entries, err := s.Load()
	if err != nil {
		return 0, err
	}
	index := make(map[string]int, len(entries)) // key: manager\x00name
	for i, e := range entries {
		index[storeKey(e.Manager, e.Name)] = i
	}
	added := 0
	for _, pkg := range pkgs {
		key := storeKey(manager, pkg)
		if idx, ok := index[key]; ok {
			entries[idx].Tags = mergeTags(entries[idx].Tags, tags)
			if rawCmd != "" {
				entries[idx].Command = rawCmd
			}
			continue
		}
		entries = append(entries, Entry{
			Name:        pkg,
			Manager:     manager,
			Tags:        normalizeTags(tags),
			InstalledAt: now.UTC().Format(time.RFC3339),
			Command:     rawCmd,
		})
		index[key] = len(entries) - 1
		added++
	}
	if err := s.Save(entries); err != nil {
		return 0, err
	}
	return added, nil
}

// Remove deletes entries matching any of pkgs (by name, any manager).
// Returns the removed entries.
func (s Store) Remove(pkgs []string) ([]Entry, error) {
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	drop := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		drop[p] = true
	}
	var kept, removed []Entry
	for _, e := range entries {
		if drop[e.Name] {
			removed = append(removed, e)
			continue
		}
		kept = append(kept, e)
	}
	if kept == nil {
		kept = []Entry{}
	}
	if len(removed) > 0 {
		if err := s.Save(kept); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

// Forget is an alias for Remove used by the --forget flag.
func (s Store) Forget(pkgs []string) ([]Entry, error) {
	return s.Remove(pkgs)
}

func storeKey(manager, name string) string {
	return manager + "\x00" + name
}

func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func mergeTags(base, extra []string) []string {
	return normalizeTags(append(append([]string{}, base...), extra...))
}

// stripJSONComments removes // line comments and /* */ block comments
// that appear outside of double-quoted strings.
func stripJSONComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inString := false
	escaped := false
	lineComment := false
	blockComment := false

	for i := 0; i < len(src); i++ {
		c := src[i]

		if lineComment {
			if c == '\n' {
				lineComment = false
				b.WriteByte(c)
			}
			continue
		}
		if blockComment {
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				blockComment = false
				i++
			}
			continue
		}
		if inString {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}

		if c == '"' {
			inString = true
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(src) {
			next := src[i+1]
			if next == '/' {
				lineComment = true
				i++
				continue
			}
			if next == '*' {
				blockComment = true
				i++
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}
