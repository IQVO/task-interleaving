package repocontract

import (
	"testing"

	"github.com/claudioed/task-interleaving/internal/application/ports"
	"github.com/claudioed/task-interleaving/internal/application/repository"
	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

// TaskPool runs the TaskPool contract over an EMPTY pool.
func TaskPool(t *testing.T, newPool func(t *testing.T) ports.TaskPool) {
	t.Helper()
	cases := map[string]func(t *testing.T, p ports.TaskPool){
		"an unseen task is not found":                  poolUnseen,
		"save then get round trips every field":        poolRoundTrip,
		"a task without capabilities reads back empty": poolNoCapabilities,
		"save upserts and keeps one row per task":      poolUpsert,
		"the pool keeps its own copy of the slice":     poolCopiesSlice,
		"available lists one site's open tasks by id":  poolAvailable,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newPool(t)) })
	}
}

func task(id, site string, state repository.TaskState) repository.Task {
	return repository.Task{
		ID: id, Type: "PICK", SiteID: site, FromLocation: "LOC-F-" + id, ToLocation: "LOC-T-" + id,
		RequiredCapabilities: []string{"FORKLIFT", "COLD"}, PriorityBand: "band-1", State: state,
	}
}

func sameTask(a, b repository.Task) bool {
	if !equalSlice(a.RequiredCapabilities, b.RequiredCapabilities) {
		return false
	}
	a.RequiredCapabilities, b.RequiredCapabilities = nil, nil
	return a.ID == b.ID && a.Type == b.Type && a.SiteID == b.SiteID && a.FromLocation == b.FromLocation &&
		a.ToLocation == b.ToLocation && a.PriorityBand == b.PriorityBand && a.State == b.State && a.ClaimedBy == b.ClaimedBy
}

func poolUnseen(t *testing.T, p ports.TaskPool) {
	_, ok, err := p.Get(bg, "nope")
	must(t, err)
	if ok {
		t.Fatal("an unseen task was found")
	}
}

func poolRoundTrip(t *testing.T, p ports.TaskPool) {
	in := task("t1", "S1", repository.TaskClaimed)
	in.ClaimedBy = "STN-1"
	must(t, p.Save(bg, in))
	got, ok, err := p.Get(bg, "t1")
	must(t, err)
	if !ok || !sameTask(got, in) {
		t.Fatalf("got %+v %v, want %+v", got, ok, in)
	}
}

func poolNoCapabilities(t *testing.T, p ports.TaskPool) {
	in := repository.Task{ID: "t1", State: repository.TaskCompleted}
	must(t, p.Save(bg, in))
	got, ok, err := p.Get(bg, "t1")
	must(t, err)
	if !ok || len(got.RequiredCapabilities) != 0 || got.State != repository.TaskCompleted || got.SiteID != "" {
		t.Fatalf("got %+v %v", got, ok)
	}
}

func poolUpsert(t *testing.T, p ports.TaskPool) {
	must(t, p.Save(bg, task("t1", "S1", repository.TaskAvailable)))
	claimed := task("t1", "S1", repository.TaskClaimed)
	claimed.ClaimedBy = "STN-9"
	claimed.ToLocation = "LOC-OTHER"
	must(t, p.Save(bg, claimed))
	got, _, err := p.Get(bg, "t1")
	must(t, err)
	if !sameTask(got, claimed) {
		t.Fatalf("after upsert: %+v", got)
	}
	open, err := p.Available(bg, "S1")
	must(t, err)
	if len(open) != 0 {
		t.Fatalf("a claimed task is still available: %+v", open)
	}
}

func poolCopiesSlice(t *testing.T, p ports.TaskPool) {
	caps := []string{"FORKLIFT"}
	in := task("t1", "S1", repository.TaskAvailable)
	in.RequiredCapabilities = caps
	must(t, p.Save(bg, in))
	caps[0] = "mutated"
	got, _, err := p.Get(bg, "t1")
	must(t, err)
	if got.RequiredCapabilities[0] != "FORKLIFT" {
		t.Fatalf("the pool shares the caller's slice: %+v", got)
	}
}

