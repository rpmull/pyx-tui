// Package tui is Pixra's full-screen terminal editor.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rpmull/pyx-tui/internal/pyxra"
	"github.com/rpmull/pyx-tui/internal/session"
)

const sideW = 30

var keyHelp = []string{
	"KEYS",
	"  arrows / h j k l    move the cursor (H J K L: 8 at a time)",
	"  0 $ g G             row start, row end, top, bottom",
	"  Space / Enter       paint the brush       x / Delete  erase",
	"  d                   pen down/up: moving paints",
	"  i                   type mode: typed characters become pixels",
	"  b <char>            choose the brush      p  pick from the canvas",
	"  1-9 [ ] Tab         choose / cycle legend characters",
	"  c                   set the brush's colour",
	"  f                   flood fill            /  line",
	"  r R                 rectangle / filled    o O  ellipse / filled",
	"  u   U / Ctrl-R      undo / redo",
	"  + -  mouse wheel    zoom in / out (1/8 .. 8)",
	"  v                   view: color, mixed (colour + characters), text",
	"  { }                 layer down / up       A  add a layer",
	"  V                   show/hide the layer   S  solo the layer",
	"  < >                 previous / next frame",
	"  s / Ctrl-S          save                  e  export",
	"  :                   command line (Tab completes)",
	"  ?                   this help             q  quit",
	"  mouse               left paints (drag draws), right picks",
	"",
}

type App struct {
	s   *session.Session
	scr tcell.Screen

	vx, vy int // viewport origin in pixels

	mode           string // normal, type, shape
	shape          string
	filled         bool
	ax, ay         int
	typeCol        int
	pen            bool
	pending        string // "brush" or "quit"
	cmd            []rune
	cmdOn          bool
	cmdPos         int
	history        []string
	histI          int
	msg            string
	msgErr         bool
	popup          []string
	popupOff       int
	mouseDown      bool
	lastPX, lastPY int
	prevButtons    tcell.ButtonMask

	top, left, rows, cols int // canvas area on screen
}

// Run starts the editor and returns when the user quits.
func Run(s *session.Session) error {
	scr, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := scr.Init(); err != nil {
		return err
	}
	defer scr.Fini()
	scr.EnableMouse(tcell.MouseMotionEvents)
	scr.SetStyle(tcell.StyleDefault)
	a := &App{s: s, scr: scr, mode: "normal"}
	if out := strings.TrimSpace(s.TakeOutput()); out != "" {
		a.show(out, false)
	} else {
		a.msg = "press ? for help, : for commands"
	}
	for {
		if !scr.HasPendingEvent() {
			a.draw()
		}
		ev := scr.PollEvent()
		if ev == nil {
			return nil
		}
		switch e := ev.(type) {
		case *tcell.EventResize:
			scr.Sync()
		case *tcell.EventKey:
			a.key(e)
		case *tcell.EventMouse:
			a.mouse(e)
		}
		if s.Quit {
			return nil
		}
	}
}

// ----- geometry -----------------------------------------------------------------------

// geom describes how pixels map to cells. When zoomed in, a pixel is cw x ch cells;
// otherwise a cell covers pw x ph pixels (half: split into top and bottom halves).
type geom struct {
	cw, ch int
	pw, ph int
	half   bool
}

func (a *App) geom() geom {
	z := a.s.Zoom
	if z >= 1 {
		return geom{cw: 1 << z, ch: 1 << (z - 1), pw: 1, ph: 1}
	}
	k := 1 << -z
	if a.s.View == session.ViewText {
		return geom{cw: 1, ch: 1, pw: k, ph: k}
	}
	return geom{cw: 1, ch: 1, pw: k, ph: 2 * k, half: true}
}

// visible returns how many pixels fit across and down.
func (a *App) visible(g geom) (int, int) {
	return (a.cols / g.cw) * g.pw, (a.rows / g.ch) * g.ph
}

func (a *App) scrollToCursor() {
	g := a.geom()
	d := a.s.Doc
	vw, vh := a.visible(g)
	if a.s.CX < a.vx {
		a.vx = a.s.CX - a.s.CX%g.pw
	} else if a.s.CX >= a.vx+vw {
		a.vx = a.s.CX - vw + g.pw
	}
	if a.s.CY < a.vy {
		a.vy = a.s.CY - a.s.CY%g.ph
	} else if a.s.CY >= a.vy+vh {
		a.vy = a.s.CY - vh + g.ph
	}
	a.vx = max(0, min(a.vx, d.W-vw))
	a.vy = max(0, min(a.vy, d.H-vh))
	a.vx -= a.vx % g.pw
	a.vy -= a.vy % g.ph
}

