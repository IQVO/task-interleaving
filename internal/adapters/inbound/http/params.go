package http

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Bounds of the request schema (apis/openapi.yaml).
const (
	maxFromLocation   = 128
	maxHintTTLSeconds = 1<<31 - 1
	// staleIfMatch is the version an If-Match that is not a version stands
	// for: no policy ever has it, so the use case answers version-mismatch.
	staleIfMatch int64 = -1
)

// pathParam returns a path segment, unescaped (an encoded '/' or space reaches
// the use case and is rejected there by the domain id rules). invalid is
// returned for a segment that is not valid percent-encoding.
func pathParam(r *http.Request, name string, invalid error) (string, error) {
	v, err := url.PathUnescape(chi.URLParam(r, name))
	if err != nil {
		return "", invalid
	}
	return v, nil
}

// intParam reads an optional positive integer query parameter. Absent is
// (0, nil); present but not a positive integer is invalid (the use case owns
// the upper bound).
func intParam(r *http.Request, name string, invalid error) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: %s must be a positive integer", invalid, name)
	}
	return n, nil
}

// parseIfMatch reads If-Match, the policy version with or without double
// quotes. An absent header is (0, false); a present value that is not a
// version (weak tags, "*", garbage) is reported as staleIfMatch: the contract
// documents no other answer than 412 version-mismatch for an If-Match that
// does not match the current version.
func parseIfMatch(r *http.Request) (version int64, present bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		return 0, false
	}
	digits := raw
	if digits[0] == '"' {
		digits = digits[1:]
	}
	if len(digits) > 0 && digits[len(digits)-1] == '"' {
		digits = digits[:len(digits)-1]
	}
	if !allDigits(digits) {
		return staleIfMatch, true
	}
	version, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return staleIfMatch, true
	}
	return version, true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// etag is the version as a quoted entity tag.
func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// isSpace reports whether r is white space or a line terminator in the
// ECMA-262 sense, which is what the `\s` of the contract's `pattern: '\S'`
// means. It is NOT unicode.IsSpace: U+0085 is not an ECMA-262 space, U+FEFF is.
func isSpace(r rune) bool {
	if r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == 0xFEFF {
		return true
	}
	if r == 0x2028 || r == 0x2029 {
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// isBlank reports whether s fails the contract's `pattern: '\S'`.
func isBlank(s string) bool {
	for _, r := range s {
		if !isSpace(r) {
			return false
		}
	}
	return true
}

// validLocationRef checks the request schema of fromLocation: 1..128
// characters, not blank.
func validLocationRef(s string) bool {
	return utf8.RuneCountInString(s) <= maxFromLocation && !isBlank(s)
}