func poolAvailable(t *testing.T, p ports.TaskPool) {
	for _, tk := range []repository.Task{
		task("t2", "S1", repository.TaskAvailable), task("t1", "S1", repository.TaskAvailable),
		task("t3", "S1", repository.TaskClaimed), task("t4", "S2", repository.TaskAvailable),
		task("t5", "S1", repository.TaskCompleted), task("T0", "S1", repository.TaskAvailable),
	} {
		must(t, p.Save(bg, tk))
	}
	open, err := p.Available(bg, "S1")
	must(t, err)
	var ids []string
	for _, tk := range open {
		ids = append(ids, tk.ID)
	}
	if want := []string{"T0", "t1", "t2"}; !equalSlice(ids, want) {
		t.Fatalf("available = %v, want %v (byte order)", ids, want)
	}
	if none, err := p.Available(bg, "S9"); err != nil || len(none) != 0 {
		t.Fatalf("unknown site = %v %v", none, err)
	}
}

// LocationBook runs the LocationBook contract over an EMPTY book.
func LocationBook(t *testing.T, newBook func(t *testing.T) ports.LocationBook) {
	t.Helper()
	cases := map[string]func(t *testing.T, b ports.LocationBook){
		"unseen codes are absent":                      bookUnseen,
		"geometry arrives before the registration":     bookGeometryFirst,
		"a location joins its zone's site":             bookJoinsZone,
		"a zone arriving later converges":              bookZoneLater,
		"geometry is replaced by the last writer":      bookGeometryReplaced,
		"a registration keeps the geometry":            bookRegistrationKeepsPoint,
		"decommission is for good and keeps the point": bookDecommission,
		"a never seen location can be decommissioned":  bookDecommissionUnseen,
		"no codes is no places":                        bookNoCodes,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newBook(t)) })
	}
}

func places(t *testing.T, b ports.LocationBook, codes ...string) map[string]repository.Place {
	t.Helper()
	got, err := b.Places(bg, codes)
	must(t, err)
	return got
}

func wantPoint(t *testing.T, p repository.Place, want travel.Point) {
	t.Helper()
	if p.Point == nil || *p.Point != want {
		t.Fatalf("point = %v, want %v", p.Point, want)
	}
}

func bookUnseen(t *testing.T, b ports.LocationBook) {
	if got := places(t, b, "LOC-1", "LOC-2"); len(got) != 0 {
		t.Fatalf("unseen = %+v", got)
	}
}

func bookGeometryFirst(t *testing.T, b ports.LocationBook) {
	must(t, b.SaveGeometry(bg, "LOC-1", 1, 2, 3))
	p := places(t, b, "LOC-1", "LOC-9")["LOC-1"]
	if p.Code != "LOC-1" || p.SiteCode != "" || p.Decommissioned {
		t.Fatalf("geometry only = %+v", p)
	}
	wantPoint(t, p, travel.Point{X: 1, Y: 2, Z: 3})
}

