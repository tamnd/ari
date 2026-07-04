package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Trust is the per-workspace trust record. Trust is always an explicit user
// decision, so DecidedBy is always "user"; there is no auto-trust, because
// the whole value of the gate is that a repo you cloned and have not vouched
// for cannot run code at you the moment you open it (doc 05 section 12).
type Trust struct {
	Root      string    `json:"root"`
	Trusted   bool      `json:"trusted"`
	DecidedAt time.Time `json:"decided_at"`
	DecidedBy string    `json:"decided_by"`
}

// TrustStore persists trust decisions keyed by workspace root path in one
// JSON file in the global nest, so a workspace is trusted once and not once
// per session. It is safe for concurrent use.
type TrustStore struct {
	path string
	mu   sync.Mutex
	recs map[string]Trust
}

// LoadTrust reads the trust file at path. A missing file is an empty store,
// not an error: a fresh install trusts nothing. A corrupt file is also an
// empty store, because a trust record you cannot parse must fail closed to
// untrusted, never open to trusted.
func LoadTrust(path string) *TrustStore {
	s := &TrustStore{path: path, recs: map[string]Trust{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var recs map[string]Trust
	if json.Unmarshal(data, &recs) == nil && recs != nil {
		s.recs = recs
	}
	return s
}

// IsTrusted reports whether root has been explicitly trusted. An unknown
// workspace is untrusted, the safe default.
func (s *TrustStore) IsTrusted(root string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.recs[root]
	return ok && t.Trusted
}

// Trust records an explicit user decision to trust a workspace and persists
// it. at is passed in so the record is deterministic under test.
func (s *TrustStore) Trust(root string, at time.Time) error {
	return s.set(root, Trust{Root: root, Trusted: true, DecidedAt: at.UTC(), DecidedBy: "user"})
}

// Revoke records that a workspace is no longer trusted, so a hook config that
// turned malicious can be shut off without deleting the record.
func (s *TrustStore) Revoke(root string, at time.Time) error {
	return s.set(root, Trust{Root: root, Trusted: false, DecidedAt: at.UTC(), DecidedBy: "user"})
}

// Lookup returns the raw record for a workspace, so doctor can report when
// and by whom trust was decided.
func (s *TrustStore) Lookup(root string) (Trust, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.recs[root]
	return t, ok
}

func (s *TrustStore) set(root string, t Trust) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[root] = t
	data, err := json.MarshalIndent(s.recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
