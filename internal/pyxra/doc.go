// Package pyxra models a Pyxra pixel-art document: a canvas with layers
// (bottom to top), frames, tags, a palette and one PNG cel per layer and frame.
package pyxra

import (
	"encoding/json"
	"fmt"
	"image"
	"strconv"
	"strings"
)

// Extra holds JSON fields this package does not interpret; they are written back unchanged.
type Extra map[string]json.RawMessage

// MaxSide is the largest canvas side Pyxra accepts.
const MaxSide = 4096

type Layer struct {
	ID      int
	Name    string
	Visible bool
	Locked  bool
	Opacity int // 0-255
	Blend   string
	Extra   Extra
}

type Frame struct {
	ID       int
	Duration int // milliseconds
	Extra    Extra
}

// Tag names a run of frames; From and To are 0-based frame indexes, inclusive.
type Tag struct {
	Name     string
	From, To int
	Extra    Extra
}

// PaletteEntry is one swatch. Raw is kept for entries that were not plain strings.
type PaletteEntry struct {
	Color Color
	Raw   json.RawMessage
}

// LegendEntry maps an ASCII character to a colour (stored under the "pixra" key).
type LegendEntry struct {
	Char  byte
	Color Color
}

// CelKey identifies a cel by layer id and frame id, as in Pyxra's "k": "layer:frame".
type CelKey struct{ Layer, Frame int }

func (k CelKey) String() string { return strconv.Itoa(k.Layer) + ":" + strconv.Itoa(k.Frame) }

// Cel is one layer's image at one frame, always held at full canvas size.
type Cel struct {
	Img   *image.NRGBA
	src   string // the original PNG data URL; empty once the cel is modified
	x, y  int    // the original placement of src
	Extra Extra
}

// Modified reports whether the cel must be re-encoded when saved.
func (c *Cel) Modified() bool { return c.src == "" }

// MarkModified drops the original PNG so the cel is re-encoded on save.
func (c *Cel) MarkModified() { c.src = "" }

// CelState is a saved copy of a cel's pixels, for undo.
type CelState struct {
	img  *image.NRGBA
	src  string
	x, y int
}

func (c *Cel) Snapshot() CelState {
	return CelState{img: cloneImg(c.Img), src: c.src, x: c.x, y: c.y}
}

func (c *Cel) Restore(s CelState) {
	c.Img, c.src, c.x, c.y = s.img, s.src, s.x, s.y
}

func cloneImg(m *image.NRGBA) *image.NRGBA {
	n := &image.NRGBA{Pix: make([]byte, len(m.Pix)), Stride: m.Stride, Rect: m.Rect}
	copy(n.Pix, m.Pix)
	return n
}

// Doc is a Pyxra document.
type Doc struct {
	Name    string
	W, H    int
	Layers  []Layer // bottom to top
	Frames  []Frame
	Tags    []Tag
	Palette []PaletteEntry
	Cels    map[CelKey]*Cel
	Legend  []LegendEntry
	Extra   Extra // other top-level fields
	version json.RawMessage
}

func CheckSize(w, h int) error {
	if w < 1 || h < 1 || w > MaxSide || h > MaxSide {
		return fmt.Errorf("size must be 1..%d on each side (got %dx%d)", MaxSide, w, h)
	}
	return nil
}

// ParseSize reads "32x24" (or "32X24", "32*24", "32").
func ParseSize(s string) (int, int, error) {
	t := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "*", "x")
	parts := strings.Split(t, "x")
	var w, h int
	var err1, err2 error
	switch len(parts) {
	case 1:
		w, err1 = strconv.Atoi(parts[0])
		h = w
	case 2:
		w, err1 = strconv.Atoi(parts[0])
		h, err2 = strconv.Atoi(parts[1])
	default:
		err1 = fmt.Errorf("x")
	}
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("bad size %q (use WIDTHxHEIGHT, e.g. 32x24)", s)
	}
	return w, h, CheckSize(w, h)
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

