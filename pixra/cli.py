"""Command-line entry point: `pixra [FILE]`, `pixra convert IN OUT`, `pixra show FILE`."""

import argparse
import os
import sys

from . import __version__, formats
from .colors import DEFAULT_PALETTE, PALETTES, rgba
from .editor import Editor
from .model import EMPTY, PixraMap, parse_size

USAGE = """\
pixra [FILE] [--size WxH] [--palette NAME]   open or create a map in the editor
pixra convert IN OUT [--scale N] [--frame N]  convert between formats without the UI
pixra show FILE [--ascii]                     print a map to the terminal

Formats are chosen by extension: .pixra.json (native), .ops.json (Pyxra edit ops),
.pyxra (Pyxra document), .png (export only), .txt or anything else (plain ASCII art).
"""


def _edit_parser():
    p = argparse.ArgumentParser(prog="pixra", usage=USAGE, add_help=True)
    p.add_argument("file", nargs="?", help="map to open; created on first save if missing")
    p.add_argument("--size", default="16x16", help="size of a new map (default 16x16)")
    p.add_argument("--palette", default=DEFAULT_PALETTE, choices=sorted(PALETTES),
                   help="palette new characters are coloured from (default %s)" % DEFAULT_PALETTE)
    p.add_argument("--frame", type=int, default=1, help="frame to import from a .pyxra (default 1)")
    p.add_argument("--version", action="version", version="pixra " + __version__)
    return p


def _convert(argv):
    p = argparse.ArgumentParser(prog="pixra convert")
    p.add_argument("src")
    p.add_argument("dst")
    p.add_argument("--scale", type=int, default=1, help="integer upscale for .png output")
    p.add_argument("--frame", type=int, default=1, help="frame to read from a .pyxra source")
    p.add_argument("--palette", default=DEFAULT_PALETTE, choices=sorted(PALETTES))
    p.add_argument("--layer", help="set the target layer name")
    p.add_argument("--origin", help="set the placement as X,Y")
    a = p.parse_args(argv)
    m, notes = formats.load(a.src, palette=a.palette, frame=a.frame)
    if a.layer:
        m.layer = a.layer
    if a.origin:
        x, y = a.origin.split(",")
        m.x, m.y = int(x), int(y)
    kind = formats.save(m, a.dst, scale=a.scale)
    for n in notes:
        print("note: " + n, file=sys.stderr)
    print("%s -> %s (%s, %dx%d)" % (a.src, a.dst, kind, m.width, m.height))
    return 0


def _show(argv):
    p = argparse.ArgumentParser(prog="pixra show")
    p.add_argument("file")
    p.add_argument("--ascii", action="store_true", help="print the raw rows and legend")
    p.add_argument("--frame", type=int, default=1)
    a = p.parse_args(argv)
    m, _ = formats.load(a.file, frame=a.frame)
    if a.ascii or not sys.stdout.isatty():
        for ch, color in m.legend.items():
            print("%s = %s" % (ch, color or "null"))
        print()
        print(formats.to_text(m), end="")
        return 0
    for row in m.grid:
        out = []
        for ch in row:
            color = m.legend.get(ch)
            if color is None:
                out.append("\033[0m" + (". " if ch == EMPTY else ch + " "))
            else:
                r, g, b, _ = rgba(color)
                out.append("\033[48;2;%d;%d;%dm  " % (r, g, b))
        print("".join(out) + "\033[0m")
    return 0


def main(argv=None):
    argv = list(sys.argv[1:] if argv is None else argv)
    try:
        if argv and argv[0] == "convert":
            return _convert(argv[1:])
        if argv and argv[0] == "show":
            return _show(argv[1:])
    except (OSError, ValueError) as e:
        print("pixra: error: %s" % e, file=sys.stderr)
        return 1

    a = _edit_parser().parse_args(argv)
    try:
        w, h = parse_size(a.size)
    except ValueError as e:
        print("pixra: error: %s" % e, file=sys.stderr)
        return 2
    ed = Editor(PixraMap(w, h), palette=a.palette)
    if a.file:
        if os.path.exists(a.file):
            try:
                ed.open(a.file, frame=a.frame)
            except (OSError, ValueError, KeyError) as e:
                print("pixra: cannot open %s: %s" % (a.file, e), file=sys.stderr)
                return 1
        else:
            if formats.kind_of(a.file) != "native":
                print("pixra: new maps are saved as .pixra.json; %s will be created on :w" % a.file,
                      file=sys.stderr)
            ed.path = a.file if formats.kind_of(a.file) == "native" else None
            ed.source = a.file
            ed.map.name = formats._base_name(a.file)
            ed.message = "new %dx%d map; press ? for help" % (w, h)
    else:
        ed.message = "new %dx%d map; press ? for help, :w <name>.pixra.json to save" % (w, h)

    if not sys.stdin.isatty() or not sys.stdout.isatty():
        print("pixra: the editor needs an interactive terminal (try `pixra convert` or `pixra show`)",
              file=sys.stderr)
        return 2
    from . import tui
    tui.run(ed)
    return 0
