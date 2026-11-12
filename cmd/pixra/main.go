// Command pixra edits Pyxra pixel-art documents as ASCII maps, in a terminal UI or from the shell.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/rpmull/bf-interpreter/internal/pyxra"
	"github.com/rpmull/bf-interpreter/internal/session"
	"github.com/rpmull/bf-interpreter/internal/tui"
)

var version = "dev"

const usage = `Pixra: draw Pyxra pixel art and maps with ASCII characters.

  pixra [FILE] [--size WxH]           open FILE (or a new document) in the editor
  pixra new FILE [WxH] [LAYER]        create a document
  pixra info FILE                     describe a document and its layers
  pixra show FILE [--view color|mixed|text] [--zoom Z] [--layer L] [--frame N]
  pixra convert IN OUT [export options]
  pixra do FILE [-o OUT] [-n] [-f SCRIPT] 'COMMAND ...' ['COMMAND ...']
  pixra COMMAND FILE [ARGS...] [-o OUT] [-n]
  pixra help [COMMAND]                list every command, or explain one
  pixra version

Every editor command works from the shell, for example:
  pixra layer add world.pyxra Trees
  pixra rect world.pyxra 0 0 15 9 '#' --fill
  pixra do world.pyxra 'layer Trees' 'text 2 2 TT.TT' 'export world.png --scale 8'

Options before the command:
  -l LAYER   start on this layer         -F N     start on frame N
  -o OUT     save to OUT instead of FILE -n       dry run: do not save
  -q         quiet                       -p NAME  palette for new colours (pico8 gameboy nes c64)

Formats are chosen by extension: .pyxra (Pyxra document), .pixra.json (readable
ASCII world), .ops.json (Pyxra edit ops), .png, and .txt (or any other: ASCII rows).
`

type globals struct {
	layer, frame, out, palette string
	dry, quiet                 bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "pixra:", err)
	return 1
}

func run(args []string) int {
	g := globals{palette: pyxra.DefaultPalette}
	// Leading global options.
	for len(args) > 0 {
		takes := map[string]*string{"-l": &g.layer, "-F": &g.frame, "-o": &g.out, "-p": &g.palette}
		if p, ok := takes[args[0]]; ok && len(args) > 1 {
			*p = args[1]
			args = args[2:]
			continue
		}
		if args[0] == "-n" {
			g.dry, args = true, args[1:]
			continue
		}
		if args[0] == "-q" {
			g.quiet, args = true, args[1:]
			continue
		}
		break
	}
	if len(args) == 0 {
		return edit("", "32x32", g)
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "help":
		if len(args) > 1 {
			s := session.New(mustNew(), "")
			if err := s.ExecTokens(append([]string{"help"}, args[1:]...)); err != nil {
				return fail(err)
			}
			fmt.Print(s.TakeOutput())
			return 0
		}
		fmt.Print(usage)
		fmt.Println()
		fmt.Println(session.HelpText())
		return 0
	case "version", "--version", "-v":
		fmt.Println("pixra", version)
		return 0
	case "new":
		return cmdNew(args[1:], g)
	case "info":
		if len(args) != 2 {
			return fail(fmt.Errorf("usage: pixra info FILE"))
		}
		return script(args[1], [][]string{{"info"}, {"layers"}, {"frames"}, {"tags"}}, g, false)
	case "show":
		return cmdShow(args[1:], g)
	case "convert":
		if len(args) < 3 {
			return fail(fmt.Errorf("usage: pixra convert IN OUT [export options]"))
		}
		return script(args[1], [][]string{append([]string{"export", args[2]}, args[3:]...)}, g, false)
	case "do", "run":
		return cmdDo(args[1:], g)
	}
	if c, n := session.Lookup(args); c != nil {
		rest := args[n:]
		if len(rest) == 0 {
			return fail(fmt.Errorf("usage: pixra %s FILE %s", c.Name, c.Usage))
		}
		file := rest[0]
		cmdArgs := stripTrailing(rest[1:], &g)
		tokens := append(strings.Fields(c.Name), cmdArgs...)
		return script(file, [][]string{tokens}, g, true)
	}
	if len(args) > 1 && !strings.HasPrefix(args[0], "-") && !strings.HasPrefix(args[1], "-") {
		return fail(fmt.Errorf("unknown command %q (see pixra help)", args[0]))
	}
	if strings.HasPrefix(args[0], "-") && args[0] != "--size" {
		return fail(fmt.Errorf("unknown option %s (see pixra --help)", args[0]))
	}
	size := "32x32"
	file := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--size" && i+1 < len(args):
			size = args[i+1]
			i++
		case file == "" && !strings.HasPrefix(args[i], "-"):
			file = args[i]
		default:
			return fail(fmt.Errorf("unexpected argument %q (see pixra --help)", args[i]))
		}
	}
	return edit(file, size, g)
}

