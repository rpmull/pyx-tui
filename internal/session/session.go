// Package session holds an open document with its ASCII legend, cursor, brush
// and undo history, and implements every editor command.
package session

import (
	"fmt"
	"strings"

	"github.com/rpmull/pyx-tui/internal/pyxra"
)

type Color = pyxra.Color

// Empty is the character for a transparent pixel.
const Empty = '.'

// CharPool is the order in which characters are handed to new colours.
const CharPool = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789" +
	"#@%&*+=-:;!?$~^<>/\\|()[]{}_,'\"`"

// ValidChar reports whether ch can stand for a colour: printable, non-space ASCII other than '.'.
func ValidChar(ch byte) bool { return ch >= 33 && ch <= 126 && ch != Empty }

// View modes.
const (
	ViewColor = "color"
	ViewMixed = "mixed"
	ViewText  = "text"
)

// Zoom levels run from MinZoom (1/8: one cell shows 8 pixels across) to MaxZoom (8 cells per pixel).
const (
	MinZoom = -3
	MaxZoom = 3
)

type Session struct {
	Doc   *pyxra.Doc
	Path  string // where `save` writes; empty for an unsaved document
	Dirty bool

	Layer, Frame int // current layer and frame index
	CX, CY       int // cursor
	Brush        byte
	AutoLegend   bool
	PaletteName  string

	Zoom int
	View string
	Solo bool
	Quit bool

	legendColor map[byte]Color
	legendChar  map[Color]byte
	legendOrder []byte

	undo, redo []*step
	cur        *step
	depth      int
	mutated    bool

	out strings.Builder
}

// New wraps a document in a session.
func New(d *pyxra.Doc, path string) *Session {
	s := &Session{AutoLegend: true, PaletteName: pyxra.DefaultPalette, Zoom: 1, View: ViewColor}
	s.SetDoc(d, path)
	return s
}

// SetDoc replaces the open document and resets history.
func (s *Session) SetDoc(d *pyxra.Doc, path string) {
	s.Doc, s.Path, s.Dirty = d, path, false
	s.Layer, s.Frame = len(d.Layers)-1, 0
	s.CX, s.CY = 0, 0
	s.undo, s.redo = nil, nil
	s.rebuildLegend()
	s.Brush = '#'
	for _, ch := range s.legendOrder {
		s.Brush = ch
		break
	}
}

// ----- output -------------------------------------------------------------------------

// Printf adds a line of command output.
func (s *Session) Printf(format string, a ...any) {
	fmt.Fprintf(&s.out, format, a...)
	if !strings.HasSuffix(format, "\n") {
		s.out.WriteByte('\n')
	}
}

// TakeOutput returns and clears the command output.
func (s *Session) TakeOutput() string {
	o := s.out.String()
	s.out.Reset()
	return o
}

// ----- legend -------------------------------------------------------------------------

func (s *Session) rebuildLegend() {
	s.legendColor = map[byte]Color{}
	s.legendChar = map[Color]byte{}
	s.legendOrder = nil
	for _, e := range s.Doc.Legend {
		if e.Color.A == 0 || !ValidChar(e.Char) {
			continue
		}
		s.bind(e.Char, e.Color)
	}
	for _, p := range s.Doc.Palette {
		if p.Color.A != 0 {
			s.charFor(p.Color)
		}
	}
	for _, c := range s.Doc.UsedColors() {
		s.charFor(c)
	}
}

func (s *Session) bind(ch byte, c Color) {
	if _, ok := s.legendColor[ch]; ok {
		return
	}
	if _, ok := s.legendChar[c]; ok {
		return
	}
	s.legendColor[ch] = c
	s.legendChar[c] = ch
	s.legendOrder = append(s.legendOrder, ch)
}

func (s *Session) unbind(ch byte) {
	c, ok := s.legendColor[ch]
	if !ok {
		return
	}
	delete(s.legendColor, ch)
	delete(s.legendChar, c)
	for i, o := range s.legendOrder {
		if o == ch {
			s.legendOrder = append(s.legendOrder[:i:i], s.legendOrder[i+1:]...)
			break
		}
	}
}

// charFor returns the character for a colour, assigning a free one if needed.
func (s *Session) charFor(c Color) byte {
	c = pyxra.Norm(c)
	if c.A == 0 {
		return Empty
	}
	if ch, ok := s.legendChar[c]; ok {
		return ch
	}
	for i := 0; i < len(CharPool); i++ {
		ch := CharPool[i]
		if _, used := s.legendColor[ch]; !used {
			s.bind(ch, c)
			return ch
		}
	}
	return '?'
}

// CharOf returns the character shown for a colour ('?' if the legend is full).
func (s *Session) CharOf(c Color) byte {
	c = pyxra.Norm(c)
	if c.A == 0 {
		return Empty
	}
	if ch, ok := s.legendChar[c]; ok {
		return ch
	}
	return '?'
}

// ColorOf returns a character's colour. '.' is transparent.
func (s *Session) ColorOf(ch byte) (Color, bool) {
	if ch == Empty {
		return pyxra.Transparent, true
	}
	c, ok := s.legendColor[ch]
	return c, ok
}

