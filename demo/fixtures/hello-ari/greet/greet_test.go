package greet

import "testing"

func TestGreeting(t *testing.T) {
	if got := Greeting("ari"); got != "Hello, ari!" {
		t.Fatalf("Greeting(ari) = %q, want %q", got, "Hello, ari!")
	}
}
