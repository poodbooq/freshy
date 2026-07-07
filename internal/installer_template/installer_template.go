// Package installer_template renders a starter shell script for a
// freshy package's local installer. The script is meant to be opened
// in $EDITOR and tweaked by the user — it is intentionally not a
// fully working build: the binary-producing steps are placeholders.
//
// The cwd at runtime is the package's cloned repo root. The script
// must exit 0 if (and only if) all `binaries` declared in the package
// exist at the expected locations and have +x bit set. A non-zero
// exit leaves freshy's PATH untouched (see internal/sync).
package installer_template

import (
	"bytes"
	"text/template"
)

// Data is passed to the template render.
type Data struct {
	// Pkg is the package name; also the suggested installer filename.
	Pkg string
	// Repo is the upstream URL (for the user's reference only).
	Repo string
	// Branch is the tracked branch.
	Branch string
	// Binaries is the list of binary names the installer must
	// produce under cwd (the repo root).
	Binaries []string
	// InstallerPath is the absolute path the user-facing installer
	// will live at after they save it.
	InstallerPath string
}

const tmplSource = `#!/usr/bin/env bash
# Freshy installer for package: {{.Pkg}}
#
# Repo:    {{.Repo}} (branch {{.Branch}})
# Binaries (must exist at cwd with +x bit when this script exits 0):
{{- range .Binaries}}
#   - {{.}}
{{- end}}
#
# Runtime contract:
#   * cwd is the cloned repo root.
#   * exit 0  -> freshy atomically swaps the binaries listed above
#                  into your PATH (settings.install_to).
#   * exit != -> freshy leaves the previous binary untouched and
#                  records the error in freshy logs.
#
# This file was generated from a template; edit freely. To re-edit:
#   freshy add {{.Repo}}   # (will detect existing installer and ask)
#
# Suggested location: {{.InstallerPath}}

set -euo pipefail

# --- begin {{.Pkg}} build steps ----------------------------------
#
# Replace the body of this block with whatever produces {{range .Binaries}}{{.}}, {{end}}
# in cwd. Common shapes:
#
#   * make-based C/Rust/Go projects:
#       make            # writes ./your-bin
#
#   * cargo (Rust):
#       cargo build --release
#       cp target/release/{{index .Binaries 0}} {{index .Binaries 0}}
#
#   * npm/node:
#       npm ci
#       npm run build
#       # whatever produces the binary ends up in ./node_modules/.bin
#         or in your build/ output — symlink or copy to cwd
#
# Keep the binary name(s) identical to the ones declared above so
# freshy's deploy step knows what to ship.

echo "[{{.Pkg}} installer] building…" >&2
# TODO: replace this with your build commands
for b in {{range .Binaries}} {{.}}{{end}}; do
    if [[ ! -x "$b" ]]; then
        echo "[{{.Pkg}} installer] missing or non-executable: $b" >&2
        exit 2
    fi
done

# --- end {{.Pkg}} build steps ------------------------------------

echo "[{{.Pkg}} installer] build OK" >&2
`

// tmpl is parsed lazily; we keep it in init() so a malformed template
// crashes immediately at start rather than on first use.
var tmpl = template.Must(template.New("installer").Parse(tmplSource))

// Render returns the template rendered with data.
func Render(data Data) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
