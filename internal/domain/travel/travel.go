// Package travel holds the travel-distance port of the interleaving context and
// its v1 implementation, rectilinear-v1. It answers one question: how far is it,
// in metres, from where an operator stands to where a task starts. A place with
// no known geometry has an UNKNOWN distance, never a distance of zero.
package travel

import (
	"errors"
	"math"
)

// ErrInvalidWeight is returned for a negative or non-finite vertical weight.
var ErrInvalidWeight = errors.New("travel: vertical weight must be a finite number of at least 0")

// RectilinearV1 names the v1 travel model.
const RectilinearV1 = "rectilinear-v1"

// Point is a position in a site's local coordinate frame, in metres.
type Point struct {
	X, Y, Z float64
}

// Place is a location code with its geometry, when known.
type Place struct {
	Location string
	Point    *Point
}

// Known reports whether the place has usable geometry: a point whose
// coordinates are all finite.
func (p Place) Known() bool {
	if p.Point == nil {
		return false
	}
	if !finite(p.Point.X) || !finite(p.Point.Y) {
		return false
	}
	return finite(p.Point.Z)
}

// Model is the travel-model port: the distance in metres between two places,
// or false when either has no known geometry. A later model (aisle and
// cross-aisle routing) implements the same port.
type Model interface {
	Name() string
	Distance(from, to Place) (float64, bool)
}

// Rectilinear is the rectilinear-v1 model:
//
//	d = |x1-x2| + |y1-y2| + verticalWeight*|z1-z2|
//
// in metres, rounded to 0.1 m.
type Rectilinear struct {
	verticalWeight float64
}

// NewRectilinear builds the model; the vertical weight must be finite and at
// least 0.
func NewRectilinear(verticalWeight float64) (Rectilinear, error) {
	if !finite(verticalWeight) || verticalWeight < 0 {
		return Rectilinear{}, ErrInvalidWeight
	}
	return Rectilinear{verticalWeight: verticalWeight}, nil
}

// Name returns "rectilinear-v1".
func (Rectilinear) Name() string { return RectilinearV1 }

// Distance returns the rectilinear distance from one place to the other, in
// metres rounded to 0.1 m, or false when either place has no known geometry.
func (r Rectilinear) Distance(from, to Place) (float64, bool) {
	if !from.Known() || !to.Known() {
		return 0, false
	}
	d := math.Abs(from.Point.X-to.Point.X) +
		math.Abs(from.Point.Y-to.Point.Y) +
		r.verticalWeight*math.Abs(from.Point.Z-to.Point.Z)
	return Round(d), true
}

// Round rounds a distance to 0.1 m.
func Round(d float64) float64 {
	return math.Round(d*10) / 10
}

func finite(f float64) bool {
	if math.IsNaN(f) {
		return false
	}
	return !math.IsInf(f, 0)
}
