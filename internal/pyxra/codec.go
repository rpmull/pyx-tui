package pyxra

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const dataURLPrefix = "data:image/png;base64,"

// ----- loading ------------------------------------------------------------------------

type fields map[string]json.RawMessage

func (f fields) take(key string, v any) (bool, error) {
	raw, ok := f[key]
	if !ok {
		return false, nil
	}
	delete(f, key)
	if string(raw) == "null" {
		return true, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return true, fmt.Errorf("field %q: %v", key, err)
	}
	return true, nil
}

func (f fields) str(key, def string) (string, error) {
	s := def
	_, err := f.take(key, &s)
	return s, err
}

func (f fields) num(key string, def int) (int, error) {
	v := float64(def)
	_, err := f.take(key, &v)
	return int(v), err
}

func (f fields) boolean(key string, def bool) (bool, error) {
	b := def
	_, err := f.take(key, &b)
	return b, err
}

func (f fields) extra() Extra { return Extra(f) }

// Load parses a .pyxra document and decodes every cel.
func Load(data []byte) (*Doc, error) {
	var top fields
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("not a Pyxra document: %v", err)
	}
	format, err := top.str("format", "pyxra")
	if err != nil {
		return nil, err
	}
	if format != "pyxra" {
		return nil, fmt.Errorf("format is %q, not pyxra", format)
	}
	d := &Doc{Cels: map[CelKey]*Cel{}}
	if raw, ok := top["version"]; ok {
		d.version = raw
		delete(top, "version")
	}
	if d.Name, err = top.str("name", ""); err != nil {
		return nil, err
	}
	if d.W, err = top.num("w", 0); err != nil {
		return nil, err
	}
	if d.H, err = top.num("h", 0); err != nil {
		return nil, err
	}
	if err := CheckSize(d.W, d.H); err != nil {
		return nil, err
	}

	var layers []fields
	if _, err := top.take("layers", &layers); err != nil {
		return nil, err
	}
	for _, f := range layers {
		var l Layer
		var e1, e2, e3, e4, e5, e6 error
		l.ID, e1 = f.num("id", 0)
		l.Name, e2 = f.str("name", "")
		l.Visible, e3 = f.boolean("visible", true)
		l.Locked, e4 = f.boolean("locked", false)
		l.Opacity, e5 = f.num("opacity", 255)
		l.Blend, e6 = f.str("blend", "normal")
		if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
			return nil, fmt.Errorf("layer: %v", err)
		}
		l.Extra = f.extra()
		d.Layers = append(d.Layers, l)
	}
	var frames []fields
	if _, err := top.take("frames", &frames); err != nil {
		return nil, err
	}
	for _, f := range frames {
		var fr Frame
		var e1, e2 error
		fr.ID, e1 = f.num("id", 0)
		fr.Duration, e2 = f.num("duration", 100)
		if err := firstErr(e1, e2); err != nil {
			return nil, fmt.Errorf("frame: %v", err)
		}
		fr.Extra = f.extra()
		d.Frames = append(d.Frames, fr)
	}
	if len(d.Layers) == 0 || len(d.Frames) == 0 {
		return nil, fmt.Errorf("document has no layers or no frames")
	}
	var tags []fields
	if _, err := top.take("tags", &tags); err != nil {
		return nil, err
	}
	for _, f := range tags {
		var t Tag
		var e1, e2, e3 error
		t.Name, e1 = f.str("name", "")
		t.From, e2 = f.num("from", 0)
		t.To, e3 = f.num("to", 0)
		if err := firstErr(e1, e2, e3); err != nil {
			return nil, fmt.Errorf("tag: %v", err)
		}
		t.Extra = f.extra()
		d.Tags = append(d.Tags, t)
	}
	var palette []json.RawMessage
	if _, err := top.take("palette", &palette); err != nil {
		return nil, err
	}
	for _, raw := range palette {
		d.Palette = append(d.Palette, parsePaletteEntry(raw))
	}
	if raw, ok := top["pixra"]; ok {
		delete(top, "pixra")
		var meta struct {
			Legend json.RawMessage `json:"legend"`
		}
		if json.Unmarshal(raw, &meta) == nil && meta.Legend != nil {
			d.Legend, _ = ParseLegend(meta.Legend)
		}
	}

	var cels []fields
	if _, err := top.take("cels", &cels); err != nil {
		return nil, err
	}
	d.Extra = top.extra()
	if d.Extra == nil {
		d.Extra = Extra{}
	}
	if err := d.decodeCels(cels); err != nil {
		return nil, err
	}
	return d, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func parsePaletteEntry(raw json.RawMessage) PaletteEntry {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if c, err := ParseColor(s); err == nil {
			return PaletteEntry{Color: c}
		}
		return PaletteEntry{Raw: raw}
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		for _, k := range []string{"color", "hex", "value", "rgba"} {
			if v, ok := obj[k].(string); ok {
				if c, err := ParseColor(v); err == nil {
					return PaletteEntry{Color: c, Raw: raw}
				}
			}
		}
	}
	return PaletteEntry{Raw: raw}
}

