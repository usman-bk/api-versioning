package migrate_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/usmanvirk/api-versioning/internal/apiver"
	"github.com/usmanvirk/api-versioning/internal/migrate"
)

type thing struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Tags  []string `json:"tags"`
	Price struct {
		Cents    int64  `json:"cents"`
		Currency string `json:"currency"`
	} `json:"price"`
}

func sample() thing {
	var t thing
	t.ID = "a"
	t.Name = "thing"
	t.Tags = []string{"x", "y"}
	t.Price.Cents = 100
	t.Price.Currency = "EUR"
	return t
}

func chain(t *testing.T, steps ...migrate.Step) *migrate.Chain {
	t.Helper()
	c, err := migrate.NewChain(steps...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNoStepsMeansTheCurrentShape(t *testing.T) {
	c := chain(t)
	doc, err := c.Apply(sample(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if doc["id"] != "a" || doc["name"] != "thing" {
		t.Fatalf("the document changed with no steps applied: %v", doc)
	}
}

func TestStepsRunInOrderDownwards(t *testing.T) {
	var order []apiver.Version
	c := chain(t,
		migrate.Step{From: 4, Why: "", Down: func(d migrate.Doc) error {
			order = append(order, 4)
			d["four"] = true
			return nil
		}},
		migrate.Step{From: 3, Why: "", Down: func(d migrate.Doc) error {
			order = append(order, 3)
			if _, ok := d["four"]; !ok {
				return errors.New("the v4 step did not run first")
			}
			d["three"] = true
			return nil
		}},
		migrate.Step{From: 2, Why: "", Down: func(d migrate.Doc) error {
			order = append(order, 2)
			return nil
		}},
	)

	doc, err := c.Apply(sample(), 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[0] != 4 || order[1] != 3 || order[2] != 2 {
		t.Fatalf("steps ran in order %v, want 4 3 2", order)
	}
	if doc["four"] != true || doc["three"] != true {
		t.Fatalf("not every step took effect: %v", doc)
	}
}

func TestStoppingPartWayDown(t *testing.T) {
	c := chain(t,
		migrate.Step{From: 3, Down: func(d migrate.Doc) error { d["three_ran"] = true; return nil }},
		migrate.Step{From: 2, Down: func(d migrate.Doc) error { d["two_ran"] = true; return nil }},
	)
	doc, err := c.Apply(sample(), 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if doc["three_ran"] != true {
		t.Fatal("the v3 step did not run")
	}
	if _, ran := doc["two_ran"]; ran {
		t.Fatal("the v2 step ran when the target was v2")
	}
}

// TestAGapIsRefusedRatherThanSkipped. Skipping a step sends the client a
// shape from a version it does not understand, and does it silently.
func TestAGapIsRefusedRatherThanSkipped(t *testing.T) {
	c := chain(t,
		migrate.Step{From: 4, Down: func(migrate.Doc) error { return nil }},
		// no step from 3
		migrate.Step{From: 2, Down: func(migrate.Doc) error { return nil }},
	)
	if _, err := c.Apply(sample(), 4, 1); err == nil {
		t.Fatal("a chain with a hole in it produced a response")
	}

	gaps := c.Missing(4, 1)
	if len(gaps) != 1 || gaps[0] != 3 {
		t.Fatalf("Missing reported %v, want just v3", gaps)
	}
	if len(c.Missing(4, 3)) != 0 {
		t.Fatal("Missing reported a gap in a range that has none")
	}
}

func TestAFailingStepFailsTheRequest(t *testing.T) {
	boom := errors.New("bad data")
	c := chain(t, migrate.Step{From: 2, Down: func(migrate.Doc) error { return boom }})
	_, err := c.Apply(sample(), 2, 1)
	if err == nil {
		t.Fatal("a step that failed produced a response anyway")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("the cause was lost: %v", err)
	}
}

func TestCannotMigrateUpwards(t *testing.T) {
	c := chain(t)
	if _, err := c.Apply(sample(), 1, 3); err == nil {
		t.Fatal("converting v1 up to v3 was allowed, and there is nothing to convert from")
	}
}

func TestNewChainRefusesNonsense(t *testing.T) {
	for name, steps := range map[string][]migrate.Step{
		"no function":     {{From: 2}},
		"below v1":        {{From: 1, Down: func(migrate.Doc) error { return nil }}},
		"duplicate steps": {{From: 2, Down: func(migrate.Doc) error { return nil }}, {From: 2, Down: func(migrate.Doc) error { return nil }}},
	} {
		if _, err := migrate.NewChain(steps...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestRename(t *testing.T) {
	d := migrate.Doc{"new_name": "x", "other": 1}
	migrate.Rename(d, "new_name", "old_name")
	if d["old_name"] != "x" {
		t.Fatalf("the value did not move: %v", d)
	}
	if _, left := d["new_name"]; left {
		t.Fatal("the old key was left behind, so the client sees both")
	}
	// Renaming something absent is a no-op, not a panic: a migration runs
	// against documents that may legitimately omit an optional field.
	migrate.Rename(d, "missing", "also_missing")
	if _, created := d["also_missing"]; created {
		t.Fatal("renaming an absent field invented one")
	}
}

func TestDrop(t *testing.T) {
	d := migrate.Doc{"a": 1, "b": 2, "c": 3}
	migrate.Drop(d, "a", "c", "never_existed")
	if len(d) != 1 || d["b"] != 2 {
		t.Fatalf("after dropping, the document is %v", d)
	}
}

func TestEach(t *testing.T) {
	d := migrate.Doc{"items": []any{
		map[string]any{"n": 1.0},
		map[string]any{"n": 2.0},
	}}
	if err := migrate.Each(d, "items", func(item migrate.Doc) error {
		item["seen"] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, raw := range d["items"].([]any) {
		if raw.(map[string]any)["seen"] != true {
			t.Fatalf("item %d was not visited", i)
		}
	}

	// An absent list is not an error.
	if err := migrate.Each(d, "absent", func(migrate.Doc) error { return nil }); err != nil {
		t.Fatalf("iterating an absent field failed: %v", err)
	}
	// A field that is not a list is, because it means the shape is not what
	// the migration thinks it is.
	bad := migrate.Doc{"items": "not a list"}
	if err := migrate.Each(bad, "items", func(migrate.Doc) error { return nil }); err == nil {
		t.Fatal("iterating a string as a list was allowed")
	}
}

// TestApplyDoesNotMutateTheInput. The handler builds one current shaped value
// and may serve it to several versions; a migration writing through to it
// would corrupt the next response.
func TestApplyDoesNotMutateTheInput(t *testing.T) {
	in := sample()
	c := chain(t, migrate.Step{From: 2, Down: func(d migrate.Doc) error {
		migrate.Drop(d, "name")
		d["price"] = 0
		return nil
	}})
	if _, err := c.Apply(in, 2, 1); err != nil {
		t.Fatal(err)
	}
	if in.Name != "thing" || in.Price.Cents != 100 {
		t.Fatalf("the migration wrote through to the caller's value: %+v", in)
	}
}

func BenchmarkThreeStepChain(b *testing.B) {
	c, err := migrate.NewChain(
		migrate.Step{From: 4, Down: func(d migrate.Doc) error { migrate.Drop(d, "tags"); return nil }},
		migrate.Step{From: 3, Down: func(d migrate.Doc) error { migrate.Rename(d, "name", "title"); return nil }},
		migrate.Step{From: 2, Down: func(d migrate.Doc) error {
			if p, ok := d["price"].(map[string]any); ok {
				d["price"] = p["cents"]
			}
			return nil
		}},
	)
	if err != nil {
		b.Fatal(err)
	}
	in := sample()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := c.Apply(in, 4, 1)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := json.Marshal(doc); err != nil {
			b.Fatal(err)
		}
	}
}
