// Package migrate turns the current response shape into an older one.
//
// The alternative, and the thing most codebases do first, is a struct per
// version and a branch in every handler. It works for two versions and
// collapses at four: the handler becomes a switch nobody dares touch, a bug
// fixed in the current shape is fixed in one branch and not the other three,
// and adding a version means editing every endpoint.
//
// Here the handler only ever builds the current shape. Each version adds one
// function that converts the version above it down to itself, and the chain
// is applied in order. Serving v1 from v4 is three small functions that have
// each been correct since the day they were written, rather than one
// four-way branch that is re-read on every change.
//
// The cost is that migrations run per request, on a decoded map rather than
// on a struct. For a response of tens of fields that is microseconds, and it
// is measured in the benchmark rather than assumed.
package migrate

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/usmanvirk/api-versioning/internal/apiver"
)

// Doc is a response part way down the chain.
//
// A map and not a struct, deliberately. A typed struct for every version is
// a type that has to be kept compiling forever, and the only thing a
// migration does is add, remove, rename and reshape fields, which is exactly
// what a map expresses without ceremony.
type Doc map[string]any

// Step converts a response from one version down to the version below it.
type Step struct {
	// From is the version this step converts down from, so a step with
	// From 4 turns a v4 response into a v3 one.
	From apiver.Version
	// Why is one line, served in the documentation endpoint and read by
	// whoever has to understand what changed.
	Why string
	// Down rewrites the document in place.
	Down func(Doc) error
}

// Chain is every step, held in one place so that the full set of
// transformations between any two versions can be read in one file.
type Chain struct {
	steps map[apiver.Version]Step
}

func NewChain(steps ...Step) (*Chain, error) {
	c := &Chain{steps: make(map[apiver.Version]Step, len(steps))}
	for _, s := range steps {
		if s.Down == nil {
			return nil, fmt.Errorf("step from %s has no function", s.From)
		}
		if s.From <= 1 {
			return nil, fmt.Errorf("step from %s would convert below v1", s.From)
		}
		if _, dup := c.steps[s.From]; dup {
			return nil, fmt.Errorf("two steps convert down from %s", s.From)
		}
		c.steps[s.From] = s
	}
	return c, nil
}

// Missing reports which steps are absent between two versions.
//
// A gap means a request for that version silently receives a newer shape,
// which is the failure this whole package exists to prevent, so it is
// checked at startup rather than discovered by a client.
func (c *Chain) Missing(from, to apiver.Version) []apiver.Version {
	var gaps []apiver.Version
	for v := from; v > to; v-- {
		if _, ok := c.steps[v]; !ok {
			gaps = append(gaps, v)
		}
	}
	return gaps
}

// Steps lists the chain, newest first, for the documentation endpoint.
func (c *Chain) Steps() []Step {
	out := make([]Step, 0, len(c.steps))
	for _, s := range c.steps {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From > out[j].From })
	return out
}

// Apply converts a current-shaped value down to the version asked for.
func (c *Chain) Apply(current any, from, to apiver.Version) (Doc, error) {
	if to > from {
		return nil, fmt.Errorf("cannot convert %s up to %s", from, to)
	}

	raw, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("encoding the current shape: %w", err)
	}
	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decoding the current shape: %w", err)
	}

	for v := from; v > to; v-- {
		step, ok := c.steps[v]
		if !ok {
			// Refused rather than skipped. Skipping sends the client a
			// shape from a version it does not understand, and it does so
			// silently.
			return nil, fmt.Errorf("no step converts %s down to %s", v, v-1)
		}
		if err := step.Down(doc); err != nil {
			return nil, fmt.Errorf("converting %s down to %s: %w", v, v-1, err)
		}
	}
	return doc, nil
}

// Rename moves a field, leaving nothing behind. The commonest step there is.
func Rename(doc Doc, from, to string) {
	if v, ok := doc[from]; ok {
		doc[to] = v
		delete(doc, from)
	}
}

// Drop removes fields a version did not have.
//
// Leaving an unknown field in is tempting, because most clients ignore what
// they do not recognise. Most is not all: a strict decoder fails on it, and
// the clients that use one are the ones generated from a schema, which is
// the kind an app usually has.
func Drop(doc Doc, fields ...string) {
	for _, f := range fields {
		delete(doc, f)
	}
}

// Each applies fn to every element of a list field, which is where most of
// the work in a real migration happens.
func Each(doc Doc, field string, fn func(Doc) error) error {
	raw, ok := doc[field]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s is %T, not a list", field, raw)
	}
	for i, item := range list {
		m, ok := item.(Doc)
		if !ok {
			mm, ok2 := item.(map[string]any)
			if !ok2 {
				return fmt.Errorf("%s[%d] is %T, not an object", field, i, item)
			}
			m = Doc(mm)
		}
		if err := fn(m); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, i, err)
		}
	}
	return nil
}
