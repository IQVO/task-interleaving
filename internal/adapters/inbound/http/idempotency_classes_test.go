package http_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
)

const adrPath = "../../../../docs/adr/0013-every-problem-type-has-a-replay-class.md"

// The ADR table is held equal to the catalogue: a problem type added, removed,
// reclassified or re-statused without the ADR fails here.
func TestTheADRDeclaresTheClassOfEveryProblemType(t *testing.T) {
	raw, err := os.ReadFile(adrPath)
	if err != nil {
		t.Fatalf("the ADR of the replay classes is missing: %v", err)
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z-]+)` \\| (FINAL|OUTCOME|RETRYABLE) \\| (\\d{3}) \\|")
	got := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(string(raw), -1) {
		got[m[1]] = m[2] + " " + m[3]
	}
	want := map[string]string{}
	for _, e := range inboundhttp.ReplayCatalogue() {
		want[e.Slug] = e.Class + " " + strconv.Itoa(e.Status)
	}
	var diffs []string
	for slug, w := range want {
		if got[slug] != w {
			diffs = append(diffs, fmt.Sprintf("%s: the ADR says %q, the catalogue %q", slug, got[slug], w))
		}
	}
	for slug := range got {
		if _, ok := want[slug]; !ok {
			diffs = append(diffs, slug+": in the ADR, not in the catalogue")
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Fatalf("ADR 0013 differs from the catalogue:\n%s", strings.Join(diffs, "\n"))
	}
}

// One slug is one entry, whichever error reaches it (newProblem refuses a
// second status or class for a slug at start).
func TestOneSlugHasOneEntry(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range inboundhttp.ReplayCatalogue() {
		if seen[e.Slug] {
			t.Errorf("%s is listed twice: a slug has one status and one class", e.Slug)
		}
		seen[e.Slug] = true
	}
}

// OUTCOME is conservative: only a refusal that provably commits state. The only
// keyed route, POST /recommendations, writes nothing but the key row and a 4xx
// rolls its unit of work back, so no problem type of this service is one.
func TestNoProblemTypeIsAnOutcome(t *testing.T) {
	for _, e := range inboundhttp.ReplayCatalogue() {
		if e.Class == "OUTCOME" {
			t.Errorf("%s is an OUTCOME: prove with a test that the state exists after the refusal (docs/adr/0013), then update this pin", e.Slug)
		}
	}
}

// problem literals live in the constructor only, so a problem cannot be built
// without a class (and cannot dodge the registry the tests above read).
func TestProblemsAreBuiltByTheConstructorOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range tree.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			inConstructor := isFunc && fn.Name.Name == "newProblem"
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if id, ok := lit.Type.(*ast.Ident); ok && id.Name == "problem" && !inConstructor {
					t.Errorf("%s: a problem literal outside newProblem: declare it with newProblem(status, slug, title, detail, class)", fset.Position(lit.Pos()))
				}
				return true
			})
		}
	}
}

// A refusal that carries no class (a handler that wrote a 4xx by hand) is
// released like RETRYABLE: nothing stored, the retry runs.
func TestAnUnclassifiedRefusalIsReleased(t *testing.T) {
	a := newReplayAdapter(t)
	var calls int
	mw := a.Middleware(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"/problems/made-up","status":409}`))
	}))
	first := post(t, mw, "k-unclassified", `{"n":1}`)
	second := post(t, mw, "k-unclassified", `{"n":1}`)
	if first.Code != http.StatusConflict || second.Code != http.StatusConflict || replayed(second) {
		t.Fatalf("answers = %d, %d replayed=%v", first.Code, second.Code, replayed(second))
	}
	if calls != 2 || a.KeyRows(t, "k-unclassified") != 0 {
		t.Fatalf("handler ran %d times, rows = %d, want 2 and 0: an unclassified refusal is released", calls, a.KeyRows(t, "k-unclassified"))
	}
}

// A concurrent duplicate while the first request ends RETRYABLE: the middleware
// holds no in-flight lock, so every copy executes itself, each is answered with
// its own refusal, and nothing is stored under the key.
func TestConcurrentCopiesOfARetryableRefusalAreAllAnsweredAndNothingIsStored(t *testing.T) {
	f := newIdempotentFixture(t)
	f.seedWorld(t) // no policy: policy-not-declared
	var refused atomic.Int32
	parallel(6, func(int) {
		if r := f.call(t, http.MethodPost, recommendPath, simulate, idemHeader, "k-burst"); r.status == http.StatusUnprocessableEntity {
			refused.Add(1)
		}
	})
	if refused.Load() != 6 || f.store.saves.Load() != 0 {
		t.Fatalf("refused %d of 6, stores %d, want 6 and 0", refused.Load(), f.store.saves.Load())
	}
	f.declare(t)
	expectFirstExecution(t, f.mustCall(t, http.MethodPost, recommendPath, simulate, http.StatusOK, idemHeader, "k-burst"))
}
