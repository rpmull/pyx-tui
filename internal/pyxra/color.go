package pyxra

import (
	"encoding/hex"
	"fmt"
	"image/color"
	"strings"
)

// Color is a straight-alpha RGBA colour, as Pyxra stores it.
type Color = color.NRGBA

// Transparent is the canonical fully transparent colour.
var Transparent = Color{}

// Norm maps every fully transparent colour to Transparent so colours can be compared.
func Norm(c Color) Color {
	if c.A == 0 {
		return Transparent
	}
	return c
}

// ParseColor reads #rgb, #rrggbb, #rrggbbaa (the '#' is optional) or one of
// null/none/erase/transparent/-, which mean Transparent.
func ParseColor(s string) (Color, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	switch t {
	case "null", "none", "erase", "transparent", "-":
		return Transparent, nil
	}
	t = strings.TrimPrefix(t, "#")
	if len(t) == 3 {
		t = string([]byte{t[0], t[0], t[1], t[1], t[2], t[2]})
	}
	if len(t) != 6 && len(t) != 8 {
		return Transparent, fmt.Errorf("not a colour: %q (use #rgb, #rrggbb, #rrggbbaa or null)", s)
	}
	b, err := hex.DecodeString(t)
	if err != nil {
		return Transparent, fmt.Errorf("not a colour: %q", s)
	}
	c := Color{R: b[0], G: b[1], B: b[2], A: 255}
	if len(b) == 4 {
		c.A = b[3]
	}
	return Norm(c), nil
}

// Hex formats a colour as #rrggbb, #rrggbbaa when not opaque, or "null" when transparent.
func Hex(c Color) string {
	if c.A == 0 {
		return "null"
	}
	if c.A == 255 {
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

// IsLight reports whether dark text reads better than light text on c.
func IsLight(c Color) bool {
	return 0.299*float64(c.R)+0.587*float64(c.G)+0.114*float64(c.B) > 140
}

// Palettes are the built-in palettes new characters can take colours from.
var Palettes = map[string][]string{
	"pico8": {
		"#000000", "#1d2b53", "#7e2553", "#008751", "#ab5236", "#5f574f", "#c2c3c7", "#fff1e8",
		"#ff004d", "#ffa300", "#ffec27", "#00e436", "#29adff", "#83769c", "#ff77a8", "#ffccaa",
	},
	"gameboy": {"#0f380f", "#306230", "#8bac0f", "#9bbc0f"},
	"nes": {
		"#000000", "#fcfcfc", "#f8f8f8", "#bcbcbc", "#7c7c7c", "#a4e4fc", "#3cbcfc", "#0078f8",
		"#0000fc", "#b8b8f8", "#6888fc", "#0058f8", "#0000bc", "#d8b8f8", "#9878f8", "#6844fc",
	},
	"c64": {
		"#000000", "#ffffff", "#880000", "#aaffee", "#cc44cc", "#00cc55", "#0000aa", "#eeee77",
		"#dd8855", "#664400", "#ff7777", "#333333", "#777777", "#aaff66", "#0088ff", "#bbbbbb",
	},
}

// DefaultPalette is the palette used when none is chosen.
const DefaultPalette = "pico8"

// Blend composites src over dst with a layer opacity (0-255) and blend mode.
// Unknown modes blend as "normal".
func Blend(dst, src Color, opacity int, mode string) Color {
	sa := float64(src.A) * float64(opacity) / (255 * 255)
	if sa <= 0 {
		return dst
	}
	da := float64(dst.A) / 255
	cs := [3]float64{float64(src.R) / 255, float64(src.G) / 255, float64(src.B) / 255}
	cb := [3]float64{float64(dst.R) / 255, float64(dst.G) / 255, float64(dst.B) / 255}
	if mode != "" && mode != "normal" && da > 0 {
		for i := 0; i < 3; i++ {
			b := blendChannel(mode, cb[i], cs[i])
			cs[i] = (1-da)*cs[i] + da*b
		}
	}
	oa := sa + da*(1-sa)
	if oa <= 0 {
		return Transparent
	}
	var out [3]uint8
	for i := 0; i < 3; i++ {
		v := (cs[i]*sa + cb[i]*da*(1-sa)) / oa
		out[i] = uint8(clamp01(v)*255 + 0.5)
	}
	return Color{R: out[0], G: out[1], B: out[2], A: uint8(clamp01(oa)*255 + 0.5)}
}

func blendChannel(mode string, b, s float64) float64 {
	switch mode {
	case "multiply":
		return b * s
	case "screen":
		return b + s - b*s
	case "overlay":
		if b <= 0.5 {
			return 2 * b * s
		}
		return 1 - 2*(1-b)*(1-s)
	case "darken":
		if b < s {
			return b
		}
		return s
	case "lighten":
		if b > s {
			return b
		}
		return s
	case "add", "additive", "linear-dodge", "plus":
		return clamp01(b + s)
	case "subtract":
		return clamp01(b - s)
	case "difference":
		if b > s {
			return b - s
		}
		return s - b
	}
	return s
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
