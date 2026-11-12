package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rpmull/bf-interpreter/internal/pyxra"
)

func newSession(t *testing.T, w, h int) *Session {
	t.Helper()
	d, err := pyxra.New(w, h, "t", "Base")
	if err != nil {
		t.Fatal(err)
	}
	return New(d, "")
}

func run(t *testing.T, s *Session, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if err := s.Exec(l); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
	}
	s.TakeOutput()
}

func rows(s *Session) []string { return s.Rows(s.Frame, s.Layer) }

func TestSplit(t *testing.T) {
	got, err := Split(`text 1 2 "a b"; color '#' #fff ; px 0 0 \;`)
	want := [][]string{{"text", "1", "2", "a b"}, {"color", "#", "#fff"}, {"px", "0", "0", ";"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Split = %q, %v", got, err)
	}
}

func TestDrawingAndUndo(t *testing.T) {
	s := newSession(t, 6, 4)
	run(t, s, "rect 0 0 5 3 '#'", "fill 2 2 o", "px 0 0 .")
	want := []string{".#####", "#oooo#", "#oooo#", "######"}
	if got := rows(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q", got)
	}
	run(t, s, "undo", "undo")
	if got := rows(s); got[1] != "#....#" {
		t.Errorf("after undo: %q", got)
	}
	if _, ok := s.ColorOf('o'); ok {
		t.Error("undo should remove the auto-added legend entry")
	}
	run(t, s, "redo")
	if got := rows(s); got[1] != "#oooo#" {
		t.Errorf("after redo: %q", got)
	}
}

func TestColorRecolorsAndChar(t *testing.T) {
	s := newSession(t, 3, 1)
	run(t, s, "pixels 0 0 RGR", "color R #ff0000")
	if c := s.Doc.At(0, 0, 0, 0); pyxra.Hex(c) != "#ff0000" {
		t.Errorf("R pixel = %s", pyxra.Hex(c))
	}
	run(t, s, "char R x")
	if got := rows(s)[0]; got != "xGx" {
		t.Errorf("after char rename: %s", got)
	}
	if err := s.Exec("color G #ff0000"); err == nil {
		t.Error("expected error when two characters would share a colour")
	}
}

func TestLayersAndFrames(t *testing.T) {
	s := newSession(t, 4, 2)
	run(t, s, "layer add Trees", "text 0 0 TT", "layer add Sky --below", "layer move Sky 1",
		"layer Trees", "frame dup", "shift 1 0", "tag add Walk 1 2", "layer opacity Trees 50%", "layer hide Base")
	if n := len(s.Doc.Layers); n != 3 || s.Doc.Layers[0].Name != "Sky" || s.Doc.Layers[2].Name != "Trees" {
		t.Fatalf("layers: %+v", s.Doc.Layers)
	}
	if got := s.Rows(1, 2)[0]; got != ".TT." {
		t.Errorf("frame 2 Trees = %q", got)
	}
	if got := s.Rows(0, 2)[0]; got != "TT.." {
		t.Errorf("frame 1 Trees = %q", got)
	}
	if s.Doc.Layers[2].Opacity != 128 || s.Doc.Layers[1].Visible {
		t.Error("layer props not set")
	}
	run(t, s, "layer lock")
	if err := s.Exec("px 0 0 T"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("locked layer accepted an edit: %v", err)
	}
	run(t, s, "layer unlock", "layer merge", "frame del 1")
	if len(s.Doc.Layers) != 2 || len(s.Doc.Frames) != 1 || s.Doc.Tags[0].To != 0 {
		t.Errorf("after merge/delete: %d layers, %d frames, tags %+v", len(s.Doc.Layers), len(s.Doc.Frames), s.Doc.Tags)
	}
}

func TestFormatsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := newSession(t, 5, 3)
	run(t, s, "pixels 0 0 .A.B. AAAAA", "layer add Top", "text 1 2 CC", "color C #123456")
	want := s.Rows(0, -1)
	for _, name := range []string{"w.pyxra", "w.pixra.json"} {
		p := filepath.Join(dir, name)
		if _, err := s.Write(p, ExportOptions{Layer: -1}); err != nil {
			t.Fatal(err)
		}
		d, _, err := Open(p, pyxra.DefaultPalette)
		if err != nil {
			t.Fatal(err)
		}
		s2 := New(d, p)
		if got := s2.Rows(0, -1); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rows %q want %q", name, got, want)
		}
		if len(d.Layers) != 2 || d.Layers[1].Name != "Top" {
			t.Errorf("%s: layers %+v", name, d.Layers)
		}
		if c, _ := s2.ColorOf('C'); pyxra.Hex(c) != "#123456" {
			t.Errorf("%s: legend lost, C = %s", name, pyxra.Hex(c))
		}
	}
	// Ops: export every layer, apply to a fresh document, compare.
	ops := filepath.Join(dir, "w.ops.json")
	if _, err := s.Write(ops, ExportOptions{Layer: -1, AllLayers: true, AddLayers: true}); err != nil {
		t.Fatal(err)
	}
	d, _, err := Open(ops, pyxra.DefaultPalette)
	if err != nil {
		t.Fatal(err)
	}
	if got := New(d, "").Rows(0, -1); !reflect.DeepEqual(got, want) {
		t.Errorf("ops rows %q want %q", got, want)
	}
	// PNG export and re-import.
	png := filepath.Join(dir, "w.png")
	if _, err := s.Write(png, ExportOptions{Layer: -1, Scale: 2}); err != nil {
		t.Fatal(err)
	}
	d, _, err = Open(png, pyxra.DefaultPalette)
	if err != nil || d.W != 10 || d.H != 6 {
		t.Fatalf("png: %v %dx%d", err, d.W, d.H)
	}
	// Text import colours characters from the palette.
	txt := filepath.Join(dir, "a.txt")
	os.WriteFile(txt, []byte("  ##\n #@@#\n"), 0o644)
	d, _, err = Open(txt, pyxra.DefaultPalette)
	if err != nil {
		t.Fatal(err)
	}
	if got := New(d, "").Rows(0, 0); !reflect.DeepEqual(got, []string{"..##.", ".#@@#"}) {
		t.Errorf("text rows %q", got)
	}
}

