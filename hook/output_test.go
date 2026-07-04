package hook

import "testing"

func TestParseOutput(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		ok     bool
	}{
		{"plain text", "formatted 3 files\n", false},
		{"empty", "", false},
		{"json array", `["a","b"]`, false},
		{"bare string", `"hello"`, false},
		{"empty object", `{}`, false},
		{"unknown field", `{"bogus": 1}`, false},
		{"additional context", `{"additionalContext": "note"}`, true},
		{"continue false", `{"continue": false, "stopReason": "stop"}`, true},
		{"permission", `{"permission": {"behavior": "deny", "message": "no"}}`, true},
	}
	for _, tc := range cases {
		out, ok := parseOutput([]byte(tc.stdout))
		if ok != tc.ok {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.ok)
		}
		if !ok && out != nil {
			t.Errorf("%s: out should be nil when not ok", tc.name)
		}
	}
}

func TestParseOutputContent(t *testing.T) {
	out, ok := parseOutput([]byte(`{"additionalContext": "hi", "continue": false, "stopReason": "done"}`))
	if !ok {
		t.Fatal("should parse")
	}
	if out.AdditionalContext != "hi" {
		t.Errorf("context = %q", out.AdditionalContext)
	}
	if out.Continue == nil || *out.Continue {
		t.Errorf("continue should be false")
	}
	if out.StopReason != "done" {
		t.Errorf("stopReason = %q", out.StopReason)
	}
}
