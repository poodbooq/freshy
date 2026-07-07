#!/usr/bin/env bash
# Freshy installer — bootstraps the binary, config dirs, and the
# systemd user timer for periodic sync.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/.../scripts/install.sh | bash
#
# Environment overrides:
#   FRESHY_VERSION    Git ref to build from (default: main)
#   FRESHY_REPO       Git URL of the freshy repo (default: git@github.com:poodbooq/freshy.git)
#   FRESHY_HOME       Override $HOME for the install (default: $HOME)

set -euo pipefail

FRESHY_VERSION="${FRESHY_VERSION:-main}"
FRESHY_REPO="${FRESHY_REPO:-git@github.com:poodbooq/freshy.git}"
: "${HOME:?HOME must be set}"

# Pick a non-interactive GOPATH-friendly temp dir.
work="$(mktemp -d -t freshy.XXXXXX)"
trap 'rm -rf "$work"' EXIT

echo "==> fetching freshy source ($FRESHY_VERSION)"
git clone --depth=1 -b "$FRESHY_VERSION" "$FRESHY_REPO" "$work/freshy"

echo "==> building"
( cd "$work/freshy"
  if [[ "${FRESHY_NO_BUILD:-0}" == "1" ]]; then
    echo "    FRESHY_NO_BUILD=1 -- skipping go build"
  else
    go build -ldflags "-s -w -X main.version=$FRESHY_VERSION" -o "$work/freshy-bin" ./cmd/freshy
  fi
)

mkdir -p "$HOME/bin"
if [[ -x "$work/freshy-bin" ]]; then
  install -m 0755 "$work/freshy-bin" "$HOME/bin/freshy"
  echo "==> installed $HOME/bin/freshy"
else
  # Allow FRESHY_NO_BUILD=1 + pre-built binary download flow (not in MVP).
  echo "error: no built binary at $work/freshy-bin" >&2
  exit 1
fi

mkdir -p \
  "$HOME/.config/freshy" \
  "$HOME/.local/share/freshy/state" \
  "$HOME/.local/share/freshy/repos" \
  "$HOME/.local/share/freshy/builds" \
  "$HOME/.local/share/freshy/logs" \
  "$HOME/.config/systemd/user"

# Copy systemd units into place and tell Go's renderUnit where to find source.
export FRESHY_SOURCE="$work/freshy"
"$HOME/bin/freshy" init || true
unset FRESHY_SOURCE

cat <<'EOF'

==> Done.

Next steps:

  1. Edit ~/.config/freshy/config.toml and add some [[packages]] entries.
  2. Run `freshy sync` once manually to verify.
  3. Check status with `freshy status`.

The timer (freshy.timer) is enabled and will run on boot, then every
`settings.schedule` interval.

EOF