// ParseLegend reads a {"char": "#hex" | null} object, keeping key order.
func ParseLegend(raw json.RawMessage) ([]LegendEntry, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("legend must be an object")
	}
	var out []LegendEntry
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := t.(string)
		var val *string
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("legend %q: %v", key, err)
		}
		if len(key) != 1 || key[0] < 33 || key[0] > 126 {
			return nil, fmt.Errorf("legend key %q must be one printable ASCII character", key)
		}
		c := Transparent
		if val != nil {
			if c, err = ParseColor(*val); err != nil {
				return nil, err
			}
		}
		out = append(out, LegendEntry{Char: key[0], Color: c})
	}
	return out, nil
}

// MarshalLegend writes legend entries as a JSON object in their order.
func MarshalLegend(entries []LegendEntry) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(string(e.Char))
		b.Write(k)
		b.WriteByte(':')
		if e.Color.A == 0 {
			b.WriteString("null")
		} else {
			b.WriteString(`"` + Hex(e.Color) + `"`)
		}
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (d *Doc) decodeCels(cels []fields) error {
	type job struct {
		key CelKey
		cel *Cel
		url string
	}
	var jobs []job
	for _, f := range cels {
		k, err := f.str("k", "")
		if err != nil {
			return err
		}
		var key CelKey
		if _, err := fmt.Sscanf(k, "%d:%d", &key.Layer, &key.Frame); err != nil {
			return fmt.Errorf("bad cel key %q", k)
		}
		url, err := f.str("png", "")
		if err != nil {
			return err
		}
		x, e1 := f.num("x", 0)
		y, e2 := f.num("y", 0)
		if err := firstErr(e1, e2); err != nil {
			return err
		}
		c := &Cel{src: url, x: x, y: y, Extra: f.extra()}
		d.Cels[key] = c
		jobs = append(jobs, job{key, c, url})
	}
	errs := make([]error, len(jobs))
	parallel(len(jobs), func(i int) {
		img, err := decodeDataURL(jobs[i].url, d.W, d.H, jobs[i].cel.x, jobs[i].cel.y)
		if err != nil {
			errs[i] = fmt.Errorf("cel %s: %v", jobs[i].key, err)
			return
		}
		jobs[i].cel.Img = img
	})
	return firstErr(errs...)
}

