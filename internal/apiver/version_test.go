package apiver_test

import (
	"errors"
	"testing"
	"time"

	"github.com/usmanvirk/api-versioning/internal/apiver"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func registry(t *testing.T, specs ...apiver.Spec) *apiver.Registry {
	t.Helper()
	r, err := apiver.NewRegistry(specs...)
	if err != nil {
		t.Fatal(err)
	}
	r.SetClock(func() time.Time { return now })
	return r
}

func three(t *testing.T) *apiver.Registry {
	return registry(t,
		apiver.Spec{Version: 1, Status: apiver.Deprecated, SunsetOn: now.AddDate(0, 1, 0)},
		apiver.Spec{Version: 2, Status: apiver.Supported},
		apiver.Spec{Version: 3, Status: apiver.Current},
	)
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want apiver.Version
		ok   bool
	}{
		{"1", 1, true}, {"v1", 1, true}, {"V12", 12, true}, {" v3 ", 3, true},
		{"", 0, false}, {"v", 0, false}, {"latest", 0, false}, {"2.1", 0, false},
		{"0", 0, false}, {"-1", 0, false}, {"v-1", 0, false},
	} {
		got, err := apiver.Parse(tc.in)
		if tc.ok && err != nil {
			t.Errorf("%q was refused: %v", tc.in, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q was accepted as %s", tc.in, got)
		}
		if tc.ok && got != tc.want {
			t.Errorf("%q parsed as %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestRegistryRefusesAnIncoherentSet(t *testing.T) {
	for name, specs := range map[string][]apiver.Spec{
		"nothing at all": {},
		"no current": {
			{Version: 1, Status: apiver.Supported},
		},
		"two currents": {
			{Version: 1, Status: apiver.Current},
			{Version: 2, Status: apiver.Current},
		},
		"a duplicate": {
			{Version: 1, Status: apiver.Current},
			{Version: 1, Status: apiver.Supported},
		},
		"version zero": {
			{Version: 0, Status: apiver.Current},
		},
		"everything retired": {
			{Version: 1, Status: apiver.Retired},
			{Version: 2, Status: apiver.Retired},
		},
	} {
		if _, err := apiver.NewRegistry(specs...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestDefaultIsTheOldestLiveVersion(t *testing.T) {
	r := three(t)
	if r.Default() != 1 {
		t.Fatalf("default is %s, want v1: defaulting forward breaks the callers who did not choose", r.Default())
	}
}

// TestDefaultMovesWhenAVersionRetires is the bug the sunset test found. Held
// as a fixed field, the default pointed at a retired version from the morning
// the sunset fell, and every unversioned request was answered 410 by a server
// with three working versions.
func TestDefaultMovesWhenAVersionRetires(t *testing.T) {
	r := three(t)
	if r.Default() != 1 {
		t.Fatalf("default starts at %s", r.Default())
	}
	r.SetClock(func() time.Time { return now.AddDate(0, 2, 0) })
	if got := r.Default(); got != 2 {
		t.Fatalf("after v1's sunset the default is %s, want v2", got)
	}
	if _, err := r.Resolve(""); err != nil {
		t.Fatalf("an unversioned request failed after a sunset: %v", err)
	}
}

func TestSunsetRetiresWithoutAnEdit(t *testing.T) {
	r := three(t)
	if _, err := r.Resolve("v1"); err != nil {
		t.Fatalf("v1 was refused before its sunset: %v", err)
	}

	r.SetClock(func() time.Time { return now.AddDate(0, 2, 0) })

	_, err := r.Resolve("v1")
	var retired apiver.ErrRetired
	if !errors.As(err, &retired) {
		t.Fatalf("v1 answered after its sunset, got %v. A sunset nobody enforces is a suggestion", err)
	}
	if retired.Current != 3 {
		t.Errorf("the error does not point at the current version, says %s", retired.Current)
	}
}

func TestLiveExcludesRetired(t *testing.T) {
	r := registry(t,
		apiver.Spec{Version: 1, Status: apiver.Retired},
		apiver.Spec{Version: 2, Status: apiver.Supported},
		apiver.Spec{Version: 3, Status: apiver.Current},
	)
	live := r.Live()
	if len(live) != 2 || live[0] != 2 || live[1] != 3 {
		t.Fatalf("live is %v, want v2 and v3 in that order", live)
	}
}

func TestResolveOnTheExactSunsetInstant(t *testing.T) {
	sunset := now.AddDate(0, 1, 0)
	r := registry(t,
		apiver.Spec{Version: 1, Status: apiver.Deprecated, SunsetOn: sunset},
		apiver.Spec{Version: 2, Status: apiver.Current},
	)

	r.SetClock(func() time.Time { return sunset.Add(-time.Second) })
	if _, err := r.Resolve("v1"); err != nil {
		t.Fatalf("v1 was refused a second before its sunset: %v", err)
	}
	// The sunset instant itself is past due. A boundary that is ambiguous is
	// a boundary somebody argues about during an incident.
	r.SetClock(func() time.Time { return sunset })
	if _, err := r.Resolve("v1"); err == nil {
		t.Fatal("v1 answered at the exact sunset instant")
	}
}

func TestResolveReportsDaysLeft(t *testing.T) {
	r := three(t)
	res, err := r.Resolve("v1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != apiver.Deprecated {
		t.Fatalf("v1 resolved as %s", res.Status)
	}
	if res.DaysLeft < 29 || res.DaysLeft > 31 {
		t.Fatalf("days left is %d, want about 30", res.DaysLeft)
	}
}

func TestUnknownVersionErrorListsTheLiveOnes(t *testing.T) {
	r := three(t)
	_, err := r.Resolve("v9")
	var unknown apiver.ErrUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("got %v, want an unknown version error", err)
	}
	if len(unknown.Live) != 3 || unknown.Current != 3 {
		t.Fatalf("the error does not tell the client what to do: %+v", unknown)
	}
}

func TestDefaultedIsReported(t *testing.T) {
	r := three(t)
	res, err := r.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Defaulted {
		t.Fatal("a request with no version was not marked as defaulted")
	}
	res, err = r.Resolve("v2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Defaulted {
		t.Fatal("an explicit version was marked as defaulted")
	}
}
