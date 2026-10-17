package http

import (
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

const openapiPath = "../../../../apis/openapi.yaml"

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

// specOperations lists "METHOD /path" for every operation of the contract.
func specOperations(t *testing.T) []string {
	t.Helper()
	paths, _ := loadSpec(t)["paths"].(map[string]any)
	var ops []string
	for path, item := range paths {
		methods, _ := item.(map[string]any)
		for method := range methods {
			if slices.Contains([]string{"get", "put", "post", "delete", "patch"}, method) {
				ops = append(ops, strings.ToUpper(method)+" "+path)
			}
		}
	}
	sort.Strings(ops)
	return ops
}

func routerOperations(t *testing.T) []string {
	t.Helper()
	routes, ok := NewRouter(&Server{}).(chi.Routes)
	if !ok {
		t.Fatal("NewRouter does not return a chi router")
	}
	var ops []string
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		ops = append(ops, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(ops)
	return ops
}

// documentedOperations are the 8 operations of apis/openapi.yaml; the test
// pins them so neither the contract nor the router can drift unnoticed.
var documentedOperations = []string{
	"GET /healthz",
	"GET /hints",
	"GET /hints/{hintId}",
	"GET /readyz",
	"GET /sites/{siteId}/policy",
	"GET /stations/{stationId}",
	"POST /recommendations",
	"PUT /sites/{siteId}/policy",
}

// TestRouterServesExactlyTheDocumentedOperations is the contract check of a
// repo without a schemathesis job: every operation of apis/openapi.yaml is
// routed and the router serves nothing the contract does not document.
func TestRouterServesExactlyTheDocumentedOperations(t *testing.T) {
	spec, router := specOperations(t), routerOperations(t)
	if !slices.Equal(spec, documentedOperations) {
		t.Fatalf("the contract changed\nwant: %v\ngot:  %v", documentedOperations, spec)
	}
	if !slices.Equal(spec, router) {
		t.Fatalf("router and contract differ\ncontract: %v\nrouter:   %v", spec, router)
	}
}

// exampleSlugs collects every problem `type` slug the contract documents.
func exampleSlugs(node any, out map[string]bool) {
	switch n := node.(type) {
	case map[string]any:
		if typ, ok := n["type"].(string); ok && strings.HasPrefix(typ, problemBaseURI) {
			out[strings.TrimPrefix(typ, problemBaseURI)] = true
		}
		for _, v := range n {
			exampleSlugs(v, out)
		}
	case []any:
		for _, v := range n {
			exampleSlugs(v, out)
		}
	}
}

// TestProblemSlugsMatchTheContract: every slug the adapter can answer has its
// example in apis/openapi.yaml and every documented slug is produced.
func TestProblemSlugsMatchTheContract(t *testing.T) {
	documented := map[string]bool{}
	exampleSlugs(loadSpec(t), documented)

	produced := map[string]bool{
		internalProblem.slug: true, idempotencyKeyRequired.slug: true, idempotencyKeyReused.slug: true,
		dependencyUnavailable.slug: true,
	}
	for _, entry := range problemCatalogue {
		produced[entry.p.slug] = true
	}
	for slug := range produced {
		if !documented[slug] {
			t.Errorf("slug %q is answered by the adapter but has no example in apis/openapi.yaml", slug)
		}
	}
	for slug := range documented {
		if !produced[slug] {
			t.Errorf("slug %q is documented in apis/openapi.yaml but the adapter never answers it", slug)
		}
	}
}

// collectStatuses records the (slug -> status) pairs of the examples under node.
func collectStatuses(node any, out map[string]int) {
	switch n := node.(type) {
	case map[string]any:
		typ, hasType := n["type"].(string)
		status, hasStatus := n["status"].(int)
		if hasType && hasStatus && strings.HasPrefix(typ, problemBaseURI) {
			out[strings.TrimPrefix(typ, problemBaseURI)] = status
		}
		for _, v := range n {
			collectStatuses(v, out)
		}
	case []any:
		for _, v := range n {
			collectStatuses(v, out)
		}
	}
}

// TestProblemStatusesMatchTheContract: a slug keeps the status of its example.
func TestProblemStatusesMatchTheContract(t *testing.T) {
	components, _ := loadSpec(t)["components"].(map[string]any)
	statusBySlug := map[string]int{}
	collectStatuses(components["responses"], statusBySlug)
	if len(statusBySlug) < 20 {
		t.Fatalf("read only %d documented problems; the contract walk is broken", len(statusBySlug))
	}
	check := func(slug string, status int) {
		if want, ok := statusBySlug[slug]; ok && want != status {
			t.Errorf("slug %q: adapter status %d, contract example status %d", slug, status, want)
		}
	}
	check(internalProblem.slug, internalProblem.status)
	check(idempotencyKeyRequired.slug, idempotencyKeyRequired.status)
	check(idempotencyKeyReused.slug, idempotencyKeyReused.status)
	for _, entry := range problemCatalogue {
		check(entry.p.slug, entry.p.status)
	}
}

// TestFixedProblemDetailsAndTitlesMatchTheContractExamples: where the adapter
// fixes a title, it is the contract example's title.
func TestFixedProblemTitlesMatchTheContractExamples(t *testing.T) {
	titles := map[string]string{}
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			typ, _ := n["type"].(string)
			title, _ := n["title"].(string)
			if strings.HasPrefix(typ, problemBaseURI) && title != "" {
				titles[strings.TrimPrefix(typ, problemBaseURI)] = title
			}
			for _, v := range n {
				walk(v)
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(loadSpec(t))
	all := []problem{internalProblem, idempotencyKeyRequired, idempotencyKeyReused}
	for _, entry := range problemCatalogue {
		all = append(all, entry.p)
	}
	for _, p := range all {
		if want, ok := titles[p.slug]; ok && want != p.title {
			t.Errorf("slug %q: title %q, contract example title %q", p.slug, p.title, want)
		}
	}
}
