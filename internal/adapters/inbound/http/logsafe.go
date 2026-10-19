package http

import "strings"

// logReplacer strips the characters that let a request-derived value forge a
// log line.
var logReplacer = strings.NewReplacer("\r", "", "\n", "")

// logSafe removes CR and LF from a request-derived value (a path, a query
// value) before it is logged (CodeQL go/log-injection).
func logSafe(s string) string { return logReplacer.Replace(s) }