// cellToPixel maps a screen cell to a pixel, or ok=false outside the canvas.
func (a *App) cellToPixel(x, y int) (int, int, bool) {
	g := a.geom()
	cx, cy := x-a.left, y-a.top
	if cx < 0 || cy < 0 || cx >= a.cols || cy >= a.rows {
		return 0, 0, false
	}
	px := a.vx + (cx/g.cw)*g.pw
	py := a.vy + (cy/g.ch)*g.ph
	if px >= a.s.Doc.W || py >= a.s.Doc.H {
		return 0, 0, false
	}
	return px, py, true
}

// ----- drawing ------------------------------------------------------------------------

var (
	checkA = tcell.NewRGBColor(0x1e, 0x1e, 0x24)
	checkB = tcell.NewRGBColor(0x2a, 0x2a, 0x32)
	dim    = tcell.StyleDefault.Foreground(tcell.ColorGray)
	bold   = tcell.StyleDefault.Bold(true)
	bar    = tcell.StyleDefault.Background(tcell.ColorTeal).Foreground(tcell.ColorBlack)
)

func tc(c pyxra.Color) tcell.Color { return tcell.NewRGBColor(int32(c.R), int32(c.G), int32(c.B)) }

func (a *App) put(x, y int, s string, st tcell.Style) int {
	w, _ := a.scr.Size()
	for _, r := range s {
		if x >= w {
			break
		}
		a.scr.SetContent(x, y, r, nil, st)
		x++
	}
	return x
}

func (a *App) fill(x, y, w int, st tcell.Style) {
	for i := 0; i < w; i++ {
		a.scr.SetContent(x+i, y, ' ', nil, st)
	}
}

// pixelColor is what the canvas shows at a pixel: the composite over a checkerboard.
func (a *App) pixelColor(px, py int, preview map[pyxra.Point]bool, brush pyxra.Color) tcell.Color {
	s := a.s
	var c pyxra.Color
	if preview != nil && preview[pyxra.Point{X: px, Y: py}] {
		c = brush
	} else {
		c = s.Doc.Composite(s.Frame, px, py, s.SoloLayer())
	}
	if c.A == 255 {
		return tc(c)
	}
	bg := pyxra.Color{R: 0x1e, G: 0x1e, B: 0x24, A: 255}
	if (px+py)&1 == 1 {
		bg = pyxra.Color{R: 0x2a, G: 0x2a, B: 0x32, A: 255}
	}
	if c.A == 0 {
		return tc(bg)
	}
	return tc(pyxra.Blend(bg, c, 255, "normal"))
}

func (a *App) pixelChar(px, py int, preview map[pyxra.Point]bool) (byte, pyxra.Color) {
	s := a.s
	if preview != nil && preview[pyxra.Point{X: px, Y: py}] {
		c, _ := s.ColorOf(s.Brush)
		return s.Brush, c
	}
	c, _ := s.Doc.Top(s.Frame, px, py, s.SoloLayer())
	return s.CharOf(c), c
}

func (a *App) draw() {
	scr := a.scr
	scr.Clear()
	W, H := scr.Size()
	if W < 40 || H < 10 {
		a.put(0, 0, "terminal too small (need 40x10)", tcell.StyleDefault)
		scr.Show()
		return
	}
	side := 0
	if W-sideW >= 30 {
		side = sideW
	}
	a.top, a.left = 2, 1
	a.rows, a.cols = H-5, W-side-2
	g := a.geom()
	d := a.s.Doc
	// Shrink the canvas box to the document when it is small.
	a.cols = min(a.cols, (d.W+g.pw-1)/g.pw*g.cw)
	a.rows = min(a.rows, (d.H+g.ph-1)/g.ph*g.ch)
	a.scrollToCursor()
	a.drawTitle(W)
	a.drawFrame()
	a.drawCanvas(g)
	if side > 0 {
		a.drawSide(W-side+1, H)
	}
	a.drawStatus(W, H)
	if len(a.popup) > 0 {
		a.drawPopup(W, H)
	}
	if a.cmdOn {
		scr.ShowCursor(1+a.cmdPos, H-1)
	} else {
		scr.HideCursor()
	}
	scr.Show()
}

func (a *App) drawTitle(W int) {
	s := a.s
	d := s.Doc
	name := s.Path
	if name == "" {
		name = "[unsaved " + s.SavePath() + "]"
	} else {
		name = filepath.Base(name)
	}
	if s.Dirty {
		name += " [+]"
	}
	tags := strings.Join(d.FrameTags(s.Frame), ",")
	if tags != "" {
		tags = " " + tags
	}
	t := fmt.Sprintf(" PIXRA  %s  %dx%d  layer %d/%d %q  frame %d/%d%s  zoom %s  %s",
		name, d.W, d.H, s.Layer+1, len(d.Layers), d.Layers[s.Layer].Name, s.Frame+1, len(d.Frames), tags,
		session.ZoomLabel(s.Zoom), s.View)
	if s.Solo {
		t += "  SOLO"
	}
	a.fill(0, 0, W, bar)
	a.put(0, 0, t, bar.Bold(true))
}

