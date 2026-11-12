"""Editor state and the ':' command language, independent of the terminal UI."""

import os
import shlex

from . import formats
from .colors import DEFAULT_PALETTE, PALETTES, parse_color
from .model import (EMPTY, History, PixraMap, ellipse_points, line_points, parse_size,
                    rect_points, valid_char)


class QuitRequested(Exception):
    pass


SHAPES = {
    "line": lambda a, b, filled: line_points(a[0], a[1], b[0], b[1]),
    "rect": lambda a, b, filled: rect_points(a[0], a[1], b[0], b[1], filled),
    "ellipse": lambda a, b, filled: ellipse_points(a[0], a[1], b[0], b[1], filled),
}

COMMAND_HELP = [
    ("w [file]", "save (format from extension)"),
    ("wq / x", "save and quit"),
    ("q / q!", "quit / quit discarding changes"),
    ("e <file>", "open a map, .txt, .ops.json or .pyxra"),
    ("new <WxH>", "start a new empty map"),
    ("export <file> [scale]", "write .pyxra, .png, .ops.json, .txt or .json"),
    ("resize <WxH>", "change canvas size (keeps top-left)"),
    ("color <c> <#hex|null>", "set a legend colour (null = erase)"),
    ("uncolor <c>", "drop a legend entry (pixels left unchanged)"),
    ("brush <c>", "choose the brush character"),
    ("palette [name]", "list palettes / pick auto-colour palette"),
    ("autocolor", "colour every used char missing from the legend"),
    ("replace <a> <b>", "replace every <a> with <b>"),
    ("fill <c>", "fill the whole canvas with <c>"),
    ("shift <dx> <dy>", "move the drawing (edges fill with '.')"),
    ("flip h|v", "mirror the drawing"),
    ("crop", "trim empty borders (origin moves to match)"),
    ("layer <name>", "Pyxra layer the map targets"),
    ("origin <x> <y>", "where the map is placed on the layer"),
    ("frame <n>", "Pyxra frame the map targets"),
    ("clear on|off", "pixels op clears the cel first"),
    ("name <name>", "document name"),
    ("set autolegend on|off", "auto-colour new characters"),
    ("help", "show the help screen"),
]


