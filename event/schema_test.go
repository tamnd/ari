package event

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "regenerate the schema golden")

// payloadFor maps every event type to its payload struct. A new event type
// without an entry here fails the golden test, which is the point: the
// schema is a reviewed artifact, not an accident.
var payloadFor = map[Type]any{
	TypeHello:               Hello{},
	TypeSessionCreated:      SessionCreated{},
	TypeSessionUpdated:      SessionUpdated{},
	TypeSessionForked:       SessionForked{},
	TypeTurnStarted:         TurnStarted{},
	TypeTurnFinished:        TurnFinished{},
	TypeTextDelta:           TextDelta{},
	TypeTextEnd:             TextEnd{},
	TypeThinkingDelta:       ThinkingDelta{},
	TypeThinkingEnd:         ThinkingEnd{},
	TypeToolStart:           ToolStart{},
	TypeToolProgress:        ToolProgress{},
	TypeToolEnd:             ToolEnd{},
	TypePermissionRequested: PermissionRequested{},
	TypePermissionResolved:  PermissionResolved{},
	TypeLedgerTurn:          LedgerTurn{},
	TypeLog:                 Log{},
	TypeError:               ErrorInfo{},
	TypeAntSpawned:          AntSpawned{},
	TypeRouteDecided:        RouteDecided{},
	TypeMemoryFolded:        MemoryFolded{},
}

// TestSchemaGolden renders every payload's JSON shape and compares it to
// the checked-in golden file. Run with -update to regenerate after a
// deliberate schema change; the diff then shows up in review.
func TestSchemaGolden(t *testing.T) {
	var b strings.Builder
	fmt.Fprintf(&b, "schema major %d\n\n", SchemaMajor)

	fmt.Fprintf(&b, "envelope Event: %s\n\n", shape(reflect.TypeFor[Event]()))

	types := make([]string, 0, len(payloadFor))
	for k := range payloadFor {
		types = append(types, string(k))
	}
	sort.Strings(types)
	for _, name := range types {
		fmt.Fprintf(&b, "%s: %s\n", name, shape(reflect.TypeOf(payloadFor[Type(name)])))
	}
	got := b.String()

	golden := filepath.Join("testdata", "schema.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("event schema changed; if deliberate, regenerate with -update and bump SchemaMajor when a field was renamed, retyped, or removed\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// shape renders a struct type as "key:kind, key:kind" from its JSON tags.
func shape(t reflect.Type) string {
	var parts []string
	for f := range t.Fields() {
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		opt := ""
		if strings.Contains(tag, ",omitempty") {
			opt = "?"
		}
		parts = append(parts, fmt.Sprintf("%s%s:%s", name, opt, kind(f.Type)))
	}
	return strings.Join(parts, ", ")
}

func kind(t reflect.Type) string {
	switch t {
	case reflect.TypeFor[time.Time]():
		return "time"
	case reflect.TypeFor[json.RawMessage]():
		return "raw"
	}
	switch t.Kind() {
	case reflect.Slice:
		return "[]" + kind(t.Elem())
	case reflect.Struct:
		return "{" + shape(t) + "}"
	case reflect.Int, reflect.Int64, reflect.Uint64:
		return "int"
	case reflect.Float64:
		return "float"
	case reflect.Bool:
		return "bool"
	default:
		return t.Kind().String()
	}
}

// TestStdlibOnly enforces the package doc's promise: event imports nothing
// outside the standard library.
func TestStdlibOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, ".") {
				t.Errorf("%s imports %s; event must stay stdlib-only", e.Name(), path)
			}
		}
	}
}

// TestRoundTrip checks the envelope survives marshal/unmarshal with a payload.
func TestRoundTrip(t *testing.T) {
	ev, err := New(TypeTextDelta, "s1", "t1", TextDelta{Part: 2, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != TypeTextDelta || back.Session != "s1" || back.Turn != "t1" || back.V != SchemaMajor {
		t.Errorf("envelope mismatch: %+v", back)
	}
	var p TextDelta
	if err := back.Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Part != 2 || p.Text != "hi" {
		t.Errorf("payload mismatch: %+v", p)
	}
}