func newLayerExtra() Extra {
	return Extra{"lockAlpha": rawJSON("false"), "label": rawJSON("null")}
}

// New makes an empty document with one layer and one frame.
func New(w, h int, name, layer string) (*Doc, error) {
	if err := CheckSize(w, h); err != nil {
		return nil, err
	}
	if layer == "" {
		layer = "Layer 1"
	}
	return &Doc{
		Name:    name,
		W:       w,
		H:       h,
		Layers:  []Layer{{ID: 1, Name: layer, Visible: true, Opacity: 255, Blend: "normal", Extra: newLayerExtra()}},
		Frames:  []Frame{{ID: 2, Duration: 100, Extra: Extra{}}},
		Cels:    map[CelKey]*Cel{},
		Extra:   Extra{"labels": rawJSON("[]"), "loadouts": rawJSON("[]"), "guides": rawJSON("[]")},
		version: rawJSON("1"),
	}, nil
}

func (d *Doc) nextID() int {
	n := 0
	for _, l := range d.Layers {
		if l.ID > n {
			n = l.ID
		}
	}
	for _, f := range d.Frames {
		if f.ID > n {
			n = f.ID
		}
	}
	return n + 1
}

func (d *Doc) newImg() *image.NRGBA { return image.NewNRGBA(image.Rect(0, 0, d.W, d.H)) }

func (d *Doc) key(li, fi int) CelKey { return CelKey{d.Layers[li].ID, d.Frames[fi].ID} }

// Cel returns the cel at layer and frame index, or nil if it is empty.
func (d *Doc) Cel(li, fi int) *Cel {
	if li < 0 || li >= len(d.Layers) || fi < 0 || fi >= len(d.Frames) {
		return nil
	}
	return d.Cels[d.key(li, fi)]
}

// EnsureCel returns the cel at layer and frame index, creating an empty one.
// The bool reports whether it was created.
func (d *Doc) EnsureCel(li, fi int) (*Cel, bool) {
	if c := d.Cel(li, fi); c != nil {
		return c, false
	}
	c := &Cel{Img: d.newImg(), Extra: Extra{}}
	d.Cels[d.key(li, fi)] = c
	return c, true
}

// At returns a layer's pixel (Transparent outside the canvas or in an empty cel).
func (d *Doc) At(li, fi, x, y int) Color {
	c := d.Cel(li, fi)
	if c == nil || x < 0 || y < 0 || x >= d.W || y >= d.H {
		return Transparent
	}
	return GetPx(c.Img, x, y)
}

// Composite blends the visible layers at a pixel. With solo >= 0 only that layer is drawn.
func (d *Doc) Composite(fi, x, y, solo int) Color {
	out := Transparent
	for li := range d.Layers {
		l := &d.Layers[li]
		if solo >= 0 {
			if li != solo {
				continue
			}
		} else if !l.Visible {
			continue
		}
		c := d.At(li, fi, x, y)
		if c.A == 0 {
			continue
		}
		if out.A == 0 && l.Opacity >= 255 && (l.Blend == "" || l.Blend == "normal") {
			out = c
			continue
		}
		out = Blend(out, c, l.Opacity, l.Blend)
	}
	return out
}

// Top returns the colour of the topmost visible, non-transparent layer pixel
// (no blending), and that layer's index or -1.
func (d *Doc) Top(fi, x, y, solo int) (Color, int) {
	for li := len(d.Layers) - 1; li >= 0; li-- {
		if solo >= 0 {
			if li != solo {
				continue
			}
		} else if !d.Layers[li].Visible {
			continue
		}
		if c := d.At(li, fi, x, y); c.A != 0 {
			return c, li
		}
	}
	return Transparent, -1
}

// ----- layers -------------------------------------------------------------------------

