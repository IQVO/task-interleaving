package kafka

// Event consumer contract tests (E1-E4) for this repo's two cross-context
// subscriptions, validated against PINNED byte-copies of the producers'
// apis/asyncapi.yaml (contracts/pinned.json; the gate in CI fails when a
// pin drifts from the producer's develop):
//
//   - warehouse.fulfillment.events (fulfillment-execution)
//   - warehouse.facility.events     (facility-layout)
//
// plus this repo's own spec self-pin (its analytics twin contract).
// Fleet asyncapi convention: a producer's OWN topics are documented under
// 'subscribe:' (channel perspective) — the tests read them that way.
// Golden fixtures are the producers' own examples where present; types the
// spec documents WITHOUT examples (locationSlotDecommissioned,
// locationGeometryUpdated) are covered by E1 only and named in the PR body
// as producer example-backfill pending. yaml.v3 only; no new deps.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"gopkg.in/yaml.v3"

	"github.com/claudioed/task-interleaving/internal/adapters/kafka/cloudevents"
)

const (
	pinnedFulfillmentSpec = "testdata/fulfillment-execution.asyncapi.yaml"
	pinnedLayoutSpec      = "testdata/facility-layout.asyncapi.yaml"
)

// --- minimal pinned-spec reader -------------------------------------------

type pinnedDoc struct {
	Channels map[string]struct {
		Subscribe *struct {
			Messages *struct {
				OneOf []struct {
					Ref string `yaml:"$ref"`
				} `yaml:"oneOf"`
			} `yaml:"message"`
		} `yaml:"subscribe"`
	} `yaml:"channels"`
	Components struct {
		Messages map[string]struct {
			Examples []struct {
				Name    string         `yaml:"name"`
				Payload map[string]any `yaml:"payload"`
			} `yaml:"examples"`
			Payload map[string]any `yaml:"payload"`
		} `yaml:"messages"`
	} `yaml:"components"`
}

func loadPinned(t *testing.T, file string) *pinnedDoc {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read pinned spec: %v", err)
	}
	var doc pinnedDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse pinned spec %s: %v", file, err)
	}
	return &doc
}

// messageKey resolves a channel's subscribe message $ref to its
// components.messages key.
func (d *pinnedDoc) messageKeys(t *testing.T, channel string) []string {
	t.Helper()
	ch, ok := d.Channels[channel]
	if !ok || ch.Subscribe == nil {
		t.Fatalf("pinned spec does not document channel %s as produced (subscribe:)", channel)
	}
	var keys []string
	oneOf := ch.Subscribe.Messages.OneOf
	if len(oneOf) == 0 {
		t.Fatalf("channel %s documents no message oneOf", channel)
	}
	for _, ref := range oneOf {
		keys = append(keys, strings.TrimPrefix(ref.Ref, "#/components/messages/"))
	}
	return keys
}

// documentedType returns the full CloudEvents type a message documents
// (its payload's type const), or "" when it declares none.
func (d *pinnedDoc) documentedType(msgKey string) string {
	m, ok := d.Components.Messages[msgKey]
	if !ok {
		return ""
	}
	return payloadTypeConst(m.Payload)
}

func payloadTypeConst(payload map[string]any) string {
	if props, ok := payload["properties"].(map[string]any); ok {
		if typ, ok := props["type"].(map[string]any); ok {
			if c, ok := typ["const"].(string); ok {
				return c
			}
		}
	}
	for _, b := range allOfBranches(payload) {
		if c := payloadTypeConst(b); c != "" {
			return c
		}
	}
	return ""
}

