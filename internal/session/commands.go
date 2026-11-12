package session

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/rpmull/bf-interpreter/internal/pyxra"
)

// Cmd is one editor command, available from the TUI command line and the CLI.
type Cmd struct {
	Name    string // one or two words, e.g. "rect" or "layer add"
	Aliases []string
	Usage   string
	Help    string
	Group   string
	Edit    bool            // changes the document: runs as one undo step
	Flags   map[string]bool // --flag names; true when the flag takes a value
	Run     func(s *Session, a *Args) error
}

// Args are a command's parsed arguments.
type Args struct {
	Pos   []string
	flags map[string]string
	cmd   *Cmd
}

func (a *Args) Has(f string) bool    { _, ok := a.flags[f]; return ok }
func (a *Args) Flag(f string) string { return a.flags[f] }
func (a *Args) N() int               { return len(a.Pos) }

func (a *Args) usage() error { return fmt.Errorf("usage: %s %s", a.cmd.Name, a.cmd.Usage) }

func (a *Args) need(lo, hi int) error {
	if len(a.Pos) < lo || (hi >= 0 && len(a.Pos) > hi) {
		return a.usage()
	}
	return nil
}

func (a *Args) Int(i int) (int, error) {
	v, err := strconv.Atoi(a.Pos[i])
	if err != nil {
		return 0, fmt.Errorf("%q is not a number (usage: %s %s)", a.Pos[i], a.cmd.Name, a.cmd.Usage)
	}
	return v, nil
}

