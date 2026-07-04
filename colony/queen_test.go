package colony

import (
	"context"
	"strings"
	"testing"
)

// TestBuiltinsRegisterClean is the slice 3 DoD: every built-in ant carries a
// real verify story and registers without error, and the colony ends with
// all of them present.
func TestBuiltinsRegisterClean(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t, fakeEmbedder{model: "m1"})
	q := NewQueen(store)
	if err := q.RegisterBuiltins(ctx); err != nil {
		t.Fatalf("built-in ants must all register clean: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != len(Builtins()) {
		t.Fatalf("registered %d cards, want %d", len(rows), len(Builtins()))
	}
}

// TestBuiltinsValidate pins that each built-in is well-formed and names a
// verification story, the two things Register demands.
func TestBuiltinsValidate(t *testing.T) {
	for _, c := range Builtins() {
		if err := c.Validate(); err != nil {
			t.Errorf("built-in %s does not validate: %v", c.ID, err)
		}
		if c.Verify.IsEmpty() {
			t.Errorf("built-in %s ships without a verify story", c.ID)
		}
	}
}

// TestRegisterEmptyVerifyRefused is the D4 rule: a card with no verification
// story is refused with an error naming D4, and nothing is inserted.
func TestRegisterEmptyVerifyRefused(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t, fakeEmbedder{model: "m1"})
	q := NewQueen(store)

	c := WorkerCard()
	c.Verify.Check = ""
	err := q.Register(ctx, c)
	if err == nil {
		t.Fatal("Register accepted a card with no verify story")
	}
	if !strings.Contains(err.Error(), "D4") {
		t.Errorf("refusal must name D4, got %q", err.Error())
	}
	rows, lerr := store.List(ctx)
	if lerr != nil {
		t.Fatalf("list: %v", lerr)
	}
	if len(rows) != 0 {
		t.Errorf("a refused registration must insert nothing, found %d rows", len(rows))
	}
}

// TestRegisterDefaultsProvisional is doc 06 section 2.4: a newborn with no
// status enters provisional and earns active later on its first verified
// task.
func TestRegisterDefaultsProvisional(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t, fakeEmbedder{model: "m1"})
	q := NewQueen(store)

	c := WorkerCard()
	c.Status = ""
	if err := q.Register(ctx, c); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := store.Load(ctx, c.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Status != StatusProvisional {
		t.Errorf("a newborn registers as %s, want provisional", got.Status)
	}
}

// TestRegisterInvalidCardRefused pins that a structurally broken card never
// reaches the store.
func TestRegisterInvalidCardRefused(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t, fakeEmbedder{model: "m1"})
	q := NewQueen(store)

	c := WorkerCard()
	c.Tools = []string{"read", "teleport"}
	if err := q.Register(ctx, c); err == nil {
		t.Fatal("Register accepted a card naming an unknown tool")
	}
	rows, _ := store.List(ctx)
	if len(rows) != 0 {
		t.Errorf("an invalid card must insert nothing, found %d rows", len(rows))
	}
}
