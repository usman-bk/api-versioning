package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/usmanvirk/api-versioning/internal/apiver"
	"github.com/usmanvirk/api-versioning/internal/migrate"
)

// Header is where the version is read from.
//
// A header and not a path prefix. /v1/ and /v2/ means every route exists
// twice in the router, the two drift, and a client cannot move one endpoint
// at a time. A header keeps one route and makes the version a property of
// the request, which is what it is.
const Header = "X-API-Version"

// Store is where listings come from.
type Store interface {
	Get(id string) (Listing, bool)
}

type Handler struct {
	Versions *apiver.Registry
	Chain    *migrate.Chain
	Store    Store
	Log      *slog.Logger
}

// New checks at startup that every live version can actually be produced.
//
// A gap in the chain means a request for that version is answered with a
// newer shape, silently. Finding that on boot, where it stops the deploy, is
// the whole reason this check exists rather than a comment asking people to
// remember.
func New(reg *apiver.Registry, chain *migrate.Chain, store Store, log *slog.Logger) (*Handler, error) {
	live := reg.Live()
	if len(live) == 0 {
		return nil, errors.New("no live versions")
	}
	oldest, current := live[0], reg.Current()
	if gaps := chain.Missing(current, oldest); len(gaps) > 0 {
		return nil, fmt.Errorf("no migration down from %v, so a request for an older version would be answered with the wrong shape", gaps)
	}
	return &Handler{Versions: reg, Chain: chain, Store: store, Log: log}, nil
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /listings/{id}", h.get)
	mux.HandleFunc("GET /versions", h.versions)
	return mux
}

type errBody struct {
	Error     string   `json:"error"`
	Code      string   `json:"code"`
	Current   string   `json:"current_version,omitempty"`
	Supported []string `json:"supported_versions,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	res, err := h.Versions.Resolve(r.Header.Get(Header))
	if err != nil {
		h.versionError(w, err)
		return
	}

	// Every response says which version produced it, including the ones that
	// were defaulted. A client that did not ask can at least be read off a
	// log when somebody is working out why it broke.
	w.Header().Set(Header, res.Version.String())
	if res.Defaulted {
		w.Header().Set("X-API-Version-Defaulted", "true")
	}
	h.deprecationHeaders(w, res)

	listing, ok := h.Store.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, errBody{Error: "no such listing", Code: "not_found"})
		return
	}

	doc, err := h.Chain.Apply(listing, h.Versions.Current(), res.Version)
	if err != nil {
		// A failure here is a bug in a migration, not a bad request. Serving
		// the current shape instead would hand an old client something it
		// cannot read, so the request fails and the error is logged loudly.
		h.Log.Error("migrating the response failed",
			"from", h.Versions.Current(), "to", res.Version, "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody{
			Error: "could not build the response for this version", Code: "migration_failed"})
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

// deprecationHeaders follow RFC 8594 and RFC 9745 closely enough to be read
// by the tooling people already have, rather than inventing a header that
// only this service emits.
func (h *Handler) deprecationHeaders(w http.ResponseWriter, res apiver.Resolution) {
	if res.Status != apiver.Deprecated {
		return
	}
	w.Header().Set("Deprecation", "true")
	if !res.Spec.SunsetOn.IsZero() {
		w.Header().Set("Sunset", res.Spec.SunsetOn.UTC().Format(http.TimeFormat))
	}
	msg := fmt.Sprintf("%s is deprecated, the current version is %s",
		res.Version, h.Versions.Current())
	if res.Spec.Note != "" {
		msg += ". " + res.Spec.Note
	}
	if !res.Spec.SunsetOn.IsZero() {
		msg += fmt.Sprintf(". It stops answering in %d days", res.DaysLeft)
	}
	w.Header().Set("Warning", `299 - "`+msg+`"`)
}

func (h *Handler) versionError(w http.ResponseWriter, err error) {
	var retired apiver.ErrRetired
	var unknown apiver.ErrUnknown

	switch {
	case errors.As(err, &retired):
		// 410 and not 400. The client did nothing wrong when it was built,
		// and the distinction tells an app whether to show "update the app"
		// or to report a bug.
		writeJSON(w, http.StatusGone, errBody{
			Error: err.Error(), Code: "version_retired",
			Current: retired.Current.String(), Supported: strings_(h.Versions.Live()),
		})
	case errors.As(err, &unknown):
		writeJSON(w, http.StatusBadRequest, errBody{
			Error: err.Error(), Code: "version_unknown",
			Current: unknown.Current.String(), Supported: strings_(h.Versions.Live()),
		})
	default:
		writeJSON(w, http.StatusBadRequest, errBody{Error: err.Error(), Code: "version_invalid"})
	}
}

func strings_(vs []apiver.Version) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.String())
	}
	return out
}

type versionInfo struct {
	Version string `json:"version"`
	Status  string `json:"status"`
	Sunset  string `json:"sunset,omitempty"`
	Note    string `json:"note,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// versions serves what the server speaks, so the answer to "can we retire
// v1" is a request rather than an archaeology exercise.
func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	var out []versionInfo
	for _, v := range h.Versions.Live() {
		s, _ := h.Versions.Spec(v)
		info := versionInfo{
			Version: v.String(),
			Status:  string(s.Status),
			Note:    s.Note,
			Default: v == h.Versions.Default(),
		}
		if !s.SunsetOn.IsZero() {
			info.Sunset = s.SunsetOn.UTC().Format(time.RFC3339)
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current":  h.Versions.Current().String(),
		"default":  h.Versions.Default().String(),
		"versions": out,
	})
}
