"""The curses terminal UI."""

import curses
import glob
import os
import sys

from .colors import is_light, rgba, xterm256, xterm_basic
from .editor import COMMAND_HELP, Editor, QuitRequested
from .model import EMPTY, line_points, valid_char

SIDE_W = 28
MIN_W, MIN_H = 40, 10

KEY_HELP = [
    ("arrows / h j k l", "move the cursor"),
    ("H J K L", "move 8 cells"),
    ("0  $  g  G", "row start / row end / top / bottom"),
    ("Space / Enter", "paint the brush at the cursor"),
    ("x / Delete", "erase (set to '.')"),
    ("d", "pen down/up: moving paints"),
    ("i", "type mode: typed characters become pixels"),
    ("b <char>", "choose the brush character"),
    ("p", "pick the brush from under the cursor"),
    ("1-9  [  ]  Tab", "choose / cycle legend entries"),
    ("c", "set the brush character's colour"),
    ("f", "flood fill with the brush"),
    ("/", "line tool"),
    ("r  R", "rectangle: outline / filled"),
    ("o  O", "ellipse: outline / filled"),
    ("u   U / Ctrl-R", "undo / redo"),
    ("v", "view: colour blocks / coloured ASCII"),
    ("z", "zoom: 1 or 2 columns per pixel"),
    ("s / Ctrl-S", "save"),
    ("e", "export (.pyxra .png .ops.json .txt)"),
    (":", "command line (Tab completes files)"),
    ("?", "this help"),
    ("q", "quit"),
    ("mouse", "left paints, right picks the brush"),
]


class Colors:
    """Allocates curses colour pairs for map colours on demand."""

    def __init__(self):
        self.ok = curses.has_colors()
        self.default_bg = -1
        self.pairs = {}
        if not self.ok:
            return
        curses.start_color()
        try:
            curses.use_default_colors()
        except curses.error:
            self.default_bg = curses.COLOR_BLACK
        self.n = curses.COLORS
        self.max_pairs = curses.COLOR_PAIRS
        self.black = 16 if self.n >= 256 else curses.COLOR_BLACK
        self.white = 231 if self.n >= 256 else curses.COLOR_WHITE

    def term(self, color):
        r, g, b, _a = rgba(color)
        if self.n >= 256:
            return xterm256(r, g, b)
        return xterm_basic(r, g, b, 16 if self.n >= 16 else 8)

    def pair(self, fg, bg):
        if not self.ok:
            return 0
        key = (fg, bg)
        if key not in self.pairs:
            pid = len(self.pairs) + 1
            if pid >= self.max_pairs:
                return 0
            try:
                curses.init_pair(pid, fg, bg)
            except curses.error:
                return 0
            self.pairs[key] = pid
        return curses.color_pair(self.pairs[key])

    def block(self, color):
        """Attribute for a cell filled with `color`, text in a readable contrast."""
        if not self.ok or color is None:
            return 0
        r, g, b, _a = rgba(color)
        return self.pair(self.black if is_light(r, g, b) else self.white, self.term(color))

    def fg(self, color):
        if not self.ok or color is None:
            return 0
        return self.pair(self.term(color), self.default_bg)

    def ui(self, name):
        if not self.ok:
            return curses.A_REVERSE if name in ("bar", "mode") else 0
        table = {
            "bar": (self.black, curses.COLOR_CYAN),
            "mode": (self.black, curses.COLOR_YELLOW),
            "insert": (self.black, curses.COLOR_GREEN),
            "shape": (self.black, curses.COLOR_MAGENTA),
            "dim": (curses.COLOR_BLUE, self.default_bg),
            "error": (curses.COLOR_RED, self.default_bg),
        }
        fg, bg = table[name]
        return self.pair(fg, bg)