func TestImportAndApply(t *testing.T) {
	dir := t.TempDir()
	art := filepath.Join(dir, "tree.txt")
	os.WriteFile(art, []byte(".T.\nTTT\n"), 0o644)
	ops := filepath.Join(dir, "x.ops.json")
	os.WriteFile(ops, []byte(`[{"op":"add_layer","name":"Box"},
		{"op":"rect","layer":"Box","frame":1,"x":0,"y":0,"w":3,"h":2,"color":"#ff0000","fill":false},
		{"op":"pixels","layer":"Box","frame":2,"x":1,"y":1,"legend":{"Q":"#00ff00"},"rows":["Q"]},
		{"op":"wiggle"}]`), 0o644)
	s := newSession(t, 6, 3)
	run(t, s, "import "+art+" --at 2,1")
	if got := s.Rows(0, 1); got[1] != "...T.." || got[2] != "..TTT." {
		t.Errorf("import rows %q", got)
	}
	if err := s.Exec("apply " + ops); err != nil {
		t.Fatal(err)
	}
	out := s.TakeOutput()
	if !strings.Contains(out, "skipped op 4") {
		t.Errorf("expected a note about the unknown op, got %q", out)
	}
	li, _ := s.Doc.FindLayer("Box")
	if len(s.Doc.Frames) != 2 || s.Rows(1, li)[1][1] != 'Q' {
		t.Errorf("apply: frames %d rows %q", len(s.Doc.Frames), s.Rows(1, li))
	}
	run(t, s, "undo")
	if _, err := s.Doc.FindLayer("Box"); err == nil || len(s.Doc.Frames) != 1 {
		t.Error("apply should undo as one step")
	}
}

func TestTransformsAndCrop(t *testing.T) {
	s := newSession(t, 4, 3)
	run(t, s, "pixels 0 0 AB.. C...")
	run(t, s, "flip h")
	if got := rows(s); got[0] != "..BA" || got[1] != "...C" {
		t.Errorf("flip: %q", got)
	}
	run(t, s, "crop")
	if s.Doc.W != 2 || s.Doc.H != 2 || rows(s)[0] != "BA" {
		t.Errorf("crop: %dx%d %q", s.Doc.W, s.Doc.H, rows(s))
	}
	run(t, s, "resize 4x4 1 1")
	if got := rows(s); got[1] != ".BA." {
		t.Errorf("resize: %q", got)
	}
}