func (a *App) drawFrame() {
	x0, y0, x1, y1 := a.left-1, a.top-1, a.left+a.cols, a.top+a.rows
	for x := x0 + 1; x < x1; x++ {
		a.scr.SetContent(x, y0, tcell.RuneHLine, nil, dim)
		a.scr.SetContent(x, y1, tcell.RuneHLine, nil, dim)
	}
	for y := y0 + 1; y < y1; y++ {
		a.scr.SetContent(x0, y, tcell.RuneVLine, nil, dim)
		a.scr.SetContent(x1, y, tcell.RuneVLine, nil, dim)
	}
	a.scr.SetContent(x0, y0, tcell.RuneULCorner, nil, dim)
	a.scr.SetContent(x1, y0, tcell.RuneURCorner, nil, dim)
	a.scr.SetContent(x0, y1, tcell.RuneLLCorner, nil, dim)
	a.scr.SetContent(x1, y1, tcell.RuneLRCorner, nil, dim)
	g := a.geom()
	vw, vh := a.visible(g)
	if a.vx > 0 {
		a.scr.SetContent(x0+1, y0, '◂', nil, bold)
	}
	if a.vx+vw < a.s.Doc.W {
		a.scr.SetContent(x1-1, y0, '▸', nil, bold)
	}
	if a.vy > 0 {
		a.scr.SetContent(x0, y0+1, '▴', nil, bold)
	}
	if a.vy+vh < a.s.Doc.H {
		a.scr.SetContent(x0, y1-1, '▾', nil, bold)
	}
}

func (a *App) previewPoints() map[pyxra.Point]bool {
	if a.mode != "shape" {
		return nil
	}
	var pts []pyxra.Point
	switch a.shape {
	case "line":
		pts = pyxra.LinePoints(a.ax, a.ay, a.s.CX, a.s.CY)
	case "rect":
		pts = pyxra.RectPoints(a.ax, a.ay, a.s.CX, a.s.CY, a.filled)
	case "ellipse":
		pts = pyxra.EllipsePoints(a.ax, a.ay, a.s.CX, a.s.CY, a.filled)
	}
	m := make(map[pyxra.Point]bool, len(pts))
	for _, p := range pts {
		m[p] = true
	}
	return m
}

func (a *App) drawCanvas(g geom) {
	s := a.s
	d := s.Doc
	preview := a.previewPoints()
	brush, _ := s.ColorOf(s.Brush)
	view := s.View
	for r := 0; r < a.rows; r++ {
		for c := 0; c < a.cols; c++ {
			px := a.vx + (c/g.cw)*g.pw
			py := a.vy + (r/g.ch)*g.ph
			if px >= d.W || py >= d.H {
				continue
			}
			sx, sy := a.left+c, a.top+r
			cursor := s.CX >= px && s.CX < px+g.pw && s.CY >= py && s.CY < py+g.ph
			switch {
			case g.half:
				top := a.pixelColor(px, py, preview, brush)
				bot := top
				if py+g.ph/2 < d.H {
					bot = a.pixelColor(px, py+g.ph/2, preview, brush)
				} else {
					bot = tcell.ColorReset
				}
				st := tcell.StyleDefault.Foreground(top).Background(bot)
				ch := '▀'
				if cursor {
					ch = '◆'
					st = tcell.StyleDefault.Foreground(contrast(a, px, py)).Background(top)
				}
				a.scr.SetContent(sx, sy, ch, nil, st)
			case view == session.ViewText:
				ch, _ := a.pixelChar(px, py, preview)
				st := tcell.StyleDefault
				if ch == session.Empty {
					st = dim
				}
				if g.cw > 1 && c%g.cw != 0 && g.cw == 2 {
					ch = ' '
				}
				if cursor {
					st = st.Reverse(true)
				}
				if a.mode == "shape" && px == a.ax && py == a.ay {
					st = st.Underline(true)
				}
				a.scr.SetContent(sx, sy, rune(ch), nil, st)
			default: // color or mixed, zoomed in
				bg := a.pixelColor(px, py, preview, brush)
				st := tcell.StyleDefault.Background(bg).Foreground(contrast(a, px, py))
				ch := ' '
				if view == session.ViewMixed && c%g.cw == 0 && r%g.ch == 0 {
					b, _ := a.pixelChar(px, py, preview)
					if b != session.Empty {
						ch = rune(b)
					}
				}
				if cursor {
					ox, oy := c%g.cw, r%g.ch
					switch {
					case g.cw == 2 && ox == 0 && ch == ' ':
						ch = '['
					case g.cw == 2 && ox == 1:
						ch = ']'
					case g.cw > 2 && oy == g.ch/2 && ox == 0 && ch == ' ':
						ch = '['
					case g.cw > 2 && oy == g.ch/2 && ox == g.cw-1:
						ch = ']'
					}
					st = st.Bold(true)
				}
				if a.mode == "shape" && px == a.ax && py == a.ay && c%g.cw == 0 && r%g.ch == 0 && ch == ' ' {
					ch = '+'
				}
				a.scr.SetContent(sx, sy, ch, nil, st)
			}
		}
	}
}

