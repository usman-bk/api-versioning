package handler_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usmanvirk/api-versioning/internal/apiver"
	"github.com/usmanvirk/api-versioning/internal/handler"
	"github.com/usmanvirk/api-versioning/internal/migrate"
)

var update = os.Getenv("UPDATE_GOLDEN") == "1"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixed is the clock every test uses, so a sunset date in a golden file does
// not change tomorrow.
var fixed = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

type store struct{}

func (store) Get(id string) (handler.Listing, bool) {
	if id != "lst_1" {
		return handler.Listing{}, false
	}
	orig := handler.Money{Cents: 62900, Currency: "EUR"}
	return handler.Listing{
		ID:       "lst_1",
		Title:    "iPhone 13 Pro 256 GB Graphite",
		Grade:    "excellent",
		Price:    handler.Money{Cents: 48900, Currency: "EUR"},
		Original: &orig,
		Images: []handler.Image{
			{URL: "https://img.example.com/a/720.avif", Width: 720},
			{URL: "https://img.example.com/b/720.avif", Width: 720},
		},
		Stock:    handler.Stock{Available: true, Count: 2},
		Seller:   handler.Seller{ID: "sel_9", Name: "Renewed Electronics GmbH", Rating: 4.7},
		Warranty: handler.Warranty{Months: 12, Extended: false},
		Battery:  93,
	}, true
}

func newServer(t *testing.T) *http.ServeMux {
	t.Helper()
	reg, err := handler.Versions(fixed)
	if err != nil {
		t.Fatal(err)
	}
	reg.SetClock(func() time.Time { return fixed })

	chain, err := handler.Chain()
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler.New(reg, chain, store{}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes()
}

func get(t *testing.T, mux *http.ServeMux, path, version string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if version != "" {
		req.Header.Set(handler.Header, version)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestEachVersionHasAGoldenFile is the test that makes this repository worth
// anything. A migration is correct if the bytes an old client receives are
// the bytes it received before, and the only way to know that is to have
// written them down.
//
// These files are the contract. A change that alters one is either a bug or
// a decision somebody has to make deliberately, and either way the diff is
// in the pull request.
func TestEachVersionHasAGoldenFile(t *testing.T) {
	mux := newServer(t)

	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		t.Run(v, func(t *testing.T) {
			rec := get(t, mux, "/listings/lst_1", v)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}

			var got any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			pretty, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			pretty = append(pretty, '\n')

			path := filepath.Join("testdata", v+".json")
			if update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, pretty, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s", path)
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v. Run with UPDATE_GOLDEN=1 to create it", err)
			}
			if string(pretty) != string(want) {
				t.Errorf("%s changed shape.\n--- want ---\n%s\n--- got ---\n%s", v, want, pretty)
			}
		})
	}
}

// TestV1LooksLikeV1 states the oldest contract in the test itself, not only
// in a file, so somebody reading the tests can see what the oldest client
// believes without opening testdata.
func TestV1LooksLikeV1(t *testing.T) {
	mux := newServer(t)
	rec := get(t, mux, "/listings/lst_1", "v1")

	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	// Price is a bare number, with no currency anywhere.
	if _, ok := doc["price"].(float64); !ok {
		t.Fatalf("price is %T, and v1 clients read a number", doc["price"])
	}
	// Stock was two top level fields.
	if _, ok := doc["in_stock"].(bool); !ok {
		t.Fatalf("in_stock is %T, want a bool", doc["in_stock"])
	}
	if _, ok := doc["stock_count"].(float64); !ok {
		t.Fatalf("stock_count is %T, want a number", doc["stock_count"])
	}
	// Images were plain strings.
	imgs, ok := doc["images"].([]any)
	if !ok || len(imgs) == 0 {
		t.Fatalf("images is %T", doc["images"])
	}
	if _, ok := imgs[0].(string); !ok {
		t.Fatalf("images[0] is %T, and v1 clients read a string", imgs[0])
	}
	// Warranty was a bare integer.
	if _, ok := doc["warranty_months"].(float64); !ok {
		t.Fatalf("warranty_months is %T, want a number", doc["warranty_months"])
	}

	// And nothing from a later version may leak in. A strict decoder, which
	// is what a generated client uses, fails on a field it has never heard
	// of, so this is not cosmetic.
	for _, later := range []string{"stock", "warranty", "battery_health_pct"} {
		if _, present := doc[later]; present {
			t.Errorf("%q reached a v1 client, which a strict decoder rejects", later)
		}
	}
}

func TestNoVersionHeaderGetsTheOldest(t *testing.T) {
	mux := newServer(t)
	rec := get(t, mux, "/listings/lst_1", "")

	if got := rec.Header().Get(handler.Header); got != "v1" {
		t.Fatalf("a request with no version got %s, and defaulting forward breaks exactly the callers who did not know they were choosing", got)
	}
	if rec.Header().Get("X-API-Version-Defaulted") != "true" {
		t.Error("the response does not say the version was defaulted")
	}
}

func TestEveryResponseSaysWhichVersionMadeIt(t *testing.T) {
	mux := newServer(t)
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		rec := get(t, mux, "/listings/lst_1", v)
		if got := rec.Header().Get(handler.Header); got != v {
			t.Errorf("asked for %s, response says %s", v, got)
		}
	}
}