// FindLayer resolves a layer by name (exact, then case-insensitive), "#id",
// or 1-based position from the bottom.
func (d *Doc) FindLayer(ref string) (int, error) {
	for i, l := range d.Layers {
		if l.Name == ref {
			return i, nil
		}
	}
	for i, l := range d.Layers {
		if strings.EqualFold(l.Name, ref) {
			return i, nil
		}
	}
	if strings.HasPrefix(ref, "#") {
		if id, err := strconv.Atoi(ref[1:]); err == nil {
			for i, l := range d.Layers {
				if l.ID == id {
					return i, nil
				}
			}
		}
	}
	if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(d.Layers) {
		return n - 1, nil
	}
	return -1, fmt.Errorf("no layer %q", ref)
}

// AddLayer inserts a new empty layer at index at (0 = bottom) and returns its index.
func (d *Doc) AddLayer(name string, at int) int {
	if at < 0 {
		at = 0
	}
	if at > len(d.Layers) {
		at = len(d.Layers)
	}
	l := Layer{ID: d.nextID(), Name: name, Visible: true, Opacity: 255, Blend: "normal", Extra: newLayerExtra()}
	d.Layers = append(d.Layers, Layer{})
	copy(d.Layers[at+1:], d.Layers[at:])
	d.Layers[at] = l
	return at
}

func (d *Doc) DeleteLayer(li int) error {
	if len(d.Layers) <= 1 {
		return fmt.Errorf("a document needs at least one layer")
	}
	id := d.Layers[li].ID
	for k := range d.Cels {
		if k.Layer == id {
			delete(d.Cels, k)
		}
	}
	d.Layers = append(d.Layers[:li:li], d.Layers[li+1:]...)
	return nil
}

// MoveLayer moves the layer at li to index to.
func (d *Doc) MoveLayer(li, to int) {
	if to < 0 {
		to = 0
	}
	if to >= len(d.Layers) {
		to = len(d.Layers) - 1
	}
	l := d.Layers[li]
	rest := append(append([]Layer{}, d.Layers[:li]...), d.Layers[li+1:]...)
	d.Layers = append(append(append([]Layer{}, rest[:to]...), l), rest[to:]...)
}

// DuplicateLayer copies a layer (all frames) directly above it and returns the new index.
func (d *Doc) DuplicateLayer(li int, name string) int {
	src := d.Layers[li]
	ni := d.AddLayer(name, li+1)
	nl := &d.Layers[ni]
	nl.Visible, nl.Locked, nl.Opacity, nl.Blend = src.Visible, src.Locked, src.Opacity, src.Blend
	for fi := range d.Frames {
		if c := d.Cel(li, fi); c != nil {
			d.Cels[d.key(ni, fi)] = &Cel{Img: cloneImg(c.Img), Extra: Extra{}}
		}
	}
	return ni
}

// MergeDown blends layer li into the layer below it (every frame) and removes li.
func (d *Doc) MergeDown(li int) error {
	if li <= 0 {
		return fmt.Errorf("nothing below layer %q to merge into", d.Layers[li].Name)
	}
	top := d.Layers[li]
	for fi := range d.Frames {
		src := d.Cel(li, fi)
		if src == nil {
			continue
		}
		dst, _ := d.EnsureCel(li-1, fi)
		dst.MarkModified()
		for y := 0; y < d.H; y++ {
			for x := 0; x < d.W; x++ {
				s := GetPx(src.Img, x, y)
				if s.A == 0 {
					continue
				}
				SetPx(dst.Img, x, y, Norm(Blend(GetPx(dst.Img, x, y), s, top.Opacity, top.Blend)))
			}
		}
	}
	return d.DeleteLayer(li)
}

// ----- frames -------------------------------------------------------------------------