func contrast(a *App, px, py int) tcell.Color {
	c := a.s.Doc.Composite(a.s.Frame, px, py, a.s.SoloLayer())
	if c.A > 0 && pyxra.IsLight(c) {
		return tcell.ColorBlack
	}
	return tcell.ColorWhite
}

func (a *App) drawSide(x, H int) {
	s := a.s
	d := s.Doc
	y := 1
	a.put(x, y, "LAYERS", bold)
	y++
	maxLayers := max(3, (H-6)/3)
	start := 0
	if len(d.Layers) > maxLayers {
		// keep the current layer visible; list is drawn top first
		topIdx := len(d.Layers) - 1 - s.Layer
		start = max(0, min(topIdx-maxLayers/2, len(d.Layers)-maxLayers))
	}
	for i := start; i < len(d.Layers) && i < start+maxLayers; i++ {
		li := len(d.Layers) - 1 - i
		l := d.Layers[li]
		st := tcell.StyleDefault
		mark := " "
		if li == s.Layer {
			mark = ">"
			st = bold
		}
		vis := "●"
		if !l.Visible {
			vis = "○"
			st = st.Dim(true)
		}
		lock := " "
		if l.Locked {
			lock = "L"
		}
		name := l.Name
		if utf8.RuneCountInString(name) > sideW-9 {
			name = string([]rune(name)[:sideW-10]) + "…"
		}
		a.put(x, y, fmt.Sprintf("%s%2d %s%s %s", mark, li+1, vis, lock, name), st)
		y++
	}
	if len(d.Layers) > maxLayers {
		a.put(x, y, fmt.Sprintf("  (%d layers)", len(d.Layers)), dim)
		y++
	}
	y++
	a.put(x, y, "LEGEND", bold)
	y++
	legend := s.Legend()
	room := H - 3 - y
	start = 0
	bi := 0
	for i, e := range legend {
		if e.Char == s.Brush {
			bi = i
		}
	}
	if len(legend) > room && room > 0 {
		start = max(0, min(bi-room/2, len(legend)-room))
	}
	for i := start; i < len(legend) && y < H-3; i++ {
		e := legend[i]
		st := tcell.StyleDefault
		mark := " "
		if e.Char == s.Brush {
			mark, st = ">", bold
		}
		num := " "
		if i < 9 {
			num = fmt.Sprint(i + 1)
		}
		a.put(x, y, fmt.Sprintf("%s%s %c ", mark, num, e.Char), st)
		a.put(x+5, y, "  ", tcell.StyleDefault.Background(tc(e.Color)))
		a.put(x+8, y, pyxra.Hex(e.Color), st)
		y++
	}
	if len(legend) == 0 {
		a.put(x, y, " (type a character to add)", dim)
	}
}

func (a *App) drawStatus(W, H int) {
	s := a.s
	label := map[string]string{"normal": " NORMAL ", "type": " TYPE ", "shape": " " + strings.ToUpper(a.shape) + " "}[a.mode]
	if a.mode == "shape" && a.filled {
		label = " FILLED" + label
	}
	if a.pen {
		label += "PEN "
	}
	color := map[string]tcell.Color{"normal": tcell.ColorYellow, "type": tcell.ColorGreen, "shape": tcell.ColorFuchsia}[a.mode]
	x := a.put(0, H-2, label, tcell.StyleDefault.Background(color).Foreground(tcell.ColorBlack).Bold(true))
	under := s.Doc.At(s.Layer, s.Frame, s.CX, s.CY)
	brush, _ := s.ColorOf(s.Brush)
	x = a.put(x, H-2, fmt.Sprintf(" %d,%d %c %s   brush %c ", s.CX, s.CY, s.CharOf(under), pyxra.Hex(under), s.Brush), tcell.StyleDefault)
	x = a.put(x, H-2, "  ", tcell.StyleDefault.Background(tc(brush)))
	x = a.put(x, H-2, " "+pyxra.Hex(brush), tcell.StyleDefault)
	if a.mode == "shape" {
		a.put(x, H-2, fmt.Sprintf("   %dx%d", abs(s.CX-a.ax)+1, abs(s.CY-a.ay)+1), tcell.StyleDefault)
	}
	if a.cmdOn {
		a.put(0, H-1, ":"+string(a.cmd), tcell.StyleDefault)
		return
	}
	msg := a.msg
	if a.pending == "brush" {
		msg = "brush: press a character"
	}
	st := tcell.StyleDefault
	if a.msgErr {
		st = st.Foreground(tcell.ColorRed)
	}
	a.put(0, H-1, msg, st)
}

