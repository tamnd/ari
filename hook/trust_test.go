package hook

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrustMissingFileIsUntrusted(t *testing.T) {
	s := LoadTrust(filepath.Join(t.TempDir(), "trust.json"))
	if s.IsTrusted("/repo") {
		t.Fatal("a fresh install trusts nothing")
	}
}

func TestTrustRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	at := time.Unix(1_700_000_000, 0)
	s := LoadTrust(path)
	if err := s.Trust("/repo", at); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !s.IsTrusted("/repo") {
		t.Fatal("should be trusted after Trust")
	}
	// A second store reading the same file sees the persisted decision.
	if !LoadTrust(path).IsTrusted("/repo") {
		t.Fatal("trust did not persist across loads")
	}
	rec, ok := LoadTrust(path).Lookup("/repo")
	if !ok || rec.DecidedBy != "user" || !rec.Trusted {
		t.Fatalf("record wrong: %+v", rec)
	}
}

func TestRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	s := LoadTrust(path)
	_ = s.Trust("/repo", time.Unix(1, 0))
	if err := s.Revoke("/repo", time.Unix(2, 0)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if LoadTrust(path).IsTrusted("/repo") {
		t.Fatal("should be untrusted after revoke")
	}
}

func TestTrustFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := LoadTrust(path).Trust("/repo", time.Unix(1, 0)); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("trust file is group/world readable: %v", info.Mode())
	}
}

func TestCorruptTrustFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A trust record that cannot be parsed must read as untrusted, never as a
	// blanket allow.
	if LoadTrust(path).IsTrusted("/repo") {
		t.Fatal("a corrupt trust file must fail closed to untrusted")
	}
}