class App:
    def __init__(self, stdscr, editor):
        self.scr = stdscr
        self.ed = editor
        self.colors = Colors()
        self.ox = 0
        self.oy = 0
        self.cell_w = 2
        self.view = "color" if self.colors.ok else "ascii"
        self.cmdline = None
        self.cmdpos = 0
        self.cmd_history = []
        self.hist_i = 0
        self.pending = None
        self.help_off = 0
        self.mouse_down = False
        self.last_drag = None
        self.canvas = (0, 0, 0, 0)    # top, left, rows, cols of the drawable pixel area

    # ----- drawing -------------------------------------------------------------------
    def put(self, y, x, text, attr=0):
        try:
            self.scr.addstr(y, x, text, attr)
        except curses.error:
            pass  # writing to the last cell of the screen raises; harmless

    def layout(self):
        H, W = self.scr.getmaxyx()
        side = SIDE_W if W - SIDE_W >= 24 else 0
        area_w = W - side
        area_h = H - 3
        inner_rows = max(1, area_h - 2)
        inner_cols = max(1, (area_w - 2) // self.cell_w)
        rows = min(inner_rows, self.ed.map.height)
        cols = min(inner_cols, self.ed.map.width)
        self.canvas = (2, 1, rows, cols)
        return H, W, side

    def scroll_to_cursor(self):
        _top, _left, rows, cols = self.canvas
        m = self.ed.map
        if self.ed.cx < self.ox:
            self.ox = self.ed.cx
        elif self.ed.cx >= self.ox + cols:
            self.ox = self.ed.cx - cols + 1
        if self.ed.cy < self.oy:
            self.oy = self.ed.cy
        elif self.ed.cy >= self.oy + rows:
            self.oy = self.ed.cy - rows + 1
        self.ox = max(0, min(self.ox, m.width - cols))
        self.oy = max(0, min(self.oy, m.height - rows))

    def draw(self):
        self.scr.erase()
        H, W, side = self.layout()
        if H < MIN_H or W < MIN_W:
            self.put(0, 0, "terminal too small (need %dx%d)" % (MIN_W, MIN_H))
            self.scr.refresh()
            return
        self.scroll_to_cursor()
        self.draw_title(W)
        self.draw_canvas()
        if side:
            self.draw_side(W - side + 1, H)
        self.draw_status(H, W)
        self.draw_message(H, W)
        if self.ed.show_help:
            self.draw_help(H, W)
        self.place_cursor(H)
        self.scr.refresh()

    def draw_title(self, W):
        ed, m = self.ed, self.ed.map
        title = " PIXRA  %s%s  %dx%d  layer '%s' @%d,%d  frame %d%s " % (
            ed.display_name(), " [+]" if ed.dirty else "", m.width, m.height,
            m.layer, m.x, m.y, m.frame, "  clear" if m.clear else "")
        self.put(0, 0, title.ljust(W)[:W], self.colors.ui("bar") | curses.A_BOLD)

    def draw_canvas(self):
        top, left, rows, cols = self.canvas
        ed, m = self.ed, self.ed.map
        cw = self.cell_w
        # frame
        dim = self.colors.ui("dim")
        width = cols * cw
        self.put(top - 1, left - 1, "+" + "-" * width + "+", dim)
        self.put(top + rows, left - 1, "+" + "-" * width + "+", dim)
        for r in range(rows):
            self.put(top + r, left - 1, "|", dim)
            self.put(top + r, left + width, "|", dim)
        if self.ox > 0:
            self.put(top - 1, left, "<", dim | curses.A_BOLD)
        if self.ox + cols < m.width:
            self.put(top - 1, left + width - 1, ">", dim | curses.A_BOLD)
        if self.oy > 0:
            self.put(top, left - 1, "^", dim | curses.A_BOLD)
        if self.oy + rows < m.height:
            self.put(top + rows - 1, left - 1, "v", dim | curses.A_BOLD)

        preview = set(ed.shape_points())
        brush_color = m.legend.get(ed.brush)
        for r in range(rows):
            y = self.oy + r
            row = m.grid[y]
            for c in range(cols):
                x = self.ox + c
                ch = row[x]
                if (x, y) in preview:
                    ch = ed.brush
                text, attr = self.cell(ch, m.legend, brush_color if (x, y) in preview else None)
                if (x, y) in preview:
                    attr |= curses.A_BOLD
                if x == ed.cx and y == ed.cy:
                    attr |= curses.A_REVERSE
                if ed.mode == "shape" and (x, y) == ed.anchor:
                    attr |= curses.A_UNDERLINE
                self.put(top + r, left + c * cw, text, attr)

    def cell(self, ch, legend, override=None):
        cw = self.cell_w
        pad = " " * (cw - 1)
        if ch == EMPTY and override is None:
            return "." + pad, self.colors.ui("dim")
        has = ch in legend
        color = override if override is not None else legend.get(ch)
        if not has and override is None:
            return ch + pad, curses.A_DIM | curses.A_UNDERLINE      # leaves pixels untouched
        if color is None:
            return ch + pad, curses.A_DIM                          # null: erases
        if self.view == "color":
            return ch + pad, self.colors.block(color)
        return ch + pad, self.colors.fg(color) | curses.A_BOLD

    def draw_side(self, x, H):
        ed, m = self.ed, self.ed.map
        y = 1
        self.put(y, x, "LEGEND", curses.A_BOLD)
        y += 1
        used = m.used_chars()
        entries = list(m.legend.items())
        extras = [c for c in used if c not in m.legend]
        max_lines = max(0, H - 3 - y - 9)
        shown = 0
        for i, (ch, color) in enumerate(entries):
            if shown >= max_lines:
                break
            num = str(i + 1) if i < 9 else " "
            mark = ">" if ch == ed.brush else " "
            attr = curses.A_BOLD if ch == ed.brush else 0
            self.put(y, x, "%s%s %s " % (mark, num, ch), attr)
            if color:
                self.put(y, x + 5, "  ", self.colors.block(color))
                self.put(y, x + 8, "%-9s %4d" % (color, used.get(ch, 0)), attr)
            else:
                self.put(y, x + 5, "-- null     %4d" % used.get(ch, 0), attr | curses.A_DIM)
            y += 1
            shown += 1
        for ch in extras:
            if shown >= max_lines:
                break
            mark = ">" if ch == ed.brush else " "
            self.put(y, x, "%s  %s    untouched %4d" % (mark, ch, used[ch]), curses.A_DIM)
            y += 1
            shown += 1
        hidden = len(entries) + len(extras) - shown
        if hidden > 0:
            self.put(y, x, "  ... %d more" % hidden, curses.A_DIM)
            y += 1
        if not entries and not extras:
            self.put(y, x, "  (empty: paint to add)", curses.A_DIM)
            y += 1
        y += 1
        self.put(y, x, "KEYS", curses.A_BOLD)
        tips = ["i type   d pen   f fill", "/ line   r rect  o oval",
                "b brush  p pick  c color", "u undo   U redo  v view",
                "s save   e export", ": command   ? help"]
        for t in tips:
            y += 1
            if y >= H - 2:
                break
            self.put(y, x, t, curses.A_DIM)

    def draw_status(self, H, W):
        ed, m = self.ed, self.ed.map
        mode = {"normal": "NORMAL", "insert": "TYPE", "shape": "SHAPE"}[ed.mode]
        if ed.mode == "shape":
            mode = ("FILLED " if ed.shape[1] else "") + ed.shape[0].upper()
        if ed.pen_down:
            mode += " PEN"
        style = {"normal": "mode", "insert": "insert", "shape": "shape"}[ed.mode]
        tag = " %s " % mode
        self.put(H - 2, 0, tag, self.colors.ui(style) | curses.A_BOLD)
        under = m.get(ed.cx, ed.cy)
        bc = m.legend.get(ed.brush, "auto" if ed.autolegend and ed.brush != EMPTY else "untouched")
        info = "  %d,%d %r   brush %r %s   %s view%s" % (
            ed.cx, ed.cy, under, ed.brush, bc if bc else "null", self.view,
            "" if ed.autolegend else "   autolegend off")
        if ed.mode == "shape":
            ax, ay = ed.anchor
            info += "   %dx%d" % (abs(ed.cx - ax) + 1, abs(ed.cy - ay) + 1)
        self.put(H - 2, len(tag), info[: max(0, W - len(tag) - 1)])

    def draw_message(self, H, W):
        if self.cmdline is not None:
            self.put(H - 1, 0, (":" + self.cmdline)[: W - 1])
            return
        if self.pending == "brush":
            msg = "brush: press a character"
        else:
            msg = self.ed.message
        attr = self.colors.ui("error") if msg.startswith(("error", "unknown", "unsaved", "wrong")) else 0
        self.put(H - 1, 0, msg[: W - 1], attr)

    def help_lines(self):
        lines = ["KEYS"]
        lines += ["  %-18s %s" % k for k in KEY_HELP]
        lines += ["", "COMMANDS  (press : then type)"]
        lines += ["  :%-22s %s" % k for k in COMMAND_HELP]
        lines += ["", "In type mode, Space writes '.', Enter goes to the next row,",
                  "Backspace erases left, Esc returns to normal mode.",
                  "'.' is empty. Characters with no legend colour leave",
                  "Pyxra pixels unchanged; a null colour erases them."]
        return lines

    def draw_help(self, H, W):
        lines = self.help_lines()
        bw = min(W - 4, max(len(l) for l in lines) + 4)
        bh = min(H - 2, len(lines) + 3)
        top, left = (H - bh) // 2, (W - bw) // 2
        body = bh - 3
        self.help_off = max(0, min(self.help_off, len(lines) - body))
        bar = self.colors.ui("bar")
        self.put(top, left, " Pixra help ".center(bw, "-")[:bw], bar | curses.A_BOLD)
        for i in range(body):
            j = self.help_off + i
            text = lines[j] if j < len(lines) else ""
            self.put(top + 1 + i, left, (" " + text).ljust(bw)[:bw],
                     curses.A_BOLD if text and not text.startswith(" ") else 0)
        more = "up/down scroll, any other key closes" if len(lines) > body else "press any key"
        self.put(top + bh - 2, left, " " * bw)
        self.put(top + bh - 1, left, (" " + more).ljust(bw, "-")[:bw], bar)

    def place_cursor(self, H):
        if self.cmdline is not None:
            try:
                curses.curs_set(1)
            except curses.error:
                pass
            try:
                self.scr.move(H - 1, min(1 + self.cmdpos, self.scr.getmaxyx()[1] - 1))
            except curses.error:
                pass
        else:
            try:
                curses.curs_set(0)
            except curses.error:
                pass

    # ----- input ---------------------------------------------------------------------
    def run(self):
        while True:
            self.draw()
            try:
                key = self.scr.get_wch()
            except curses.error:
                continue
            except KeyboardInterrupt:
                key = "\x03"
            try:
                self.handle(key)
            except QuitRequested:
                return

    def handle(self, key):
        if key == curses.KEY_RESIZE:
            return
        if self.ed.show_help:
            if key in (curses.KEY_UP, "k"):
                self.help_off -= 1
            elif key in (curses.KEY_DOWN, "j", " "):
                self.help_off += 1
            elif key == curses.KEY_PPAGE:
                self.help_off -= 10
            elif key == curses.KEY_NPAGE:
                self.help_off += 10
            else:
                self.ed.show_help = False
            return
        if self.cmdline is not None:
            self.handle_cmdline(key)
            return
        if key == curses.KEY_MOUSE:
            self.handle_mouse()
            return
        if self.pending == "brush":
            self.pending = None
            if isinstance(key, str) and valid_char(key):
                self.ed.set_brush(key)
            else:
                self.ed.message = "brush unchanged"
            return
        if self.pending == "quit":
            self.pending = None
            if key in ("q", "\x03"):
                raise QuitRequested
        if self.ed.mode == "insert":
            self.handle_insert(key)
        elif self.ed.mode == "shape":
            self.handle_shape(key)
        else:
            self.handle_normal(key)

    MOVES = {
        curses.KEY_LEFT: (-1, 0), curses.KEY_RIGHT: (1, 0),
        curses.KEY_UP: (0, -1), curses.KEY_DOWN: (0, 1),
    }
    VI_MOVES = {"h": (-1, 0), "l": (1, 0), "k": (0, -1), "j": (0, 1),
                "H": (-8, 0), "L": (8, 0), "K": (0, -8), "J": (0, 8)}

    def movement(self, key, vi=True):
        d = self.MOVES.get(key) or (self.VI_MOVES.get(key) if vi else None)
        if d:
            self.ed.move(*d)
            return True
        m = self.ed.map
        if key == curses.KEY_HOME or (vi and key == "0"):
            self.ed.move_to(0, self.ed.cy)
        elif key == curses.KEY_END or (vi and key == "$"):
            self.ed.move_to(m.width - 1, self.ed.cy)
        elif vi and key == "g":
            self.ed.move_to(self.ed.cx, 0)
        elif vi and key == "G":
            self.ed.move_to(self.ed.cx, m.height - 1)
        elif key == curses.KEY_PPAGE:
            self.ed.move(0, -max(1, self.canvas[2] - 1))
        elif key == curses.KEY_NPAGE:
            self.ed.move(0, max(1, self.canvas[2] - 1))
        else:
            return False
        return True

    def handle_normal(self, key):
        ed = self.ed
        if self.movement(key):
            return
        if key in (" ", "\n", "\r", curses.KEY_ENTER):
            ed.paint()
        elif key in ("x", curses.KEY_DC):
            ed.erase()
        elif key == "d":
            ed.toggle_pen()
        elif key in ("i", "a"):
            ed.enter_insert()
        elif key == "b":
            self.pending = "brush"
        elif key == "p":
            ed.pick()
        elif isinstance(key, str) and len(key) == 1 and key in "123456789":
            ed.brush_index(int(key))
        elif key in ("]", "\t"):
            ed.cycle_brush(1)
        elif key in ("[", curses.KEY_BTAB):
            ed.cycle_brush(-1)
        elif key == "c":
            current = ed.map.legend.get(ed.brush)
            self.open_cmdline("color %s %s" % (_quote(ed.brush), current or "#"))
        elif key == "f":
            ed.flood_fill()
        elif key == "/":
            ed.start_shape("line")
        elif key in ("r", "R"):
            ed.start_shape("rect", key == "R")
        elif key in ("o", "O"):
            ed.start_shape("ellipse", key == "O")
        elif key == "u":
            ed.undo()
        elif key in ("U", "\x12"):
            ed.redo()
        elif key == "v":
            self.view = "ascii" if self.view == "color" else "color"
            ed.message = "view: %s" % ("colour blocks" if self.view == "color" else "coloured ASCII")
        elif key == "z":
            self.cell_w = 1 if self.cell_w == 2 else 2
            ed.message = "zoom: %d column(s) per pixel" % self.cell_w
        elif key in ("s", "\x13"):
            if ed.path or ed.suggested_path():
                ed.run("w")
            else:
                self.open_cmdline("w %s.pixra.json" % _quote(ed.map.name))
        elif key == "e":
            self.open_cmdline("export %s.pyxra" % _quote(ed.map.name))
        elif key == ":":
            self.open_cmdline("")
        elif key == "?":
            ed.show_help = True
            self.help_off = 0
        elif key in ("q", "\x03"):
            if ed.pen_down:
                ed.toggle_pen()
            if ed.dirty:
                self.pending = "quit"
                ed.message = "unsaved changes: press q again to quit without saving (s saves)"
            else:
                raise QuitRequested
        elif key == "\x1b":
            if ed.pen_down:
                ed.toggle_pen()
            ed.message = ""

    def handle_insert(self, key):
        ed = self.ed
        if key == "\x1b":
            ed.leave_insert()
        elif key in ("\n", "\r", curses.KEY_ENTER):
            ed.insert_newline()
        elif key in (curses.KEY_BACKSPACE, "\x7f", "\x08"):
            ed.insert_backspace()
        elif key == curses.KEY_DC:
            ed.map.set(ed.cx, ed.cy, EMPTY)
        elif self.movement(key, vi=False):
            ed.insert_col = ed.cx
        elif key == curses.KEY_MOUSE:
            self.handle_mouse()
        elif isinstance(key, str) and len(key) == 1:
            ed.insert_char(key)

    def handle_shape(self, key):
        ed = self.ed
        if self.movement(key):
            return
        if key in (" ", "\n", "\r", curses.KEY_ENTER):
            ed.commit_shape()
        elif key in ("\x1b", "q"):
            ed.cancel_shape()
            ed.message = "cancelled"

    def handle_mouse(self):
        try:
            _id, mx, my, _z, bstate = curses.getmouse()
        except curses.error:
            return
        top, left, rows, cols = self.canvas
        cx = (mx - left) // self.cell_w + self.ox
        cy = my - top + self.oy
        inside = 0 <= my - top < rows and 0 <= mx - left < cols * self.cell_w
        pressed = bstate & (curses.BUTTON1_PRESSED | curses.BUTTON1_CLICKED)
        released = bstate & curses.BUTTON1_RELEASED
        motion = bstate & getattr(curses, "REPORT_MOUSE_POSITION", 0)
        if bstate & (curses.BUTTON3_PRESSED | curses.BUTTON3_CLICKED):
            if inside:
                self.ed.move_to(cx, cy)
                self.ed.pick()
            return
        if not inside:
            if released:
                self.end_drag()
            return
        self.ed.move_to(cx, cy)
        if self.ed.mode == "insert":
            self.ed.insert_col = cx
            return
        if self.ed.mode == "shape":
            return
        if pressed and not self.mouse_down:
            self.mouse_down = True
            self.last_drag = (cx, cy)
            self.ed.begin_group()
            self.ed.paint()
            if bstate & curses.BUTTON1_CLICKED:
                self.end_drag()
        elif motion and self.mouse_down:
            # Join successive drag positions so fast strokes have no gaps.
            self.ed.paint_points(line_points(self.last_drag[0], self.last_drag[1], cx, cy))
            self.last_drag = (cx, cy)
        if released:
            if not self.mouse_down:
                self.ed.paint()
            self.end_drag()

    def end_drag(self):
        if self.mouse_down:
            self.mouse_down = False
            self.ed.end_group()

    # ----- command line --------------------------------------------------------------
    def open_cmdline(self, text):
        self.cmdline = text
        self.cmdpos = len(text)
        self.hist_i = len(self.cmd_history)

    def handle_cmdline(self, key):
        s, p = self.cmdline, self.cmdpos
        if key in ("\n", "\r", curses.KEY_ENTER):
            self.cmdline = None
            if s.strip():
                if not self.cmd_history or self.cmd_history[-1] != s:
                    self.cmd_history.append(s)
                self.ed.run(s)
                if self.ed.show_help:
                    self.help_off = 0
            return
        if key in ("\x1b", "\x03"):
            self.cmdline = None
            return
        if key in (curses.KEY_BACKSPACE, "\x7f", "\x08"):
            if not s:
                self.cmdline = None
                return
            if p > 0:
                self.cmdline, self.cmdpos = s[:p - 1] + s[p:], p - 1
        elif key == curses.KEY_DC:
            self.cmdline = s[:p] + s[p + 1:]
        elif key == curses.KEY_LEFT:
            self.cmdpos = max(0, p - 1)
        elif key == curses.KEY_RIGHT:
            self.cmdpos = min(len(s), p + 1)
        elif key in (curses.KEY_HOME, "\x01"):
            self.cmdpos = 0
        elif key in (curses.KEY_END, "\x05"):
            self.cmdpos = len(s)
        elif key == "\x15":
            self.cmdline, self.cmdpos = "", 0
        elif key == curses.KEY_UP and self.cmd_history:
            self.hist_i = max(0, self.hist_i - 1)
            self.cmdline = self.cmd_history[self.hist_i]
            self.cmdpos = len(self.cmdline)
        elif key == curses.KEY_DOWN and self.cmd_history:
            self.hist_i = min(len(self.cmd_history), self.hist_i + 1)
            self.cmdline = self.cmd_history[self.hist_i] if self.hist_i < len(self.cmd_history) else ""
            self.cmdpos = len(self.cmdline)
        elif key == "\t":
            self.complete()
        elif isinstance(key, str) and key.isprintable():
            self.cmdline, self.cmdpos = s[:p] + key + s[p:], p + len(key)

    def complete(self):
        """Complete the last word as a file path."""
        s = self.cmdline[: self.cmdpos]
        head, _, word = s.rpartition(" ")
        if not head:
            return
        matches = sorted(glob.glob(os.path.expanduser(word) + "*"))
        if not matches:
            self.ed.message = "no match"
            return
        common = os.path.commonprefix(matches)
        if len(matches) == 1 and os.path.isdir(common):
            common += os.sep
        if len(matches) > 1:
            self.ed.message = "  ".join(os.path.basename(m) for m in matches[:12])
        new = head + " " + common
        self.cmdline = new + self.cmdline[self.cmdpos:]
        self.cmdpos = len(new)


def _quote(text):
    if text and all(c not in text for c in " '\"\\#"):
        return text
    return "'" + text.replace("'", "'\\''") + "'"


def _main(stdscr, editor):
    curses.raw()
    curses.noecho()
    stdscr.keypad(True)
    try:
        curses.curs_set(0)
    except curses.error:
        pass
    curses.mousemask(curses.ALL_MOUSE_EVENTS | getattr(curses, "REPORT_MOUSE_POSITION", 0))
    curses.mouseinterval(0)
    # Ask for motion events while a button is held so dragging paints.
    sys.stdout.write("\033[?1002h")
    sys.stdout.flush()
    try:
        App(stdscr, editor).run()
    finally:
        sys.stdout.write("\033[?1002l")
        sys.stdout.flush()


def run(editor):
    os.environ.setdefault("ESCDELAY", "25")
    curses.wrapper(_main, editor)