func (a *App) drawPopup(W, H int) {
	lines := a.popup
	bw := 0
	for _, l := range lines {
		bw = max(bw, utf8.RuneCountInString(l))
	}
	bw = min(W-4, bw+4)
	bh := min(H-2, len(lines)+2)
	body := bh - 2
	a.popupOff = max(0, min(a.popupOff, len(lines)-body))
	x, y := (W-bw)/2, (H-bh)/2
	a.fill(x, y, bw, bar)
	a.put(x+1, y, "Pixra", bar.Bold(true))
	for i := 0; i < body; i++ {
		a.fill(x, y+1+i, bw, tcell.StyleDefault.Background(tcell.ColorBlack))
		if j := a.popupOff + i; j < len(lines) {
			st := tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite)
			if !strings.HasPrefix(lines[j], " ") {
				st = st.Bold(true)
			}
			a.put(x+2, y+1+i, lines[j], st)
		}
	}
	more := " any key closes "
	if len(lines) > body {
		more = " up/down/PgUp/PgDn scroll, any other key closes "
	}
	a.fill(x, y+bh-1, bw, bar)
	a.put(x+1, y+bh-1, more, bar)
}

// show displays command output: one line as a message, more in a popup.
func (a *App) show(out string, isErr bool) {
	out = strings.TrimRight(out, "\n")
	if strings.Contains(out, "\n") {
		a.popup = strings.Split(out, "\n")
		a.popupOff = 0
		a.msg = ""
		return
	}
	a.msg, a.msgErr = out, isErr
}

// run executes a command line and shows its output.
func (a *App) run(line string) {
	a.endPen()
	err := a.s.Exec(line)
	out := a.s.TakeOutput()
	if err != nil {
		if out != "" {
			out += "\n"
		}
		a.show(out+"error: "+err.Error(), true)
		return
	}
	a.show(out, false)
}

// ----- input --------------------------------------------------------------------------

func (a *App) key(ev *tcell.EventKey) {
	if len(a.popup) > 0 {
		switch {
		case ev.Key() == tcell.KeyUp || ev.Rune() == 'k':
			a.popupOff--
		case ev.Key() == tcell.KeyDown || ev.Rune() == 'j':
			a.popupOff++
		case ev.Key() == tcell.KeyPgUp:
			a.popupOff -= 10
		case ev.Key() == tcell.KeyPgDn || ev.Rune() == ' ':
			a.popupOff += 10
		default:
			a.popup = nil
		}
		return
	}
	if a.cmdOn {
		a.cmdKey(ev)
		return
	}
	if a.pending == "brush" {
		a.pending = ""
		if ev.Key() == tcell.KeyRune && ev.Rune() < 127 {
			a.run("brush " + quote(string(ev.Rune())))
		} else {
			a.msg = "brush unchanged"
		}
		return
	}
	quitAgain := a.pending == "quit"
	a.pending = ""
	switch a.mode {
	case "type":
		a.typeKey(ev)
	case "shape":
		a.shapeKey(ev)
	default:
		a.normalKey(ev, quitAgain)
	}
}

func (a *App) moveBy(dx, dy int) {
	s := a.s
	s.CX = clampi(s.CX+dx, 0, s.Doc.W-1)
	s.CY = clampi(s.CY+dy, 0, s.Doc.H-1)
	if a.pen && a.mode == "normal" {
		a.paintAt(s.CX, s.CY)
	}
}

// movement handles cursor keys; vi enables hjkl and friends.
func (a *App) movement(ev *tcell.EventKey, vi bool) bool {
	g := a.geom()
	sx, sy := g.pw, g.ph
	if g.half {
		sy = g.ph / 2
	}
	sx, sy = max(1, sx), max(1, sy)
	s := a.s
	switch ev.Key() {
	case tcell.KeyLeft:
		a.moveBy(-sx, 0)
	case tcell.KeyRight:
		a.moveBy(sx, 0)
	case tcell.KeyUp:
		a.moveBy(0, -sy)
	case tcell.KeyDown:
		a.moveBy(0, sy)
	case tcell.KeyHome:
		s.CX = 0
	case tcell.KeyEnd:
		s.CX = s.Doc.W - 1
	case tcell.KeyPgUp:
		_, vh := a.visible(g)
		a.moveBy(0, -max(1, vh-1))
	case tcell.KeyPgDn:
		_, vh := a.visible(g)
		a.moveBy(0, max(1, vh-1))
	case tcell.KeyRune:
		if !vi {
			return false
		}
		switch ev.Rune() {
		case 'h':
			a.moveBy(-sx, 0)
		case 'l':
			a.moveBy(sx, 0)
		case 'k':
			a.moveBy(0, -sy)
		case 'j':
			a.moveBy(0, sy)
		case 'H':
			a.moveBy(-8*sx, 0)
		case 'L':
			a.moveBy(8*sx, 0)
		case 'K':
			a.moveBy(0, -8*sy)
		case 'J':
			a.moveBy(0, 8*sy)
		case '0':
			s.CX = 0
		case '$':
			s.CX = s.Doc.W - 1
		case 'g':
			s.CY = 0
		case 'G':
			s.CY = s.Doc.H - 1
		default:
			return false
		}
	default:
		return false
	}
	return true
}

