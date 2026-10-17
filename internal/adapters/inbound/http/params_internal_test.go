package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/task-interleaving/internal/domain/recommend"
	"github.com/claudioed/task-interleaving/internal/domain/stationtrip"
)

// requestWithParam builds a request whose chi route context carries one raw
// (still percent-encoded) path parameter, as the router would hand it over.
func requestWithParam(name, raw string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(name, raw)
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

var errInvalidParam = errors.New("invalid param")

// A server never delivers an invalid escape to the router, but the adapter
// still refuses one rather than using the raw text.
func TestPathParamRefusesInvalidPercentEncoding(t *testing.T) {
	if _, err := pathParam(requestWithParam("siteId", "%zz"), "siteId", errInvalidParam); !errors.Is(err, errInvalidParam) {
		t.Fatalf("invalid escape: %v", err)
	}
	if got, err := pathParam(requestWithParam("siteId", "SITE%2D1"), "siteId", errInvalidParam); err != nil || got != "SITE-1" {
		t.Fatalf("a valid escape is decoded: %q %v", got, err)
	}
}

func TestParseIfMatch(t *testing.T) {
	for raw, want := range map[string]int64{`1`: 1, `"7"`: 7, `0`: 0, `"12`: 12, `3"`: 3} {
		req := httptest.NewRequest(http.MethodPut, "/", http.NoBody)
		req.Header.Set("If-Match", raw)
		if got, present := parseIfMatch(req); !present || got != want {
			t.Fatalf("If-Match %q = %d %v, want %d", raw, got, present, want)
		}
	}
	for _, raw := range []string{`""`, `"`, `a`, `1.0`, `+1`, `-1`, ` 1`, `W/"1"`, `1,2`, `*`, `99999999999999999999`} {
		req := httptest.NewRequest(http.MethodPut, "/", http.NoBody)
		req.Header.Set("If-Match", raw)
		if got, present := parseIfMatch(req); !present || got != staleIfMatch {
			t.Fatalf("If-Match %q = %d %v, want the stale marker", raw, got, present)
		}
	}
	if _, present := parseIfMatch(httptest.NewRequest(http.MethodPut, "/", http.NoBody)); present {
		t.Fatal("an absent header is not present")
	}
}

func TestIntParam(t *testing.T) {
	get := func(q string) (int, error) {
		return intParam(httptest.NewRequest(http.MethodGet, "/hints?"+q, http.NoBody), "limit", errInvalidParam)
	}
	if n, err := get(""); n != 0 || err != nil {
		t.Fatalf("absent = %d %v", n, err)
	}
	if n, err := get("limit=7"); n != 7 || err != nil {
		t.Fatalf("7 = %d %v", n, err)
	}
	for _, q := range []string{"limit=0", "limit=-1", "limit=x", "limit=1.5"} {
		if _, err := get(q); !errors.Is(err, errInvalidParam) {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// The contract's `pattern: '\S'` is ECMA-262: U+FEFF is white space, U+0085 is not.
func TestBlankFollowsTheECMA262WhiteSpaceRule(t *testing.T) {
	for _, s := range []string{"", " ", "\t\n\v\f\r", "\u00a0", "\u2003", "\u2028\u2029", "\ufeff", " \ufeff "} {
		if !isBlank(s) {
			t.Errorf("%q must be blank", s)
		}
	}
	for _, s := range []string{"a", " a ", "\u0085", "\u200b", "0"} {
		if isBlank(s) {
			t.Errorf("%q must not be blank", s)
		}
	}
	if validLocationRef("") || validLocationRef("  ") || !validLocationRef("A-01") {
		t.Fatal("location bounds: blank refused, text accepted")
	}
}

func TestUnknownOriginDetailNamesTheLocationOnlyWhenKnown(t *testing.T) {
	p := problemFor(recommend.ErrOriginUnknown)
	named := errors.Join(errors.New("x")) // an error that does not carry the prefix
	if got := problemDetailFor(named, problem{detail: "fixed"}); got != "fixed" {
		t.Fatalf("fixed detail = %q", got)
	}
	if got := problemDetailFor(recommend.ErrOriginUnknown, p); got != "the origin location has no known geometry" {
		t.Fatalf("bare = %q", got)
	}
	wrapped := errors.New(recommend.ErrOriginUnknown.Error() + ": A-77")
	if got := problemDetailFor(joined{wrapped, recommend.ErrOriginUnknown}, p); got != "no geometry is known for location A-77" {
		t.Fatalf("named = %q", got)
	}
	if got := problemDetailFor(stationtrip.ErrInvalidHintID, problemFor(stationtrip.ErrInvalidHintID)); got == "" {
		t.Fatal("empty detail")
	}
	if got := problemDetailFor(errInvalidLocation, problemFor(errInvalidLocation)); got != errInvalidLocation.Error() {
		t.Fatalf("own message = %q", got)
	}
}

// joined is an error with the text of its first part that also matches `is`.
type joined struct {
	text error
	is   error
}

func (j joined) Error() string        { return j.text.Error() }
func (j joined) Is(target error) bool { return target == j.is }
