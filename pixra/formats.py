"""Reading and writing Pixra maps.

Formats (chosen by file extension):

  *.pixra.json / *.json   native map (rows + legend + placement), the editor's save format
  *.ops.json              a Pyxra `edit` ops file: [ {"op": "pixels", ...} ]
  *.pyxra                 a Pyxra document (one layer, one frame, PNG cel)
  *.png                   a PNG image (export only; optional integer upscale)
  *.txt / anything else   plain ASCII art, one line per row; spaces become '.'
"""

import base64
import json
import os

from . import png
from .colors import DEFAULT_PALETTE, PALETTES, parse_color, rgba, to_hex
from .model import EMPTY, PixraMap, valid_char

NATIVE_FORMAT = "pixra-map"
NATIVE_VERSION = 1

# Characters handed out, in order, when colours must become characters.
CHAR_POOL = (
    "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    "abcdefghijklmnopqrstuvwxyz"
    "0123456789"
    "#@%&*+=-:;!?$~^<>/\\|()[]{}_,'\"`"
)
assert EMPTY not in CHAR_POOL


def kind_of(path):
    p = path.lower()
    if p.endswith(".ops.json"):
        return "ops"
    if p.endswith(".json"):
        return "native"
    if p.endswith(".pyxra"):
        return "pyxra"
    if p.endswith(".png"):
        return "png"
    return "text"


def _base_name(path):
    name = os.path.basename(path)
    for ext in (".pixra.json", ".ops.json", ".json", ".pyxra", ".png", ".txt"):
        if name.lower().endswith(ext):
            return name[: -len(ext)] or "untitled"
    return os.path.splitext(name)[0] or "untitled"


def _clean_legend(legend):
    out = {}
    for ch, color in (legend or {}).items():
        if not valid_char(ch):
            raise ValueError("legend key %r must be a single printable ASCII character" % ch)
        out[ch] = parse_color(color)
    return out


def assign_colors(m, palette=DEFAULT_PALETTE):
    """Give every used character without a legend entry a colour from the palette."""
    colors = PALETTES.get(palette, PALETTES[DEFAULT_PALETTE])
    taken = {c for c in m.legend.values() if c}
    free = [c for c in colors if c not in taken] or list(colors)
    i = 0
    added = []
    for ch in m.used_chars():
        if ch not in m.legend:
            m.legend[ch] = free[i % len(free)]
            added.append(ch)
            i += 1
    return added


# ----- native / pixels op ------------------------------------------------------------
def to_native(m):
    return {
        "format": NATIVE_FORMAT,
        "version": NATIVE_VERSION,
        "name": m.name,
        "w": m.width,
        "h": m.height,
        "layer": m.layer,
        "frame": m.frame,
        "x": m.x,
        "y": m.y,
        "clear": m.clear,
        "legend": dict(m.legend),
        "rows": m.rows(),
    }


def to_pixels_op(m):
    """The map as one Pyxra `pixels` op."""
    op = {
        "op": "pixels",
        "layer": m.layer,
        "frame": m.frame,
        "x": m.x,
        "y": m.y,
        "legend": dict(m.legend),
        "rows": m.rows(),
    }
    if m.clear:
        op["clear"] = True
    return op


def _from_mapping(d, name):
    if "rows" not in d:
        raise ValueError("JSON has no 'rows'")
    return PixraMap.from_rows(
        list(d["rows"]),
        legend=_clean_legend(d.get("legend")),
        name=d.get("name", name),
        layer=d.get("layer", "Layer 1"),
        frame=int(d.get("frame", 1)),
        x=int(d.get("x", 0)),
        y=int(d.get("y", 0)),
        clear=bool(d.get("clear", False)),
    )


def from_json(data, name="untitled"):
    """Accept a native map, a single `pixels` op, a list of ops, or {"ops": [...]}."""
    notes = []
    if isinstance(data, dict) and "ops" in data and "rows" not in data:
        data = data["ops"]
    if isinstance(data, list):
        pix = [op for op in data if isinstance(op, dict) and op.get("op") == "pixels"]
        if not pix:
            raise ValueError("ops file has no 'pixels' op")
        if len(data) > 1:
            notes.append("loaded the first pixels op; %d other op(s) ignored" % (len(data) - 1))
        data = pix[0]
    if not isinstance(data, dict):
        raise ValueError("unrecognised JSON map")
    return _from_mapping(data, name), notes


# ----- RGBA / PNG / .pyxra -----------------------------------------------------------
def to_rgba(m):
    buf = bytearray(m.width * m.height * 4)
    cache = {}
    for y, row in enumerate(m.grid):
        for x, ch in enumerate(row):
            if ch not in cache:
                cache[ch] = bytes(rgba(m.legend.get(ch)))
            o = (y * m.width + x) * 4
            buf[o:o + 4] = cache[ch]
    return buf


def to_png(m, scale=1):
    return png.encode_rgba(m.width, m.height, to_rgba(m), scale=scale)