func TestVersionSpellings(t *testing.T) {
	mux := newServer(t)
	for _, spelling := range []string{"2", "v2", "V2", " v2 "} {
		rec := get(t, mux, "/listings/lst_1", spelling)
		if rec.Code != 200 {
			t.Errorf("%q was refused with %d: %s", spelling, rec.Code, rec.Body)
			continue
		}
		if got := rec.Header().Get(handler.Header); got != "v2" {
			t.Errorf("%q resolved to %s", spelling, got)
		}
	}
}

// TestLatestIsRefused. Three clients will send three spellings of the number
// and none of those is worth failing a request over. "latest" is, because it
// means the client has no idea which shape it is about to receive.
func TestVagueVersionsAreRefused(t *testing.T) {
	mux := newServer(t)
	for _, bad := range []string{"latest", "current", "2.1", "-1", "0", "v", "abc"} {
		rec := get(t, mux, "/listings/lst_1", bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q gave status %d, want 400", bad, rec.Code)
		}
	}
}

func TestUnknownVersionListsWhatIsAvailable(t *testing.T) {
	mux := newServer(t)
	rec := get(t, mux, "/listings/lst_1", "v99")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"v1", "v4", "supported_versions"} {
		if !strings.Contains(body, want) {
			t.Errorf("the error does not mention %s: %s", want, body)
		}
	}
}

// TestADeprecatedVersionSaysSoOnEveryResponse. The only lever a server has
// over an app it cannot update is telling it early and repeatedly.
func TestADeprecatedVersionSaysSoOnEveryResponse(t *testing.T) {
	mux := newServer(t)
	rec := get(t, mux, "/listings/lst_1", "v1")

	if rec.Header().Get("Deprecation") != "true" {
		t.Error("no Deprecation header on a deprecated version")
	}
	if rec.Header().Get("Sunset") == "" {
		t.Error("no Sunset header, so the client cannot tell how long it has")
	}
	warning := rec.Header().Get("Warning")
	if !strings.Contains(warning, "v1 is deprecated") || !strings.Contains(warning, "v4") {
		t.Errorf("the Warning header does not say what to do: %q", warning)
	}
	if !strings.Contains(warning, "days") {
		t.Errorf("the Warning header does not say how long is left: %q", warning)
	}
}

func TestASupportedVersionIsNotMarkedDeprecated(t *testing.T) {
	mux := newServer(t)
	for _, v := range []string{"v2", "v3", "v4"} {
		rec := get(t, mux, "/listings/lst_1", v)
		if rec.Header().Get("Deprecation") != "" {
			t.Errorf("%s is marked deprecated and is not", v)
		}
	}
}

// TestASunsetVersionStopsAnsweringOnItsOwn. The date is the deadline, and a
// version that keeps working past it because nobody edited a list is how a
// sunset becomes a suggestion.
func TestASunsetVersionStopsAnsweringOnItsOwn(t *testing.T) {
	reg, err := handler.Versions(fixed)
	if err != nil {
		t.Fatal(err)
	}
	after := fixed.AddDate(0, 4, 0) // past v1's sunset
	reg.SetClock(func() time.Time { return after })

	chain, err := handler.Chain()
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler.New(reg, chain, store{}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	rec := get(t, h.Routes(), "/listings/lst_1", "v1")
	if rec.Code != http.StatusGone {
		t.Fatalf("status %d, want 410: a retired version answered", rec.Code)
	}
	// 410 and not 400, because the client did nothing wrong when it was
	// built, and the app shows a different screen for each.
	if !strings.Contains(rec.Body.String(), "version_retired") {
		t.Errorf("the error code does not distinguish retirement: %s", rec.Body)
	}

	// And the default must have moved up, or every unversioned request now
	// resolves to a version that no longer answers.
	if reg.Default() == 1 {
		t.Error("the default is still a retired version")
	}
	if rec := get(t, h.Routes(), "/listings/lst_1", ""); rec.Code != 200 {
		t.Fatalf("an unversioned request got %d after v1 retired", rec.Code)
	}
}

func TestNotFound(t *testing.T) {
	mux := newServer(t)
	if rec := get(t, mux, "/listings/nope", "v4"); rec.Code != 404 {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestVersionsEndpoint(t *testing.T) {
	mux := newServer(t)
	rec := get(t, mux, "/versions", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		Current  string
		Default  string
		Versions []struct {
			Version, Status, Sunset, Note string
			Default                       bool
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Current != "v4" || out.Default != "v1" {
		t.Fatalf("current %s default %s", out.Current, out.Default)
	}
	if len(out.Versions) != 4 {
		t.Fatalf("%d versions listed, want 4", len(out.Versions))
	}
	for _, v := range out.Versions {
		if v.Version == "v1" {
			if v.Status != string(apiver.Deprecated) || v.Sunset == "" || v.Note == "" {
				t.Errorf("v1 is listed as %+v, which does not tell anybody when it goes", v)
			}
		}
	}
}

// TestAGapInTheChainStopsTheServiceStarting. A missing migration means a
// request for that version is answered with a newer shape, silently. Finding
// it on boot is the point.
func TestAGapInTheChainStopsTheServiceStarting(t *testing.T) {
	reg, err := handler.Versions(fixed)
	if err != nil {
		t.Fatal(err)
	}
	// A chain missing the v3 to v2 step.
	chain, err := migrate.NewChain(
		migrate.Step{From: 4, Why: "x", Down: func(migrate.Doc) error { return nil }},
		migrate.Step{From: 2, Why: "y", Down: func(migrate.Doc) error { return nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.New(reg, chain, store{}, quiet()); err == nil {
		t.Fatal("a service with a hole in its migration chain started, and would answer v2 with a v3 shape")
	}
}
