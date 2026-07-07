# freshy

Keep your GitHub-sourced binaries fresh.

`freshy` is a small Go daemon that watches a list of GitHub repositories,
runs each one's install script on change, and atomically drops the
resulting binaries into your `PATH`.

## Features

- **Public repos over HTTPS or SSH** — works with the URLs you already
  have in your `git` config.
- **User-space install** — nothing under `/etc` or `/usr`; everything
  lives under `~/bin`, `~/.config`, and `~/.local`.
- **Parallel sync** — pull, build, and deploy multiple packages
  concurrently (configurable).
- **Atomic deploys** — old binaries stay in place until the new build
  succeeds; staging uses temp files + rename, so a crash never leaves
  a half-written binary in `PATH`.
- **systemd timer** — runs on boot, then every `settings.schedule`
  interval. `Persistent=true` means missed runs catch up after sleep.
- **Self-documenting status** — `freshy status` shows each package's
  last SHA, last sync time, and last error.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/poodbooq/freshy/main/scripts/install.sh | bash
```

Or, from a local checkout:

```bash
just install
```

Both flows:

1. build `$HOME/bin/freshy`,
2. create `~/.config/freshy/`, `~/.local/share/freshy/{state,repos,builds,logs}`, and the systemd user dir,
3. copy `freshy.service`/`freshy.timer` to `~/.config/systemd/user/`,
4. `systemctl --user enable --now freshy.timer`.

## Configure

Edit `~/.config/freshy/config.toml`. See `example/config.toml` for a
full sample. The minimum edit is to add a `[[packages]]` entry per
tool you want to track:

```toml
[[packages]]
name           = "rg"
repo           = "https://github.com/BurntSushi/ripgrep.git"
branch         = "master"
install_script = "scripts/install.sh"
binaries       = ["rg"]
```

Use `freshy add <repo-url>` for a guided interactive add.

## CLI

| command                  | what it does                                       |
|--------------------------|----------------------------------------------------|
| `freshy init`            | bootstrap dirs, config skeleton, systemd units    |
| `freshy sync [pkg ...]`  | sync one or all packages (default: all)           |
| `freshy status`          | show each package's last SHA, sync, error         |
| `freshy add <url>`       | interactively add a package to the config         |
| `freshy remove <pkg>`    | uninstall a package + drop it from config         |
| `freshy logs [-f] [-n N]`| tail the log file                                 |
| `freshy doctor`          | check config, paths, required tools              |
| `freshy version`         | print version                                     |

## How sync works (per package)

1. If no clone exists → `git clone --depth=1 -b <branch> <repo>`.
2. Otherwise → `git fetch` + `git merge --ff-only`. A non-fast-forward
   pull is logged and skipped (your local divergence is preserved).
3. Compare the new `HEAD` SHA to `state.last_sha`. If they match → done.
4. Run `install_script` with the repo as cwd. Capture stdout/stderr to
   the log file.
5. If the script exits non-zero → log the error and **do not** touch
   `PATH`. The previous binary remains in place.
6. Otherwise → stage each declared binary under
   `~/.local/share/freshy/builds/<pkg>/`, verify (non-empty, executable
   bit), then atomically rename into `settings.install_to/<binary>`.
7. Optionally prune older staged files (default: on).
8. Persist `state.last_sha` and `state.last_synced_at`.

## Layout

```
~/bin/freshy                                 # the binary
~/.config/freshy/config.toml                 # the config
~/.local/share/freshy/
  state/<pkg>.json                           # per-package state
  repos/<pkg>/                               # git clones
  builds/<pkg>/                              # staged binaries (pruned)
  logs/freshy.log                            # text log
~/.local/bin/<binary>                        # PATH target
~/.config/systemd/user/freshy.{service,timer}
```

## Uninstall

```bash
./scripts/uninstall.sh           # keep config + state
./scripts/uninstall.sh --purge   # wipe everything
```

Manual teardown, if you ever need it:

```bash
systemctl --user disable --now freshy.timer
rm ~/bin/freshy \
   ~/.config/systemd/user/freshy.service \
   ~/.config/systemd/user/freshy.timer
systemctl --user daemon-reload
```

## Development

```bash
just build       # ./bin/freshy
just check       # go vet + build
just smoke       # build + run version/doctor
just run CMD="--help"
just install
just uninstall
just fmt
just tidy
```

Layout (Go package structure):

- `cmd/freshy/main.go`     — CLI entrypoint.
- `internal/paths`          — XDG paths, `~` expansion.
- `internal/logger`         — text file + stderr logger.
- `internal/config`         — TOML load/save/validate.
- `internal/state`          — per-package JSON state.
- `internal/sync`           — clone, pull, install, deploy (parallel).
- `lib/systemd/`            — service + (rendered) timer.
- `scripts/install.sh`      — curl-pipe-bash bootstrap.
- `scripts/uninstall.sh`    — reverse.
- `example/config.toml`     — annotated sample.
- `justfile`                — local task runner.

## Limitations

- Public repos only. Private-repo auth (SSH keys, deploy tokens) is
  intentionally out of scope for v1; the SSH support today relies on
  your user having the right key in their agent.
- No notifications on failure yet. Watch `freshy status` or
  `journalctl --user -u freshy.service`.
- Pre/post-deploy hooks are not implemented.
