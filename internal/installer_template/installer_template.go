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
# ═══════════════════════════════════════════════════════════
# Freshy installer for: {{.Pkg}}
# ═══════════════════════════════════════════════════════════
#
# Repo:    {{.Repo}}
# Branch:  {{.Branch}}
#
# This script runs AFTER git pull. Its job is to BUILD the
# project so that the files below appear in the current folder:
{{- range .Binaries}}
#   ☐ {{.}}
{{- end}}
#
# Current folder (= cwd) at runtime: the cloned repo root.
# The files listed above will be atomically copied into your
# PATH (~/.local/bin) by freshy when this script exits 0.
#
# ── How to use this template ─────────────────────────────
# 1. Pick your project's build tool below.
# 2. UNCOMMENT the lines that apply.
# 3. Replace placeholder names (like "myapp") with real ones.
# 4. SAVE and close. Then run:  freshy sync {{.Pkg}}
#
# File location: {{.InstallerPath}}
# ═══════════════════════════════════════════════════════════

set -euo pipefail

echo "▶ [{{.Pkg}} installer] building…"

# ═══════════════════════════════════════════════════════════
# ▼▼▼  EDIT BELOW  ▼▼▼
# ═══════════════════════════════════════════════════════════
#
# Uncomment ONE block below that matches your project.
# Delete or comment out the others.
#
# ──────────────────────────────────────────────────────────
# Option A: Rust / Cargo
# ──────────────────────────────────────────────────────────
# cargo build --release
# cp target/release/{{index .Binaries 0}} {{index .Binaries 0}}

# ──────────────────────────────────────────────────────────
# Option B: Go
# ──────────────────────────────────────────────────────────
# go build -o {{index .Binaries 0}} .

# ──────────────────────────────────────────────────────────
# Option C: Make / Autotools
# ──────────────────────────────────────────────────────────
# make
# cp build/{{index .Binaries 0}} {{index .Binaries 0}}

# ──────────────────────────────────────────────────────────
# Option D: Node.js / npm
# ──────────────────────────────────────────────────────────
# npm ci
# npm run build
# cp node_modules/.bin/{{index .Binaries 0}} {{index .Binaries 0}}

# ──────────────────────────────────────────────────────────
# Option E: Python / pip
# ──────────────────────────────────────────────────────────
# pip install .
# # or: python setup.py build
# cp $(which {{index .Binaries 0}}) {{index .Binaries 0}}

# ──────────────────────────────────────────────────────────
# Option F: Anything else — write your own commands below
# ──────────────────────────────────────────────────────────

# ═══════════════════════════════════════════════════════════
# ▲▲▲  EDIT ABOVE  ▲▲▲
# ═══════════════════════════════════════════════════════════

# ── Post-build check ─────────────────────────────────────
# freshy checks that the expected files exist and are executable.
# If a file is missing, the installer FAILS and freshy keeps
# your previous version (no broken update).
FAILED=0
for f in{{range .Binaries}} {{.}}{{end}}; do
    if [[ ! -x "$f" ]]; then
        echo "✗ [{{.Pkg}} installer] MISSING: $f (not found or not executable)" >&2
        FAILED=1
    fi
done

if [ "$FAILED" -ne 0 ]; then
    echo "✗ [{{.Pkg}} installer] build failed — previous binary kept" >&2
    exit 2
fi

echo "✓ [{{.Pkg}} installer] build OK — freshy will deploy now"
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