// Legend returns the legend in display order.
func (s *Session) Legend() []pyxra.LegendEntry {
	out := make([]pyxra.LegendEntry, 0, len(s.legendOrder))
	for _, ch := range s.legendOrder {
		out = append(out, pyxra.LegendEntry{Char: ch, Color: s.legendColor[ch]})
	}
	return out
}

// colorForNewChar picks a colour for a character that has none yet.
func (s *Session) colorForNewChar() Color {
	for _, p := range s.Doc.Palette {
		if _, used := s.legendChar[p.Color]; !used && p.Color.A != 0 {
			return p.Color
		}
	}
	for _, h := range pyxra.Palettes[s.PaletteName] {
		c, _ := pyxra.ParseColor(h)
		if _, used := s.legendChar[c]; !used {
			return c
		}
	}
	// Spread further colours around the hue wheel.
	for i := len(s.legendOrder); ; i++ {
		h := float64(i) * 137.508
		c := hsv(h, 0.65, 0.55+0.35*float64(i%3)/2)
		if _, used := s.legendChar[c]; !used {
			return c
		}
	}
}

func hsv(h, sat, v float64) Color {
	h = h - 360*float64(int(h/360))
	c := v * sat
	x := c * (1 - abs(fmod(h/60, 2)-1))
	m := v - c
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return Color{R: uint8((r + m) * 255), G: uint8((g + m) * 255), B: uint8((b + m) * 255), A: 255}
}

func fmod(a, b float64) float64 { return a - b*float64(int(a/b)) }
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// Resolve turns a colour argument (one character, or #hex/null) into a colour,
// adding legend entries (and palette swatches) as needed. Call inside a step.
func (s *Session) Resolve(arg string) (Color, error) {
	if len(arg) == 1 {
		ch := arg[0]
		if c, ok := s.ColorOf(ch); ok {
			return c, nil
		}
		if !ValidChar(ch) {
			return pyxra.Transparent, fmt.Errorf("%q cannot be a colour character", arg)
		}
		if !s.AutoLegend {
			return pyxra.Transparent, fmt.Errorf("%q has no colour (set one with: color %s #rrggbb)", arg, arg)
		}
		c := s.colorForNewChar()
		s.structural()
		s.bind(ch, c)
		s.addSwatch(c)
		return c, nil
	}
	c, err := pyxra.ParseColor(arg)
	if err != nil {
		return c, err
	}
	if c.A != 0 {
		if _, ok := s.legendChar[c]; !ok {
			s.structural()
			s.charFor(c)
			s.addSwatch(c)
		}
	}
	return c, nil
}

func (s *Session) addSwatch(c Color) {
	for _, p := range s.Doc.Palette {
		if p.Color == c {
			return
		}
	}
	s.Doc.Palette = append(s.Doc.Palette, pyxra.PaletteEntry{Color: c})
}

// SyncLegend copies the session legend into the document so it is saved.
func (s *Session) SyncLegend() { s.Doc.Legend = s.Legend() }

// ----- undo ---------------------------------------------------------------------------

type docState struct {
	name                 string
	w, h                 int
	layers               []pyxra.Layer
	frames               []pyxra.Frame
	tags                 []pyxra.Tag
	palette              []pyxra.PaletteEntry
	cels                 map[pyxra.CelKey]*pyxra.Cel
	legendOrder          []byte
	legendColors         []Color
	layer, frame, cx, cy int
}

type step struct {
	label  string
	before docState
	cels   map[*pyxra.Cel]pyxra.CelState
}

func (s *Session) capture() docState {
	d := s.Doc
	st := docState{
		name: d.Name, w: d.W, h: d.H,
		layers:  append([]pyxra.Layer(nil), d.Layers...),
		frames:  append([]pyxra.Frame(nil), d.Frames...),
		tags:    append([]pyxra.Tag(nil), d.Tags...),
		palette: append([]pyxra.PaletteEntry(nil), d.Palette...),
		cels:    make(map[pyxra.CelKey]*pyxra.Cel, len(d.Cels)),
		layer:   s.Layer, frame: s.Frame, cx: s.CX, cy: s.CY,
	}
	for k, v := range d.Cels {
		st.cels[k] = v
	}
	st.legendOrder = append([]byte(nil), s.legendOrder...)
	for _, ch := range s.legendOrder {
		st.legendColors = append(st.legendColors, s.legendColor[ch])
	}
	return st
}

func (s *Session) restore(st docState) {
	d := s.Doc
	d.Name, d.W, d.H = st.name, st.w, st.h
	d.Layers = append([]pyxra.Layer(nil), st.layers...)
	d.Frames = append([]pyxra.Frame(nil), st.frames...)
	d.Tags = append([]pyxra.Tag(nil), st.tags...)
	d.Palette = append([]pyxra.PaletteEntry(nil), st.palette...)
	d.Cels = make(map[pyxra.CelKey]*pyxra.Cel, len(st.cels))
	for k, v := range st.cels {
		d.Cels[k] = v
	}
	s.legendColor = map[byte]Color{}
	s.legendChar = map[Color]byte{}
	s.legendOrder = nil
	for i, ch := range st.legendOrder {
		s.bind(ch, st.legendColors[i])
	}
	s.Layer, s.Frame, s.CX, s.CY = st.layer, st.frame, st.cx, st.cy
	s.clampState()
}