class Editor:
    def __init__(self, m=None, path=None, palette=DEFAULT_PALETTE):
        self.map = m or PixraMap(16, 16)
        self.path = path
        self.source = path
        self.palette = palette
        self.history = History()
        self.dirty = False
        self.autolegend = True
        self.brush = self._default_brush()
        self.cx = 0
        self.cy = 0
        self.mode = "normal"          # normal | insert | shape
        self.shape = None             # (kind, filled)
        self.anchor = None
        self.insert_col = 0
        self.pen_down = False
        self.message = ""
        self._group_depth = 0
        self._group_snap = None
        self.show_help = False

    # ----- helpers -------------------------------------------------------------------
    def _default_brush(self):
        for ch in self.map.legend:
            if self.map.legend[ch]:
                return ch
        return "#"

    def display_name(self):
        return os.path.basename(self.path) if self.path else "[new map]"

    def clamp_cursor(self):
        self.cx = max(0, min(self.map.width - 1, self.cx))
        self.cy = max(0, min(self.map.height - 1, self.cy))

    def ensure_color(self, ch):
        """With autolegend on, give a newly used character a palette colour."""
        if not self.autolegend or ch == EMPTY or ch in self.map.legend:
            return
        colors = PALETTES.get(self.palette, PALETTES[DEFAULT_PALETTE])
        taken = {c for c in self.map.legend.values() if c}
        free = [c for c in colors if c not in taken]
        self.map.legend[ch] = free[0] if free else colors[len(self.map.legend) % len(colors)]

    # ----- undo grouping -------------------------------------------------------------
    def begin_group(self):
        if self._group_depth == 0:
            self._group_snap = self.map.snapshot()
        self._group_depth += 1

    def end_group(self):
        if self._group_depth == 0:
            return
        self._group_depth -= 1
        if self._group_depth == 0:
            if self.map.snapshot() != self._group_snap:
                self.history.push(self._group_snap)
                self.dirty = True
            self._group_snap = None

    def mutate(self, fn):
        """Run fn() as one undoable step; returns fn's result."""
        self.begin_group()
        try:
            return fn()
        finally:
            self.end_group()

    def undo(self):
        snap = self.history.undo(self.map.snapshot())
        if snap is None:
            self.message = "nothing to undo"
            return
        self.map.restore(snap)
        self.dirty = True
        self.clamp_cursor()
        self.message = "undone"

    def redo(self):
        snap = self.history.redo(self.map.snapshot())
        if snap is None:
            self.message = "nothing to redo"
            return
        self.map.restore(snap)
        self.dirty = True
        self.clamp_cursor()
        self.message = "redone"

    # ----- cursor & painting ---------------------------------------------------------
    def move(self, dx, dy):
        self.cx += dx
        self.cy += dy
        self.clamp_cursor()
        if self.pen_down and self.mode == "normal":
            self.paint()

    def move_to(self, x, y):
        self.cx, self.cy = x, y
        self.clamp_cursor()

    def paint(self, ch=None):
        ch = self.brush if ch is None else ch

        def go():
            self.ensure_color(ch)
            self.map.set(self.cx, self.cy, ch)
        self.mutate(go)

    def paint_points(self, points, ch=None):
        ch = self.brush if ch is None else ch

        def go():
            self.ensure_color(ch)
            return self.map.paint(points, ch)
        return self.mutate(go)

    def erase(self):
        self.mutate(lambda: self.map.set(self.cx, self.cy, EMPTY))

    def set_brush(self, ch):
        if not valid_char(ch):
            self.message = "brush must be a printable ASCII character"
            return False
        self.brush = ch
        color = self.map.legend.get(ch, "(auto)" if self.autolegend and ch != EMPTY else "(untouched)")
        self.message = "brush %r %s" % (ch, color if color else "erase")
        return True

    def pick(self):
        ch = self.map.get(self.cx, self.cy)
        if ch is not None:
            self.set_brush(ch)

    def cycle_brush(self, step):
        chars = list(self.map.legend) or [self.brush]
        if self.brush in chars:
            i = (chars.index(self.brush) + step) % len(chars)
        else:
            i = 0
        self.set_brush(chars[i])

    def brush_index(self, n):
        chars = list(self.map.legend)
        if 1 <= n <= len(chars):
            self.set_brush(chars[n - 1])
        else:
            self.message = "no legend entry %d" % n

    def flood_fill(self):
        ch = self.brush

        def go():
            self.ensure_color(ch)
            return self.map.flood_fill(self.cx, self.cy, ch)
        n = self.mutate(go)
        self.message = "filled %d pixel(s)" % n

    def toggle_pen(self):
        self.pen_down = not self.pen_down
        if self.pen_down:
            self.begin_group()
            self.paint()
            self.message = "pen down: moving paints with %r" % self.brush
        else:
            self.end_group()
            self.message = "pen up"

    # ----- insert (typing) mode ------------------------------------------------------
    def enter_insert(self):
        if self.pen_down:
            self.toggle_pen()
        self.mode = "insert"
        self.insert_col = self.cx
        self.begin_group()
        self.message = "type characters onto the canvas; Enter = next row, Esc = done"

    def insert_char(self, ch):
        if ch == " ":
            ch = EMPTY
        if not valid_char(ch):
            return
        self.ensure_color(ch)
        self.map.set(self.cx, self.cy, ch)
        if self.cx < self.map.width - 1:
            self.cx += 1

    def insert_newline(self):
        self.cx = self.insert_col
        if self.cy < self.map.height - 1:
            self.cy += 1

    def insert_backspace(self):
        if self.cx > 0:
            self.cx -= 1
        self.map.set(self.cx, self.cy, EMPTY)

    def leave_insert(self):
        self.mode = "normal"
        self.end_group()
        self.message = ""

    # ----- shapes --------------------------------------------------------------------
    def start_shape(self, kind, filled=False):
        if self.pen_down:
            self.toggle_pen()
        self.mode = "shape"
        self.shape = (kind, filled)
        self.anchor = (self.cx, self.cy)
        label = ("filled " if filled else "") + kind
        self.message = "%s: move to the other corner, Space/Enter to draw, Esc to cancel" % label

    def shape_points(self):
        if self.mode != "shape":
            return []
        kind, filled = self.shape
        return SHAPES[kind](self.anchor, (self.cx, self.cy), filled)

    def commit_shape(self):
        pts = self.shape_points()
        ch = self.brush

        def go():
            self.ensure_color(ch)
            return self.map.paint(pts, ch)
        n = self.mutate(go)
        self.cancel_shape()
        self.message = "drew %d pixel(s)" % n

    def cancel_shape(self):
        self.mode = "normal"
        self.shape = None
        self.anchor = None

    # ----- files ---------------------------------------------------------------------
    def save(self, path=None):
        """Save the map. Only the native .json format keeps everything (legend,
        layer, origin), so only that becomes the working file; other formats are
        written as exports."""
        path = path or self.path or self.suggested_path()
        if not path:
            raise ValueError("no file name: use :w <name>.pixra.json")
        kind = formats.save(self.map, path)
        if kind == "native":
            self.path = path
            self.dirty = False
            self.message = "wrote %s (%dx%d)" % (path, self.map.width, self.map.height)
        else:
            self.message = "exported %s (%s); save as .pixra.json to keep the legend" % (path, kind)

    def suggested_path(self):
        if self.source and formats.kind_of(self.source) != "native":
            base = os.path.join(os.path.dirname(self.source), formats._base_name(self.source))
            cand = base + ".pixra.json"
            if not os.path.exists(cand):
                return cand
        return None

    def open(self, path, frame=1):
        m, notes = formats.load(path, palette=self.palette, frame=frame)
        self.map = m
        self.source = path
        self.path = path if formats.kind_of(path) == "native" else None
        self.history.clear()
        self.dirty = False
        self.brush = self._default_brush()
        self.cx = self.cy = 0
        msg = "opened %s (%dx%d)" % (path, m.width, m.height)
        if self.path is None:
            sug = self.suggested_path()
            msg += "; :w saves to %s" % sug if sug else "; use :w <name>.pixra.json to save"
        self.message = msg + ("; " + "; ".join(notes) if notes else "")

    # ----- ':' commands --------------------------------------------------------------
    def run(self, line):
        """Execute a ':' command. Raises QuitRequested to exit."""
        try:
            args = shlex.split(line.strip(), posix=True)
        except ValueError as e:
            self.message = "error: %s" % e
            return
        if not args:
            return
        cmd, rest = args[0], args[1:]
        handler = getattr(self, "cmd_" + cmd.replace("!", "_bang"), None)
        if handler is None:
            alias = {"write": "w", "quit": "q", "edit": "e", "open": "e", "?": "help", "h": "help",
                     "colour": "color", "uncolour": "uncolor", "wq!": "wq"}.get(cmd)
            handler = getattr(self, "cmd_" + alias, None) if alias else None
        if handler is None:
            self.message = "unknown command: %s (try :help)" % cmd
            return
        try:
            handler(*rest)
        except QuitRequested:
            raise
        except TypeError:
            self.message = "wrong arguments for :%s (try :help)" % cmd
        except (ValueError, OSError, KeyError) as e:
            self.message = "error: %s" % e

    def cmd_w(self, path=None):
        self.save(path)

    def cmd_wq(self, path=None):
        self.save(path)
        raise QuitRequested

    cmd_x = cmd_wq

    def cmd_q(self):
        if self.dirty:
            self.message = "unsaved changes: :w to save, :q! to discard"
            return
        raise QuitRequested

    def cmd_q_bang(self):
        raise QuitRequested

    def cmd_e(self, path):
        if self.dirty:
            self.message = "unsaved changes: :w first (or :e! <file>)"
            return
        self.open(path)

    def cmd_e_bang(self, path):
        self.open(path)

    def cmd_new(self, size="16x16"):
        if self.dirty:
            self.message = "unsaved changes: :w first (or :new! <WxH>)"
            return
        self.cmd_new_bang(size)

    def cmd_new_bang(self, size="16x16"):
        w, h = parse_size(size)
        self.map = PixraMap(w, h)
        self.path = None
        self.source = None
        self.history.clear()
        self.dirty = False
        self.cx = self.cy = 0
        self.message = "new %dx%d map" % (w, h)

    def cmd_export(self, path, scale="1"):
        kind = formats.save(self.map, path, scale=int(scale))
        self.message = "exported %s (%s)" % (path, kind)

    def cmd_resize(self, size):
        w, h = parse_size(size)
        self.mutate(lambda: self.map.resize(w, h))
        self.clamp_cursor()
        self.message = "canvas is now %dx%d" % (w, h)

    def cmd_color(self, ch, color):
        if not valid_char(ch):
            raise ValueError("%r is not a single printable ASCII character" % ch)
        c = parse_color(color)
        self.mutate(lambda: self.map.legend.__setitem__(ch, c))
        self.message = "%s = %s" % (ch, c or "null (erase)")

    def cmd_uncolor(self, ch):
        if ch not in self.map.legend:
            raise ValueError("%r is not in the legend" % ch)
        self.mutate(lambda: self.map.legend.pop(ch))
        self.message = "removed %r from the legend" % ch

    def cmd_brush(self, ch):
        self.set_brush(ch)

    def cmd_palette(self, name=None):
        if name is None:
            self.message = "palettes: %s (current: %s)" % (", ".join(sorted(PALETTES)), self.palette)
            return
        if name not in PALETTES:
            raise ValueError("unknown palette %r (have %s)" % (name, ", ".join(sorted(PALETTES))))
        self.palette = name
        self.message = "new characters take colours from %s" % name

    def cmd_autocolor(self):
        added = self.mutate(lambda: formats.assign_colors(self.map, self.palette))
        self.message = "coloured %s" % (" ".join(added) if added else "nothing (all chars have colours)")

    def cmd_replace(self, old, new):
        for c in (old, new):
            if not valid_char(c):
                raise ValueError("%r is not a single printable ASCII character" % c)

        def go():
            self.ensure_color(new)
            return self.map.replace(old, new)
        self.message = "replaced %d pixel(s)" % self.mutate(go)

    def cmd_fill(self, ch):
        if not valid_char(ch):
            raise ValueError("%r is not a single printable ASCII character" % ch)

        def go():
            self.ensure_color(ch)
            for row in self.map.grid:
                row[:] = [ch] * len(row)
        self.mutate(go)
        self.message = "filled canvas with %r" % ch

    def cmd_shift(self, dx, dy="0"):
        self.mutate(lambda: self.map.shift(int(dx), int(dy)))
        self.message = "shifted by %s,%s" % (dx, dy)

    def cmd_flip(self, axis):
        self.mutate(lambda: self.map.flip(axis))
        self.message = "flipped %s" % axis

    def cmd_crop(self):
        ok = self.mutate(self.map.crop)
        self.clamp_cursor()
        self.message = ("cropped to %dx%d, origin %d,%d" % (self.map.width, self.map.height, self.map.x, self.map.y)
                        if ok else "nothing to crop")

    def _meta(self, attr, value, label):
        setattr(self.map, attr, value)
        self.dirty = True
        self.message = "%s: %s" % (label, value)

    def cmd_layer(self, *name):
        if not name:
            raise ValueError("usage: layer <name>")
        self._meta("layer", " ".join(name), "layer")

    def cmd_name(self, *name):
        if not name:
            raise ValueError("usage: name <name>")
        self._meta("name", " ".join(name), "name")

    def cmd_origin(self, x, y):
        self.map.x, self.map.y = int(x), int(y)
        self.dirty = True
        self.message = "origin: %d,%d" % (self.map.x, self.map.y)

    def cmd_frame(self, n):
        n = int(n)
        if n < 1:
            raise ValueError("frames are numbered from 1")
        self._meta("frame", n, "frame")

    def cmd_clear(self, value):
        self._meta("clear", _onoff(value), "clear cel first")

    def cmd_set(self, key, value):
        if key == "autolegend":
            self.autolegend = _onoff(value)
            self.message = "autolegend %s" % ("on" if self.autolegend else "off")
        else:
            raise ValueError("unknown setting %r" % key)

    def cmd_help(self):
        self.show_help = True


def _onoff(value):
    v = value.lower()
    if v in ("on", "true", "yes", "1"):
        return True
    if v in ("off", "false", "no", "0"):
        return False
    raise ValueError("expected on or off, got %r" % value)
