package kafka_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	kafkaconsumer "github.com/claudioed/task-interleaving/internal/adapters/inbound/kafka"
)

func TestLayout_ZonesLocationsAndGeometryFeedTheLayoutCopy(t *testing.T) {
	w := newWorld()
	c := w.layoutConsumer()
	handle(t, c, zoneMsg("e1", "z1", "S1"))
	handle(t, c, locationMsg("e2", "R-07", "z1"))
	handle(t, c, geometryMsg("e3", "R-07", 1, 2, 0))

	places, err := w.book.Places(bg, []string{"R-07"})
	place, known := places["R-07"]
	if err != nil || !known {
		t.Fatalf("Places = %+v, %v", places, err)
	}
	if place.SiteCode != "S1" || place.Decommissioned || place.Point == nil || place.Point.X != 1 || place.Point.Y != 2 || place.Point.Z != 0 {
		t.Fatalf("place = %+v, want an active location of S1 at (1, 2, 0)", place)
	}
}

// A geometry that arrives before its location still lands (the copies converge
// in any order).
func TestLayout_GeometryBeforeTheLocationConverges(t *testing.T) {
	w := newWorld()
	c := w.layoutConsumer()
	handle(t, c, geometryMsg("e1", "R-07", 5, 6, 1))
	handle(t, c, zoneMsg("e2", "z1", "S1"))
	handle(t, c, locationMsg("e3", "R-07", "z1"))

	places, _ := w.book.Places(bg, []string{"R-07"})
	if p := places["R-07"]; p.SiteCode != "S1" || p.Point == nil || p.Point.Z != 1 {
		t.Fatalf("place = %+v, want site S1 and the early geometry", p)
	}
}

func TestLayout_DecommissionRetiresTheLocation(t *testing.T) {
	w := newWorld()
	c := w.layoutConsumer()
	handle(t, c, zoneMsg("e1", "z1", "S1"))
	handle(t, c, locationMsg("e2", "R-07", "z1"))
	handle(t, c, decommissionedMsg("e3", "R-07"))
	handle(t, c, locationMsg("e4", "R-07", "z1")) // a later registration does not bring it back

	places, _ := w.book.Places(bg, []string{"R-07"})
	if !places["R-07"].Decommissioned {
		t.Fatalf("place = %+v, want decommissioned for good", places["R-07"])
	}
}

func TestLayout_DuplicateEventIDIsAppliedOnce(t *testing.T) {
	w := newWorld()
	c := w.layoutConsumer()
	handle(t, c, zoneMsg("e1", "z1", "S1"))
	handle(t, c, zoneMsg("e1", "z1", "S2")) // same id: the replay must not move the zone to another site
	handle(t, c, locationMsg("e2", "R-07", "z1"))

	places, _ := w.book.Places(bg, []string{"R-07"})
	if places["R-07"].SiteCode != "S1" {
		t.Fatalf("site = %q, want S1: the duplicate id must be skipped", places["R-07"].SiteCode)
	}
	if !strings.Contains(w.logs.String(), "outcome=duplicate") {
		t.Fatalf("the replay must be logged as a duplicate:\n%s", w.logs.String())
	}
}

func TestLayout_UnknownTypeIsIgnoredSilently(t *testing.T) {
	w := newWorld()
	updated := facilityMsg("e1", "com.warehouse.wms.facility-layout.zone.ZoneRenamed", "z1", `{"zoneId":"z1"}`)
	handle(t, w.layoutConsumer(), updated)
	if w.uow.count() != 0 || strings.Contains(w.logs.String(), "level=WARN") {
		t.Fatalf("an unknown type must be ignored silently (uow calls %d):\n%s", w.uow.count(), w.logs.String())
	}
}

func TestLayout_LegacyFlatMessageIsSkippedWithWarnAndCommittedPast(t *testing.T) {
	w := newWorld()
	legacy := `{"event_id":"e-1","event_type":"ZoneRegistered","occurred_at":"2026-10-17T09:00:00Z","zoneId":"z1","siteCode":"S1"}`
	reader := newFakeReader([]byte(legacy), zoneMsg("e1", "z1", "S1"), locationMsg("e2", "R-07", "z1"))
	if err := runUntilCommits(t, w.layoutConsumer(), reader, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if !strings.Contains(w.logs.String(), "level=WARN") || !strings.Contains(w.logs.String(), "not a valid CloudEvent") {
		t.Fatalf("the legacy flat message must be skipped with a WARN:\n%s", w.logs.String())
	}
	if places, _ := w.book.Places(bg, []string{"R-07"}); places["R-07"].SiteCode != "S1" {
		t.Fatalf("place = %+v: the messages after the legacy one must still be applied", places["R-07"])
	}
}

func TestLayout_PayloadsThatCanNeverBeAppliedAreDeadLettered(t *testing.T) {
	w := newWorld()
	noZone := facilityMsg("e1", typeLocation, "R-07", `{"locationCode":"R-07"}`)
	noCode := facilityMsg("e2", typeDecom, "x", `{}`)
	noSite := facilityMsg("e3", typeZone, "z-1", `{"zoneId":"z-1"}`)
	noY := facilityMsg("e4", typeGeometry, "R-07", `{"locationCode":"R-07","xM":1,"zM":0}`)
	noGeoCode := geometryMsg("e5", "", 1, 2, 3)
	notObject := facilityMsg("e6", typeGeometry, "R-07", `[1,2,3]`)
	reader := newFakeReader(noZone, noCode, noSite, noY, noGeoCode, notObject, zoneMsg("e7", "z1", "S1"))
	dlq := &fakeDLQWriter{}
	c := w.layoutConsumer()
	c.DLQ, c.DeadLetterInvalid = dlq, true

	if err := runUntilCommits(t, c, reader, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	got := dlq.published()
	if len(got) != 6 || got[0].Topic != kafkaconsumer.LayoutTopic+".dlq" {
		t.Fatalf("dead-lettered %d messages (%+v), want the six poison payloads on the .dlq topic", len(got), got)
	}
	if w.uow.count() != 1 {
		t.Fatalf("%d units of work: only the valid zone may open one", w.uow.count())
	}
}

func TestLayout_ConstructorBuildsTheConsumerWithoutDialling(t *testing.T) {
	w := newWorld()
	c := kafkaconsumer.NewLayoutConsumer([]string{"127.0.0.1:1"}, "group-from-env", w.layoutUseCases(), w.logger())
	assertConstructed(t, c, kafkaconsumer.LayoutTopic,
		kafkaconsumer.TypeZoneRegistered, kafkaconsumer.TypeLocationSlotRegistered,
		kafkaconsumer.TypeLocationSlotDecommissioned, kafkaconsumer.TypeLocationGeometryUpdated)
}
