package pyxra

import "image"

// Point is a pixel coordinate.
type Point struct{ X, Y int }

// GetPx reads a pixel (no bounds check beyond the image's own).
func GetPx(m *image.NRGBA, x, y int) Color {
	i := y*m.Stride + x*4
	p := m.Pix[i : i+4 : i+4]
	if p[3] == 0 {
		return Transparent
	}
	return Color{R: p[0], G: p[1], B: p[2], A: p[3]}
}

// SetPx writes a pixel.
func SetPx(m *image.NRGBA, x, y int, c Color) {
	i := y*m.Stride + x*4
	p := m.Pix[i : i+4 : i+4]
	p[0], p[1], p[2], p[3] = c.R, c.G, c.B, c.A
}

// LinePoints is a Bresenham line, endpoints included.
func LinePoints(x0, y0, x1, y1 int) []Point {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	pts := make([]Point, 0, max(dx, -dy)+1)
	for {
		pts = append(pts, Point{x0, y0})
		if x0 == x1 && y0 == y1 {
			return pts
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func box(x0, y0, x1, y1 int) (int, int, int, int) {
	return min(x0, x1), min(y0, y1), max(x0, x1), max(y0, y1)
}

// RectPoints covers the box with corners (x0,y0) and (x1,y1).
func RectPoints(x0, y0, x1, y1 int, filled bool) []Point {
	l, t, r, b := box(x0, y0, x1, y1)
	var pts []Point
	for y := t; y <= b; y++ {
		for x := l; x <= r; x++ {
			if filled || y == t || y == b || x == l || x == r {
				pts = append(pts, Point{x, y})
			}
		}
	}
	return pts
}

// EllipsePoints covers the ellipse inscribed in the box; the outline is a clean 1 px edge.
func EllipsePoints(x0, y0, x1, y1 int, filled bool) []Point {
	l, t, r, b := box(x0, y0, x1, y1)
	w, h := r-l+1, b-t+1
	cx, cy := float64(w-1)/2, float64(h-1)/2
	rx, ry := float64(w)/2, float64(h)/2
	inside := func(x, y int) bool {
		if x < 0 || y < 0 || x >= w || y >= h {
			return false
		}
		nx, ny := (float64(x)-cx)/rx, (float64(y)-cy)/ry
		return nx*nx+ny*ny <= 1+1e-9
	}
	var pts []Point
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !inside(x, y) {
				continue
			}
			if filled || !(inside(x+1, y) && inside(x-1, y) && inside(x, y+1) && inside(x, y-1)) {
				pts = append(pts, Point{l + x, t + y})
			}
		}
	}
	return pts
}

// FloodPoints returns the 4-connected region of the colour at (x, y).
func FloodPoints(m *image.NRGBA, x, y int) []Point {
	b := m.Rect
	if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
		return nil
	}
	w, h := b.Dx(), b.Dy()
	target := GetPx(m, x, y)
	seen := make([]bool, w*h)
	stack := []Point{{x, y}}
	var out []Point
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if p.X < 0 || p.Y < 0 || p.X >= w || p.Y >= h || seen[p.Y*w+p.X] {
			continue
		}
		seen[p.Y*w+p.X] = true
		if GetPx(m, p.X, p.Y) != target {
			continue
		}
		out = append(out, p)
		stack = append(stack, Point{p.X + 1, p.Y}, Point{p.X - 1, p.Y}, Point{p.X, p.Y + 1}, Point{p.X, p.Y - 1})
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