func allOfBranches(payload map[string]any) []map[string]any {
	raw, ok := payload["allOf"].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, b := range raw {
		if m, ok := b.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// --- E1: the pinned specs document every routed type on the subscribed channel

func TestPinnedAsyncAPICoversSubscriptions(t *testing.T) {
	for _, tc := range []struct {
		spec   string
		topic  string
		routed []string
	}{
		{pinnedFulfillmentSpec, FulfillmentTopic, []string{TypeTaskAvailable, TypeTaskClaimed, TypeTaskCompleted}},
		{pinnedLayoutSpec, LayoutTopic, []string{TypeZoneRegistered, TypeLocationSlotRegistered, TypeLocationSlotDecommissioned, TypeLocationGeometryUpdated}},
	} {
		doc := loadPinned(t, tc.spec)
		documented := map[string]bool{}
		for _, key := range doc.messageKeys(t, tc.topic) {
			if typ := doc.documentedType(key); typ != "" {
				documented[typ] = true
			}
		}
		for _, typ := range tc.routed {
			if !documented[typ] {
				t.Errorf("pinned spec does not document routed type %s on %s (documented: %v)", typ, tc.topic, documented)
			}
		}
	}
}

// --- E2 + E3 + E4 over the producers' golden examples -----------------------

// contractConsumer builds a Consumer whose handlers capture every routed
// event, so decode + full-type dispatch run through the REAL HandleMessage.
func contractConsumer(routed ...string) (*Consumer, *[]ce.Event) {
	seen := &[]ce.Event{}
	handlers := map[string]HandlerFunc{}
	for _, typ := range routed {
		handlers[typ] = func(ctx context.Context, e ce.Event) error {
			*seen = append(*seen, e)
			return nil
		}
	}
	return &Consumer{Name: "contract-test", Topic: FulfillmentTopic, Handlers: handlers}, seen
}

// TestGoldenEventsDecodeAndCarryTheEnvelope runs every golden example of
// every ROUTED type through the real decode+dispatch (E2) and holds each
// to the fleet CloudEvents 1.0 envelope bar (E3): specversion, the identity
// attributes, datacontenttype, and the producer's urn: dataschema.
func TestGoldenEventsDecodeAndCarryTheEnvelope(t *testing.T) {
	for _, tc := range []struct {
		spec   string
		topic  string
		routed []string
		urn    string
	}{
		{pinnedFulfillmentSpec, FulfillmentTopic,
			[]string{TypeTaskAvailable, TypeTaskClaimed, TypeTaskCompleted},
			"urn:warehouse:fulfillment-execution:events:"},
		{pinnedLayoutSpec, LayoutTopic,
			[]string{TypeZoneRegistered, TypeLocationSlotRegistered},
			"urn:warehouse:facility-layout:events:"},
	} {
		doc := loadPinned(t, tc.spec)
		routedSet := map[string]bool{}
		for _, typ := range tc.routed {
			routedSet[typ] = true
		}
		ran := 0
		for _, key := range doc.messageKeys(t, tc.topic) {
			if typ := doc.documentedType(key); !routedSet[typ] {
				continue
			}
			ran += runGoldenExamples(t, doc, key, tc.topic, tc.routed, tc.urn)
		}
		if ran == 0 {
			t.Fatalf("%s: no golden example exercised any routed type", tc.spec)
		}
	}
}

// runGoldenExamples drives one documented message's golden examples through
// the real decode+dispatch and the envelope bar; it returns how many ran.
func runGoldenExamples(t *testing.T, doc *pinnedDoc, key, topic string, routed []string, urn string) int {
	t.Helper()
	msg := doc.Components.Messages[key]
	if len(msg.Examples) == 0 {
		t.Logf("NOTE: documents %s WITHOUT examples — E2 pending producer example backfill", key)
		return 0
	}
	for _, ex := range msg.Examples {
		raw, err := json.Marshal(ex.Payload)
		if err != nil {
			t.Fatalf("marshal golden example %s/%s: %v", key, ex.Name, err)
		}
		c, seen := contractConsumer(routed...)
		c.Topic = topic
		if err := c.HandleMessage(context.Background(), raw); err != nil {
			t.Fatalf("%s/%s: handle golden example: %v", key, ex.Name, err)
		}
		if len(*seen) != 1 {
			t.Fatalf("%s/%s: dispatched %d events, want exactly 1", key, ex.Name, len(*seen))
		}
		assertFleetEnvelope(t, ex.Name, (*seen)[0], urn)
	}
	return len(msg.Examples)
}

// assertFleetEnvelope holds one golden event to the fleet bar.
func assertFleetEnvelope(t *testing.T, name string, e ce.Event, urnPrefix string) {
	t.Helper()
	if e.SpecVersion() != cloudevents.SpecVersion {
		t.Errorf("%s: specversion %q", name, e.SpecVersion())
	}
	if e.ID() == "" || e.Subject() == "" || e.Source() == "" || e.Time().IsZero() {
		t.Errorf("%s: id/source/subject/time must all be present", name)
	}
	if e.DataMediaType() != cloudevents.DataContentType {
		t.Errorf("%s: datacontenttype %q", name, e.DataMediaType())
	}
	if ds := e.DataSchema(); !strings.HasPrefix(ds, urnPrefix) {
		t.Errorf("%s: dataschema %q, want prefix %q", name, ds, urnPrefix)
	}
}

// TestUnknownTolerated (E4): unknown ADDITIVE data keys must not stop the
// dispatch; an unknown event type is skipped silently; a non-CloudEvent
// message is skipped, never an error.
func TestUnknownTolerated(t *testing.T) {
	doc := loadPinned(t, pinnedFulfillmentSpec)
	var golden map[string]any
	for _, key := range doc.messageKeys(t, FulfillmentTopic) {
		if doc.documentedType(key) == TypeTaskAvailable && len(doc.Components.Messages[key].Examples) > 0 {
			golden = doc.Components.Messages[key].Examples[0].Payload
			break
		}
	}
	if golden == nil {
		t.Fatal("no TaskAvailable golden example to perturb")
	}

	// additive data key
	tolerant := cloneMap(golden)
	data, _ := tolerant["data"].(map[string]any)
	data["future_additive_field"] = "unknown-to-this-consumer"
	raw, _ := json.Marshal(tolerant)
	c, seen := contractConsumer(TypeTaskAvailable)
	if err := c.HandleMessage(context.Background(), raw); err != nil {
		t.Fatalf("unknown data key must not fail: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("unknown data key lost the dispatch: %d events", len(*seen))
	}

	// unknown event type on the shared topic: skipped, no handler
	other := cloneMap(golden)
	other["type"] = "com.warehouse.wes.fulfillment-execution.task.TaskCancelled"
	raw, _ = json.Marshal(other)
	if err := c.HandleMessage(context.Background(), raw); err != nil {
		t.Fatalf("unknown type must be a silent skip: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("unknown type reached a handler: %d events", len(*seen))
	}

	// not a CloudEvent at all: skipped, never an error
	if err := c.HandleMessage(context.Background(), []byte(`{"load_id":"legacy"}`)); err != nil {
		t.Fatalf("non-CloudEvent must be a silent skip: %v", err)
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if inner, ok := v.(map[string]any); ok {
			out[k] = cloneMap(inner)
		} else {
			out[k] = v
		}
	}
	return out
}
