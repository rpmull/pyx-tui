"""Colour parsing, built-in palettes and terminal colour approximation."""

import re

_HEX = re.compile(r"^#?([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$")
_ERASE_WORDS = {"null", "none", "erase", "transparent", "-"}


def parse_color(text):
    """Normalise a colour string.

    Returns '#rrggbb' or '#rrggbbaa' (lowercase, alpha dropped when opaque),
    or None for an eraser ("null", "none", "erase", "transparent", "-").
    Raises ValueError for anything else.
    """
    if text is None:
        return None
    s = str(text).strip()
    if s.lower() in _ERASE_WORDS:
        return None
    m = _HEX.match(s)
    if not m:
        raise ValueError("not a colour: %r (use #rgb, #rrggbb, #rrggbbaa or null)" % text)
    h = m.group(1).lower()
    if len(h) == 3:
        h = "".join(c * 2 for c in h)
    if len(h) == 8 and h.endswith("ff"):
        h = h[:6]
    return "#" + h


def rgba(color):
    """'#rrggbb[aa]' -> (r, g, b, a). None -> fully transparent."""
    if color is None:
        return (0, 0, 0, 0)
    h = color.lstrip("#")
    r, g, b = int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16)
    a = int(h[6:8], 16) if len(h) == 8 else 255
    return (r, g, b, a)


def to_hex(r, g, b, a=255):
    s = "#%02x%02x%02x" % (r, g, b)
    if a != 255:
        s += "%02x" % a
    return s


PALETTES = {
    "pico8": [
        "#000000", "#1d2b53", "#7e2553", "#008751",
        "#ab5236", "#5f574f", "#c2c3c7", "#fff1e8",
        "#ff004d", "#ffa300", "#ffec27", "#00e436",
        "#29adff", "#83769c", "#ff77a8", "#ffccaa",
    ],
    "gameboy": ["#0f380f", "#306230", "#8bac0f", "#9bbc0f"],
    "nes": [
        "#000000", "#fcfcfc", "#f8f8f8", "#bcbcbc",
        "#7c7c7c", "#a4e4fc", "#3cbcfc", "#0078f8",
        "#0000fc", "#b8b8f8", "#6888fc", "#0058f8",
        "#0000bc", "#d8b8f8", "#9878f8", "#6844fc",
    ],
    "c64": [
        "#000000", "#ffffff", "#880000", "#aaffee",
        "#cc44cc", "#00cc55", "#0000aa", "#eeee77",
        "#dd8855", "#664400", "#ff7777", "#333333",
        "#777777", "#aaff66", "#0088ff", "#bbbbbb",
    ],
}
DEFAULT_PALETTE = "pico8"

# The 16 basic ANSI colours as most terminals render them by default.
_ANSI16 = [
    (0, 0, 0), (205, 0, 0), (0, 205, 0), (205, 205, 0),
    (0, 0, 238), (205, 0, 205), (0, 205, 205), (229, 229, 229),
    (127, 127, 127), (255, 0, 0), (0, 255, 0), (255, 255, 0),
    (92, 92, 255), (255, 0, 255), (0, 255, 255), (255, 255, 255),
]
_CUBE = [0, 95, 135, 175, 215, 255]


def _dist(a, b):
    # Weighted RGB distance; cheap and good enough for picking terminal colours.
    dr, dg, db = a[0] - b[0], a[1] - b[1], a[2] - b[2]
    return 2 * dr * dr + 4 * dg * dg + 3 * db * db


def _nearest_cube(v):
    return min(range(6), key=lambda i: abs(_CUBE[i] - v))


def xterm256(r, g, b):
    """Nearest xterm-256 colour index (searching the 6x6x6 cube and gray ramp)."""
    ci = (_nearest_cube(r), _nearest_cube(g), _nearest_cube(b))
    cube_rgb = tuple(_CUBE[i] for i in ci)
    cube_idx = 16 + 36 * ci[0] + 6 * ci[1] + ci[2]
    gray_level = max(0, min(23, int(round(((r + g + b) / 3 - 8) / 10))))
    gv = 8 + 10 * gray_level
    gray_idx = 232 + gray_level
    if _dist((r, g, b), (gv, gv, gv)) < _dist((r, g, b), cube_rgb):
        return gray_idx
    return cube_idx


def xterm_basic(r, g, b, n=8):
    """Nearest of the first n (8 or 16) ANSI colours."""
    return min(range(n), key=lambda i: _dist((r, g, b), _ANSI16[i]))


def is_light(r, g, b):
    return (0.299 * r + 0.587 * g + 0.114 * b) > 140
