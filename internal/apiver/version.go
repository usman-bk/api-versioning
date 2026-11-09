// Package apiver resolves which version of the API a request wants, and how
// long a version has left.
//
// The problem this exists for: a release is on phones that cannot be forced
// to update, and some of those phones will not be updated for a year. The
// server therefore speaks more than one version of itself at once, and the
// question on every change is not "what should the API look like" but "what
// does the oldest client still running believe it looks like".
package apiver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Version is a whole number. Not semver.
//
// Semver invites a judgement about whether a change is major or minor, and
// that judgement is made by the person making the change, who is the person
// least able to see what it breaks. A counter removes the judgement: a new
// version exists when the response shape changes in a way an old client
// cannot read, and the number says nothing else.
type Version int

const (
	// Unknown is what a request with no version header resolves to before
	// the default is applied.
	Unknown Version = 0
)

func (v Version) String() string { return "v" + strconv.Itoa(int(v)) }

// Status is where a version sits in its life.
type Status string

const (
	// Current is the version new clients are built against.
	Current Status = "current"
	// Supported is older, still correct, and still has clients on it.
	Supported Status = "supported"
	// Deprecated still works and is being counted down. Clients on it are
	// told so on every response, because the only lever the server has over
	// an app it cannot update is telling it early and often.
	Deprecated Status = "deprecated"
	// Retired no longer answers.
	Retired Status = "retired"
)

// Spec describes one version.
type Spec struct {
	Version Version
	Status  Status
	// SunsetOn is when a deprecated version stops answering. Zero for a
	// version with no end date set.
	SunsetOn time.Time
	// Note is one line explaining what changed, served in the deprecation
	// header so whoever reads it has somewhere to start.
	Note string
}

// Registry is every version the server speaks.
//
// It is a list and not a set of if statements spread through the handlers,
// so that "what do we still support" has an answer somebody can read, and so
// that retiring a version is one edit rather than an audit.
type Registry struct {
	specs map[Version]Spec
	now   func() time.Time
}

func NewRegistry(specs ...Spec) (*Registry, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("a registry with no versions in it")
	}
	r := &Registry{specs: make(map[Version]Spec, len(specs)), now: time.Now}

	var current int
	live := 0
	for _, s := range specs {
		if s.Version <= 0 {
			return nil, fmt.Errorf("version %d is not a positive number", s.Version)
		}
		if _, dup := r.specs[s.Version]; dup {
			return nil, fmt.Errorf("%s is listed twice", s.Version)
		}
		if s.Status == Current {
			current++
		}
		if s.Status != Retired {
			live++
		}
		r.specs[s.Version] = s
	}
	if current != 1 {
		return nil, fmt.Errorf("%d versions marked current, want exactly 1", current)
	}
	if live == 0 {
		return nil, fmt.Errorf("every version is retired")
	}
	return r, nil
}

// Default is what a request with no version header gets.
//
// The oldest version that still answers, never the newest. A client that does
// not send a version is either very old or is not an app at all, and giving
// it the newest shape is the one choice guaranteed to break it. Defaulting
// forward looks tidy and breaks exactly the callers who had no idea they
// were making a choice.
//
// Computed on each call rather than fixed at startup, because a version
// retires by its sunset date passing and not by anybody editing the list.
// Held as a field, this returned a retired version from the morning the
// sunset fell, and every unversioned request was answered 410 by a server
// that had four working versions. The test for the sunset found it.
func (r *Registry) Default() Version {
	live := r.Live()
	if len(live) == 0 {
		return Unknown
	}
	return live[0]
}

// SetClock replaces the clock, so a sunset can be crossed without waiting.
func (r *Registry) SetClock(f func() time.Time) { r.now = f }

// Spec returns the spec for a version.
func (r *Registry) Spec(v Version) (Spec, bool) {
	s, ok := r.specs[v]
	return s, ok
}

// Current is the version new clients should use.
func (r *Registry) Current() Version {
	for v, s := range r.specs {
		if s.Status == Current {
			return v
		}
	}
	return Unknown
}

// Live lists the versions that still answer, oldest first.
func (r *Registry) Live() []Version {
	out := make([]Version, 0, len(r.specs))
	for v, s := range r.specs {
		if r.effective(s) != Retired {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// effective is the status now, which is not always the status written down:
// a deprecated version whose sunset date has passed is retired whether or
// not anybody edited the list.
func (r *Registry) effective(s Spec) Status {
	if s.Status == Deprecated && !s.SunsetOn.IsZero() && !r.now().Before(s.SunsetOn) {
		return Retired
	}
	return s.Status
}

// Resolution is the outcome of reading a request's version.
type Resolution struct {
	Version   Version
	Spec      Spec
	Status    Status
	Defaulted bool
	// DaysLeft is how long a deprecated version has. Negative is past due.
	DaysLeft int
}

// Errors a caller has to tell apart, because they are different answers to
// the client: one is "update your app", the other is "you asked for
// something that never existed".
type (
	ErrRetired struct {
		Version  Version
		SunsetOn time.Time
		Current  Version
	}
	ErrUnknown struct {
		Raw     string
		Live    []Version
		Current Version
	}
)

func (e ErrRetired) Error() string {
	return fmt.Sprintf("%s was retired on %s, the current version is %s",
		e.Version, e.SunsetOn.Format("2 January 2006"), e.Current)
}

func (e ErrUnknown) Error() string {
	parts := make([]string, 0, len(e.Live))
	for _, v := range e.Live {
		parts = append(parts, v.String())
	}
	return fmt.Sprintf("unknown version %q, this server speaks %s",
		e.Raw, strings.Join(parts, ", "))
}

// Resolve reads a version header and decides what the request gets.
func (r *Registry) Resolve(raw string) (Resolution, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		d := r.Default()
		return r.resolution(d, r.specs[d], true), nil
	}

	v, err := Parse(raw)
	if err != nil {
		return Resolution{}, ErrUnknown{Raw: raw, Live: r.Live(), Current: r.Current()}
	}

	s, ok := r.specs[v]
	if !ok {
		return Resolution{}, ErrUnknown{Raw: raw, Live: r.Live(), Current: r.Current()}
	}
	if r.effective(s) == Retired {
		return Resolution{}, ErrRetired{Version: v, SunsetOn: s.SunsetOn, Current: r.Current()}
	}
	return r.resolution(v, s, false), nil
}

func (r *Registry) resolution(v Version, s Spec, defaulted bool) Resolution {
	res := Resolution{Version: v, Spec: s, Status: r.effective(s), Defaulted: defaulted}
	if !s.SunsetOn.IsZero() {
		res.DaysLeft = int(s.SunsetOn.Sub(r.now()).Hours() / 24)
	}
	return res
}

// Parse reads "3", "v3" or "V3".
//
// Lenient on the spelling and strict on the value. Three clients will send
// three spellings whatever the documentation says, and none of those is a
// reason to fail a request, but "latest" is: it means the client has no idea
// which shape it is about to receive, which is the whole problem.
func Parse(raw string) (Version, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return Unknown, fmt.Errorf("%q is not a version number", raw)
	}
	return Version(n), nil
}