// stripTrailing pulls -o OUT and -n from the end of a shorthand command's arguments.
func stripTrailing(args []string, g *globals) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-o" && i+1 < len(args):
			g.out = args[i+1]
			i++
		case args[i] == "-n":
			g.dry = true
		case args[i] == "-q":
			g.quiet = true
		default:
			out = append(out, args[i])
		}
	}
	return out
}

func mustNew() *pyxra.Doc {
	d, _ := pyxra.New(16, 16, "untitled", "Layer 1")
	return d
}

func open(file string, g globals) (*session.Session, error) {
	d, notes, err := session.Open(file, g.palette)
	if err != nil {
		return nil, err
	}
	s := session.New(d, file)
	s.PaletteName = g.palette
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	if g.layer != "" {
		if err := s.ExecTokens([]string{"layer", g.layer}); err != nil {
			return nil, err
		}
		s.TakeOutput()
	}
	if g.frame != "" {
		if err := s.ExecTokens([]string{"frame", g.frame}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// script opens a file, runs commands and saves when they changed it.
func script(file string, cmds [][]string, g globals, save bool) int {
	s, err := open(file, g)
	if err != nil {
		return fail(err)
	}
	for _, c := range cmds {
		err := s.ExecTokens(c)
		if out := s.TakeOutput(); out != "" && !g.quiet {
			fmt.Print(out)
		}
		if err != nil {
			return fail(err)
		}
	}
	if !save || g.dry || (!s.Dirty && g.out == "") {
		if g.dry && s.Dirty && !g.quiet {
			fmt.Fprintln(os.Stderr, "dry run: not saved")
		}
		return 0
	}
	target := g.out
	if target == "" {
		target = file
	}
	if _, err := s.Write(target, session.ExportOptions{Layer: -1, Frame: s.Frame}); err != nil {
		return fail(err)
	}
	if !g.quiet {
		fmt.Fprintf(os.Stderr, "saved %s\n", target)
	}
	return 0
}

func cmdDo(args []string, g globals) int {
	var file, scriptFile string
	var lines []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o" && i+1 < len(args):
			g.out = args[i+1]
			i++
		case a == "-f" && i+1 < len(args):
			scriptFile = args[i+1]
			i++
		case a == "-n":
			g.dry = true
		case a == "-q":
			g.quiet = true
		case file == "":
			file = a
		default:
			lines = append(lines, a)
		}
	}
	if file == "" {
		return fail(fmt.Errorf("usage: pixra do FILE [-o OUT] [-n] [-f SCRIPT] 'COMMAND ...'"))
	}
	if scriptFile != "" {
		var r io.Reader = os.Stdin
		if scriptFile != "-" {
			f, err := os.Open(scriptFile)
			if err != nil {
				return fail(err)
			}
			defer f.Close()
			r = f
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" && !strings.HasPrefix(t, "#") {
				lines = append(lines, t)
			}
		}
	}
	var cmds [][]string
	for _, l := range lines {
		stmts, err := session.Split(l)
		if err != nil {
			return fail(fmt.Errorf("%q: %v", l, err))
		}
		cmds = append(cmds, stmts...)
	}
	return script(file, cmds, g, true)
}

func cmdNew(args []string, g globals) int {
	if len(args) < 1 {
		return fail(fmt.Errorf("usage: pixra new FILE [WxH] [LAYER]"))
	}
	file, size, layer := args[0], "32x32", "Layer 1"
	if len(args) > 1 {
		size = args[1]
	}
	if len(args) > 2 {
		layer = strings.Join(args[2:], " ")
	}
	if _, err := os.Stat(file); err == nil {
		return fail(fmt.Errorf("%s already exists", file))
	}
	w, h, err := pyxra.ParseSize(size)
	if err != nil {
		return fail(err)
	}
	d, err := pyxra.New(w, h, session.BaseName(file), layer)
	if err != nil {
		return fail(err)
	}
	s := session.New(d, file)
	if _, err := s.Write(file, session.ExportOptions{Layer: -1}); err != nil {
		return fail(err)
	}
	if !g.quiet {
		fmt.Printf("created %s (%dx%d)\n", file, w, h)
	}
	return 0
}