def to_pyxra(m):
    """A one-layer, one-frame Pyxra document holding the map at its origin.

    The document is sized to fit the map placed at (x, y)."""
    w = m.width + max(0, m.x)
    h = m.height + max(0, m.y)
    canvas = bytearray(w * h * 4)
    src = to_rgba(m)
    for y in range(m.height):
        for x in range(m.width):
            dx, dy = x + m.x, y + m.y
            if 0 <= dx < w and 0 <= dy < h:
                s = (y * m.width + x) * 4
                d = (dy * w + dx) * 4
                canvas[d:d + 4] = src[s:s + 4]
    cel = "data:image/png;base64," + base64.b64encode(png.encode_rgba(w, h, canvas)).decode()
    palette = []
    for c in m.legend.values():
        if c and c not in palette:
            palette.append(c)
    return {
        "format": "pyxra",
        "version": 1,
        "name": m.name,
        "w": w,
        "h": h,
        "layers": [{
            "id": 1, "name": m.layer, "visible": True, "locked": False, "lockAlpha": False,
            "opacity": 255, "blend": "normal", "label": None,
        }],
        "frames": [{"id": 2, "duration": 100}],
        "tags": [],
        "labels": [],
        "loadouts": [],
        "palette": palette,
        "guides": [],
        "cels": [{"k": "1:2", "png": cel}],
    }


def _over(dst, src, opacity):
    """Composite one RGBA pixel over another (straight alpha)."""
    sa = src[3] * opacity / (255.0 * 255.0)
    if sa <= 0:
        return dst
    da = dst[3] / 255.0
    oa = sa + da * (1 - sa)
    if oa <= 0:
        return (0, 0, 0, 0)
    rgb = [int(round((src[i] * sa + dst[i] * da * (1 - sa)) / oa)) for i in range(3)]
    return (rgb[0], rgb[1], rgb[2], int(round(oa * 255)))


def from_pyxra(doc, frame=1, layers=None, name="untitled"):
    """Composite a Pyxra document's visible layers at a frame (1-based) into a map.

    Each distinct colour becomes a character; fully transparent pixels become '.'.
    """
    if doc.get("format", "pyxra") != "pyxra":
        raise ValueError("not a Pyxra document")
    w, h = int(doc["w"]), int(doc["h"])
    frames = doc.get("frames") or []
    if not 1 <= frame <= len(frames):
        raise ValueError("document has %d frame(s); frame %d does not exist" % (len(frames), frame))
    fid = frames[frame - 1]["id"]
    cels = {c["k"]: c for c in doc.get("cels", [])}
    pixels = [(0, 0, 0, 0)] * (w * h)
    used_layers = []
    for layer in doc.get("layers", []):
        if layers is not None:
            if layer["name"] not in layers:
                continue
        elif not layer.get("visible", True):
            continue
        cel = cels.get("%s:%s" % (layer["id"], fid))
        if not cel:
            continue
        cw, ch, data = png.decode(base64.b64decode(cel["png"].split(",", 1)[1]))
        ox, oy = int(cel.get("x", 0)), int(cel.get("y", 0))
        opacity = int(layer.get("opacity", 255))
        used_layers.append(layer["name"])
        for y in range(ch):
            for x in range(cw):
                dx, dy = x + ox, y + oy
                if 0 <= dx < w and 0 <= dy < h:
                    o = (y * cw + x) * 4
                    i = dy * w + dx
                    pixels[i] = _over(pixels[i], tuple(data[o:o + 4]), opacity)

    color_char = {}
    legend = {}
    rows = []
    for y in range(h):
        row = []
        for x in range(w):
            p = pixels[y * w + x]
            if p[3] == 0:
                row.append(EMPTY)
                continue
            if p not in color_char:
                if len(color_char) >= len(CHAR_POOL):
                    raise ValueError(
                        "image uses more than %d colours; reduce the palette before importing" % len(CHAR_POOL))
                c = CHAR_POOL[len(color_char)]
                color_char[p] = c
                legend[c] = to_hex(*p)
            row.append(color_char[p])
        rows.append("".join(row))
    layer_name = used_layers[0] if len(used_layers) == 1 else "Layer 1"
    return PixraMap.from_rows(rows, legend=legend, name=doc.get("name") or name, layer=layer_name)


# ----- plain text --------------------------------------------------------------------
def from_text(text, name="untitled", palette=DEFAULT_PALETTE):
    lines = text.replace("\t", "    ").splitlines()
    rows = ["".join(c if valid_char(c) else EMPTY for c in line) for line in lines]
    m = PixraMap.from_rows(rows, name=name)
    assign_colors(m, palette)
    return m


def to_text(m):
    return "\n".join(m.rows()) + "\n"


# ----- entry points ------------------------------------------------------------------
def load(path, palette=DEFAULT_PALETTE, frame=1):
    """Load a map from any supported file. Returns (map, notes)."""
    kind = kind_of(path)
    name = _base_name(path)
    if kind == "png":
        raise ValueError("PNG import is not supported; wrap it in a .pyxra document first")
    if kind == "pyxra":
        with open(path, "r", encoding="utf-8") as f:
            return from_pyxra(json.load(f), frame=frame, name=name), []
    with open(path, "r", encoding="utf-8") as f:
        text = f.read()
    if kind in ("native", "ops"):
        return from_json(json.loads(text), name=name)
    return from_text(text, name=name, palette=palette), []


def save(m, path, scale=1):
    """Write a map in the format implied by the path. Returns the kind written."""
    kind = kind_of(path)
    if kind == "png":
        data = to_png(m, scale=scale)
        with open(path, "wb") as f:
            f.write(data)
        return kind
    if kind == "native":
        payload = json.dumps(to_native(m), indent=2) + "\n"
    elif kind == "ops":
        payload = json.dumps([to_pixels_op(m)], indent=2) + "\n"
    elif kind == "pyxra":
        payload = json.dumps(to_pyxra(m), separators=(",", ":"))
    else:
        payload = to_text(m)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(payload)
    os.replace(tmp, path)
    return kind
