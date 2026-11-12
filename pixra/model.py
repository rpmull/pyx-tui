"""The Pixra map: a grid of ASCII characters plus a legend mapping characters to colours.

This mirrors the Pyxra `pixels` op: `rows` is one string per pixel row, `legend`
maps single characters to '#rrggbb[aa]' colours (or None to erase), and any
character not in the legend leaves the pixel unchanged. EMPTY ('.') is the
conventional "don't touch" character.
"""

EMPTY = "."
MAX_SIDE = 4096


def valid_char(ch):
    """A paintable character: one printable, non-space ASCII character."""
    return isinstance(ch, str) and len(ch) == 1 and 33 <= ord(ch) <= 126


def _check_size(w, h):
    if not (1 <= w <= MAX_SIDE and 1 <= h <= MAX_SIDE):
        raise ValueError("size must be 1..%d on each side (got %dx%d)" % (MAX_SIDE, w, h))


def parse_size(text):
    """'32x24' (or '32X24', '32*24', '32') -> (32, 24)."""
    s = text.lower().replace("*", "x").strip()
    parts = s.split("x")
    try:
        if len(parts) == 1:
            w = h = int(parts[0])
        elif len(parts) == 2:
            w, h = int(parts[0]), int(parts[1])
        else:
            raise ValueError
    except ValueError:
        raise ValueError("bad size %r (use WIDTHxHEIGHT, e.g. 32x24)" % text)
    _check_size(w, h)
    return w, h


class PixraMap:
    def __init__(self, width, height, legend=None, name="untitled", layer="Layer 1",
                 frame=1, x=0, y=0, clear=False):
        _check_size(width, height)
        self.grid = [[EMPTY] * width for _ in range(height)]
        self.legend = dict(legend or {})
        self.name = name
        self.layer = layer
        self.frame = frame
        self.x = x
        self.y = y
        self.clear = clear

    # ----- construction / inspection -------------------------------------------------
    @classmethod
    def from_rows(cls, rows, legend=None, **meta):
        rows = [r.rstrip("\r\n") for r in rows]
        while rows and not rows[-1].strip():
            rows.pop()
        if not rows:
            raise ValueError("map has no rows")
        w = max(len(r) for r in rows) or 1
        m = cls(w, len(rows), legend=legend, **meta)
        for y, row in enumerate(rows):
            for x, ch in enumerate(row):
                m.grid[y][x] = ch if valid_char(ch) else EMPTY
        return m

    @property
    def width(self):
        return len(self.grid[0])

    @property
    def height(self):
        return len(self.grid)

    def rows(self):
        return ["".join(r) for r in self.grid]

    def in_bounds(self, x, y):
        return 0 <= x < self.width and 0 <= y < self.height

    def get(self, x, y):
        return self.grid[y][x] if self.in_bounds(x, y) else None

    def set(self, x, y, ch):
        if self.in_bounds(x, y):
            self.grid[y][x] = ch
            return True
        return False

    def used_chars(self):
        seen = {}
        for row in self.grid:
            for ch in row:
                if ch != EMPTY:
                    seen[ch] = seen.get(ch, 0) + 1
        return seen

    def color_of(self, ch):
        """Colour a character renders as ('#..' or None); characters without a
        legend entry are transparent."""
        return self.legend.get(ch)

    # ----- snapshots for undo --------------------------------------------------------
    def snapshot(self):
        return (tuple(self.rows()), tuple(sorted(self.legend.items())))

    def restore(self, snap):
        rows, legend = snap
        self.grid = [list(r) for r in rows]
        self.legend = dict(legend)

    # ----- drawing -------------------------------------------------------------------
    def paint(self, points, ch):
        n = 0
        for x, y in points:
            if self.in_bounds(x, y) and self.grid[y][x] != ch:
                self.grid[y][x] = ch
                n += 1
        return n

    def flood_fill(self, x, y, ch):
        if not self.in_bounds(x, y):
            return 0
        target = self.grid[y][x]
        if target == ch:
            return 0
        stack = [(x, y)]
        n = 0
        while stack:
            cx, cy = stack.pop()
            if not self.in_bounds(cx, cy) or self.grid[cy][cx] != target:
                continue
            self.grid[cy][cx] = ch
            n += 1
            stack.extend(((cx + 1, cy), (cx - 1, cy), (cx, cy + 1), (cx, cy - 1)))
        return n

    def replace(self, old, new):
        n = 0
        for row in self.grid:
            for i, ch in enumerate(row):
                if ch == old:
                    row[i] = new
                    n += 1
        return n

    def resize(self, width, height):
        _check_size(width, height)
        grid = [[EMPTY] * width for _ in range(height)]
        for y in range(min(height, self.height)):
            for x in range(min(width, self.width)):
                grid[y][x] = self.grid[y][x]
        self.grid = grid

    def shift(self, dx, dy):
        w, h = self.width, self.height
        grid = [[EMPTY] * w for _ in range(h)]
        for y in range(h):
            for x in range(w):
                nx, ny = x + dx, y + dy
                if 0 <= nx < w and 0 <= ny < h:
                    grid[ny][nx] = self.grid[y][x]
        self.grid = grid

    def flip(self, axis):
        if axis in ("h", "x", "horizontal"):
            self.grid = [list(reversed(r)) for r in self.grid]
        elif axis in ("v", "y", "vertical"):
            self.grid = list(reversed(self.grid))
        else:
            raise ValueError("flip axis must be h or v")

    def crop(self):
        """Trim EMPTY borders. Moves the map origin so placement is unchanged."""
        pts = [(x, y) for y, r in enumerate(self.grid) for x, ch in enumerate(r) if ch != EMPTY]
        if not pts:
            return False
        x0 = min(p[0] for p in pts)
        x1 = max(p[0] for p in pts)
        y0 = min(p[1] for p in pts)
        y1 = max(p[1] for p in pts)
        self.grid = [row[x0:x1 + 1] for row in self.grid[y0:y1 + 1]]
        self.x += x0
        self.y += y0
        return True


