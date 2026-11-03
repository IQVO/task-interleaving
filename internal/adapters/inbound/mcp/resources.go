package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// policyURITemplate is the ONE resource this server offers (Agentic Standard
// v2, AS-3): a scoped read model this context owns, not a dump of a table.
// It answers "how is interleaving configured at this site?" for ONE site —
// the policy its operator declared, the same GetPolicy read use case the
// get_site_policy tool and GET /sites/{siteId}/policy use. Bounded by
// construction: one policy declaration, eight fields, JSON text.
const policyURITemplate = "warehouse://task-interleaving/policies/{siteId}"

// registerResources adds the resource surface (read-only by nature, L1; the
// charter keeps resources context data an agent can load without a tool
// call). No resource ever writes: Deps holds read use cases only (ADR 0007).
func (d Deps) registerResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: policyURITemplate,
		Name:        "interleaving policy of a site",
		Description: "The interleaving policy one site declared: maxDetourM (the farthest a candidate may lie from the station), maxCandidates (1..5), hintTtlSeconds, eligibleTaskTypes, verticalWeight, version and declaredAt. A site that never declared a policy is an error (it is inert: no hint is ever issued for it).",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		siteID, err := parsePolicyURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		rec, err := d.GetPolicy.Handle(ctx, siteID)
		if err != nil {
			return nil, mapError(err)
		}
		return resourceResult(req.Params.URI, toPolicy(rec))
	})
}

// resourceResult marshals one read model into the resource's text contents.
func resourceResult(uri string, view any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: mimeTypeJSON,
			Text:     string(body),
		}},
	}, nil
}

// parsePolicyURI extracts the siteId from a
// warehouse://task-interleaving/policies/{siteId} URI. The siteId is
// model-supplied and therefore untrusted: a malformed URI is rejected rather
// than silently defaulted; the use case validates the id itself.
func parsePolicyURI(uri string) (string, error) {
	const scheme = "warehouse://task-interleaving/policies/"
	rest, ok := strings.CutPrefix(uri, scheme)
	if !ok {
		return "", fmt.Errorf("resource uri %q must start with %q", uri, scheme)
	}
	if rest == "" || strings.Contains(rest, "/") {
		return "", fmt.Errorf("resource uri %q must carry exactly one non-empty siteId after %q", uri, scheme)
	}
	return rest, nil
}
