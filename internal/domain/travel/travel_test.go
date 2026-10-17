package travel_test

import (
	"errors"
	"math"
	"testing"

	"github.com/claudioed/task-interleaving/internal/domain/travel"
)

func place(loc string, x, y, z float64) travel.Place {
	return travel.Place{Location: loc, Point: &travel.Point{X: x, Y: y, Z: z}}
}

func mustModel(t *testing.T, w float64) travel.Rectilinear {
	t.Helper()
	m, err := travel.NewRectilinear(w)
	if err != nil {
		t.Fatalf("NewRectilinear(%v): %v", w, err)
	}
	return m
}

func TestRectilinearNameAndInterface(t *testing.T) {
	var m travel.Model = mustModel(t, 1)
	if m.Name() != "rectilinear-v1" || travel.RectilinearV1 != "rectilinear-v1" {
		t.Fatalf("name %q", m.Name())
	}
}

func TestNewRectilinearWeight(t *testing.T) {
	cases := []struct {
		name string
		w    float64
		ok   bool
	}{
		{"zero", 0, true},
		{"one", 1, true},
		{"negative", -0.5, false},
		{"NaN", math.NaN(), false},
		{"+Inf", math.Inf(1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := travel.NewRectilinear(tc.w)
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok && !errors.Is(err, travel.ErrInvalidWeight) {
				t.Fatalf("want ErrInvalidWeight, got %v", err)
			}
		})
	}
}

// Every axis delta is distinct and non-zero, so a mutant that swaps a - for a +
// or drops a term changes the result.
func TestDistanceUsesEveryAxisWithItsWeight(t *testing.T) {
	m := mustModel(t, 2)
	from := place("A", 10, 20, 3)
	to := place("B", 4, 25, 1) // dx 6, dy 5, dz 2 -> 6 + 5 + 2*2 = 15
	got, ok := m.Distance(from, to)
	if !ok || got != 15 {
		t.Fatalf("got %v %v want 15", got, ok)
	}
}

func TestDistanceIsSymmetricAndZeroForTheSamePoint(t *testing.T) {
	m := mustModel(t, 1.5)
	a := place("A", 1, 2, 3)
	b := place("B", 8, 6, 0.5)
	ab, _ := m.Distance(a, b)
	ba, _ := m.Distance(b, a)
	if ab != ba {
		t.Fatalf("not symmetric: %v vs %v", ab, ba)
	}
	if d, ok := m.Distance(a, a); !ok || d != 0 {
		t.Fatalf("same point: got %v %v want 0 true", d, ok)
	}
}

func TestDistanceWithZeroVerticalWeightIgnoresHeight(t *testing.T) {
	m := mustModel(t, 0)
	d, ok := m.Distance(place("A", 0, 0, 0), place("B", 3, 4, 9))
	if !ok || d != 7 {
		t.Fatalf("got %v %v want 7", d, ok)
	}
}

func TestDistanceRoundsToTenthOfAMetre(t *testing.T) {
	cases := []struct {
		name string
		to   float64
		want float64
	}{
		{"rounds down", 1.24, 1.2},
		{"rounds up", 1.26, 1.3},
		{"already tenth", 1.3, 1.3},
		{"just below half rounds down", 2.449, 2.4},
		{"half rounds up", 2.5, 2.5},
	}
	m := mustModel(t, 1)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := m.Distance(place("A", 0, 0, 0), place("B", tc.to, 0, 0))
			if !ok || got != tc.want {
				t.Fatalf("got %v %v want %v", got, ok, tc.want)
			}
		})
	}
}

func TestRound(t *testing.T) {
	if travel.Round(0.04) != 0 || travel.Round(0.05) != 0.1 || travel.Round(12.34) != 12.3 {
		t.Fatalf("round: %v %v %v", travel.Round(0.04), travel.Round(0.05), travel.Round(12.34))
	}
}

func TestUnknownGeometryIsUnknownNeverZero(t *testing.T) {
	m := mustModel(t, 1)
	known := place("A", 1, 2, 3)
	cases := []struct {
		name     string
		from, to travel.Place
	}{
		{"origin has no point", travel.Place{Location: "X"}, known},
		{"target has no point", known, travel.Place{Location: "X"}},
		{"both unknown", travel.Place{Location: "X"}, travel.Place{Location: "Y"}},
		{"NaN x", place("N", math.NaN(), 0, 0), known},
		{"NaN y", place("N", 0, math.NaN(), 0), known},
		{"NaN z", place("N", 0, 0, math.NaN()), known},
		{"Inf x", known, place("I", math.Inf(1), 0, 0)},
		{"Inf y", known, place("I", 0, math.Inf(-1), 0)},
		{"Inf z", known, place("I", 0, 0, math.Inf(1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := m.Distance(tc.from, tc.to)
			if ok || d != 0 {
				t.Fatalf("want unknown (0, false), got %v %v", d, ok)
			}
		})
	}
}

func TestPlaceKnown(t *testing.T) {
	if !place("A", 0, 0, 0).Known() {
		t.Fatal("a point at the origin is known geometry")
	}
	if (travel.Place{Location: "A"}).Known() {
		t.Fatal("a place without a point is unknown")
	}
}
