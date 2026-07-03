package tea_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportGraph enforces the layering rule from doc 02 section 1: the
// core never imports the UI or any terminal rendering library, and the
// UI reaches into the core only through the shared vocabulary (event)
// and the typed tool displays (tool). Everything else the UI needs
// arrives as messages over the bus.
func TestImportGraph(t *testing.T) {
	out, err := exec.Command("go", "list", "-f",
		`{{.ImportPath}} {{join .Imports " "}}`, "github.com/tamnd/ari/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	const mod = "github.com/tamnd/ari/"
	uiAllowedCore := map[string]bool{
		mod + "event": true,
		mod + "tool":  true,
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		pkg, imports := fields[0], fields[1:]
		short := strings.TrimPrefix(pkg, mod)

		switch {
		case strings.HasPrefix(short, "cmd/"), short == "cmd":
			// main wires both sides together; no constraint.
		case strings.HasPrefix(short, "ui/"), short == "ui":
			for _, imp := range imports {
				if strings.HasPrefix(imp, mod) &&
					!strings.HasPrefix(imp, mod+"ui/") &&
					imp != mod+"ui" &&
					!uiAllowedCore[imp] {
					t.Errorf("%s imports core package %s; the UI talks to the core over the bus, not by import", pkg, imp)
				}
			}
		default: // a core package
			for _, imp := range imports {
				if strings.HasPrefix(imp, "charm.land/") ||
					strings.HasPrefix(imp, "github.com/charmbracelet/") {
					t.Errorf("core package %s imports terminal library %s", pkg, imp)
				}
				if strings.HasPrefix(imp, mod+"ui/") {
					t.Errorf("core package %s imports UI package %s", pkg, imp)
				}
			}
		}
	}
}