# ----- shape rasterisers (return lists of (x, y)) ------------------------------------
def line_points(x0, y0, x1, y1):
    """Bresenham line, endpoints included."""
    pts = []
    dx, dy = abs(x1 - x0), -abs(y1 - y0)
    sx = 1 if x0 < x1 else -1
    sy = 1 if y0 < y1 else -1
    err = dx + dy
    while True:
        pts.append((x0, y0))
        if x0 == x1 and y0 == y1:
            return pts
        e2 = 2 * err
        if e2 >= dy:
            err += dy
            x0 += sx
        if e2 <= dx:
            err += dx
            y0 += sy


def _box(x0, y0, x1, y1):
    return min(x0, x1), min(y0, y1), max(x0, x1), max(y0, y1)


def rect_points(x0, y0, x1, y1, filled=False):
    l, t, r, b = _box(x0, y0, x1, y1)
    if filled:
        return [(x, y) for y in range(t, b + 1) for x in range(l, r + 1)]
    pts = set()
    for x in range(l, r + 1):
        pts.add((x, t))
        pts.add((x, b))
    for y in range(t, b + 1):
        pts.add((l, y))
        pts.add((r, y))
    return sorted(pts, key=lambda p: (p[1], p[0]))


def ellipse_points(x0, y0, x1, y1, filled=False):
    """The ellipse inscribed in the box; the outline is a clean 1 px edge."""
    l, t, r, b = _box(x0, y0, x1, y1)
    w, h = r - l + 1, b - t + 1
    cx, cy = (w - 1) / 2.0, (h - 1) / 2.0
    rx, ry = w / 2.0, h / 2.0

    def inside(x, y):
        if not (0 <= x < w and 0 <= y < h):
            return False
        nx, ny = (x - cx) / rx, (y - cy) / ry
        return nx * nx + ny * ny <= 1.0 + 1e-9

    pts = []
    for y in range(h):
        for x in range(w):
            if not inside(x, y):
                continue
            if filled or not all(inside(x + ax, y + ay) for ax, ay in ((1, 0), (-1, 0), (0, 1), (0, -1))):
                pts.append((l + x, t + y))
    return pts


class History:
    """Undo/redo stacks of map snapshots."""

    def __init__(self, limit=200):
        self.limit = limit
        self.undo_stack = []
        self.redo_stack = []

    def push(self, snap):
        self.undo_stack.append(snap)
        if len(self.undo_stack) > self.limit:
            self.undo_stack.pop(0)
        self.redo_stack.clear()

    def undo(self, current):
        if not self.undo_stack:
            return None
        self.redo_stack.append(current)
        return self.undo_stack.pop()

    def redo(self, current):
        if not self.redo_stack:
            return None
        self.undo_stack.append(current)
        return self.redo_stack.pop()

    def clear(self):
        self.undo_stack.clear()
        self.redo_stack.clear()
