package permission

import "testing"

func TestAppliesToServerGlob(t *testing.T) {
	cases := []struct {
		pattern string
		tool    string
		want    bool
	}{
		{"*", "sqlite__query", true},
		{"sqlite__query", "sqlite__query", true},
		{"sqlite__query", "sqlite__write", false},
		{"sqlite__*", "sqlite__query", true}, // server-wide glob
		{"sqlite__*", "sqlite__write", true}, // server-wide glob
		{"sqlite__*", "docs__query", false},  // a different server
		{"sqlite__*", "sqlite", false},       // the bare server name is not a tool
		{"read", "read", true},               // a core tool, no glob
		{"read", "sqlite__read", false},      // exact, not a suffix match
	}
	for _, c := range cases {
		r := MustParse(c.pattern, LayerUser)
		if got := r.appliesTo(c.tool); got != c.want {
			t.Errorf("Parse(%q).appliesTo(%q) = %v, want %v", c.pattern, c.tool, got, c.want)
		}
	}
}
