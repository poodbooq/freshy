#!/usr/bin/env bash
# Freshy uninstaller — removes the binary, systemd units, and (by
# default) the data directory holding clones, builds, and state.
#
# Usage:
#   ./scripts/uninstall.sh            # keep config and state
#   ./scripts/uninstall.sh --purge    # wipe everything incl. config

set -euo pipefail
: "${HOME:?HOME must be set}"

PURGE=0
if [[ "${1:-}" == "--purge" ]]; then
  PURGE=1
fi

if command -v systemctl >/dev/null 2>&1; then
  echo "==> disabling freshy.timer"
  systemctl --user disable --now freshy.timer || true
fi

for f in \
  "$HOME/bin/freshy" \
  "$HOME/.config/systemd/user/freshy.service" \
  "$HOME/.config/systemd/user/freshy.timer"; do
  if [[ -e "$f" ]]; then
    rm -f "$f"
    echo "==> removed $f"
  fi
done

if [[ "$PURGE" == "1" ]]; then
  rm -rf "$HOME/.config/freshy" "$HOME/.local/share/freshy"
  echo "==> purged config + data"
else
  echo "==> kept config + state in $HOME/.config/freshy and $HOME/.local/share/freshy"
  echo "    (re-run with --purge to remove)"
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user daemon-reload || true
fi

echo "Done."
