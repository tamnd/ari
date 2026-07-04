package greeter

import (
	"strings"
	"testing"
)

func TestGreetLeadsWithHello(t *testing.T) {
	got := Greet("ari")
	if !strings.HasPrefix(got, "Hello, ari!") {
		t.Fatalf("Greet(ari) = %q, want it to start with the hello", got)
	}
}
