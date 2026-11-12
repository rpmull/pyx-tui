package pyxra

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"strings"
	"testing"
)

func pngURL(t *testing.T, m *image.NRGBA) string {
	t.Helper()
	b, err := EncodePNG(m)
	if err != nil {
		t.Fatal(err)
	}
	return dataURLPrefix + base64.StdEncoding.EncodeToString(b)
}

func TestParseColor(t *testing.T) {
	cases := map[string]string{"#F00": "#ff0000", "12ab34": "#12ab34", "#112233ff": "#112233", "#11223380": "#11223380", "null": "null"}
	for in, want := range cases {
		c, err := ParseColor(in)
		if err != nil || Hex(c) != want {
			t.Errorf("ParseColor(%q) = %s, %v; want %s", in, Hex(c), err, want)
		}
	}
	if _, err := ParseColor("red"); err == nil {
		t.Error("expected an error for 'red'")
	}
}

func TestShapes(t *testing.T) {
	if got := LinePoints(0, 0, 3, 3); len(got) != 4 || got[3] != (Point{3, 3}) {
		t.Errorf("line: %v", got)
	}
	if n := len(RectPoints(0, 0, 3, 2, false)); n != 10 {
		t.Errorf("rect outline: %d", n)
	}
	if n := len(RectPoints(3, 2, 0, 0, true)); n != 12 {
		t.Errorf("rect fill: %d", n)
	}
	fill := EllipsePoints(0, 0, 6, 6, true)
	out := EllipsePoints(0, 0, 6, 6, false)
	if len(out) >= len(fill) {
		t.Errorf("outline %d should be smaller than fill %d", len(out), len(fill))
	}
}

func TestRoundTripKeepsUnknownFields(t *testing.T) {
	base := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	SetPx(base, 1, 1, Color{R: 255, A: 255})
	small := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	SetPx(small, 0, 0, Color{G: 255, A: 255})
	SetPx(small, 1, 0, Color{G: 255, A: 255})
	src := map[string]any{
		"format": "pyxra", "version": 1, "name": "t", "w": 4, "h": 3, "custom": []int{1, 2},
		"layers": []any{
			map[string]any{"id": 1, "name": "A", "visible": true, "locked": false, "opacity": 255, "blend": "normal", "future": "x"},
			map[string]any{"id": 4, "name": "B", "visible": false, "locked": true, "opacity": 100, "blend": "multiply"},
		},
		"frames":  []any{map[string]any{"id": 2, "duration": 50}, map[string]any{"id": 3, "duration": 70}},
		"tags":    []any{map[string]any{"name": "Run", "from": 0, "to": 1, "color": "red"}},
		"palette": []any{"#ff0000", map[string]any{"color": "#00ff00", "name": "leaf"}},
		"cels": []any{
			map[string]any{"k": "1:2", "png": pngURL(t, base)},
			map[string]any{"k": "4:3", "png": pngURL(t, small), "x": 2, "y": 2, "extra": true},
		},
	}
	raw, _ := json.Marshal(src)
	d, err := Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.At(0, 0, 1, 1); got != (Color{R: 255, A: 255}) {
		t.Errorf("pixel 1,1 = %v", got)
	}
	if got := d.At(1, 1, 3, 2); got != (Color{G: 255, A: 255}) {
		t.Errorf("offset cel pixel 3,2 = %v", got)
	}
	if d.Layers[1].Visible || !d.Layers[1].Locked || d.Layers[1].Opacity != 100 {
		t.Errorf("layer B props lost: %+v", d.Layers[1])
	}
	out, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back["custom"] == nil || !strings.Contains(string(out), `"future":"x"`) || !strings.Contains(string(out), `"color":"red"`) {
		t.Errorf("unknown fields lost: %s", out)
	}
	if !strings.Contains(string(out), `"name":"leaf"`) {
		t.Error("object palette entry lost")
	}
	cels := back["cels"].([]any)
	if len(cels) != 2 || cels[1].(map[string]any)["x"] != float64(2) {
		t.Errorf("unmodified offset cel changed: %v", cels)
	}
	// After an edit the cel is re-encoded at full size without an offset.
	c := d.Cel(1, 1)
	c.MarkModified()
	out, _ = d.Marshal()
	d2, err := Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := d2.At(1, 1, 3, 2); got != (Color{G: 255, A: 255}) {
		t.Errorf("re-encoded cel pixel = %v", got)
	}
}

func TestLayerAndFrameOps(t *testing.T) {
	d, _ := New(4, 4, "t", "Bottom")
	top := d.AddLayer("Top", 1)
	c, _ := d.EnsureCel(top, 0)
	SetPx(c.Img, 0, 0, Color{B: 255, A: 255})
	dup := d.DuplicateLayer(top, "Copy")
	if d.Layers[dup].Name != "Copy" || d.At(dup, 0, 0, 0).B != 255 {
		t.Fatal("duplicate failed")
	}
	d.Tags = []Tag{{Name: "T", From: 0, To: 0}}
	f := d.AddFrame(1, 0)
	if d.At(top, f, 0, 0).B != 255 {
		t.Error("frame duplicate lost pixels")
	}
	d.AddFrame(0, -1)
	if d.Tags[0].From != 1 || d.Tags[0].To != 1 {
		t.Errorf("tag not shifted: %+v", d.Tags[0])
	}
	if err := d.DeleteFrame(0); err != nil || d.Tags[0].From != 0 {
		t.Errorf("delete frame: %v %+v", err, d.Tags)
	}
	if err := d.MergeDown(dup); err != nil || len(d.Layers) != 2 {
		t.Errorf("merge: %v, %d layers", err, len(d.Layers))
	}
	d.MoveLayer(1, 0)
	if d.Layers[0].Name != "Top" {
		t.Errorf("move: %v", d.Layers)
	}
	if err := d.Resize(6, 5, 1, 1); err != nil || d.At(0, 0, 1, 1).B != 255 {
		t.Error("resize lost content")
	}
}

func TestBlend(t *testing.T) {
	red := Color{R: 255, A: 255}
	half := Color{B: 255, A: 255}
	got := Blend(red, half, 128, "normal")
	if got.R < 120 || got.R > 135 || got.B < 120 || got.B > 135 {
		t.Errorf("50%% blend = %v", got)
	}
	if got := Blend(Color{R: 200, G: 200, B: 200, A: 255}, Color{R: 128, G: 255, B: 0, A: 255}, 255, "multiply"); got.R != 100 || got.B != 0 {
		t.Errorf("multiply = %v", got)
	}
}
