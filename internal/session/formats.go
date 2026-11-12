package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rpmull/bf-interpreter/internal/pyxra"
)

// Kind is a file format, chosen by extension.
type Kind string

const (
	KindPyxra  Kind = "pyxra"  // .pyxra
	KindNative Kind = "native" // .pixra.json / .json
	KindOps    Kind = "ops"    // .ops.json
	KindPNG    Kind = "png"    // .png
	KindText   Kind = "text"   // .txt and anything else
)

func KindOf(path string) Kind {
	p := strings.ToLower(path)
	switch {
	case strings.HasSuffix(p, ".ops.json"):
		return KindOps
	case strings.HasSuffix(p, ".json"):
		return KindNative
	case strings.HasSuffix(p, ".pyxra"):
		return KindPyxra
	case strings.HasSuffix(p, ".png"):
		return KindPNG
	}
	return KindText
}

// BaseName strips the directory and known extensions.
func BaseName(path string) string {
	name := filepath.Base(path)
	for _, ext := range []string{".pixra.json", ".ops.json", ".json", ".pyxra", ".png", ".txt"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	if name == "" {
		return "untitled"
	}
	return name
}

// ----- opening ------------------------------------------------------------------------

// Open loads any supported file as a document. palette colours characters in plain text.
func Open(path, palette string) (*pyxra.Doc, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return Parse(data, KindOf(path), BaseName(path), palette)
}

// Parse reads document bytes of a given kind.
func Parse(data []byte, kind Kind, name, palette string) (*pyxra.Doc, []string, error) {
	switch kind {
	case KindPyxra:
		d, err := pyxra.Load(data)
		return d, nil, err
	case KindPNG:
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, nil, err
		}
		m := pyxra.ImageToCanvas(img)
		d, err := pyxra.New(m.Rect.Dx(), m.Rect.Dy(), name, name)
		if err != nil {
			return nil, nil, err
		}
		cel, _ := d.EnsureCel(0, 0)
		cel.Img = m
		return d, nil, nil
	case KindNative, KindOps:
		return parseJSON(data, name, palette)
	}
	return parseText(string(data), name, palette)
}

func parseText(text, name, palette string) (*pyxra.Doc, []string, error) {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\t", "    "), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil, nil, fmt.Errorf("no rows in text")
	}
	w := 0
	for _, l := range lines {
		w = max(w, len(l))
	}
	d, err := pyxra.New(max(w, 1), len(lines), name, "Layer 1")
	if err != nil {
		return nil, nil, err
	}
	legend := map[byte]Color{}
	if err := drawRows(d, 0, 0, 0, 0, lines, legend, palette, false); err != nil {
		return nil, nil, err
	}
	return d, nil, nil
}

// drawRows paints ASCII rows onto a layer at (x, y). Characters without a legend
// colour get one from the palette (and are recorded in legend and d.Legend).
// With erase, '.' clears pixels; otherwise '.' and spaces leave them alone.
func drawRows(d *pyxra.Doc, li, fi, x, y int, rows []string, legend map[byte]Color, palette string, erase bool) error {
	cel, _ := d.EnsureCel(li, fi)
	cel.MarkModified()
	for _, e := range d.Legend {
		if _, ok := legend[e.Char]; !ok {
			legend[e.Char] = e.Color
		}
	}
	next := 0
	colors := pyxra.Palettes[palette]
	if colors == nil {
		colors = pyxra.Palettes[pyxra.DefaultPalette]
	}
	for r, row := range rows {
		for i := 0; i < len(row); i++ {
			ch := row[i]
			px, py := x+i, y+r
			if px < 0 || py < 0 || px >= d.W || py >= d.H {
				continue
			}
			if ch == Empty || ch == ' ' {
				if erase && ch == Empty {
					pyxra.SetPx(cel.Img, px, py, pyxra.Transparent)
				}
				continue
			}
			if ch < 33 || ch > 126 {
				continue
			}
			c, ok := legend[ch]
			if !ok {
				used := map[Color]bool{}
				for _, v := range legend {
					used[v] = true
				}
				for next < len(colors) {
					cc, _ := pyxra.ParseColor(colors[next])
					next++
					if !used[cc] {
						c = cc
						break
					}
				}
				if c.A == 0 {
					c = hsv(float64(len(legend))*137.508, 0.65, 0.8)
				}
				legend[ch] = c
				d.Legend = append(d.Legend, pyxra.LegendEntry{Char: ch, Color: c})
			}
			pyxra.SetPx(cel.Img, px, py, c)
		}
	}
	return nil
}