func (s *Session) clampState() {
	s.Layer = clamp(s.Layer, 0, len(s.Doc.Layers)-1)
	s.Frame = clamp(s.Frame, 0, len(s.Doc.Frames)-1)
	s.CX = clamp(s.CX, 0, s.Doc.W-1)
	s.CY = clamp(s.CY, 0, s.Doc.H-1)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Begin opens an undo step; steps nest and only the outermost one is recorded.
func (s *Session) Begin(label string) {
	if s.depth == 0 {
		s.cur = &step{label: label, before: s.capture(), cels: map[*pyxra.Cel]pyxra.CelState{}}
		s.mutated = false
	}
	s.depth++
}

// End closes an undo step, recording it if anything changed.
func (s *Session) End() {
	if s.depth == 0 {
		return
	}
	s.depth--
	if s.depth > 0 {
		return
	}
	if s.mutated {
		s.undo = append(s.undo, s.cur)
		if len(s.undo) > 500 {
			s.undo = s.undo[1:]
		}
		s.redo = nil
		s.Dirty = true
	}
	s.cur = nil
}

// InStep reports whether an undo step is open.
func (s *Session) InStep() bool { return s.depth > 0 }

// structural marks the open step as changing the document structure or legend.
func (s *Session) structural() { s.mutated = true }

// touch records a cel's pixels before they change.
func (s *Session) touch(c *pyxra.Cel) {
	if s.cur != nil {
		if _, ok := s.cur.cels[c]; !ok {
			s.cur.cels[c] = c.Snapshot()
		}
	}
	c.MarkModified()
	s.mutated = true
}

func (s *Session) Undo() (string, bool) {
	if len(s.undo) == 0 {
		return "", false
	}
	st := s.undo[len(s.undo)-1]
	s.undo = s.undo[:len(s.undo)-1]
	s.redo = append(s.redo, s.swap(st))
	s.Dirty = true
	return st.label, true
}

func (s *Session) Redo() (string, bool) {
	if len(s.redo) == 0 {
		return "", false
	}
	st := s.redo[len(s.redo)-1]
	s.redo = s.redo[:len(s.redo)-1]
	s.undo = append(s.undo, s.swap(st))
	s.Dirty = true
	return st.label, true
}

// swap restores a step and returns the step that undoes the restore.
func (s *Session) swap(st *step) *step {
	back := &step{label: st.label, before: s.capture(), cels: map[*pyxra.Cel]pyxra.CelState{}}
	for c := range st.cels {
		back.cels[c] = c.Snapshot()
	}
	s.restore(st.before)
	for c, cs := range st.cels {
		c.Restore(cs)
	}
	return back
}

// ----- painting -----------------------------------------------------------------------

func (s *Session) checkLayer(li int) error {
	if s.Doc.Layers[li].Locked {
		return fmt.Errorf("layer %q is locked (layer unlock)", s.Doc.Layers[li].Name)
	}
	return nil
}

// PaintOn sets pixels on a layer and frame; transparent erases. Returns pixels changed.
func (s *Session) PaintOn(li, fi int, pts []pyxra.Point, c Color) (int, error) {
	if err := s.checkLayer(li); err != nil {
		return 0, err
	}
	c = pyxra.Norm(c)
	d := s.Doc
	cel := d.Cel(li, fi)
	if cel == nil {
		if c.A == 0 {
			return 0, nil
		}
		cel, _ = d.EnsureCel(li, fi)
		s.structural()
	}
	n := 0
	touched := false
	for _, p := range pts {
		if p.X < 0 || p.Y < 0 || p.X >= d.W || p.Y >= d.H {
			continue
		}
		if pyxra.GetPx(cel.Img, p.X, p.Y) == c {
			continue
		}
		if !touched {
			s.touch(cel)
			touched = true
		}
		pyxra.SetPx(cel.Img, p.X, p.Y, c)
		n++
	}
	return n, nil
}

// Paint sets pixels on the current layer and frame.
func (s *Session) Paint(pts []pyxra.Point, c Color) (int, error) {
	return s.PaintOn(s.Layer, s.Frame, pts, c)
}

// PaintChar paints with a character's colour inside its own undo step.
func (s *Session) PaintChar(pts []pyxra.Point, ch byte) (int, error) {
	s.Begin("paint")
	defer s.End()
	c, err := s.Resolve(string(ch))
	if err != nil {
		return 0, err
	}
	return s.Paint(pts, c)
}

// CursorChar is the character of the current layer under the cursor.
func (s *Session) CursorChar() byte {
	return s.CharOf(s.Doc.At(s.Layer, s.Frame, s.CX, s.CY))
}

// SoloLayer returns the layer index to draw alone, or -1.
func (s *Session) SoloLayer() int {
	if s.Solo {
		return s.Layer
	}
	return -1
}