func decodeDataURL(url string, w, h, ox, oy int) (*image.NRGBA, error) {
	i := strings.IndexByte(url, ',')
	if i < 0 || !strings.Contains(url[:i], "base64") {
		return nil, fmt.Errorf("cel is not a base64 data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(url[i+1:])
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return toCanvas(img, w, h, ox, oy), nil
}

// toCanvas places img at (ox, oy) on a w x h transparent canvas.
func toCanvas(img image.Image, w, h, ox, oy int) *image.NRGBA {
	b := img.Bounds()
	if n, ok := img.(*image.NRGBA); ok && ox == 0 && oy == 0 && b == image.Rect(0, 0, w, h) && n.Stride == 4*w {
		normalize(n.Pix)
		return n
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		dy := y - b.Min.Y + oy
		if dy < 0 || dy >= h {
			continue
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := x - b.Min.X + ox
			if dx < 0 || dx >= w {
				continue
			}
			var c Color
			switch m := img.(type) {
			case *image.NRGBA:
				i := m.PixOffset(x, y)
				c = Color{R: m.Pix[i], G: m.Pix[i+1], B: m.Pix[i+2], A: m.Pix[i+3]}
			default:
				c = color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			}
			SetPx(out, dx, dy, Norm(c))
		}
	}
	return out
}

func normalize(p []byte) {
	for i := 0; i+3 < len(p); i += 4 {
		if p[i+3] == 0 {
			p[i], p[i+1], p[i+2] = 0, 0, 0
		}
	}
}

// ImageToCanvas converts any image to a canvas of its own size.
func ImageToCanvas(img image.Image) *image.NRGBA {
	b := img.Bounds()
	return toCanvas(img, b.Dx(), b.Dy(), 0, 0)
}

// ----- saving -------------------------------------------------------------------------

// EncodePNG encodes an image as PNG bytes.
func EncodePNG(m image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func withExtra(e Extra, kv ...any) map[string]any {
	m := make(map[string]any, len(e)+len(kv)/2)
	for k, v := range e {
		m[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// Marshal writes the document as .pyxra JSON. Unmodified cels keep their original PNG.
func (d *Doc) Marshal() ([]byte, error) {
	type celOut struct {
		key CelKey
		cel *Cel
		url string
		err error
	}
	var outs []*celOut
	for _, l := range d.Layers {
		for _, f := range d.Frames {
			k := CelKey{l.ID, f.ID}
			if c := d.Cels[k]; c != nil {
				outs = append(outs, &celOut{key: k, cel: c})
			}
		}
	}
	parallel(len(outs), func(i int) {
		o := outs[i]
		if !o.cel.Modified() {
			o.url = o.cel.src
			return
		}
		if Empty(o.cel.Img) {
			return
		}
		b, err := EncodePNG(o.cel.Img)
		if err != nil {
			o.err = err
			return
		}
		o.url = dataURLPrefix + base64.StdEncoding.EncodeToString(b)
	})
	cels := make([]any, 0, len(outs))
	for _, o := range outs {
		if o.err != nil {
			return nil, fmt.Errorf("cel %s: %v", o.key, o.err)
		}
		if o.url == "" {
			continue
		}
		m := withExtra(o.cel.Extra, "k", o.key.String(), "png", o.url)
		if !o.cel.Modified() && (o.cel.x != 0 || o.cel.y != 0) {
			m["x"], m["y"] = o.cel.x, o.cel.y
		}
		cels = append(cels, m)
	}

	layers := make([]any, len(d.Layers))
	for i, l := range d.Layers {
		layers[i] = withExtra(l.Extra, "id", l.ID, "name", l.Name, "visible", l.Visible,
			"locked", l.Locked, "opacity", l.Opacity, "blend", l.Blend)
	}
	frames := make([]any, len(d.Frames))
	for i, f := range d.Frames {
		frames[i] = withExtra(f.Extra, "id", f.ID, "duration", f.Duration)
	}
	tags := make([]any, len(d.Tags))
	for i, t := range d.Tags {
		tags[i] = withExtra(t.Extra, "name", t.Name, "from", t.From, "to", t.To)
	}
	palette := make([]any, len(d.Palette))
	for i, p := range d.Palette {
		if p.Raw != nil {
			palette[i] = p.Raw
		} else {
			palette[i] = Hex(p.Color)
		}
	}
	version := d.version
	if version == nil {
		version = json.RawMessage("1")
	}
	top := withExtra(d.Extra, "format", "pyxra", "version", version, "name", d.Name,
		"w", d.W, "h", d.H, "layers", layers, "frames", frames, "tags", tags,
		"palette", palette, "cels", cels)
	if len(d.Legend) > 0 {
		top["pixra"] = map[string]any{"version": 1, "legend": MarshalLegend(d.Legend)}
	}
	for _, k := range []string{"labels", "loadouts", "guides"} {
		if _, ok := top[k]; !ok {
			top[k] = []any{}
		}
	}
	return json.Marshal(top)
}

// parallel runs fn(0..n-1) on all CPUs.
func parallel(n int, fn func(int)) {
	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var wg sync.WaitGroup
	next := make(chan int, n)
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// SortedKeys returns an Extra's keys in order (for stable output in tools).
func SortedKeys(e Extra) []string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
