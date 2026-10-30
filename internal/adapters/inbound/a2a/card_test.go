package a2a

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

const testAgentURL = "http://task-interleaving-a2a.warehouse-systems.svc.cluster.local:8093/"

func testCard() *a2a.AgentCard { return Card(Config{AgentURL: testAgentURL, Version: "1.0.0"}) }

// TestCardShape pins the identity, interface and capability surface of the
// Agent Card: the discovery contract other contexts program against.
func TestCardShape(t *testing.T) {
	card := testCard()

	if card.Name != "task-interleaving" {
		t.Errorf("card name = %q, want task-interleaving", card.Name)
	}
	if len(card.SupportedInterfaces) != 1 {
		t.Fatalf("card has %d interfaces, want 1", len(card.SupportedInterfaces))
	}
	iface := card.SupportedInterfaces[0]
	if iface.URL != testAgentURL || iface.ProtocolBinding != a2a.TransportProtocolJSONRPC {
		t.Errorf("interface = %+v, want JSONRPC at the configured agent URL", iface)
	}
	if card.Capabilities.Streaming {
		t.Error("streaming must not be advertised: this is a message-only agent")
	}
	if card.SecuritySchemes != nil || card.SecurityRequirements != nil {
		t.Error("card must declare no security schemes: the surface is unauthenticated fleet-wide")
	}
	if card.Provider == nil || card.Provider.Org != "IQVO" {
		t.Errorf("provider = %+v, want IQVO", card.Provider)
	}
}

// TestCardSkillsAreCuratedAndTruthful pins the two-skill, READ-ONLY surface and
// proves the card never lies about its own contract: every advertised example
// must parse under the SAME strict decoder the executor uses, name its own
// skill, and carry arguments that skill accepts.
func TestCardSkillsAreCuratedAndTruthful(t *testing.T) {
	card := testCard()

	want := map[string]bool{skillStationHintStatus: true, skillSiteInterleavingOverview: true}
	if len(card.Skills) != len(want) {
		t.Fatalf("card has %d skills, want exactly %d (curated, goal-shaped, not one per endpoint)", len(card.Skills), len(want))
	}
	for _, s := range card.Skills {
		if !want[s.ID] {
			t.Errorf("unexpected skill %q on the card", s.ID)
			continue
		}
		if s.Name == "" || s.Description == "" || len(s.Examples) == 0 {
			t.Errorf("skill %q lacks a name, description or example", s.ID)
		}
		if !containsTag(s.Tags, "read") || containsTag(s.Tags, "write") {
			t.Errorf("skill %q must be tagged read and never write (the MCP charter is read-only), tags = %v", s.ID, s.Tags)
		}
		for _, ex := range s.Examples {
			var req skillRequest
			if err := decodeStrict([]byte(ex), &req); err != nil {
				t.Errorf("skill %q example is not valid under the strict decoder: %v", s.ID, err)
				continue
			}
			if req.Skill != s.ID {
				t.Errorf("skill %q example names skill %q", s.ID, req.Skill)
			}
			assertArgsAccepted(t, s.ID, req.Arguments)
		}
	}
}

func assertArgsAccepted(t *testing.T, skill string, raw json.RawMessage) {
	t.Helper()
	var err error
	switch skill {
	case skillStationHintStatus:
		err = decodeStrict(raw, &stationHintStatusArgs{})
	case skillSiteInterleavingOverview:
		err = decodeStrict(raw, &siteInterleavingOverviewArgs{})
	}
	if err != nil {
		t.Errorf("skill %q example arguments are rejected by its own strict decoder: %v", skill, err)
	}
}

func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// TestCardSerializable proves the card marshals to JSON a client can parse
// back, with the discovery fields intact.
func TestCardSerializable(t *testing.T) {
	raw, err := json.Marshal(testCard())
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	var back a2a.AgentCard
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("card JSON does not round-trip: %v", err)
	}
	if back.Name != "task-interleaving" || len(back.Skills) != 2 {
		t.Errorf("round-tripped card = %q with %d skills", back.Name, len(back.Skills))
	}
}

// TestCardPromisesNoWrite: the card must not promise a state change this
// read-only agent cannot make (write gate, docs/adr/0009): no skill id carries
// a write verb as a word (the same verbs the MCP governance test refuses).
func TestCardPromisesNoWrite(t *testing.T) {
	writeVerbs := map[string]bool{
		"declare": true, "issue": true, "consume": true, "expire": true, "supersede": true, "apply": true,
		"recommend": true, "create": true, "update": true, "delete": true, "set": true, "put": true,
		"post": true, "assign": true, "claim": true, "complete": true,
	}
	for _, s := range testCard().Skills {
		for _, word := range strings.Split(s.ID, "_") {
			if writeVerbs[word] {
				t.Errorf("skill %q carries the write verb %q, which the read-only MCP surface does not offer", s.ID, word)
			}
		}
	}
}
