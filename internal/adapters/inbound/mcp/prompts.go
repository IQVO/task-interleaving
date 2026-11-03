package mcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// adviseNextTaskSOP is the operator standard-operating-procedure the
// advise_next_task prompt hands to the model (Agentic Standard v2, AS-3).
// Per the charter, prompts encode how to interpret results and what "done"
// means; this one names tools but NEVER calls them, and it cannot write:
// every tool it names is read-only (ADR 0007).
const adviseNextTaskSOP = `You are advising which open task a warehouse station should take next, using only the MCP tools of task-interleaving; never assume state — read it.

Procedure:
1. Call get_station(stationId = STATION). The answer carries the station's site, its position, the last task it completed and when, and its ACTIVE hint if it holds one (hintId, state, issuedAt, expiresAt, policyVersion and its ranked candidate tasks: rank, taskId, taskType, location, detourM). A station that never recorded a completion is answered station-not-found: say so and stop.
2. If the station holds a hint, call get_hint(hintId = that hintId) for the full record: the task it follows (afterTaskId), the origin location it was ranked from, and how it ended once it ends (consumedTaskId/consumedAt or supersededAt).
3. Call get_site_policy(siteId = the station's site) to read the envelope the hint was ranked under: maxDetourM (no candidate lies farther than this from the station), maxCandidates, hintTtlSeconds, eligibleTaskTypes and verticalWeight. A site without a declared policy is inert — no hint can exist for it.

Interpretation:
- A hint is ADVISORY: it ranks open tasks by travel detour, it never assigns or claims anything. Recommend the rank-1 candidate unless the operator's situation says otherwise, and say why.
- Check expiresAt against now: an expired hint is stale history, not advice. Consumed and Superseded hints are history too — if the station's hint has ended, say that there is no live advice and list_hints(stationId = STATION) shows the history.
- detourM is the extra travel the candidate adds over the direct route, weighted by verticalWeight for the vertical component; candidates outside eligibleTaskTypes never appear.
- Nothing in this server changes data: declaring a policy is a REST operation of task-interleaving, and hints are issued and retired by the service itself.

Done means: you have named the station's live advice — the ACTIVE hint's ranked candidate tasks with their detours, or the fact that it holds none — checked it against the site's policy envelope (detour cap, TTL, eligible task types), and stated plainly that a hint is a suggestion, never an assignment.`

// registerPrompts adds the workflow prompt (the operator SOP of this
// context). Read-only by construction: a prompt returns text.
func (d Deps) registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "advise_next_task",
		Description: "Standard operating procedure for advising which open task a station should take next: read the station and the hint it holds, the full hint record, and the site's policy envelope — then interpret, without ever assigning.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "stationId",
			Description: "the station to advise, e.g. STN-07",
			Required:    true,
		}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		station := strings.TrimSpace(req.Params.Arguments["stationId"])
		if station == "" {
			station = "<stationId>"
		}
		return &mcp.GetPromptResult{
			Description: "How to advise a station's next task: read the station, its hint and the site policy, then interpret.",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: strings.ReplaceAll(adviseNextTaskSOP, "STATION", station)},
			}},
		}, nil
	})
}