func (a *App) paintAt(x, y int) {
	if _, err := a.s.PaintChar([]pyxra.Point{{X: x, Y: y}}, a.s.Brush); err != nil {
		a.msg, a.msgErr = err.Error(), true
	}
}

func (a *App) paintLine(x0, y0, x1, y1 int) {
	if _, err := a.s.PaintChar(pyxra.LinePoints(x0, y0, x1, y1), a.s.Brush); err != nil {
		a.msg, a.msgErr = err.Error(), true
	}
}

func (a *App) endPen() {
	if a.pen {
		a.pen = false
		a.s.End()
	}
	if a.mouseDown {
		a.mouseDown = false
		a.s.End()
	}
}

func (a *App) normalKey(ev *tcell.EventKey, quitAgain bool) {
	s := a.s
	a.msgErr = false
	if a.movement(ev, true) {
		return
	}
	switch ev.Key() {
	case tcell.KeyEnter:
		a.paintAt(s.CX, s.CY)
		return
	case tcell.KeyDelete:
		a.run(fmt.Sprintf("erase %d %d", s.CX, s.CY))
		a.msg = ""
		return
	case tcell.KeyTab:
		a.cycle(1)
		return
	case tcell.KeyBacktab:
		a.cycle(-1)
		return
	case tcell.KeyCtrlS:
		a.save()
		return
	case tcell.KeyCtrlR:
		a.undoRedo("redo")
		return
	case tcell.KeyCtrlC:
		a.quit(quitAgain)
		return
	case tcell.KeyEscape:
		a.endPen()
		a.msg = ""
		return
	case tcell.KeyRune:
	default:
		return
	}
	r := ev.Rune()
	switch r {
	case ' ':
		a.paintAt(s.CX, s.CY)
	case 'x':
		a.run(fmt.Sprintf("erase %d %d", s.CX, s.CY))
		a.msg = ""
	case 'd':
		if a.pen {
			a.endPen()
			a.msg = "pen up"
		} else {
			a.pen = true
			s.Begin("pen")
			a.paintAt(s.CX, s.CY)
			a.msg = fmt.Sprintf("pen down: moving paints %c (d or Esc to lift)", s.Brush)
		}
	case 'i', 'a':
		a.endPen()
		a.mode, a.typeCol = "type", s.CX
		s.Begin("type")
		a.msg = "type characters onto the canvas; Enter = next row, Space/. = erase, Esc = done"
	case 'b':
		a.pending = "brush"
	case 'p':
		c, li := s.Doc.Top(s.Frame, s.CX, s.CY, s.SoloLayer())
		if li < 0 {
			a.msg = "nothing to pick here"
		} else {
			s.Brush = s.CharOf(c)
			a.msg = fmt.Sprintf("brush %c %s (from %s)", s.Brush, pyxra.Hex(c), s.Doc.Layers[li].Name)
		}
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		l := s.Legend()
		if i := int(r - '1'); i < len(l) {
			s.Brush = l[i].Char
		}
	case ']':
		a.cycle(1)
	case '[':
		a.cycle(-1)
	case 'c':
		c, _ := s.ColorOf(s.Brush)
		a.openCmd(fmt.Sprintf("color %s %s", quote(string(s.Brush)), pyxra.Hex(c)))
	case 'f':
		a.run(fmt.Sprintf("fill %d %d %s", s.CX, s.CY, quote(string(s.Brush))))
	case '/':
		a.startShape("line", false)
	case 'r', 'R':
		a.startShape("rect", r == 'R')
	case 'o', 'O':
		a.startShape("ellipse", r == 'O')
	case 'u':
		a.undoRedo("undo")
	case 'U':
		a.undoRedo("redo")
	case '+', '=':
		a.run("zoom in")
	case '-', '_':
		a.run("zoom out")
	case 'v':
		next := map[string]string{session.ViewColor: session.ViewMixed, session.ViewMixed: session.ViewText, session.ViewText: session.ViewColor}[s.View]
		a.run("view " + next)
		a.msg = "view: " + next
	case '{':
		if s.Layer > 0 {
			s.Layer--
		}
	case '}':
		if s.Layer < len(s.Doc.Layers)-1 {
			s.Layer++
		}
	case 'A':
		a.openCmd("layer add ")
	case 'V':
		a.run("layer toggle")
	case 'S':
		a.run("solo")
		a.msg = map[bool]string{true: "solo: only this layer", false: "solo off"}[s.Solo]
	case '<', ',':
		s.Frame = (s.Frame + len(s.Doc.Frames) - 1) % len(s.Doc.Frames)
	case '>', '.':
		s.Frame = (s.Frame + 1) % len(s.Doc.Frames)
	case 's':
		a.save()
	case 'e':
		a.openCmd("export " + quote(session.BaseName(s.SavePath())+".png") + " --scale 8")
	case ':':
		a.openCmd("")
	case '?':
		a.popup = append(append([]string{}, keyHelp...), strings.Split(session.HelpText(), "\n")...)
		a.popupOff = 0
	case 'q':
		a.quit(quitAgain)
	}
}