func (a *Args) Ints(from, n int) ([]int, error) {
	out := make([]int, n)
	for i := 0; i < n; i++ {
		v, err := a.Int(from + i)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (a *Args) FlagInt(f string, def int) (int, error) {
	v, ok := a.flags[f]
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("--%s needs a number", f)
	}
	return n, nil
}

var (
	registry []*Cmd
	byName   = map[string]*Cmd{}
)

func register(c *Cmd) {
	registry = append(registry, c)
	byName[c.Name] = c
	for _, a := range c.Aliases {
		byName[a] = c
	}
}

// Commands lists every command in registration order.
func Commands() []*Cmd { return registry }

// Lookup finds the command for a token list; it returns the command and how many tokens named it.
func Lookup(tokens []string) (*Cmd, int) {
	if len(tokens) >= 2 {
		if c, ok := byName[tokens[0]+" "+tokens[1]]; ok {
			return c, 2
		}
	}
	if len(tokens) >= 1 {
		if c, ok := byName[tokens[0]]; ok {
			return c, 1
		}
	}
	return nil, 0
}

// Split tokenizes a command line: shell-style quotes and backslashes; an unquoted ';'
// separates commands (quote or escape it to use ';' as a character).
func Split(line string) ([][]string, error) {
	var stmts [][]string
	var cur []string
	var tok strings.Builder
	inTok := false
	var quote byte
	flush := func() {
		if inTok {
			cur = append(cur, tok.String())
			tok.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(line) && (line[i+1] == '"' || line[i+1] == '\\') {
				i++
				tok.WriteByte(line[i])
			} else {
				tok.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inTok = c, true
		case c == '\\' && i+1 < len(line):
			i++
			tok.WriteByte(line[i])
			inTok = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == ';':
			flush()
			if len(cur) > 0 {
				stmts = append(stmts, cur)
			}
			cur = nil
		default:
			tok.WriteByte(c)
			inTok = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	if len(cur) > 0 {
		stmts = append(stmts, cur)
	}
	return stmts, nil
}

// Exec runs one command line (which may hold several ';'-separated commands).
func (s *Session) Exec(line string) error {
	stmts, err := Split(line)
	if err != nil {
		return err
	}
	for _, t := range stmts {
		if err := s.ExecTokens(t); err != nil {
			return err
		}
	}
	return nil
}

// ExecTokens runs one already-split command.
func (s *Session) ExecTokens(tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	c, n := Lookup(tokens)
	if c == nil {
		return fmt.Errorf("unknown command %q (try: help)", tokens[0])
	}
	a := &Args{flags: map[string]string{}, cmd: c}
	rest := tokens[n:]
	for i := 0; i < len(rest); i++ {
		t := rest[i]
		if len(t) > 2 && strings.HasPrefix(t, "--") {
			name, val, hasVal := strings.Cut(t[2:], "=")
			takes, ok := c.Flags[name]
			if !ok {
				return fmt.Errorf("%s: unknown option --%s (usage: %s %s)", c.Name, name, c.Name, c.Usage)
			}
			if takes && !hasVal {
				if i+1 >= len(rest) {
					return fmt.Errorf("%s: --%s needs a value", c.Name, name)
				}
				i++
				val = rest[i]
			}
			a.flags[name] = val
			continue
		}
		a.Pos = append(a.Pos, t)
	}
	if c.Edit {
		s.Begin(c.Name)
		defer s.End()
	}
	return c.Run(s, a)
}

// ----- argument helpers ---------------------------------------------------------------

func (s *Session) layerArg(a *Args, i int) (int, error) {
	if i < a.N() {
		return s.Doc.FindLayer(a.Pos[i])
	}
	return s.Layer, nil
}

// brushOr resolves an optional colour argument, defaulting to the brush.
func (s *Session) brushOr(a *Args, i int) (Color, error) {
	if i < a.N() {
		return s.Resolve(a.Pos[i])
	}
	return s.Resolve(string(s.Brush))
}

func (s *Session) drawn(n int, err error) error {
	if err != nil {
		return err
	}
	s.Printf("%d pixel(s) changed", n)
	return nil
}

func onOff(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off, got %q", v)
}

func (s *Session) frameArg(a *Args, i int) (int, error) {
	if i >= a.N() {
		return s.Frame, nil
	}
	n, err := a.Int(i)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > len(s.Doc.Frames) {
		return 0, fmt.Errorf("frame %d does not exist (1..%d)", n, len(s.Doc.Frames))
	}
	return n - 1, nil
}

// SavePath picks the default file name for a document that has none.
func (s *Session) SavePath() string {
	if s.Path != "" {
		return s.Path
	}
	name := s.Doc.Name
	if name == "" {
		name = "untitled"
	}
	return name + ".pyxra"
}

// Save writes the working file; a .pyxra or .json path becomes the working file.
func (s *Session) Save(path string) error {
	if path == "" {
		path = s.SavePath()
	}
	kind, err := s.Write(path, ExportOptions{Layer: -1, Frame: s.Frame})
	if err != nil {
		return err
	}
	if kind == KindPyxra || kind == KindNative {
		s.Path = path
		s.Dirty = false
		s.Printf("wrote %s (%dx%d, %d layer(s), %d frame(s))", path, s.Doc.W, s.Doc.H, len(s.Doc.Layers), len(s.Doc.Frames))
	} else {
		s.Printf("exported %s (%s); the working file is unchanged", path, kind)
	}
	return nil
}

// OpenPath replaces the document with a file.
func (s *Session) OpenPath(path string) error {
	d, notes, err := Open(path, s.PaletteName)
	if err != nil {
		return err
	}
	k := KindOf(path)
	keep := ""
	if k == KindPyxra || k == KindNative {
		keep = path
	}
	s.SetDoc(d, keep)
	s.Printf("opened %s (%dx%d, %d layer(s), %d frame(s))", path, d.W, d.H, len(d.Layers), len(d.Frames))
	for _, n := range notes {
		s.Printf("note: %s", n)
	}
	if keep == "" {
		s.Printf("save writes %s", s.SavePath())
	}
	return nil
}

func (s *Session) shapeCmd(a *Args, kind string) error {
	if err := a.need(4, 5); err != nil {
		return err
	}
	v, err := a.Ints(0, 4)
	if err != nil {
		return err
	}
	c, err := s.brushOr(a, 4)
	if err != nil {
		return err
	}
	var pts []pyxra.Point
	switch kind {
	case "line":
		pts = pyxra.LinePoints(v[0], v[1], v[2], v[3])
	case "rect":
		pts = pyxra.RectPoints(v[0], v[1], v[2], v[3], a.Has("fill"))
	case "ellipse":
		pts = pyxra.EllipsePoints(v[0], v[1], v[2], v[3], a.Has("fill"))
	}
	return s.drawn(s.Paint(pts, c))
}

// eachCel runs fn on the current cel, every frame of the layer (all), or every cel (doc).
func (s *Session) eachCel(a *Args, fn func(li, fi int) error) error {
	for li := range s.Doc.Layers {
		for fi := range s.Doc.Frames {
			if a.Has("doc") || (li == s.Layer && (a.Has("all") || fi == s.Frame)) {
				if err := fn(li, fi); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// transform moves every pixel of a cel through fn (pixels mapped off-canvas are dropped).
func (s *Session) transform(li, fi int, fn func(x, y int) (int, int)) error {
	cel := s.Doc.Cel(li, fi)
	if cel == nil {
		return nil
	}
	if err := s.checkLayer(li); err != nil {
		return err
	}
	src := cel.Snapshot()
	tmp := &pyxra.Cel{}
	tmp.Restore(src)
	s.touch(cel)
	d := s.Doc
	for i := range cel.Img.Pix {
		cel.Img.Pix[i] = 0
	}
	for y := 0; y < d.H; y++ {
		for x := 0; x < d.W; x++ {
			c := pyxra.GetPx(tmp.Img, x, y)
			if c.A == 0 {
				continue
			}
			nx, ny := fn(x, y)
			if nx >= 0 && ny >= 0 && nx < d.W && ny < d.H {
				pyxra.SetPx(cel.Img, nx, ny, c)
			}
		}
	}
	return nil
}

// ----- the commands -------------------------------------------------------------------

func init() {
	// File
	register(&Cmd{Name: "new", Group: "File", Usage: "WxH [name]", Help: "start a new document",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, -1); err != nil {
				return err
			}
			w, h, err := pyxra.ParseSize(a.Pos[0])
			if err != nil {
				return err
			}
			name := strings.Join(a.Pos[1:], " ")
			if name == "" {
				name = "untitled"
			}
			d, err := pyxra.New(w, h, name, "Layer 1")
			if err != nil {
				return err
			}
			s.SetDoc(d, "")
			s.Printf("new %dx%d document", w, h)
			return nil
		}})
	register(&Cmd{Name: "open", Aliases: []string{"e", "edit"}, Group: "File", Usage: "FILE",
		Help: "open a .pyxra, .pixra.json, .ops.json, .png or text file",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			return s.OpenPath(a.Pos[0])
		}})
	register(&Cmd{Name: "save", Aliases: []string{"w", "write"}, Group: "File", Usage: "[FILE]",
		Help: "save (.pyxra or .pixra.json becomes the working file; other types export)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(0, 1); err != nil {
				return err
			}
			p := ""
			if a.N() == 1 {
				p = a.Pos[0]
			}
			return s.Save(p)
		}})
	register(&Cmd{Name: "export", Group: "File",
		Usage: "FILE [--scale N] [--layer L] [--frame N] [--all-frames] [--all-layers] [--clear] [--add-layers]",
		Help:  "write .png (composite, or one --layer; --all-frames makes a sheet), .txt, .ops.json, .pixra.json or .pyxra",
		Flags: map[string]bool{"scale": true, "layer": true, "frame": true, "all-frames": false, "all-layers": false, "clear": false, "add-layers": false},
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			o := ExportOptions{Layer: -1, Frame: s.Frame, AllFrames: a.Has("all-frames"),
				AllLayers: a.Has("all-layers"), Clear: a.Has("clear"), AddLayers: a.Has("add-layers")}
			var err error
			if o.Scale, err = a.FlagInt("scale", 1); err != nil {
				return err
			}
			if a.Has("layer") {
				if o.Layer, err = s.Doc.FindLayer(a.Flag("layer")); err != nil {
					return err
				}
			}
			if a.Has("frame") {
				f, err := strconv.Atoi(a.Flag("frame"))
				if err != nil || f < 1 || f > len(s.Doc.Frames) {
					return fmt.Errorf("no frame %q", a.Flag("frame"))
				}
				o.Frame = f - 1
			}
			kind, err := s.Write(a.Pos[0], o)
			if err != nil {
				return err
			}
			s.Printf("exported %s (%s)", a.Pos[0], kind)
			return nil
		}})
	register(&Cmd{Name: "import", Group: "File", Edit: true, Usage: "FILE [--at X,Y] [--name NAME]",
		Help:  "add another file's layers above the current layer",
		Flags: map[string]bool{"at": true, "name": true},
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			x, y := 0, 0
			if a.Has("at") {
				if _, err := fmt.Sscanf(a.Flag("at"), "%d,%d", &x, &y); err != nil {
					return fmt.Errorf("--at needs X,Y")
				}
			}
			names, err := s.Import(a.Pos[0], x, y, a.Flag("name"))
			if err != nil {
				return err
			}
			s.Printf("imported layer(s): %s", strings.Join(names, ", "))
			return nil
		}})
	register(&Cmd{Name: "apply", Group: "File", Edit: true, Usage: "OPS.json",
		Help: "apply a Pyxra edit ops file (pixels, rect, line, ellipse, fill, clear, layer and recolor ops)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			notes, err := s.ApplyFile(a.Pos[0])
			for _, n := range notes {
				s.Printf("note: %s", n)
			}
			if err == nil {
				s.Printf("applied %s", a.Pos[0])
			}
			return err
		}})
	register(&Cmd{Name: "info", Group: "File", Help: "describe the document",
		Run: func(s *Session, a *Args) error {
			d := s.Doc
			path := s.Path
			if path == "" {
				path = "(not saved)"
			}
			s.Printf("name     %s", d.Name)
			s.Printf("file     %s%s", path, map[bool]string{true: " (modified)"}[s.Dirty])
			s.Printf("size     %dx%d", d.W, d.H)
			s.Printf("layers   %d (current: %s)", len(d.Layers), d.Layers[s.Layer].Name)
			s.Printf("frames   %d (current: %d)", len(d.Frames), s.Frame+1)
			s.Printf("tags     %d", len(d.Tags))
			s.Printf("cels     %d", len(d.Cels))
			s.Printf("palette  %d swatch(es)", len(d.Palette))
			s.Printf("legend   %d character(s)", len(s.legendOrder))
			if keys := pyxra.SortedKeys(d.Extra); len(keys) > 0 {
				s.Printf("other    %s (kept as-is)", strings.Join(keys, ", "))
			}
			return nil
		}})
	register(&Cmd{Name: "rename", Aliases: []string{"name"}, Group: "File", Edit: true, Usage: "NAME",
		Help: "set the document name",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, -1); err != nil {
				return err
			}
			s.Doc.Name = strings.Join(a.Pos, " ")
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "quit", Aliases: []string{"q"}, Group: "File", Help: "quit (refuses with unsaved changes)",
		Run: func(s *Session, a *Args) error {
			if s.Dirty {
				return fmt.Errorf("unsaved changes: save first, or quit! to discard")
			}
			s.Quit = true
			return nil
		}})
	register(&Cmd{Name: "quit!", Aliases: []string{"q!"}, Group: "File", Help: "quit, discarding changes",
		Run: func(s *Session, a *Args) error { s.Quit = true; return nil }})
	register(&Cmd{Name: "wq", Aliases: []string{"x"}, Group: "File", Usage: "[FILE]", Help: "save and quit",
		Run: func(s *Session, a *Args) error {
			p := ""
			if a.N() > 0 {
				p = a.Pos[0]
			}
			if err := s.Save(p); err != nil {
				return err
			}
			s.Quit = true
			return nil
		}})

	// Canvas
	register(&Cmd{Name: "resize", Group: "Canvas", Edit: true, Usage: "WxH [X Y] [--center]",
		Help:  "change the canvas size; old content moves to X,Y (or the centre)",
		Flags: map[string]bool{"center": false},
		Run: func(s *Session, a *Args) error {
			if a.N() != 1 && a.N() != 3 {
				return a.usage()
			}
			w, h, err := pyxra.ParseSize(a.Pos[0])
			if err != nil {
				return err
			}
			x, y := 0, 0
			if a.N() == 3 {
				v, err := a.Ints(1, 2)
				if err != nil {
					return err
				}
				x, y = v[0], v[1]
			} else if a.Has("center") {
				x, y = (w-s.Doc.W)/2, (h-s.Doc.H)/2
			}
			if err := s.Doc.Resize(w, h, x, y); err != nil {
				return err
			}
			s.structural()
			s.clampState()
			s.Printf("canvas is %dx%d", w, h)
			return nil
		}})
	register(&Cmd{Name: "crop", Group: "Canvas", Edit: true, Usage: "[X Y W H]",
		Help: "crop the canvas to a box, or to the bounds of everything drawn",
		Run: func(s *Session, a *Args) error {
			d := s.Doc
			var x, y, w, h int
			switch a.N() {
			case 4:
				v, err := a.Ints(0, 4)
				if err != nil {
					return err
				}
				x, y, w, h = v[0], v[1], v[2], v[3]
			case 0:
				x0, y0, x1, y1 := d.W, d.H, -1, -1
				for _, c := range d.Cels {
					for py := 0; py < d.H; py++ {
						for px := 0; px < d.W; px++ {
							if c.Img.Pix[py*c.Img.Stride+px*4+3] != 0 {
								x0, y0, x1, y1 = min(x0, px), min(y0, py), max(x1, px), max(y1, py)
							}
						}
					}
				}
				if x1 < 0 {
					return fmt.Errorf("nothing drawn to crop to")
				}
				x, y, w, h = x0, y0, x1-x0+1, y1-y0+1
			default:
				return a.usage()
			}
			if err := d.Resize(w, h, -x, -y); err != nil {
				return err
			}
			s.structural()
			s.clampState()
			s.Printf("canvas is %dx%d", w, h)
			return nil
		}})

	// Layers
	register(&Cmd{Name: "layers", Aliases: []string{"layer list", "ls"}, Group: "Layers", Help: "list layers, top first",
		Run: func(s *Session, a *Args) error {
			d := s.Doc
			for li := len(d.Layers) - 1; li >= 0; li-- {
				l := d.Layers[li]
				mark := " "
				if li == s.Layer {
					mark = ">"
				}
				cels := 0
				for fi := range d.Frames {
					if d.Cel(li, fi) != nil {
						cels++
					}
				}
				flags := ""
				if !l.Visible {
					flags += " hidden"
				}
				if l.Locked {
					flags += " locked"
				}
				if l.Opacity != 255 {
					flags += fmt.Sprintf(" opacity=%d", l.Opacity)
				}
				if l.Blend != "" && l.Blend != "normal" {
					flags += " blend=" + l.Blend
				}
				s.Printf("%s %2d  %-20s #%d  %d cel(s)%s", mark, li+1, l.Name, l.ID, cels, flags)
			}
			return nil
		}})
	register(&Cmd{Name: "layer", Group: "Layers", Usage: "L", Help: "select a layer by name, #id or number (1 = bottom)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, -1); err != nil {
				return err
			}
			li, err := s.Doc.FindLayer(strings.Join(a.Pos, " "))
			if err != nil {
				return err
			}
			s.Layer = li
			s.Printf("layer %s", s.Doc.Layers[li].Name)
			return nil
		}})
	register(&Cmd{Name: "layer add", Aliases: []string{"layer new"}, Group: "Layers", Edit: true,
		Usage: "NAME [--below] [--top]", Help: "add an empty layer above the current one (or below it, or on top)",
		Flags: map[string]bool{"below": false, "top": false},
		Run: func(s *Session, a *Args) error {
			name := strings.Join(a.Pos, " ")
			if name == "" {
				name = s.uniqueLayerName("Layer")
			}
			at := s.Layer + 1
			if a.Has("below") {
				at = s.Layer
			}
			if a.Has("top") {
				at = len(s.Doc.Layers)
			}
			s.Layer = s.Doc.AddLayer(name, at)
			s.structural()
			s.Printf("added layer %s", name)
			return nil
		}})
	register(&Cmd{Name: "layer del", Aliases: []string{"layer rm", "layer delete"}, Group: "Layers", Edit: true,
		Usage: "[L]", Help: "delete a layer and its cels",
		Run: func(s *Session, a *Args) error {
			li, err := s.layerArg(a, 0)
			if err != nil {
				return err
			}
			name := s.Doc.Layers[li].Name
			if err := s.Doc.DeleteLayer(li); err != nil {
				return err
			}
			s.structural()
			s.clampState()
			s.Printf("deleted layer %s", name)
			return nil
		}})
	register(&Cmd{Name: "layer rename", Group: "Layers", Edit: true, Usage: "[L] NAME",
		Help: "rename the current layer (or L, when two or more words are given and the first names a layer)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, -1); err != nil {
				return err
			}
			li, name := s.Layer, strings.Join(a.Pos, " ")
			if a.N() >= 2 {
				if l, err := s.Doc.FindLayer(a.Pos[0]); err == nil {
					li, name = l, strings.Join(a.Pos[1:], " ")
				}
			}
			s.Doc.Layers[li].Name = name
			s.structural()
			return nil
		}})
	move := func(delta int) func(s *Session, a *Args) error {
		return func(s *Session, a *Args) error {
			li, err := s.layerArg(a, 0)
			if err != nil {
				return err
			}
			to := clamp(li+delta, 0, len(s.Doc.Layers)-1)
			s.Doc.MoveLayer(li, to)
			if li == s.Layer {
				s.Layer = to
			}
			s.structural()
			return nil
		}
	}
	register(&Cmd{Name: "layer up", Aliases: []string{"layer raise"}, Group: "Layers", Edit: true, Usage: "[L]", Help: "move a layer up", Run: move(1)})
	register(&Cmd{Name: "layer down", Aliases: []string{"layer lower"}, Group: "Layers", Edit: true, Usage: "[L]", Help: "move a layer down", Run: move(-1)})
	register(&Cmd{Name: "layer move", Group: "Layers", Edit: true, Usage: "L POS", Help: "move a layer to position POS (1 = bottom)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			li, err := s.Doc.FindLayer(a.Pos[0])
			if err != nil {
				return err
			}
			pos, err := a.Int(1)
			if err != nil {
				return err
			}
			cur := s.Doc.Layers[s.Layer].ID
			s.Doc.MoveLayer(li, pos-1)
			s.Layer, _ = s.Doc.FindLayer(fmt.Sprintf("#%d", cur))
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "layer dup", Aliases: []string{"layer duplicate"}, Group: "Layers", Edit: true, Usage: "[L] [--name NAME]",
		Help: "duplicate a layer (all frames)", Flags: map[string]bool{"name": true},
		Run: func(s *Session, a *Args) error {
			li, err := s.layerArg(a, 0)
			if err != nil {
				return err
			}
			name := a.Flag("name")
			if name == "" {
				name = s.uniqueLayerName(s.Doc.Layers[li].Name + " copy")
			}
			s.Layer = s.Doc.DuplicateLayer(li, name)
			s.structural()
			s.Printf("added layer %s", name)
			return nil
		}})
	setter := func(apply func(l *pyxra.Layer)) func(s *Session, a *Args) error {
		return func(s *Session, a *Args) error {
			li, err := s.layerArg(a, 0)
			if err != nil {
				return err
			}
			apply(&s.Doc.Layers[li])
			s.structural()
			return nil
		}
	}
	register(&Cmd{Name: "layer show", Group: "Layers", Edit: true, Usage: "[L]", Help: "make a layer visible", Run: setter(func(l *pyxra.Layer) { l.Visible = true })})
	register(&Cmd{Name: "layer hide", Group: "Layers", Edit: true, Usage: "[L]", Help: "hide a layer", Run: setter(func(l *pyxra.Layer) { l.Visible = false })})
	register(&Cmd{Name: "layer toggle", Group: "Layers", Edit: true, Usage: "[L]", Help: "toggle a layer's visibility", Run: setter(func(l *pyxra.Layer) { l.Visible = !l.Visible })})
	register(&Cmd{Name: "layer lock", Group: "Layers", Edit: true, Usage: "[L]", Help: "lock a layer against edits", Run: setter(func(l *pyxra.Layer) { l.Locked = true })})
	register(&Cmd{Name: "layer unlock", Group: "Layers", Edit: true, Usage: "[L]", Help: "unlock a layer", Run: setter(func(l *pyxra.Layer) { l.Locked = false })})
	register(&Cmd{Name: "layer opacity", Group: "Layers", Edit: true, Usage: "[L] N|N%", Help: "set opacity, 0-255 or a percentage",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 2); err != nil {
				return err
			}
			li, err := s.layerArg(&Args{Pos: a.Pos[:a.N()-1]}, 0)
			if err != nil {
				return err
			}
			v := a.Pos[a.N()-1]
			pct := strings.HasSuffix(v, "%")
			n, err := strconv.Atoi(strings.TrimSuffix(v, "%"))
			if err != nil {
				return a.usage()
			}
			if pct {
				n = (n*255 + 50) / 100
			}
			s.Doc.Layers[li].Opacity = clamp(n, 0, 255)
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "layer blend", Group: "Layers", Edit: true, Usage: "[L] MODE",
		Help: "set the blend mode (normal multiply screen overlay darken lighten add subtract difference)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 2); err != nil {
				return err
			}
			li, err := s.layerArg(&Args{Pos: a.Pos[:a.N()-1]}, 0)
			if err != nil {
				return err
			}
			s.Doc.Layers[li].Blend = a.Pos[a.N()-1]
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "layer merge", Group: "Layers", Edit: true, Usage: "[L]", Help: "merge a layer into the one below",
		Run: func(s *Session, a *Args) error {
			li, err := s.layerArg(a, 0)
			if err != nil {
				return err
			}
			if err := s.checkLayer(li); err != nil {
				return err
			}
			if li > 0 {
				if err := s.checkLayer(li - 1); err != nil {
					return err
				}
				for fi := range s.Doc.Frames {
					if c := s.Doc.Cel(li-1, fi); c != nil {
						s.touch(c)
					}
				}
			}
			if err := s.Doc.MergeDown(li); err != nil {
				return err
			}
			s.structural()
			s.Layer = li - 1
			for _, c := range s.Doc.UsedColors() {
				s.charFor(c)
			}
			return nil
		}})
	register(&Cmd{Name: "layer clear", Group: "Layers", Edit: true, Usage: "[L] [--all]",
		Help: "erase a layer at the current frame (or every frame)", Flags: map[string]bool{"all": false},
		Run: func(s *Session, a *Args) error {
			li, err := s.layerArg(a, 0)
			if err != nil {
				return err
			}
			for fi := range s.Doc.Frames {
				if fi == s.Frame || a.Has("all") {
					if err := s.clearCel(li, fi, nil); err != nil {
						return err
					}
				}
			}
			return nil
		}})

	// Frames and tags
	register(&Cmd{Name: "frames", Aliases: []string{"frame list"}, Group: "Frames", Help: "list frames",
		Run: func(s *Session, a *Args) error {
			for fi, f := range s.Doc.Frames {
				mark := " "
				if fi == s.Frame {
					mark = ">"
				}
				tags := strings.Join(s.Doc.FrameTags(fi), ", ")
				s.Printf("%s %3d  %4dms  %s", mark, fi+1, f.Duration, tags)
			}
			return nil
		}})
	register(&Cmd{Name: "frame", Group: "Frames", Usage: "N", Help: "go to frame N (1-based)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			fi, err := s.frameArg(a, 0)
			if err != nil {
				return err
			}
			s.Frame = fi
			return nil
		}})
	register(&Cmd{Name: "frame add", Aliases: []string{"frame new"}, Group: "Frames", Edit: true, Usage: "[N]",
		Help: "insert an empty frame after the current one (or at position N)",
		Run: func(s *Session, a *Args) error {
			at := s.Frame + 1
			if a.N() > 0 {
				n, err := a.Int(0)
				if err != nil {
					return err
				}
				at = n - 1
			}
			s.Frame = s.Doc.AddFrame(at, -1)
			s.structural()
			s.Printf("frame %d added", s.Frame+1)
			return nil
		}})
	register(&Cmd{Name: "frame dup", Aliases: []string{"frame duplicate"}, Group: "Frames", Edit: true, Usage: "[N]",
		Help: "duplicate a frame after itself",
		Run: func(s *Session, a *Args) error {
			fi, err := s.frameArg(a, 0)
			if err != nil {
				return err
			}
			s.Frame = s.Doc.AddFrame(fi+1, fi)
			s.structural()
			s.Printf("frame %d added", s.Frame+1)
			return nil
		}})
	register(&Cmd{Name: "frame del", Aliases: []string{"frame rm", "frame delete"}, Group: "Frames", Edit: true, Usage: "[N]",
		Help: "delete a frame",
		Run: func(s *Session, a *Args) error {
			fi, err := s.frameArg(a, 0)
			if err != nil {
				return err
			}
			if err := s.Doc.DeleteFrame(fi); err != nil {
				return err
			}
			s.structural()
			s.clampState()
			return nil
		}})
	register(&Cmd{Name: "frame move", Group: "Frames", Edit: true, Usage: "N POS", Help: "move frame N to position POS",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			fi, err := s.frameArg(a, 0)
			if err != nil {
				return err
			}
			pos, err := a.Int(1)
			if err != nil {
				return err
			}
			s.Doc.MoveFrame(fi, pos-1)
			s.Frame = clamp(pos-1, 0, len(s.Doc.Frames)-1)
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "frame duration", Group: "Frames", Edit: true, Usage: "[N] MS", Help: "set a frame's duration in milliseconds",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 2); err != nil {
				return err
			}
			fi := s.Frame
			var err error
			if a.N() == 2 {
				if fi, err = s.frameArg(a, 0); err != nil {
					return err
				}
			}
			ms, err := a.Int(a.N() - 1)
			if err != nil {
				return err
			}
			s.Doc.Frames[fi].Duration = ms
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "tags", Aliases: []string{"tag list"}, Group: "Frames", Help: "list tags",
		Run: func(s *Session, a *Args) error {
			for _, t := range s.Doc.Tags {
				s.Printf("%-16s frames %d-%d", t.Name, t.From+1, t.To+1)
			}
			return nil
		}})
	register(&Cmd{Name: "tag add", Group: "Frames", Edit: true, Usage: "NAME FROM TO", Help: "tag frames FROM..TO (1-based, inclusive)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(3, 3); err != nil {
				return err
			}
			v, err := a.Ints(1, 2)
			if err != nil {
				return err
			}
			if v[0] < 1 || v[1] < v[0] || v[1] > len(s.Doc.Frames) {
				return fmt.Errorf("frames must be within 1..%d", len(s.Doc.Frames))
			}
			s.Doc.Tags = append(s.Doc.Tags, pyxra.Tag{Name: a.Pos[0], From: v[0] - 1, To: v[1] - 1, Extra: pyxra.Extra{}})
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "tag del", Aliases: []string{"tag rm"}, Group: "Frames", Edit: true, Usage: "NAME", Help: "remove a tag",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			for i, t := range s.Doc.Tags {
				if t.Name == a.Pos[0] {
					s.Doc.Tags = append(s.Doc.Tags[:i:i], s.Doc.Tags[i+1:]...)
					s.structural()
					return nil
				}
			}
			return fmt.Errorf("no tag %q", a.Pos[0])
		}})

	// Colours
	register(&Cmd{Name: "legend", Group: "Colours", Help: "list characters and their colours",
		Run: func(s *Session, a *Args) error {
			inPal := map[Color]bool{}
			for _, p := range s.Doc.Palette {
				inPal[p.Color] = true
			}
			for _, e := range s.Legend() {
				mark := " "
				if e.Char == s.Brush {
					mark = ">"
				}
				pal := ""
				if inPal[e.Color] {
					pal = "  (palette)"
				}
				s.Printf("%s %c  %s%s", mark, e.Char, pyxra.Hex(e.Color), pal)
			}
			return nil
		}})
	register(&Cmd{Name: "color", Aliases: []string{"colour"}, Group: "Colours", Edit: true, Usage: "C #HEX",
		Help: "give character C a colour; pixels already drawn with C change everywhere",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			if len(a.Pos[0]) != 1 || !ValidChar(a.Pos[0][0]) {
				return fmt.Errorf("%q is not a colour character", a.Pos[0])
			}
			ch := a.Pos[0][0]
			c, err := pyxra.ParseColor(a.Pos[1])
			if err != nil {
				return err
			}
			if c.A == 0 {
				return fmt.Errorf("'.' is the transparent character; use: replace %c .", ch)
			}
			if other, ok := s.legendChar[c]; ok && other != ch {
				return fmt.Errorf("%s is already %q (rename it with: char %c %c)", pyxra.Hex(c), other, other, ch)
			}
			s.structural()
			if old, ok := s.legendColor[ch]; ok {
				// Re-point the character first so the recolour keeps it.
				delete(s.legendChar, old)
				s.legendColor[ch] = c
				s.legendChar[c] = ch
				n := s.recolorAll(old, c, -1)
				for i := range s.Doc.Palette {
					if s.Doc.Palette[i].Color == old && s.Doc.Palette[i].Raw == nil {
						s.Doc.Palette[i].Color = c
					}
				}
				s.Printf("%c = %s (%d pixel(s) recoloured)", ch, pyxra.Hex(c), n)
				return nil
			}
			s.bind(ch, c)
			s.addSwatch(c)
			s.Printf("%c = %s", ch, pyxra.Hex(c))
			return nil
		}})
	register(&Cmd{Name: "char", Group: "Colours", Edit: true, Usage: "OLD NEW", Help: "change which character stands for a colour",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			if len(a.Pos[0]) != 1 || len(a.Pos[1]) != 1 || !ValidChar(a.Pos[1][0]) {
				return a.usage()
			}
			o, n := a.Pos[0][0], a.Pos[1][0]
			c, ok := s.legendColor[o]
			if !ok {
				return fmt.Errorf("%q has no colour", a.Pos[0])
			}
			if _, used := s.legendColor[n]; used {
				return fmt.Errorf("%q is already used", a.Pos[1])
			}
			s.structural()
			s.unbind(o)
			s.bind(n, c)
			if s.Brush == o {
				s.Brush = n
			}
			return nil
		}})
	register(&Cmd{Name: "brush", Group: "Colours", Usage: "C|#HEX", Help: "choose the drawing character (a new one gets a palette colour)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			s.Begin("brush")
			defer s.End()
			c, err := s.Resolve(a.Pos[0])
			if err != nil {
				return err
			}
			s.Brush = s.CharOf(c)
			s.Printf("brush %c %s", s.Brush, pyxra.Hex(c))
			return nil
		}})
	register(&Cmd{Name: "palette", Group: "Colours", Help: "list the document's palette swatches",
		Run: func(s *Session, a *Args) error {
			for i, p := range s.Doc.Palette {
				if p.Color.A == 0 && p.Raw != nil {
					s.Printf("%3d  %s", i+1, string(p.Raw))
					continue
				}
				s.Printf("%3d  %c  %s", i+1, s.CharOf(p.Color), pyxra.Hex(p.Color))
			}
			s.Printf("new characters take colours from: palette swatches, then %s", s.PaletteName)
			return nil
		}})
	register(&Cmd{Name: "palette add", Group: "Colours", Edit: true, Usage: "#HEX...", Help: "add swatches (each gets a character)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, -1); err != nil {
				return err
			}
			for _, h := range a.Pos {
				c, err := pyxra.ParseColor(h)
				if err != nil {
					return err
				}
				s.structural()
				s.addSwatch(c)
				s.charFor(c)
			}
			return nil
		}})
	register(&Cmd{Name: "palette rm", Aliases: []string{"palette del"}, Group: "Colours", Edit: true, Usage: "#HEX|C",
		Help: "remove a swatch (pixels keep their colour)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			var c Color
			var err error
			if len(a.Pos[0]) == 1 {
				var ok bool
				if c, ok = s.legendColor[a.Pos[0][0]]; !ok {
					return fmt.Errorf("%q has no colour", a.Pos[0])
				}
			} else if c, err = pyxra.ParseColor(a.Pos[0]); err != nil {
				return err
			}
			for i, p := range s.Doc.Palette {
				if p.Color == c {
					s.Doc.Palette = append(s.Doc.Palette[:i:i], s.Doc.Palette[i+1:]...)
					s.structural()
					return nil
				}
			}
			return fmt.Errorf("%s is not in the palette", pyxra.Hex(c))
		}})
	register(&Cmd{Name: "palette load", Group: "Colours", Edit: true, Usage: "NAME",
		Help: "add a built-in palette's colours as swatches (pico8 gameboy nes c64)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			cols, ok := pyxra.Palettes[a.Pos[0]]
			if !ok {
				return fmt.Errorf("no palette %q (have %s)", a.Pos[0], paletteNames())
			}
			for _, h := range cols {
				c, _ := pyxra.ParseColor(h)
				s.addSwatch(c)
				s.charFor(c)
			}
			s.structural()
			return nil
		}})
	register(&Cmd{Name: "recolor", Aliases: []string{"recolour"}, Group: "Colours", Edit: true, Usage: "FROM TO [--layer L]",
		Help: "swap one colour (character or #hex) for another on every layer and frame", Flags: map[string]bool{"layer": true},
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			from, err := s.Resolve(a.Pos[0])
			if err != nil {
				return err
			}
			to, err := s.Resolve(a.Pos[1])
			if err != nil {
				return err
			}
			li := -1
			if a.Has("layer") {
				if li, err = s.Doc.FindLayer(a.Flag("layer")); err != nil {
					return err
				}
			}
			s.Printf("%d pixel(s) recoloured", s.recolorAll(from, to, li))
			return nil
		}})

	// Drawing
	register(&Cmd{Name: "px", Aliases: []string{"pixel", "set"}, Group: "Draw", Edit: true, Usage: "X Y [C]",
		Help: "set one pixel (C is a character or #hex; default the brush)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 3); err != nil {
				return err
			}
			v, err := a.Ints(0, 2)
			if err != nil {
				return err
			}
			c, err := s.brushOr(a, 2)
			if err != nil {
				return err
			}
			return s.drawn(s.Paint([]pyxra.Point{{X: v[0], Y: v[1]}}, c))
		}})
	register(&Cmd{Name: "erase", Group: "Draw", Edit: true, Usage: "X Y", Help: "make one pixel transparent",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			v, err := a.Ints(0, 2)
			if err != nil {
				return err
			}
			return s.drawn(s.Paint([]pyxra.Point{{X: v[0], Y: v[1]}}, pyxra.Transparent))
		}})
	register(&Cmd{Name: "line", Group: "Draw", Edit: true, Usage: "X0 Y0 X1 Y1 [C]", Help: "draw a line",
		Run: func(s *Session, a *Args) error { return s.shapeCmd(a, "line") }})
	register(&Cmd{Name: "rect", Group: "Draw", Edit: true, Usage: "X0 Y0 X1 Y1 [C] [--fill]", Help: "draw a rectangle between corners",
		Flags: map[string]bool{"fill": false}, Run: func(s *Session, a *Args) error { return s.shapeCmd(a, "rect") }})
	register(&Cmd{Name: "ellipse", Aliases: []string{"oval"}, Group: "Draw", Edit: true, Usage: "X0 Y0 X1 Y1 [C] [--fill]",
		Help: "draw the ellipse inside a box", Flags: map[string]bool{"fill": false},
		Run: func(s *Session, a *Args) error { return s.shapeCmd(a, "ellipse") }})
	register(&Cmd{Name: "fill", Aliases: []string{"flood"}, Group: "Draw", Edit: true, Usage: "X Y [C]",
		Help: "flood-fill the area of one colour",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 3); err != nil {
				return err
			}
			v, err := a.Ints(0, 2)
			if err != nil {
				return err
			}
			c, err := s.brushOr(a, 2)
			if err != nil {
				return err
			}
			img := s.Doc.Cel(s.Layer, s.Frame)
			var pts []pyxra.Point
			if img == nil {
				pts = pyxra.RectPoints(0, 0, s.Doc.W-1, s.Doc.H-1, true)
				if v[0] < 0 || v[1] < 0 || v[0] >= s.Doc.W || v[1] >= s.Doc.H {
					pts = nil
				}
			} else {
				pts = pyxra.FloodPoints(img.Img, v[0], v[1])
			}
			return s.drawn(s.Paint(pts, c))
		}})
	register(&Cmd{Name: "text", Group: "Draw", Edit: true, Usage: "X Y STRING...",
		Help: "write characters as pixels left to right (spaces skip, '.' erases)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(3, -1); err != nil {
				return err
			}
			v, err := a.Ints(0, 2)
			if err != nil {
				return err
			}
			return s.drawn(s.drawText(v[0], v[1], []string{strings.Join(a.Pos[2:], " ")}, true))
		}})
	register(&Cmd{Name: "pixels", Group: "Draw", Edit: true, Usage: "X Y ROW... [--erase]",
		Help:  "write rows of characters like a Pyxra pixels op ('.' leaves pixels alone unless --erase)",
		Flags: map[string]bool{"erase": false},
		Run: func(s *Session, a *Args) error {
			if err := a.need(3, -1); err != nil {
				return err
			}
			v, err := a.Ints(0, 2)
			if err != nil {
				return err
			}
			return s.drawn(s.drawText(v[0], v[1], a.Pos[2:], a.Has("erase")))
		}})
	register(&Cmd{Name: "paste", Group: "Draw", Edit: true, Usage: "FILE [X Y] [--erase]",
		Help: "draw a text file's rows onto the current layer", Flags: map[string]bool{"erase": false},
		Run: func(s *Session, a *Args) error {
			if a.N() != 1 && a.N() != 3 {
				return a.usage()
			}
			x, y := 0, 0
			if a.N() == 3 {
				v, err := a.Ints(1, 2)
				if err != nil {
					return err
				}
				x, y = v[0], v[1]
			}
			data, err := os.ReadFile(a.Pos[0])
			if err != nil {
				return err
			}
			rows := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
			return s.drawn(s.drawText(x, y, rows, a.Has("erase")))
		}})
	register(&Cmd{Name: "clear", Group: "Draw", Edit: true, Usage: "[--all]",
		Help: "erase the current layer at this frame (or every frame)", Flags: map[string]bool{"all": false},
		Run: func(s *Session, a *Args) error {
			for fi := range s.Doc.Frames {
				if fi == s.Frame || a.Has("all") {
					if err := s.clearCel(s.Layer, fi, nil); err != nil {
						return err
					}
				}
			}
			return nil
		}})
	register(&Cmd{Name: "replace", Group: "Draw", Edit: true, Usage: "A B [--all] [--doc]",
		Help:  "replace colour A with B on this layer and frame (--all: every frame; --doc: everywhere)",
		Flags: map[string]bool{"all": false, "doc": false},
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			from, err := s.Resolve(a.Pos[0])
			if err != nil {
				return err
			}
			to, err := s.Resolve(a.Pos[1])
			if err != nil {
				return err
			}
			total := 0
			err = s.eachCel(a, func(li, fi int) error {
				cel := s.Doc.Cel(li, fi)
				if cel == nil {
					return nil
				}
				var pts []pyxra.Point
				for y := 0; y < s.Doc.H; y++ {
					for x := 0; x < s.Doc.W; x++ {
						if pyxra.GetPx(cel.Img, x, y) == from {
							pts = append(pts, pyxra.Point{X: x, Y: y})
						}
					}
				}
				n, err := s.PaintOn(li, fi, pts, to)
				total += n
				return err
			})
			return s.drawn(total, err)
		}})
	register(&Cmd{Name: "shift", Aliases: []string{"nudge"}, Group: "Draw", Edit: true, Usage: "DX DY [--all] [--doc]",
		Help:  "move pixels (current cel; --all: every frame of the layer; --doc: everything)",
		Flags: map[string]bool{"all": false, "doc": false},
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			v, err := a.Ints(0, 2)
			if err != nil {
				return err
			}
			return s.eachCel(a, func(li, fi int) error {
				return s.transform(li, fi, func(x, y int) (int, int) { return x + v[0], y + v[1] })
			})
		}})
	register(&Cmd{Name: "flip", Aliases: []string{"mirror"}, Group: "Draw", Edit: true, Usage: "h|v [--all] [--doc]",
		Help:  "mirror pixels horizontally or vertically",
		Flags: map[string]bool{"all": false, "doc": false},
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			w, h := s.Doc.W, s.Doc.H
			var fn func(x, y int) (int, int)
			switch a.Pos[0] {
			case "h", "x", "horizontal":
				fn = func(x, y int) (int, int) { return w - 1 - x, y }
			case "v", "y", "vertical":
				fn = func(x, y int) (int, int) { return x, h - 1 - y }
			default:
				return a.usage()
			}
			return s.eachCel(a, func(li, fi int) error { return s.transform(li, fi, fn) })
		}})
	register(&Cmd{Name: "paint", Group: "Draw", Edit: true, Usage: "[C]", Help: "paint at the cursor",
		Run: func(s *Session, a *Args) error {
			c, err := s.brushOr(a, 0)
			if err != nil {
				return err
			}
			_, err = s.Paint([]pyxra.Point{{X: s.CX, Y: s.CY}}, c)
			return err
		}})

	// History
	register(&Cmd{Name: "undo", Aliases: []string{"u"}, Group: "History", Help: "undo the last change",
		Run: func(s *Session, a *Args) error {
			label, ok := s.Undo()
			if !ok {
				return fmt.Errorf("nothing to undo")
			}
			s.Printf("undid %s", label)
			return nil
		}})
	register(&Cmd{Name: "redo", Group: "History", Help: "redo the last undone change",
		Run: func(s *Session, a *Args) error {
			label, ok := s.Redo()
			if !ok {
				return fmt.Errorf("nothing to redo")
			}
			s.Printf("redid %s", label)
			return nil
		}})

	// View
	register(&Cmd{Name: "print", Aliases: []string{"rows"}, Group: "View", Usage: "[--layer L] [--frame N]",
		Help: "print the frame as ASCII (topmost visible character, or one layer)", Flags: map[string]bool{"layer": true, "frame": true},
		Run: func(s *Session, a *Args) error {
			li, fi := -1, s.Frame
			var err error
			if a.Has("layer") {
				if li, err = s.Doc.FindLayer(a.Flag("layer")); err != nil {
					return err
				}
			}
			if a.Has("frame") {
				n, err := strconv.Atoi(a.Flag("frame"))
				if err != nil || n < 1 || n > len(s.Doc.Frames) {
					return fmt.Errorf("no frame %q", a.Flag("frame"))
				}
				fi = n - 1
			}
			for _, r := range s.Rows(fi, li) {
				s.Printf("%s", r)
			}
			return nil
		}})
	register(&Cmd{Name: "goto", Group: "View", Usage: "X Y", Help: "move the cursor",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			v, err := a.Ints(0, 2)
			if err != nil {
				return err
			}
			s.CX, s.CY = v[0], v[1]
			s.clampState()
			return nil
		}})
	register(&Cmd{Name: "zoom", Group: "View", Usage: "in|out|1/8|1/4|1/2|1|2|4|8", Help: "set the zoom (cells per pixel)",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			switch a.Pos[0] {
			case "in", "+":
				s.Zoom = min(s.Zoom+1, MaxZoom)
			case "out", "-":
				s.Zoom = max(s.Zoom-1, MinZoom)
			default:
				z, ok := map[string]int{"1/8": -3, "1/4": -2, "1/2": -1, "1": 0, "2": 1, "4": 2, "8": 3}[a.Pos[0]]
				if !ok {
					return a.usage()
				}
				s.Zoom = z
			}
			s.Printf("zoom %s", ZoomLabel(s.Zoom))
			return nil
		}})
	register(&Cmd{Name: "view", Aliases: []string{"mode"}, Group: "View", Usage: "color|mixed|text",
		Help: "colour blocks, colour blocks with characters, or plain text",
		Run: func(s *Session, a *Args) error {
			if err := a.need(1, 1); err != nil {
				return err
			}
			switch a.Pos[0] {
			case ViewColor, "colour", ViewMixed, ViewText, "plain", "ascii":
			default:
				return a.usage()
			}
			s.View = map[string]string{"colour": ViewColor, "plain": ViewText, "ascii": ViewText}[a.Pos[0]]
			if s.View == "" {
				s.View = a.Pos[0]
			}
			return nil
		}})
	register(&Cmd{Name: "solo", Group: "View", Usage: "[on|off]", Help: "show only the current layer",
		Run: func(s *Session, a *Args) error {
			if a.N() == 0 {
				s.Solo = !s.Solo
				return nil
			}
			v, err := onOff(a.Pos[0])
			s.Solo = v
			return err
		}})
	register(&Cmd{Name: "set", Group: "View", Usage: "autolegend on|off | palette NAME",
		Help: "editor settings: auto-colour new characters; built-in palette for new colours",
		Run: func(s *Session, a *Args) error {
			if err := a.need(2, 2); err != nil {
				return err
			}
			switch a.Pos[0] {
			case "autolegend":
				v, err := onOff(a.Pos[1])
				s.AutoLegend = v
				return err
			case "palette":
				if _, ok := pyxra.Palettes[a.Pos[1]]; !ok {
					return fmt.Errorf("no palette %q (have %s)", a.Pos[1], paletteNames())
				}
				s.PaletteName = a.Pos[1]
				return nil
			}
			return a.usage()
		}})
	register(&Cmd{Name: "help", Aliases: []string{"?", "commands"}, Group: "View", Usage: "[COMMAND]", Help: "list commands, or explain one",
		Run: func(s *Session, a *Args) error {
			if a.N() > 0 {
				c, _ := Lookup(a.Pos)
				if c == nil {
					return fmt.Errorf("no command %q", strings.Join(a.Pos, " "))
				}
				s.Printf("%s %s", c.Name, c.Usage)
				s.Printf("  %s", c.Help)
				if len(c.Aliases) > 0 {
					s.Printf("  also: %s", strings.Join(c.Aliases, ", "))
				}
				return nil
			}
			s.Printf("%s", HelpText())
			return nil
		}})
}

