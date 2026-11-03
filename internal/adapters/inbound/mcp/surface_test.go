// The MCP surface beyond tools (Agentic Standard v2, AS-3): ONE resource —
// the scoped site-policy read model — and ONE prompt — this context's
// operator SOP. Pinned through the same in-memory client the tool tests use.
package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The resource template is advertised with the fleet URI shape.
func TestSurface_ResourceTemplateListed(t *testing.T) {
	h := newHarness(t)
	h.declarePolicy(t)
	listed, err := h.session.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	found := false
	for _, rt := range listed.ResourceTemplates {
		if rt.URITemplate == "warehouse://task-interleaving/policies/{siteId}" {
			found = true
			if rt.MIMEType != "application/json" {
				t.Errorf("template MIMEType = %q, want application/json", rt.MIMEType)
			}
		}
	}
	if !found {
		t.Fatalf("policy resource template not advertised: %+v", listed.ResourceTemplates)
	}
}

// Reading the resource returns the same read model as the get_site_policy
// tool.
func TestSurface_ResourceRead(t *testing.T) {
	h := newHarness(t)
	h.declarePolicy(t)
	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{
		URI: "warehouse://task-interleaving/policies/SITE-1",
	})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d entries, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.URI != "warehouse://task-interleaving/policies/SITE-1" {
		t.Errorf("content URI = %q", c.URI)
	}
	var policy struct {
		SiteID            string   `json:"siteId"`
		MaxDetourM        float64  `json:"maxDetourM"`
		MaxCandidates     int      `json:"maxCandidates"`
		HintTTLSeconds    int64    `json:"hintTtlSeconds"`
		EligibleTaskTypes []string `json:"eligibleTaskTypes"`
		VerticalWeight    float64  `json:"verticalWeight"`
		Version           int64    `json:"version"`
		DeclaredAt        string   `json:"declaredAt"`
	}
	if err := json.Unmarshal([]byte(c.Text), &policy); err != nil {
		t.Fatalf("resource text is not the policy JSON: %v (%s)", err, c.Text)
	}
	if policy.SiteID != "SITE-1" || policy.MaxDetourM != 25 || policy.MaxCandidates != 3 ||
		policy.HintTTLSeconds != 90 || policy.Version != 1 || policy.DeclaredAt == "" {
		t.Errorf("policy = %+v, want SITE-1, detour 25, candidates 3, ttl 90, version 1", policy)
	}
}

// A malformed URI and an unknown site are errors, never silent defaults.
func TestSurface_ResourceRejectsMalformedAndUnknown(t *testing.T) {
	h := newHarness(t)
	h.declarePolicy(t)
	for _, uri := range []string{
		"staffing://task-interleaving/policies/SITE-1",        // wrong scheme
		"warehouse://task-interleaving/hints/SITE-1",          // wrong model
		"warehouse://task-interleaving/policies/",             // empty siteId
		"warehouse://task-interleaving/policies/SITE-1/extra", // trailing segment
		"warehouse://task-interleaving/policies/SITE-9",       // site that never declared
	} {
		if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("read resource %q: expected an error", uri)
		}
	}
}

// The prompt is advertised with its typed argument and returns the SOP text.
func TestSurface_PromptListedAndGet(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	listed, err := h.session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(listed.Prompts) != 1 || listed.Prompts[0].Name != "advise_next_task" {
		t.Fatalf("prompts = %+v, want exactly advise_next_task", listed.Prompts)
	}
	if len(listed.Prompts[0].Arguments) != 1 || listed.Prompts[0].Arguments[0].Name != "stationId" || !listed.Prompts[0].Arguments[0].Required {
		t.Fatalf("prompt arguments = %+v, want one required stationId", listed.Prompts[0].Arguments)
	}

	res, err := h.session.GetPrompt(ctx, &sdk.GetPromptParams{
		Name:      "advise_next_task",
		Arguments: map[string]string{"stationId": "STN-07"},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(res.Messages))
	}
	text := res.Messages[0].Content.(*sdk.TextContent).Text
	for _, needle := range []string{"get_station", "get_hint", "get_site_policy", "STN-07", "never an assignment"} {
		if !strings.Contains(text, needle) {
			t.Errorf("SOP text does not mention %q", needle)
		}
	}

	// A missing argument degrades to the placeholder, never to a hidden call.
	noArg, err := h.session.GetPrompt(ctx, &sdk.GetPromptParams{Name: "advise_next_task"})
	if err != nil {
		t.Fatalf("get prompt without arguments: %v", err)
	}
	if plain := noArg.Messages[0].Content.(*sdk.TextContent).Text; !strings.Contains(plain, "<stationId>") {
		t.Errorf("SOP without an argument should carry the <stationId> placeholder")
	}
}
