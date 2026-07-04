package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/doctor"
)

// TestCodedErrorExitCodes pins doctor's own 0/1/2/3 contract onto the
// process exit path: a codeError names its code, and a silent one is not
// re-printed by Execute (doc 14 section 12.4).
func TestCodedErrorExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   int
		silent bool
	}{
		{"clean", coded(0, nil), 0, false},
		{"warn", coded(1, nil), 1, true},
		{"critical", coded(2, nil), 2, true},
		{"run failure", coded(3, os.ErrPermission), 3, false},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.code {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.code)
		}
		if got := silent(c.err); got != c.silent {
			t.Errorf("%s: silent %v, want %v", c.name, got, c.silent)
		}
	}
}

// TestDoctorExitMapping pins the status-to-code table doctor prints against.
func TestDoctorExitMapping(t *testing.T) {
	cases := []struct {
		status doctor.Status
		code   int
	}{
		{doctor.StatusOK, 0},
		{doctor.StatusWarn, 1},
		{doctor.StatusCritical, 2},
	}
	for _, c := range cases {
		if got := doctorExit(c.status); got != c.code {
			t.Errorf("status %v: exit %d, want %d", c.status, got, c.code)
		}
	}
}

// TestRunDoctorReportsAndFixes drives the command end to end over a nest
// that has a world-readable credential and a repo missing the local-config
// ignore: the first run is critical, --fix repairs both, and the second
// run is clean.
func TestRunDoctorReportsAndFixes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARI_HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	repo := t.TempDir()
	mustMkdir(t, filepath.Join(repo, ".git"))
	mustMkdir(t, filepath.Join(repo, ".ari"))
	chdir(t, repo)

	// A world-readable credential file under the auth dir is the critical.
	auth := filepath.Join(home, "auth")
	mustMkdir(t, auth)
	if err := os.WriteFile(filepath.Join(auth, "anthropic.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runDoctor(doctorCmd, &out, false, false)
	if code := exitCode(err); code != 2 {
		t.Fatalf("first run exit %d, want 2\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "critical") {
		t.Errorf("report did not mention a critical finding:\n%s", out.String())
	}

	out.Reset()
	err = runDoctor(doctorCmd, &out, true, false)
	if code := exitCode(err); code != 0 {
		t.Fatalf("after --fix exit %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "result: clean") {
		t.Errorf("after --fix report not clean:\n%s", out.String())
	}
	// The ignore line landed and the credential tightened.
	data, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if !strings.Contains(string(data), ".ari/local.toml") {
		t.Errorf("--fix did not add the ignore line, got %q", data)
	}
	info, _ := os.Stat(filepath.Join(auth, "anthropic.json"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("--fix left credential mode %o, want 600", info.Mode().Perm())
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Resolve symlinks so the nest root matches what doctor computes; on
	// macOS a temp dir under /var is really /private/var.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(real); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