func (a *App) quit(again bool) {
	a.endPen()
	if !a.s.Dirty || again {
		a.s.Quit = true
		return
	}
	a.pending = "quit"
	a.msg, a.msgErr = "unsaved changes: press q again to quit without saving (s saves)", true
}

func (a *App) save() {
	a.run("save " + quote(a.s.SavePath()))
}

func (a *App) undoRedo(which string) {
	a.endPen()
	a.run(which)
}

func (a *App) cycle(step int) {
	l := a.s.Legend()
	if len(l) == 0 {
		return
	}
	i := 0
	for j, e := range l {
		if e.Char == a.s.Brush {
			i = j
		}
	}
	a.s.Brush = l[(i+step+len(l))%len(l)].Char
}

func (a *App) startShape(kind string, filled bool) {
	a.endPen()
	a.mode, a.shape, a.filled = "shape", kind, filled
	a.ax, a.ay = a.s.CX, a.s.CY
	a.msg = "move to the other end; Space/Enter draws, Esc cancels"
}

func (a *App) shapeKey(ev *tcell.EventKey) {
	if a.movement(ev, true) {
		return
	}
	if ev.Key() == tcell.KeyEnter || ev.Rune() == ' ' {
		s := a.s
		cmd := fmt.Sprintf("%s %d %d %d %d %s", a.shape, a.ax, a.ay, s.CX, s.CY, quote(string(s.Brush)))
		if a.filled {
			cmd += " --fill"
		}
		a.mode = "normal"
		a.run(cmd)
		return
	}
	if ev.Key() == tcell.KeyEscape || ev.Rune() == 'q' {
		a.mode = "normal"
		a.msg = "cancelled"
	}
}

func (a *App) typeKey(ev *tcell.EventKey) {
	s := a.s
	switch ev.Key() {
	case tcell.KeyEscape:
		a.mode = "normal"
		s.End()
		a.msg = ""
		return
	case tcell.KeyEnter:
		s.CX = a.typeCol
		s.CY = min(s.CY+1, s.Doc.H-1)
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		s.CX = max(0, s.CX-1)
		s.Paint([]pyxra.Point{{X: s.CX, Y: s.CY}}, pyxra.Transparent)
		return
	case tcell.KeyDelete:
		s.Paint([]pyxra.Point{{X: s.CX, Y: s.CY}}, pyxra.Transparent)
		return
	case tcell.KeyRune:
		r := ev.Rune()
		if r == ' ' || r == '.' {
			s.Paint([]pyxra.Point{{X: s.CX, Y: s.CY}}, pyxra.Transparent)
		} else if r < 127 && session.ValidChar(byte(r)) {
			c, err := s.Resolve(string(r))
			if err == nil {
				_, err = s.Paint([]pyxra.Point{{X: s.CX, Y: s.CY}}, c)
			}
			if err != nil {
				a.msg, a.msgErr = err.Error(), true
				return
			}
		} else {
			return
		}
		s.CX = min(s.CX+1, s.Doc.W-1)
		return
	}
	if a.movement(ev, false) {
		a.typeCol = s.CX
	}
}

func (a *App) mouse(ev *tcell.EventMouse) {
	if len(a.popup) > 0 || a.cmdOn {
		return
	}
	btn := ev.Buttons()
	prev := a.prevButtons
	a.prevButtons = btn
	x, y := ev.Position()
	if btn&tcell.WheelUp != 0 {
		a.run("zoom in")
		return
	}
	if btn&tcell.WheelDown != 0 {
		a.run("zoom out")
		return
	}
	px, py, inside := a.cellToPixel(x, y)
	s := a.s
	if btn&tcell.Button1 == 0 && a.mouseDown {
		a.mouseDown = false
		s.End()
	}
	if !inside {
		return
	}
	if btn&tcell.Button2 != 0 && prev&tcell.Button2 == 0 {
		s.CX, s.CY = px, py
		if c, li := s.Doc.Top(s.Frame, px, py, s.SoloLayer()); li >= 0 {
			s.Brush = s.CharOf(c)
			a.msg = fmt.Sprintf("brush %c %s", s.Brush, pyxra.Hex(c))
		}
		return
	}
	if btn&tcell.Button1 == 0 {
		return
	}
	s.CX, s.CY = px, py
	switch a.mode {
	case "type":
		a.typeCol = px
		return
	case "shape":
		return
	}
	if !a.mouseDown {
		a.mouseDown = true
		s.Begin("draw")
		a.paintAt(px, py)
	} else {
		a.paintLine(a.lastPX, a.lastPY, px, py)
	}
	a.lastPX, a.lastPY = px, py
}

