# rempkg

One wrapper for many package managers (`pacman`, `yay`, `paru`, `apt`, `dnf`, ...).
It runs the install exactly as usual, then records what was installed —
with tags — in a JSONC file.

## Example

```bash
sudo rempkg pacman -S cowsay
sudo rempkg --tag delete-later --tag tool pacman -S cowsay
sudo rempkg yay -S google-chrome
sudo rempkg pacman -Rns cowsay   # also untracks
```

## Why should I use rempkg?
Sometimes you want to know all packages did you install on your computer, but probably you use multiple package managers, and if you list them it also shows dependencies.
rempkg will show just the packages you installed manually, and you can also tag them for your organization.

## Install

```bash
./install.sh
# or: PREFIX=/usr ~/.local ./install.sh  (via BIN_DIR=~/.local/bin)
```

*Requires Go >= 1.21.*

## Usage

```
sudo rempkg [--tag <tag>...] [--file <path>] <manager> [manager-args...]
rempkg --list [--filter-tag <tag>]
rempkg --forget <package> [...]
```

| Flag | Meaning |
| ---- | ------- |
| `--tag, -t` | attach a tag (repeatable) |
| `--file, -f` | tracking file, overrides `$REMPKG_FILE` |
| `--dry-run` | print command without running it |
| `--list, -l` | list tracked packages |
| `--forget` | untrack without uninstalling |
| `--help, --version` | ... |

Env: `REMPKG_FILE` sets the JSONC path.
Default: `~/rempkg/packages.jsonc` (`/home/<user>/rempkg/packages.jsonc`,
even when run with `sudo`).

## Tracking file

JSONC (JSON with `//` and `/* */` comments). Shape:

```jsonc
// rempkg tracked packages.
[
  {
    "name": "opencode",
    "manager": "pacman",
    "tags": ["tool", "delete-later"],
    "installed_at": "2026-09-25T00:00:00Z",
    "command": "pacman -S opencode"
  }
]
```

- Install verbs (`-S`, `install`, `add`, ...) upsert entries (tags merged).
- Remove verbs (`-R`, `remove`, `uninstall`, ...) untrack by name.
- Query/update/search (`-Ss`, `-Si`, `-Syu` with no packages, `apt update`, ...) only run, never tracked.