// nativeWorld is the readable multi-layer Pixra format.
type nativeWorld struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	Name    string          `json:"name"`
	W       int             `json:"w"`
	H       int             `json:"h"`
	Legend  json.RawMessage `json:"legend"`
	Frames  []nativeFrame   `json:"frames"`
	Tags    []nativeTag     `json:"tags,omitempty"`
	Layers  []nativeLayer   `json:"layers"`
}

type nativeFrame struct {
	Duration int `json:"duration"`
}

type nativeTag struct {
	Name string `json:"name"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

type nativeLayer struct {
	Name    string     `json:"name"`
	Visible bool       `json:"visible"`
	Locked  bool       `json:"locked,omitempty"`
	Opacity int        `json:"opacity"`
	Blend   string     `json:"blend,omitempty"`
	Frames  [][]string `json:"frames"` // per frame: rows, or null when empty
}

func parseJSON(data []byte, name, palette string) (*pyxra.Doc, []string, error) {
	trim := bytes.TrimSpace(data)
	if len(trim) > 0 && trim[0] == '[' {
		return docFromOps(data, name)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, nil, err
	}
	var format string
	if raw, ok := probe["format"]; ok {
		_ = json.Unmarshal(raw, &format)
	}
	switch {
	case format == "pixra-world":
		return parseWorld(data, name, palette)
	case format == "pyxra":
		d, err := pyxra.Load(data)
		return d, nil, err
	case probe["ops"] != nil:
		return docFromOps(probe["ops"], name)
	case probe["rows"] != nil:
		return parseMap(probe, name, palette)
	}
	return nil, nil, fmt.Errorf("unrecognised JSON (expected a pixra world or map, Pyxra ops or a Pyxra document)")
}

func parseWorld(data []byte, name, palette string) (*pyxra.Doc, []string, error) {
	var w nativeWorld
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, nil, err
	}
	if w.Name == "" {
		w.Name = name
	}
	if len(w.Layers) == 0 {
		return nil, nil, fmt.Errorf("world has no layers")
	}
	d, err := pyxra.New(w.W, w.H, w.Name, w.Layers[0].Name)
	if err != nil {
		return nil, nil, err
	}
	if w.Legend != nil {
		if d.Legend, err = pyxra.ParseLegend(w.Legend); err != nil {
			return nil, nil, err
		}
	}
	nframes := max(1, len(w.Frames))
	for _, l := range w.Layers {
		nframes = max(nframes, len(l.Frames))
	}
	for i := 1; i < nframes; i++ {
		d.AddFrame(i, -1)
	}
	for i, f := range w.Frames {
		if f.Duration > 0 {
			d.Frames[i].Duration = f.Duration
		}
	}
	for _, t := range w.Tags {
		d.Tags = append(d.Tags, pyxra.Tag{Name: t.Name, From: t.From, To: t.To, Extra: pyxra.Extra{}})
	}
	legend := map[byte]Color{}
	for li, l := range w.Layers {
		if li > 0 {
			d.AddLayer(l.Name, li)
		}
		dl := &d.Layers[li]
		dl.Visible, dl.Locked, dl.Opacity = l.Visible, l.Locked, l.Opacity
		if l.Blend != "" {
			dl.Blend = l.Blend
		}
		for fi, rows := range l.Frames {
			if rows == nil {
				continue
			}
			if err := drawRows(d, li, fi, 0, 0, rows, legend, palette, false); err != nil {
				return nil, nil, err
			}
		}
	}
	return d, nil, nil
}

// parseMap reads the single-map format (also a bare Pyxra `pixels` op).
func parseMap(f map[string]json.RawMessage, name, palette string) (*pyxra.Doc, []string, error) {
	var m struct {
		Name   string   `json:"name"`
		Layer  string   `json:"layer"`
		Frame  int      `json:"frame"`
		X      int      `json:"x"`
		Y      int      `json:"y"`
		Rows   []string `json:"rows"`
		Legend json.RawMessage
	}
	raw, _ := json.Marshal(f)
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, err
	}
	if m.Name == "" {
		m.Name = name
	}
	if m.Layer == "" {
		m.Layer = "Layer 1"
	}
	w := 0
	for _, r := range m.Rows {
		w = max(w, len(r))
	}
	if len(m.Rows) == 0 || w == 0 {
		return nil, nil, fmt.Errorf("map has no rows")
	}
	d, err := pyxra.New(w+max(0, m.X), len(m.Rows)+max(0, m.Y), m.Name, m.Layer)
	if err != nil {
		return nil, nil, err
	}
	for len(d.Frames) < max(1, m.Frame) {
		d.AddFrame(len(d.Frames), -1)
	}
	legend := map[byte]Color{}
	if raw := f["legend"]; raw != nil {
		entries, err := pyxra.ParseLegend(raw)
		if err != nil {
			return nil, nil, err
		}
		for _, e := range entries {
			legend[e.Char] = e.Color
			if e.Color.A != 0 {
				d.Legend = append(d.Legend, e)
			}
		}
	}
	if err := drawRows(d, 0, max(1, m.Frame)-1, m.X, m.Y, m.Rows, legend, palette, false); err != nil {
		return nil, nil, err
	}
	return d, nil, nil
}

func docFromOps(data []byte, name string) (*pyxra.Doc, []string, error) {
	ops, err := decodeOps(data)
	if err != nil {
		return nil, nil, err
	}
	// Size the canvas to fit every op.
	w, h := 1, 1
	for _, op := range ops {
		x, y := opInt(op, "x", 0), opInt(op, "y", 0)
		if rows, ok := op["rows"].([]any); ok {
			rw := 0
			for _, r := range rows {
				if s, ok := r.(string); ok {
					rw = max(rw, len(s))
				}
			}
			w, h = max(w, x+rw), max(h, y+len(rows))
		}
		w, h = max(w, x+opInt(op, "w", 0)), max(h, y+opInt(op, "h", 0))
		w = max(w, max(opInt(op, "x0", 0), opInt(op, "x1", 0))+1)
		h = max(h, max(opInt(op, "y0", 0), opInt(op, "y1", 0))+1)
	}
	d, err := pyxra.New(min(w, pyxra.MaxSide), min(h, pyxra.MaxSide), name, "Layer 1")
	if err != nil {
		return nil, nil, err
	}
	s := New(d, "")
	if len(ops) == 0 {
		return nil, nil, fmt.Errorf("ops file has no ops")
	}
	if l, ok := ops[0]["layer"].(string); ok {
		d.Layers[0].Name = l // name the starting layer after the first op's layer
	}
	notes, err := s.applyOps(ops)
	if err != nil {
		return nil, nil, err
	}
	// Drop the starting layer if the ops built their own layers and left it empty.
	if len(d.Layers) > 1 && d.Layers[0].Name == "Layer 1" {
		empty := true
		for fi := range d.Frames {
			if d.Cel(0, fi) != nil {
				empty = false
			}
		}
		if empty {
			_ = d.DeleteLayer(0)
		}
	}
	s.SyncLegend()
	return d, notes, nil
}

// ----- Pyxra ops ----------------------------------------------------------------------

func decodeOps(data []byte) ([]map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if m, ok := v.(map[string]any); ok {
		if o, ok := m["ops"]; ok {
			v = o
		} else if _, ok := m["op"]; ok {
			v = []any{m}
		}
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("ops file must be a list of ops")
	}
	var ops []map[string]any
	for _, o := range list {
		if m, ok := o.(map[string]any); ok {
			ops = append(ops, m)
		}
	}
	return ops, nil
}

func opInt(op map[string]any, key string, def int) int {
	switch v := op[key].(type) {
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return int(f)
		}
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func opBool(op map[string]any, key string, def bool) bool {
	if b, ok := op[key].(bool); ok {
		return b
	}
	return def
}

func opColor(op map[string]any, key string) (Color, error) {
	v, ok := op[key]
	if !ok || v == nil {
		return pyxra.Transparent, nil
	}
	s, ok := v.(string)
	if !ok {
		return pyxra.Transparent, fmt.Errorf("%s must be a colour string", key)
	}
	return pyxra.ParseColor(s)
}

// opLayer finds (or, when create is set, adds) the layer an op names; default current.
func (s *Session) opLayer(op map[string]any, create bool) (int, error) {
	v, ok := op["layer"]
	if !ok {
		return s.Layer, nil
	}
	ref := fmt.Sprint(v)
	li, err := s.Doc.FindLayer(ref)
	if err != nil && create {
		li = s.Doc.AddLayer(ref, len(s.Doc.Layers))
		s.structural()
		return li, nil
	}
	return li, err
}

// opFrames reads "frame" or "frames" (number, list, "all" or "a-b"), 1-based; default current.
func (s *Session) opFrames(op map[string]any) ([]int, error) {
	v, ok := op["frames"]
	if !ok {
		v, ok = op["frame"]
	}
	if !ok {
		return []int{s.Frame}, nil
	}
	var nums []int
	add := func(n int) {
		for n > len(s.Doc.Frames) {
			s.Doc.AddFrame(len(s.Doc.Frames), -1)
			s.structural()
		}
		if n >= 1 {
			nums = append(nums, n-1)
		}
	}
	switch t := v.(type) {
	case json.Number:
		n, _ := t.Int64()
		add(int(n))
	case []any:
		for _, e := range t {
			if n, ok := e.(json.Number); ok {
				i, _ := n.Int64()
				add(int(i))
			}
		}
	case string:
		if t == "all" {
			for i := range s.Doc.Frames {
				nums = append(nums, i)
			}
			break
		}
		a, b, found := strings.Cut(t, "-")
		x, err1 := strconv.Atoi(a)
		y := x
		var err2 error
		if found {
			y, err2 = strconv.Atoi(b)
		}
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad frames %q", t)
		}
		for i := x; i <= y; i++ {
			add(i)
		}
	default:
		return nil, fmt.Errorf("bad frames value %v", v)
	}
	return nums, nil
}

// applyOps runs Pyxra `edit` ops against the document. Unknown ops are skipped with a note.
func (s *Session) applyOps(ops []map[string]any) ([]string, error) {
	var notes []string
	d := s.Doc
	for i, op := range ops {
		name, _ := op["op"].(string)
		fail := func(err error) error { return fmt.Errorf("op %d (%s): %v", i+1, name, err) }
		switch name {
		case "add_layer":
			lname, _ := op["name"].(string)
			at := len(d.Layers)
			if ref, ok := op["above"]; ok {
				li, err := d.FindLayer(fmt.Sprint(ref))
				if err != nil {
					return notes, fail(err)
				}
				at = li + 1
			} else if ref, ok := op["below"]; ok {
				li, err := d.FindLayer(fmt.Sprint(ref))
				if err != nil {
					return notes, fail(err)
				}
				at = li
			}
			s.Layer = d.AddLayer(lname, at)
			s.structural()
			continue
		case "delete_layer", "set_layer", "duplicate_layer", "move_layer":
			li, err := s.opLayer(op, false)
			if err != nil {
				return notes, fail(err)
			}
			switch name {
			case "delete_layer":
				err = d.DeleteLayer(li)
			case "duplicate_layer":
				n, _ := op["name"].(string)
				if n == "" {
					n = d.Layers[li].Name + " copy"
				}
				d.DuplicateLayer(li, n)
			case "move_layer":
				to := li
				if ref, ok := op["above"]; ok {
					if t, e := d.FindLayer(fmt.Sprint(ref)); e == nil {
						to = t
						if t < li {
							to = t + 1
						}
					}
				} else if ref, ok := op["below"]; ok {
					if t, e := d.FindLayer(fmt.Sprint(ref)); e == nil {
						to = t
						if t > li {
							to = t - 1
						}
					}
				} else {
					to = opInt(op, "to", li)
				}
				d.MoveLayer(li, to)
			case "set_layer":
				l := &d.Layers[li]
				if v, ok := op["name"].(string); ok {
					l.Name = v
				}
				l.Visible = opBool(op, "visible", l.Visible)
				l.Locked = opBool(op, "locked", l.Locked)
				l.Opacity = opInt(op, "opacity", l.Opacity)
				if v, ok := op["blend"].(string); ok {
					l.Blend = v
				}
			}
			if err != nil {
				return notes, fail(err)
			}
			s.structural()
			s.clampState()
			continue
		case "recolor":
			m, _ := op["map"].(map[string]any)
			for from, to := range m {
				fc, err1 := pyxra.ParseColor(from)
				tc, err2 := pyxra.ParseColor(fmt.Sprint(to))
				if err := firstErr(err1, err2); err != nil {
					return notes, fail(err)
				}
				s.recolorAll(fc, tc, -1)
			}
			continue
		case "pixels", "rect", "line", "ellipse", "fill", "clear":
		default:
			notes = append(notes, fmt.Sprintf("skipped op %d (%q)", i+1, name))
			continue
		}
		li, err := s.opLayer(op, name == "pixels")
		if err != nil {
			return notes, fail(err)
		}
		frames, err := s.opFrames(op)
		if err != nil {
			return notes, fail(err)
		}
		for _, fi := range frames {
			if err := s.applyDrawOp(name, op, li, fi); err != nil {
				return notes, fail(err)
			}
		}
	}
	return notes, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func (s *Session) applyDrawOp(name string, op map[string]any, li, fi int) error {
	d := s.Doc
	x, y := opInt(op, "x", 0), opInt(op, "y", 0)
	switch name {
	case "pixels":
		if opBool(op, "clear", false) {
			if err := s.clearCel(li, fi, nil); err != nil {
				return err
			}
		}
		legend := map[byte]Color{}
		if raw, ok := op["legend"].(map[string]any); ok {
			for k, v := range raw {
				if len(k) != 1 {
					return fmt.Errorf("legend key %q must be one character", k)
				}
				c := pyxra.Transparent
				if v != nil {
					var err error
					if c, err = pyxra.ParseColor(fmt.Sprint(v)); err != nil {
						return err
					}
				}
				legend[k[0]] = c
				if c.A != 0 && ValidChar(k[0]) {
					s.bind(k[0], c) // keep the op's own character when it is free
				}
				s.charFor(c)
			}
		}
		rows, _ := op["rows"].([]any)
		byColor := map[Color][]pyxra.Point{}
		for r, rv := range rows {
			row, _ := rv.(string)
			for i := 0; i < len(row); i++ {
				if c, ok := legend[row[i]]; ok {
					byColor[c] = append(byColor[c], pyxra.Point{X: x + i, Y: y + r})
				}
			}
		}
		for c, pts := range byColor {
			if _, err := s.PaintOn(li, fi, pts, c); err != nil {
				return err
			}
		}
		return nil
	case "clear":
		var region *image.Rectangle
		if r, ok := op["region"].(map[string]any); ok {
			rr := image.Rect(opInt(r, "x", 0), opInt(r, "y", 0), opInt(r, "x", 0)+opInt(r, "w", d.W), opInt(r, "y", 0)+opInt(r, "h", d.H))
			region = &rr
		}
		return s.clearCel(li, fi, region)
	}
	c, err := opColor(op, "color")
	if err != nil {
		return err
	}
	s.charFor(c)
	var pts []pyxra.Point
	switch name {
	case "rect":
		pts = pyxra.RectPoints(x, y, x+opInt(op, "w", 1)-1, y+opInt(op, "h", 1)-1, opBool(op, "fill", true))
	case "ellipse":
		pts = pyxra.EllipsePoints(x, y, x+opInt(op, "w", 1)-1, y+opInt(op, "h", 1)-1, opBool(op, "fill", true))
	case "line":
		pts = pyxra.LinePoints(opInt(op, "x0", 0), opInt(op, "y0", 0), opInt(op, "x1", 0), opInt(op, "y1", 0))
	case "fill":
		cel := d.Cel(li, fi)
		if cel == nil {
			cel = &pyxra.Cel{Img: image.NewNRGBA(image.Rect(0, 0, d.W, d.H))}
		}
		pts = pyxra.FloodPoints(cel.Img, x, y)
	}
	_, err = s.PaintOn(li, fi, pts, c)
	return err
}

func (s *Session) clearCel(li, fi int, region *image.Rectangle) error {
	cel := s.Doc.Cel(li, fi)
	if cel == nil {
		return nil
	}
	if err := s.checkLayer(li); err != nil {
		return err
	}
	r := cel.Img.Rect
	if region != nil {
		r = r.Intersect(*region)
	}
	var pts []pyxra.Point
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			pts = append(pts, pyxra.Point{X: x, Y: y})
		}
	}
	_, err := s.PaintOn(li, fi, pts, pyxra.Transparent)
	return err
}

// recolorAll swaps a colour on one layer (or every layer when li < 0), every frame.
func (s *Session) recolorAll(from, to Color, li int) int {
	from, to = pyxra.Norm(from), pyxra.Norm(to)
	n := 0
	for l := range s.Doc.Layers {
		if li >= 0 && l != li || s.Doc.Layers[l].Locked {
			continue
		}
		for f := range s.Doc.Frames {
			cel := s.Doc.Cel(l, f)
			if cel == nil {
				continue
			}
			var pts []pyxra.Point
			for y := 0; y < s.Doc.H; y++ {
				for x := 0; x < s.Doc.W; x++ {
					if pyxra.GetPx(cel.Img, x, y) == from {
						pts = append(pts, pyxra.Point{X: x, Y: y})
					}
				}
			}
			k, _ := s.PaintOn(l, f, pts, to)
			n += k
		}
	}
	s.charFor(to)
	return n
}

// ----- writing ------------------------------------------------------------------------

// ExportOptions select what an export writes.
type ExportOptions struct {
	Scale     int  // PNG upscale
	Layer     int  // -1: composite (PNG/text) or current layer (ops)
	AllLayers bool // ops: every layer
	AllFrames bool // ops: every frame; PNG: a horizontal sheet of every frame
	Clear     bool // ops: clear each cel first
	AddLayers bool // ops: start with add_layer ops
	Frame     int  // frame index
}

// Write saves the document (or part of it) in the format the path implies.
func (s *Session) Write(path string, o ExportOptions) (Kind, error) {
	if o.Scale < 1 {
		o.Scale = 1
	}
	kind := KindOf(path)
	var data []byte
	var err error
	switch kind {
	case KindPyxra:
		s.SyncLegend()
		data, err = s.Doc.Marshal()
	case KindNative:
		data, err = s.worldJSON()
	case KindOps:
		data, err = s.opsJSON(o)
	case KindPNG:
		data, err = s.pngBytes(o)
	default:
		data = []byte(strings.Join(s.Rows(o.Frame, o.Layer), "\n") + "\n")
	}
	if err != nil {
		return kind, err
	}
	return kind, writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Rows renders a frame as ASCII: one layer, or (li < 0) the topmost visible character.
func (s *Session) Rows(fi, li int) []string {
	d := s.Doc
	rows := make([]string, d.H)
	buf := make([]byte, d.W)
	for y := 0; y < d.H; y++ {
		for x := 0; x < d.W; x++ {
			var c Color
			if li >= 0 {
				c = d.At(li, fi, x, y)
			} else {
				c, _ = d.Top(fi, x, y, -1)
			}
			buf[x] = s.CharOf(c)
		}
		rows[y] = string(buf)
	}
	return rows
}

func (s *Session) worldJSON() ([]byte, error) {
	d := s.Doc
	w := nativeWorld{Format: "pixra-world", Version: 2, Name: d.Name, W: d.W, H: d.H,
		Legend: pyxra.MarshalLegend(s.Legend())}
	for _, f := range d.Frames {
		w.Frames = append(w.Frames, nativeFrame{Duration: f.Duration})
	}
	for _, t := range d.Tags {
		w.Tags = append(w.Tags, nativeTag{t.Name, t.From, t.To})
	}
	for li, l := range d.Layers {
		nl := nativeLayer{Name: l.Name, Visible: l.Visible, Locked: l.Locked, Opacity: l.Opacity, Blend: l.Blend}
		for fi := range d.Frames {
			c := d.Cel(li, fi)
			if c == nil || pyxra.Empty(c.Img) {
				nl.Frames = append(nl.Frames, nil)
				continue
			}
			rows := s.Rows(fi, li)
			for _, r := range rows {
				if strings.IndexByte(r, '?') >= 0 {
					return nil, fmt.Errorf("layer %q uses more colours than the %d ASCII characters available; save as .pyxra", l.Name, len(CharPool))
				}
			}
			nl.Frames = append(nl.Frames, rows)
		}
		w.Layers = append(w.Layers, nl)
	}
	return json.MarshalIndent(w, "", "  ")
}

func (s *Session) opsJSON(o ExportOptions) ([]byte, error) {
	d := s.Doc
	layers := []int{s.Layer}
	if o.Layer >= 0 {
		layers = []int{o.Layer}
	}
	if o.AllLayers {
		layers = layers[:0]
		for i := range d.Layers {
			layers = append(layers, i)
		}
	}
	frames := []int{o.Frame}
	if o.AllFrames {
		frames = frames[:0]
		for i := range d.Frames {
			frames = append(frames, i)
		}
	}
	var ops []any
	if o.AddLayers {
		for _, li := range layers {
			ops = append(ops, map[string]any{"op": "add_layer", "name": d.Layers[li].Name})
		}
	}
	for _, li := range layers {
		for _, fi := range frames {
			if d.Cel(li, fi) == nil && !o.Clear {
				continue
			}
			rows := s.Rows(fi, li)
			used := map[byte]bool{}
			for _, r := range rows {
				for i := 0; i < len(r); i++ {
					used[r[i]] = true
				}
			}
			if used['?'] {
				return nil, fmt.Errorf("layer %q uses more colours than the ASCII legend can hold", d.Layers[li].Name)
			}
			var legend []pyxra.LegendEntry
			for _, e := range s.Legend() {
				if used[e.Char] {
					legend = append(legend, e)
				}
			}
			op := map[string]any{"op": "pixels", "layer": d.Layers[li].Name, "frame": fi + 1,
				"x": 0, "y": 0, "legend": pyxra.MarshalLegend(legend), "rows": rows}
			if o.Clear {
				op["clear"] = true
			}
			ops = append(ops, op)
		}
	}
	if ops == nil {
		ops = []any{}
	}
	return json.MarshalIndent(ops, "", "  ")
}

func (s *Session) pngBytes(o ExportOptions) ([]byte, error) {
	d := s.Doc
	frames := []int{o.Frame}
	if o.AllFrames {
		frames = frames[:0]
		for i := range d.Frames {
			frames = append(frames, i)
		}
	}
	sc := o.Scale
	if d.W*sc*len(frames) > 1<<15 || d.H*sc > 1<<15 {
		return nil, fmt.Errorf("image would be too large; lower --scale")
	}
	img := image.NewNRGBA(image.Rect(0, 0, d.W*sc*len(frames), d.H*sc))
	for n, fi := range frames {
		for y := 0; y < d.H; y++ {
			for x := 0; x < d.W; x++ {
				var c Color
				if o.Layer >= 0 {
					c = d.At(o.Layer, fi, x, y)
				} else {
					c = d.Composite(fi, x, y, -1)
				}
				if c.A == 0 {
					continue
				}
				for dy := 0; dy < sc; dy++ {
					for dx := 0; dx < sc; dx++ {
						pyxra.SetPx(img, (n*d.W+x)*sc+dx, y*sc+dy, c)
					}
				}
			}
		}
	}
	return pyxra.EncodePNG(img)
}

// ----- importing ----------------------------------------------------------------------

// Import adds the layers of another file as new layers above the current one,
// placed at (ox, oy). Returns the names of the added layers.
func (s *Session) Import(path string, ox, oy int, rename string) ([]string, error) {
	src, _, err := Open(path, s.PaletteName)
	if err != nil {
		return nil, err
	}
	d := s.Doc
	srcSession := New(src, "")
	for len(d.Frames) < len(src.Frames) {
		d.AddFrame(len(d.Frames), -1)
	}
	var names []string
	at := s.Layer + 1
	for sli, sl := range src.Layers {
		name := sl.Name
		if rename != "" {
			name = rename
			if len(src.Layers) > 1 {
				name = fmt.Sprintf("%s %d", rename, sli+1)
			}
		}
		name = s.uniqueLayerName(name)
		li := d.AddLayer(name, at)
		at++
		l := &d.Layers[li]
		l.Visible, l.Opacity, l.Blend = sl.Visible, sl.Opacity, sl.Blend
		for fi := range src.Frames {
			sc := src.Cel(sli, fi)
			if sc == nil {
				continue
			}
			cel, _ := d.EnsureCel(li, fi)
			cel.MarkModified()
			for y := 0; y < src.H; y++ {
				for x := 0; x < src.W; x++ {
					c := pyxra.GetPx(sc.Img, x, y)
					if c.A == 0 {
						continue
					}
					if dx, dy := x+ox, y+oy; dx >= 0 && dy >= 0 && dx < d.W && dy < d.H {
						pyxra.SetPx(cel.Img, dx, dy, c)
					}
				}
			}
		}
		names = append(names, name)
		s.Layer = li
	}
	// Bring the source's characters across where they are free.
	for _, e := range srcSession.Legend() {
		if _, used := s.legendColor[e.Char]; !used {
			s.bind(e.Char, e.Color)
		}
	}
	for _, c := range src.UsedColors() {
		s.charFor(c)
	}
	s.structural()
	return names, nil
}

func (s *Session) uniqueLayerName(name string) string {
	if _, err := s.Doc.FindLayer(name); err != nil {
		return name
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s %d", name, i)
		if _, err := s.Doc.FindLayer(n); err != nil {
			return n
		}
	}
}

// ApplyFile runs a Pyxra ops file against the document.
func (s *Session) ApplyFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ops, err := decodeOps(data)
	if err != nil {
		return nil, err
	}
	return s.applyOps(ops)
}