func bookJoinsZone(t *testing.T, b ports.LocationBook) {
	must(t, b.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"}))
	must(t, b.SaveLocation(bg, "LOC-1", "z1"))
	must(t, b.SaveLocation(bg, "LOC-2", "z1"))
	must(t, b.SaveGeometry(bg, "LOC-1", -1.5, 0, 4.25))
	got := places(t, b, "LOC-1", "LOC-2", "LOC-9")
	if len(got) != 2 {
		t.Fatalf("places = %+v", got)
	}
	if got["LOC-1"].SiteCode != "SITE-1" || got["LOC-1"].Decommissioned {
		t.Fatalf("LOC-1 = %+v", got["LOC-1"])
	}
	wantPoint(t, got["LOC-1"], travel.Point{X: -1.5, Y: 0, Z: 4.25})
	if got["LOC-2"].SiteCode != "SITE-1" || got["LOC-2"].Point != nil {
		t.Fatalf("LOC-2 (no geometry) = %+v", got["LOC-2"])
	}
}

func bookZoneLater(t *testing.T, b ports.LocationBook) {
	must(t, b.SaveLocation(bg, "LOC-1", "z1"))
	if got := places(t, b, "LOC-1")["LOC-1"]; got.SiteCode != "" {
		t.Fatalf("before the zone: %+v", got)
	}
	must(t, b.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "SITE-1", ZoneCode: "FWD"}))
	if got := places(t, b, "LOC-1")["LOC-1"]; got.SiteCode != "SITE-1" {
		t.Fatalf("after the zone: %+v", got)
	}
	must(t, b.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "SITE-2", ZoneCode: "FWD"}))
	if got := places(t, b, "LOC-1")["LOC-1"]; got.SiteCode != "SITE-2" {
		t.Fatalf("zone replaced: %+v", got)
	}
}

func bookGeometryReplaced(t *testing.T, b ports.LocationBook) {
	must(t, b.SaveGeometry(bg, "LOC-1", 1, 2, 3))
	must(t, b.SaveGeometry(bg, "LOC-1", 4, 5, 6))
	wantPoint(t, places(t, b, "LOC-1")["LOC-1"], travel.Point{X: 4, Y: 5, Z: 6})
}

func bookRegistrationKeepsPoint(t *testing.T, b ports.LocationBook) {
	must(t, b.SaveGeometry(bg, "LOC-1", 1, 2, 3))
	must(t, b.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "SITE-1"}))
	must(t, b.SaveLocation(bg, "LOC-1", "z1"))
	must(t, b.SaveLocation(bg, "LOC-1", "z1")) // a replayed registration
	p := places(t, b, "LOC-1")["LOC-1"]
	if p.SiteCode != "SITE-1" {
		t.Fatalf("site = %q", p.SiteCode)
	}
	wantPoint(t, p, travel.Point{X: 1, Y: 2, Z: 3})
}

func bookDecommission(t *testing.T, b ports.LocationBook) {
	must(t, b.SaveZone(bg, repository.Zone{ID: "z1", SiteCode: "SITE-1"}))
	must(t, b.SaveLocation(bg, "LOC-1", "z1"))
	must(t, b.SaveGeometry(bg, "LOC-1", 1, 2, 3))
	must(t, b.DecommissionLocation(bg, "LOC-1"))
	must(t, b.SaveLocation(bg, "LOC-1", "z1")) // a late registration cannot bring it back
	p := places(t, b, "LOC-1")["LOC-1"]
	if !p.Decommissioned || p.SiteCode != "SITE-1" {
		t.Fatalf("LOC-1 = %+v", p)
	}
	wantPoint(t, p, travel.Point{X: 1, Y: 2, Z: 3})
	must(t, b.SaveGeometry(bg, "LOC-1", 7, 8, 9)) // geometry still follows
	wantPoint(t, places(t, b, "LOC-1")["LOC-1"], travel.Point{X: 7, Y: 8, Z: 9})
}

func bookDecommissionUnseen(t *testing.T, b ports.LocationBook) {
	must(t, b.DecommissionLocation(bg, "LOC-X"))
	p, ok := places(t, b, "LOC-X")["LOC-X"]
	if !ok || !p.Decommissioned || p.Point != nil || p.SiteCode != "" {
		t.Fatalf("LOC-X = %+v %v", p, ok)
	}
	must(t, b.SaveLocation(bg, "LOC-X", "z1"))
	if !places(t, b, "LOC-X")["LOC-X"].Decommissioned {
		t.Fatal("a registration after the decommission revived the location")
	}
}

func bookNoCodes(t *testing.T, b ports.LocationBook) {
	must(t, b.SaveGeometry(bg, "LOC-1", 1, 2, 3))
	if got := places(t, b); len(got) != 0 {
		t.Fatalf("places = %+v", got)
	}
}