func (s *Session) drawText(x, y int, rows []string, erase bool) (int, error) {
	byColor := map[Color][]pyxra.Point{}
	for r, row := range rows {
		for i := 0; i < len(row); i++ {
			ch := row[i]
			if ch == ' ' || (ch == Empty && !erase) {
				continue
			}
			c, err := s.Resolve(string(ch))
			if err != nil {
				return 0, err
			}
			byColor[c] = append(byColor[c], pyxra.Point{X: x + i, Y: y + r})
		}
	}
	total := 0
	for c, pts := range byColor {
		n, err := s.PaintOn(s.Layer, s.Frame, pts, c)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func paletteNames() string {
	var n []string
	for k := range pyxra.Palettes {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, " ")
}

// ZoomLabel names a zoom level.
func ZoomLabel(z int) string {
	return map[int]string{-3: "1/8", -2: "1/4", -1: "1/2", 0: "1", 1: "2", 2: "4", 3: "8"}[z]
}

// HelpText lists every command grouped.
func HelpText() string {
	var b strings.Builder
	group := ""
	for _, c := range registry {
		if c.Group != group {
			if group != "" {
				b.WriteByte('\n')
			}
			group = c.Group
			b.WriteString(strings.ToUpper(group) + "\n")
		}
		fmt.Fprintf(&b, "  %-34s %s\n", c.Name+" "+c.Usage, c.Help)
	}
	return strings.TrimRight(b.String(), "\n")
}