// AddFrame inserts a frame at index at, copying frame dupFrom's cels when dupFrom >= 0.
func (d *Doc) AddFrame(at, dupFrom int) int {
	if at < 0 {
		at = 0
	}
	if at > len(d.Frames) {
		at = len(d.Frames)
	}
	dur := 100
	if dupFrom >= 0 {
		dur = d.Frames[dupFrom].Duration
	}
	f := Frame{ID: d.nextID(), Duration: dur, Extra: Extra{}}
	var src []*Cel
	if dupFrom >= 0 {
		for li := range d.Layers {
			src = append(src, d.Cel(li, dupFrom))
		}
	}
	d.Frames = append(d.Frames, Frame{})
	copy(d.Frames[at+1:], d.Frames[at:])
	d.Frames[at] = f
	for li, c := range src {
		if c != nil {
			d.Cels[CelKey{d.Layers[li].ID, f.ID}] = &Cel{Img: cloneImg(c.Img), Extra: Extra{}}
		}
	}
	for i := range d.Tags {
		t := &d.Tags[i]
		if t.From >= at {
			t.From++
		}
		if t.To >= at {
			t.To++
		}
	}
	return at
}

func (d *Doc) DeleteFrame(fi int) error {
	if len(d.Frames) <= 1 {
		return fmt.Errorf("a document needs at least one frame")
	}
	id := d.Frames[fi].ID
	for k := range d.Cels {
		if k.Frame == id {
			delete(d.Cels, k)
		}
	}
	d.Frames = append(d.Frames[:fi:fi], d.Frames[fi+1:]...)
	tags := d.Tags[:0:0]
	for _, t := range d.Tags {
		if t.From == fi && t.To == fi {
			continue
		}
		if t.From > fi {
			t.From--
		}
		if t.To >= fi {
			t.To--
		}
		tags = append(tags, t)
	}
	d.Tags = tags
	return nil
}

// MoveFrame moves the frame at fi to index to (tags keep their indexes).
func (d *Doc) MoveFrame(fi, to int) {
	if to < 0 {
		to = 0
	}
	if to >= len(d.Frames) {
		to = len(d.Frames) - 1
	}
	f := d.Frames[fi]
	rest := append(append([]Frame{}, d.Frames[:fi]...), d.Frames[fi+1:]...)
	d.Frames = append(append(append([]Frame{}, rest[:to]...), f), rest[to:]...)
}

// FrameTags lists the tags that include frame fi.
func (d *Doc) FrameTags(fi int) []string {
	var out []string
	for _, t := range d.Tags {
		if fi >= t.From && fi <= t.To {
			out = append(out, t.Name)
		}
	}
	return out
}

// ----- canvas -------------------------------------------------------------------------

// Resize changes the canvas size; old content is placed at (ox, oy).
func (d *Doc) Resize(w, h, ox, oy int) error {
	if err := CheckSize(w, h); err != nil {
		return err
	}
	for k, c := range d.Cels {
		n := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < d.H; y++ {
			ny := y + oy
			if ny < 0 || ny >= h {
				continue
			}
			for x := 0; x < d.W; x++ {
				nx := x + ox
				if nx >= 0 && nx < w {
					SetPx(n, nx, ny, GetPx(c.Img, x, y))
				}
			}
		}
		d.Cels[k] = &Cel{Img: n, Extra: c.Extra}
	}
	d.W, d.H = w, h
	return nil
}

// UsedColors returns every distinct non-transparent colour, in layer then frame order.
func (d *Doc) UsedColors() []Color {
	seen := map[Color]bool{}
	var out []Color
	for li := range d.Layers {
		for fi := range d.Frames {
			c := d.Cel(li, fi)
			if c == nil {
				continue
			}
			p := c.Img.Pix
			var last Color
			for i := 0; i+3 < len(p); i += 4 {
				if p[i+3] == 0 {
					continue
				}
				col := Color{R: p[i], G: p[i+1], B: p[i+2], A: p[i+3]}
				if col == last {
					continue
				}
				last = col
				if !seen[col] {
					seen[col] = true
					out = append(out, col)
				}
			}
		}
	}
	return out
}

// Empty reports whether an image has no visible pixels.
func Empty(m *image.NRGBA) bool {
	for i := 3; i < len(m.Pix); i += 4 {
		if m.Pix[i] != 0 {
			return false
		}
	}
	return true
}