// ----- command line -------------------------------------------------------------------

func (a *App) openCmd(text string) {
	a.cmdOn = true
	a.cmd = []rune(text)
	a.cmdPos = len(a.cmd)
	a.histI = len(a.history)
}

func (a *App) cmdKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEnter:
		line := string(a.cmd)
		a.cmdOn = false
		if strings.TrimSpace(line) != "" {
			if len(a.history) == 0 || a.history[len(a.history)-1] != line {
				a.history = append(a.history, line)
			}
			a.run(line)
		}
	case tcell.KeyEscape, tcell.KeyCtrlC:
		a.cmdOn = false
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(a.cmd) == 0 {
			a.cmdOn = false
		} else if a.cmdPos > 0 {
			a.cmd = append(a.cmd[:a.cmdPos-1], a.cmd[a.cmdPos:]...)
			a.cmdPos--
		}
	case tcell.KeyDelete:
		if a.cmdPos < len(a.cmd) {
			a.cmd = append(a.cmd[:a.cmdPos], a.cmd[a.cmdPos+1:]...)
		}
	case tcell.KeyLeft:
		a.cmdPos = max(0, a.cmdPos-1)
	case tcell.KeyRight:
		a.cmdPos = min(len(a.cmd), a.cmdPos+1)
	case tcell.KeyHome, tcell.KeyCtrlA:
		a.cmdPos = 0
	case tcell.KeyEnd, tcell.KeyCtrlE:
		a.cmdPos = len(a.cmd)
	case tcell.KeyCtrlU:
		a.cmd, a.cmdPos = nil, 0
	case tcell.KeyUp:
		if a.histI > 0 {
			a.histI--
			a.cmd = []rune(a.history[a.histI])
			a.cmdPos = len(a.cmd)
		}
	case tcell.KeyDown:
		if a.histI < len(a.history) {
			a.histI++
			a.cmd = nil
			if a.histI < len(a.history) {
				a.cmd = []rune(a.history[a.histI])
			}
			a.cmdPos = len(a.cmd)
		}
	case tcell.KeyTab:
		a.complete()
	case tcell.KeyRune:
		a.cmd = append(a.cmd[:a.cmdPos], append([]rune{ev.Rune()}, a.cmd[a.cmdPos:]...)...)
		a.cmdPos++
	}
}

// complete finishes the word before the cursor: a command name, a layer name or a file.
func (a *App) complete() {
	before := string(a.cmd[:a.cmdPos])
	after := string(a.cmd[a.cmdPos:])
	i := strings.LastIndexByte(before, ' ')
	word := before[i+1:]
	head := before[:i+1]
	var cands []string
	if strings.TrimSpace(head) == "" {
		seen := map[string]bool{}
		for _, c := range session.Commands() {
			n := strings.Fields(c.Name)[0]
			if strings.HasPrefix(n, word) && !seen[n] {
				seen[n] = true
				cands = append(cands, n)
			}
		}
	} else {
		matches, _ := filepath.Glob(word + "*")
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.IsDir() {
				m += string(filepath.Separator)
			}
			cands = append(cands, m)
		}
		if strings.HasPrefix(strings.TrimSpace(head), "layer") {
			for _, l := range a.s.Doc.Layers {
				if strings.HasPrefix(strings.ToLower(l.Name), strings.ToLower(word)) {
					cands = append(cands, quote(l.Name))
				}
			}
			for _, sub := range []string{"add", "del", "rename", "up", "down", "move", "dup", "show", "hide", "toggle", "lock", "unlock", "opacity", "blend", "merge", "clear", "list"} {
				if strings.HasPrefix(sub, word) && strings.TrimSpace(head) == "layer" {
					cands = append(cands, sub)
				}
			}
		}
	}
	if len(cands) == 0 {
		a.msg = "no match"
		return
	}
	sort.Strings(cands)
	common := cands[0]
	for _, c := range cands[1:] {
		for !strings.HasPrefix(c, common) {
			common = common[:len(common)-1]
		}
	}
	if len(cands) == 1 && !strings.HasSuffix(common, string(filepath.Separator)) {
		common += " "
	}
	if len(cands) > 1 {
		a.msg = strings.Join(cands, "  ")
	}
	a.cmd = []rune(head + common + after)
	a.cmdPos = len([]rune(head + common))
}

// quote makes a string safe to put on a command line.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " '\"\\;\t") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func clampi(v, lo, hi int) int { return max(lo, min(v, hi)) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
