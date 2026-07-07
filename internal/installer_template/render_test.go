package installer_template

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestTemplateRenderHumanReadable(t *testing.T) {
	out, err := Render(Data{
		Pkg:           "bat",
		Repo:          "https://github.com/sharkdp/bat.git",
		Branch:        "master",
		Binaries:      []string{"bat"},
		InstallerPath: "/home/user/.local/share/freshy/installers/bat.sh",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Print so we can inspect visually
	fmt.Println(out)

	// Write to file for bash syntax check
	if err := os.WriteFile("/tmp/hermes-rendered-template.sh", []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}

	// Basic assertions
	for _, want := range []string{
		"bat",
		"sharkdp/bat",
		"EDIT BELOW",
		"EDIT ABOVE",
		"Option A: Rust / Cargo",
		"cargo build --release",
		"cp target/release/bat bat",
		"freshy sync bat",
		"set -euo pipefail",
		"FAILED=0",
		"previous binary kept",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing: %q", want)
		}
	}
}
