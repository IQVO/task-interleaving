package http_test

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// A tiny OpenAPI schema walker: the responses the adapter sends must have
// exactly the shape apis/openapi.yaml documents (required properties present,
// no undocumented property, types and enums respected).

type specDoc map[string]any

func loadOpenAPI(t *testing.T) specDoc {
	t.Helper()
	raw, err := os.ReadFile("../../../../apis/openapi.yaml")
	must(t, err)
	var doc map[string]any
	must(t, yaml.Unmarshal(raw, &doc))
	return specDoc(doc)
}

func (d specDoc) schema(name string) map[string]any {
	components, _ := d["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	s, _ := schemas[name].(map[string]any)
	return s
}

// resolve follows a `$ref: "#/components/schemas/Name"`.
func (d specDoc) resolve(s map[string]any) map[string]any {
	ref, ok := s["$ref"].(string)
	if !ok {
		return s
	}
	return d.schema(strings.TrimPrefix(ref, "#/components/schemas/"))
}

// conforms returns the problems of value against the named schema.
func (d specDoc) conforms(name string, value any) []string {
	return d.check(d.schema(name), value, name)
}

func (d specDoc) check(s map[string]any, value any, path string) []string {
	s = d.resolve(s)
	if nullable, _ := s["nullable"].(bool); nullable && value == nil {
		return nil
	}
	if enum, ok := s["enum"].([]any); ok {
		return checkEnum(enum, value, path)
	}
	switch s["type"] {
	case "object":
		return d.checkObject(s, value, path)
	case "array":
		return d.checkArray(s, value, path)
	case "string":
		return checkString(s, value, path)
	case "integer", "number":
		if _, ok := value.(float64); !ok {
			return []string{fmt.Sprintf("%s: %v is not a number", path, value)}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return []string{fmt.Sprintf("%s: %v is not a boolean", path, value)}
		}
	}
	return nil
}

func checkEnum(enum []any, value any, path string) []string {
	for _, e := range enum {
		if e == value {
			return nil
		}
	}
	return []string{fmt.Sprintf("%s: %v is not one of %v", path, value, enum)}
}

func checkString(s map[string]any, value any, path string) []string {
	str, ok := value.(string)
	if !ok {
		return []string{fmt.Sprintf("%s: %v is not a string", path, value)}
	}
	if s["format"] == "date-time" {
		if _, err := time.Parse(time.RFC3339, str); err != nil {
			return []string{fmt.Sprintf("%s: %q is not a date-time", path, str)}
		}
	}
	return nil
}

func (d specDoc) checkObject(s map[string]any, value any, path string) []string {
	obj, ok := value.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s: %v is not an object", path, value)}
	}
	props, _ := s["properties"].(map[string]any)
	var problems []string
	required, _ := s["required"].([]any)
	for _, r := range required {
		if _, present := obj[r.(string)]; !present {
			problems = append(problems, fmt.Sprintf("%s: required property %q is missing", path, r))
		}
	}
	for key, v := range obj {
		sub, documented := props[key].(map[string]any)
		if !documented {
			problems = append(problems, fmt.Sprintf("%s: property %q is not documented", path, key))
			continue
		}
		problems = append(problems, d.check(sub, v, path+"."+key)...)
	}
	return problems
}

func (d specDoc) checkArray(s map[string]any, value any, path string) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{fmt.Sprintf("%s: %v is not an array", path, value)}
	}
	var problems []string
	if min, ok := s["minItems"].(int); ok && len(items) < min {
		problems = append(problems, fmt.Sprintf("%s: %d items, minimum %d", path, len(items), min))
	}
	elem, _ := s["items"].(map[string]any)
	for i, it := range items {
		problems = append(problems, d.check(elem, it, fmt.Sprintf("%s[%d]", path, i))...)
	}
	return problems
}

func expectShape(t *testing.T, doc specDoc, schema string, r response) {
	t.Helper()
	if problems := doc.conforms(schema, r.body); len(problems) > 0 {
		t.Fatalf("%s does not conform to %s:\n  %s\n%s", r.raw, schema, strings.Join(problems, "\n  "), r.raw)
	}
}

func TestResponsesHaveTheDocumentedShape(t *testing.T) {
	doc := loadOpenAPI(t)
	f := withHint(t)
	expectShape(t, doc, "Policy", f.mustCall(t, http.MethodGet, "/sites/SITE-1/policy", "", http.StatusOK))
	expectShape(t, doc, "Policy", f.mustCall(t, http.MethodPut, "/sites/SITE-1/policy", policyBody, http.StatusOK, "If-Match", `"1"`))
	expectShape(t, doc, "Hint", f.mustCall(t, http.MethodGet, "/hints/hint-test-001", "", http.StatusOK))
	expectShape(t, doc, "HintPage", f.mustCall(t, http.MethodGet, "/hints", "", http.StatusOK))
	expectShape(t, doc, "Station", f.mustCall(t, http.MethodGet, "/stations/STN-07", "", http.StatusOK))
	expectShape(t, doc, "Recommendation", f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-07"}`, http.StatusOK))
	expectShape(t, doc, "Recommendation", f.mustCall(t, http.MethodPost, recommendPath, `{"stationId":"STN-07","fromLocation":"REMOTE-01"}`, http.StatusOK))
	expectShape(t, doc, "Status", f.mustCall(t, http.MethodGet, "/healthz", "", http.StatusOK))
	expectShape(t, doc, "Status", f.mustCall(t, http.MethodGet, "/readyz", "", http.StatusOK))
	expectShape(t, doc, "Problem", f.call(t, http.MethodGet, "/hints/hint-unknown", ""))
}

func TestFinishedHintsHaveTheDocumentedShape(t *testing.T) {
	doc := loadOpenAPI(t)
	f := withHint(t)
	f.clock.Advance(time.Second)
	f.complete(t, "STN-07", "task-8a20") // supersedes hint 1, issues hint 2
	_, err := f.claimed.Handle(t.Context(), "evt-claim", usecases.TaskClaim{TaskID: "task-9c2e", TaskType: "PICK", StationID: "STN-07", At: f.clock.Now()})
	must(t, err) // consumes hint 2
	f.clock.Advance(time.Hour)
	f.complete(t, "STN-08", "task-8a21") // a third hint, left to expire
	f.clock.Advance(time.Hour)
	_, err = f.expire.Handle(t.Context())
	must(t, err)
	states := map[string]bool{}
	for _, id := range []string{"hint-test-001", "hint-test-002", "hint-test-003"} {
		r := f.mustCall(t, http.MethodGet, "/hints/"+id, "", http.StatusOK)
		expectShape(t, doc, "Hint", r)
		states[r.body["state"].(string)] = true
	}
	for _, want := range []string{"Superseded", "Consumed", "Expired"} {
		if !states[want] {
			t.Errorf("no %s hint among %v", want, states)
		}
	}
}

// The checker itself must be able to fail.
func TestTheShapeCheckerCatchesDrift(t *testing.T) {
	doc := loadOpenAPI(t)
	good := map[string]any{"rank": 1.0, "taskId": "x", "taskType": "PICK", "location": "L", "detourM": 1.5}
	if p := doc.conforms("Candidate", good); len(p) != 0 {
		t.Fatalf("a valid candidate fails: %v", p)
	}
	bad := map[string]any{"rank": "1", "taskId": "x", "taskType": "FLY", "extra": true}
	if p := doc.conforms("Candidate", bad); len(p) < 4 {
		t.Fatalf("drift not caught: %v", p)
	}
}