func edit(file, size string, g globals) int {
	var s *session.Session
	if file != "" {
		if _, err := os.Stat(file); err == nil {
			var err error
			if s, err = open(file, g); err != nil {
				return fail(err)
			}
			k := session.KindOf(file)
			if k != session.KindPyxra && k != session.KindNative {
				// Save next to the source as .pyxra, never over the original file.
				s.Path = filepath.Join(filepath.Dir(file), session.BaseName(file)+".pyxra")
				if _, err := os.Stat(s.Path); err == nil {
					s.Path = ""
				}
				s.Printf("opened %s; save writes %s", file, s.SavePath())
			}
		}
	}
	if s == nil {
		w, h, err := pyxra.ParseSize(size)
		if err != nil {
			return fail(err)
		}
		name := "untitled"
		if file != "" {
			name = session.BaseName(file)
		}
		d, err := pyxra.New(w, h, name, "Layer 1")
		if err != nil {
			return fail(err)
		}
		path := ""
		if k := session.KindOf(file); file != "" && (k == session.KindPyxra || k == session.KindNative) {
			path = file
		}
		s = session.New(d, path)
		s.PaletteName = g.palette
		s.Printf("new %dx%d document; press ? for help", w, h)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fail(fmt.Errorf("the editor needs a terminal; use pixra do / show / convert in scripts"))
	}
	if err := tui.Run(s); err != nil {
		return fail(err)
	}
	return 0
}

func cmdShow(args []string, g globals) int {
	view, zoom, layer, frame, file := "", "1", "", "", ""
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--view", "--mode":
			view = next()
		case "--zoom":
			zoom = next()
		case "--layer":
			layer = next()
		case "--frame":
			frame = next()
		default:
			file = args[i]
		}
	}
	if file == "" {
		return fail(fmt.Errorf("usage: pixra show FILE [--view color|mixed|text] [--zoom 1|2] [--layer L] [--frame N]"))
	}
	g.frame = frame
	s, err := open(file, g)
	if err != nil {
		return fail(err)
	}
	li := -1
	if layer != "" {
		if li, err = s.Doc.FindLayer(layer); err != nil {
			return fail(err)
		}
	}
	tty := term.IsTerminal(int(os.Stdout.Fd()))
	if view == "" {
		view = session.ViewColor
		if !tty {
			view = session.ViewText
		}
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	d := s.Doc
	at := func(x, y int) pyxra.Color {
		if li >= 0 {
			return d.At(li, s.Frame, x, y)
		}
		return d.Composite(s.Frame, x, y, -1)
	}
	switch view {
	case session.ViewText, "plain", "ascii":
		for _, r := range s.Rows(s.Frame, li) {
			fmt.Fprintln(w, r)
		}
	case session.ViewColor, "colour", session.ViewMixed:
		if zoom == "2" || view == session.ViewMixed {
			for y := 0; y < d.H; y++ {
				for x := 0; x < d.W; x++ {
					c := at(x, y)
					ch := "  "
					if view == session.ViewMixed {
						ch = string(s.CharOf(c)) + " "
					}
					if c.A == 0 {
						fmt.Fprintf(w, "\x1b[0m%s", strings.Replace(ch, ".", "·", 1))
						continue
					}
					fg := "97"
					if pyxra.IsLight(c) {
						fg = "30"
					}
					fmt.Fprintf(w, "\x1b[%s;48;2;%d;%d;%dm%s", fg, c.R, c.G, c.B, ch)
				}
				fmt.Fprint(w, "\x1b[0m\n")
			}
			return 0
		}
		for y := 0; y < d.H; y += 2 {
			for x := 0; x < d.W; x++ {
				t := at(x, y)
				b := pyxra.Transparent
				if y+1 < d.H {
					b = at(x, y+1)
				}
				switch {
				case t.A == 0 && b.A == 0:
					fmt.Fprint(w, "\x1b[0m ")
				case t.A == 0:
					fmt.Fprintf(w, "\x1b[0;38;2;%d;%d;%dm▄", b.R, b.G, b.B)
				case b.A == 0:
					fmt.Fprintf(w, "\x1b[0;38;2;%d;%d;%dm▀", t.R, t.G, t.B)
				default:
					fmt.Fprintf(w, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", t.R, t.G, t.B, b.R, b.G, b.B)
				}
			}
			fmt.Fprint(w, "\x1b[0m\n")
		}
	default:
		return fail(fmt.Errorf("unknown view %q (color, mixed or text)", view))
	}
	return 0
}
