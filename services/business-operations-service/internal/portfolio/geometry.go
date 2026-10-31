package portfolio

import (
	"encoding/json"
	"math"
)

// Geometry stores bounded RFC 7946 WGS84 Polygon/MultiPolygon coordinates.
// This is topology validation only, not surveying, legal title or geodesic area.
// Antimeridian-spanning polygons require professional normalisation first.
type Geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

type point [2]float64

func orient(a, b, c point) float64 { return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]) }
func between(a, b, c point) bool {
	return math.Abs(orient(a, b, c)) < 1e-12 && c[0] >= math.Min(a[0], b[0]) && c[0] <= math.Max(a[0], b[0]) && c[1] >= math.Min(a[1], b[1]) && c[1] <= math.Max(a[1], b[1])
}
func intersects(a, b, c, d point) bool {
	x, y, z, w := orient(a, b, c), orient(a, b, d), orient(c, d, a), orient(c, d, b)
	return ((x > 0 && y < 0 || x < 0 && y > 0) && (z > 0 && w < 0 || z < 0 && w > 0)) || between(a, b, c) || between(a, b, d) || between(c, d, a) || between(c, d, b)
}
func contains(r []point, p point) bool {
	inside := false
	for i := 0; i < len(r)-1; i++ {
		a, b := r[i], r[i+1]
		if between(a, b, p) {
			return false
		}
		if (a[1] > p[1]) != (b[1] > p[1]) && p[0] < (b[0]-a[0])*(p[1]-a[1])/(b[1]-a[1])+a[0] {
			inside = !inside
		}
	}
	return inside
}
func cross(a, b []point) bool {
	for i := 0; i < len(a)-1; i++ {
		for j := 0; j < len(b)-1; j++ {
			if intersects(a[i], a[i+1], b[j], b[j+1]) {
				return true
			}
		}
	}
	return false
}
func (g Geometry) Validate() error {
	if len(g.Coordinates) > 128<<10 {
		return ErrInvalid
	}
	var polys [][][][]float64
	switch g.Type {
	case "Polygon":
		var p [][][]float64
		if json.Unmarshal(g.Coordinates, &p) != nil {
			return ErrInvalid
		}
		polys = append(polys, p)
	case "MultiPolygon":
		if json.Unmarshal(g.Coordinates, &polys) != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if len(polys) < 1 || len(polys) > 32 {
		return ErrInvalid
	}
	count := 0
	shells := [][]point{}
	for _, poly := range polys {
		if len(poly) < 1 || len(poly) > 32 {
			return ErrInvalid
		}
		rings := [][]point{}
		for _, coords := range poly {
			if len(coords) < 4 || len(coords) > 1024 {
				return ErrInvalid
			}
			count += len(coords)
			if count > 4096 {
				return ErrInvalid
			}
			ring := []point{}
			lo, hi := 180.0, -180.0
			for _, xy := range coords {
				if len(xy) != 2 || !finite(xy[0]) || !finite(xy[1]) || xy[0] < -180 || xy[0] > 180 || xy[1] < -90 || xy[1] > 90 {
					return ErrInvalid
				}
				p := point{xy[0], xy[1]}
				if len(ring) > 0 && ring[len(ring)-1] == p {
					return ErrInvalid
				}
				ring = append(ring, p)
				lo = math.Min(lo, p[0])
				hi = math.Max(hi, p[0])
			}
			if ring[0] != ring[len(ring)-1] || hi-lo > 180 {
				return ErrInvalid
			}
			area := 0.0
			for i := 0; i < len(ring)-1; i++ {
				area += ring[i][0]*ring[i+1][1] - ring[i+1][0]*ring[i][1]
				for j := i + 2; j < len(ring)-1; j++ {
					if i == 0 && j == len(ring)-2 {
						continue
					}
					if intersects(ring[i], ring[i+1], ring[j], ring[j+1]) {
						return ErrInvalid
					}
				}
			}
			if math.Abs(area) < 1e-12 {
				return ErrInvalid
			}
			rings = append(rings, ring)
		}
		for i := 1; i < len(rings); i++ {
			if !contains(rings[0], rings[i][0]) || cross(rings[0], rings[i]) {
				return ErrInvalid
			}
			for j := 1; j < i; j++ {
				if cross(rings[i], rings[j]) || contains(rings[i], rings[j][0]) || contains(rings[j], rings[i][0]) {
					return ErrInvalid
				}
			}
		}
		for _, other := range shells {
			if cross(other, rings[0]) || contains(other, rings[0][0]) || contains(rings[0], other[0]) {
				return ErrInvalid
			}
		}
		shells = append(shells, rings[0])
	}
	return nil
}
