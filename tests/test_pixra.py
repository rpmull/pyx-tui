import json
import os
import tempfile
import unittest

from pixra import formats, png
from pixra.colors import parse_color, xterm256
from pixra.editor import Editor, QuitRequested
from pixra.model import EMPTY, PixraMap, ellipse_points, line_points, parse_size, rect_points

HEART = [
    ".RR..RR.",
    "RRRRRRRR",
    "RRRRRRRR",
    ".RRRRRR.",
    "..RRRR..",
    "...RR...",
]


class ColorTests(unittest.TestCase):
    def test_parse(self):
        self.assertEqual(parse_color("#F00"), "#ff0000")
        self.assertEqual(parse_color("12ab34"), "#12ab34")
        self.assertEqual(parse_color("#112233ff"), "#112233")
        self.assertEqual(parse_color("#11223380"), "#11223380")
        self.assertIsNone(parse_color("null"))
        with self.assertRaises(ValueError):
            parse_color("red")

    def test_xterm(self):
        self.assertEqual(xterm256(255, 0, 0), 196)
        self.assertEqual(xterm256(0, 0, 0), 16)
        self.assertEqual(xterm256(128, 128, 128), 244)


class ModelTests(unittest.TestCase):
    def test_from_rows_pads(self):
        m = PixraMap.from_rows(["ab", "c", ""])
        self.assertEqual(m.rows(), ["ab", "c."])

    def test_parse_size(self):
        self.assertEqual(parse_size("32x24"), (32, 24))
        self.assertEqual(parse_size("8"), (8, 8))
        with self.assertRaises(ValueError):
            parse_size("0x5")

    def test_flood_fill(self):
        m = PixraMap.from_rows(["....", ".##.", ".#..", "...."])
        self.assertEqual(m.flood_fill(0, 0, "o"), 13)
        self.assertEqual(m.rows(), ["oooo", "o##o", "o#oo", "oooo"])

    def test_shapes(self):
        self.assertEqual(line_points(0, 0, 3, 3), [(0, 0), (1, 1), (2, 2), (3, 3)])
        self.assertEqual(len(rect_points(0, 0, 3, 2)), 10)
        self.assertEqual(len(rect_points(3, 2, 0, 0, filled=True)), 12)
        filled = set(ellipse_points(0, 0, 6, 6, True))
        outline = set(ellipse_points(0, 0, 6, 6))
        self.assertTrue(outline < filled)
        self.assertIn((3, 0), outline)
        self.assertNotIn((0, 0), filled)

    def test_crop_moves_origin(self):
        m = PixraMap.from_rows(["....", "..#.", "..##", "...."])
        self.assertTrue(m.crop())
        self.assertEqual(m.rows(), ["#.", "##"])
        self.assertEqual((m.x, m.y), (2, 1))

    def test_shift_flip(self):
        m = PixraMap.from_rows(["ab.", "..."])
        m.shift(1, 1)
        self.assertEqual(m.rows(), ["...", ".ab"])
        m.flip("h")
        self.assertEqual(m.rows(), ["...", "ba."])


class FormatTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.m = PixraMap.from_rows(HEART, legend={"R": "#e03a3e"}, name="heart", layer="Heart")

    def tearDown(self):
        self.tmp.cleanup()

    def path(self, name):
        return os.path.join(self.tmp.name, name)

    def test_native_roundtrip(self):
        p = self.path("h.pixra.json")
        formats.save(self.m, p)
        m2, notes = formats.load(p)
        self.assertEqual(m2.rows(), HEART)
        self.assertEqual(m2.legend, {"R": "#e03a3e"})
        self.assertEqual(m2.layer, "Heart")
        self.assertEqual(notes, [])

    def test_ops_file_is_pyxra_pixels_op(self):
        p = self.path("h.ops.json")
        self.m.x, self.m.y, self.m.clear = 4, 2, True
        formats.save(self.m, p)
        with open(p) as f:
            ops = json.load(f)
        self.assertEqual(ops, [{"op": "pixels", "layer": "Heart", "frame": 1, "x": 4, "y": 2,
                                "legend": {"R": "#e03a3e"}, "rows": HEART, "clear": True}])
        m2, _ = formats.load(p)
        self.assertEqual((m2.x, m2.y, m2.clear), (4, 2, True))

    def test_pyxra_roundtrip(self):
        self.m.legend["."] = None  # explicit eraser stays transparent
        p = self.path("h.pyxra")
        formats.save(self.m, p)
        with open(p) as f:
            doc = json.load(f)
        self.assertEqual((doc["w"], doc["h"]), (8, 6))
        self.assertEqual(doc["layers"][0]["name"], "Heart")
        self.assertEqual(doc["palette"], ["#e03a3e"])
        m2, _ = formats.load(p)
        self.assertEqual(m2.rows(), [r.replace("R", "A") for r in HEART])
        self.assertEqual(m2.legend, {"A": "#e03a3e"})

    def test_pyxra_origin_offsets_canvas(self):
        self.m.x, self.m.y = 2, 3
        doc = formats.to_pyxra(self.m)
        self.assertEqual((doc["w"], doc["h"]), (10, 9))

    def test_png(self):
        data = formats.to_png(self.m, scale=3)
        w, h, px = png.decode(data)
        self.assertEqual((w, h), (24, 18))
        self.assertEqual(tuple(px[0:4]), (0, 0, 0, 0))
        o = (3 * w + 3) * 4  # pixel (1,1) in map space, scaled
        self.assertEqual(tuple(px[o:o + 4]), (0xe0, 0x3a, 0x3e, 255))

    def test_text_import_assigns_colors(self):
        p = self.path("art.txt")
        with open(p, "w") as f:
            f.write("  ##\n #@@#\n")
        m, _ = formats.load(p)
        self.assertEqual(m.rows(), ["..##.", ".#@@#"])
        self.assertEqual(set(m.legend), {"#", "@"})

    def test_ops_list_takes_first_pixels_op(self):
        m, notes = formats.from_json([{"op": "rect"}, formats.to_pixels_op(self.m)])
        self.assertEqual(m.rows(), HEART)
        self.assertTrue(notes)


class EditorTests(unittest.TestCase):
    def test_paint_autolegend_undo(self):
        ed = Editor(PixraMap(4, 4))
        ed.set_brush("X")
        ed.move(1, 1)
        ed.paint()
        self.assertEqual(ed.map.get(1, 1), "X")
        self.assertIn("X", ed.map.legend)
        self.assertTrue(ed.dirty)
        ed.undo()
        self.assertEqual(ed.map.get(1, 1), EMPTY)
        self.assertNotIn("X", ed.map.legend)
        ed.redo()
        self.assertEqual(ed.map.get(1, 1), "X")

    def test_insert_mode_is_one_undo_step(self):
        ed = Editor(PixraMap(6, 3))
        ed.enter_insert()
        for c in "ab c":
            ed.insert_char(c)
        ed.insert_newline()
        ed.insert_char("d")
        ed.leave_insert()
        self.assertEqual(ed.map.rows(), ["ab.c..", "d.....", "......"])
        ed.undo()
        self.assertEqual(ed.map.rows(), ["......"] * 3)

    def test_shape(self):
        ed = Editor(PixraMap(5, 5))
        ed.set_brush("#")
        ed.start_shape("rect")
        ed.move(4, 2)
        ed.commit_shape()
        self.assertEqual(ed.map.rows()[:3], ["#####", "#...#", "#####"])

    def test_pen(self):
        ed = Editor(PixraMap(5, 1))
        ed.set_brush("=")
        ed.toggle_pen()
        ed.move(1, 0)
        ed.move(1, 0)
        ed.toggle_pen()
        ed.move(1, 0)
        self.assertEqual(ed.map.rows(), ["===.."])
        ed.undo()
        self.assertEqual(ed.map.rows(), ["....."])

    def test_commands(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        ed = Editor(PixraMap(4, 4))
        ed.run("color R #ff0000")
        self.assertEqual(ed.map.legend["R"], "#ff0000")
        ed.run("color E null")
        self.assertIsNone(ed.map.legend["E"])
        ed.run("fill R")
        ed.run("replace R E")
        self.assertEqual(ed.map.rows(), ["EEEE"] * 4)
        ed.run("resize 6x2")
        self.assertEqual((ed.map.width, ed.map.height), (6, 2))
        ed.run("layer Back ground")
        self.assertEqual(ed.map.layer, "Back ground")
        ed.run("origin 3 4")
        self.assertEqual((ed.map.x, ed.map.y), (3, 4))
        ed.run("bogus")
        self.assertIn("unknown command", ed.message)
        ed.run("color R notacolour")
        self.assertTrue(ed.message.startswith("error"))
        with self.assertRaises(QuitRequested):
            ed.run("q!")
        ed.run("q")
        self.assertIn("unsaved", ed.message)
        p = os.path.join(tmp.name, "m.pixra.json")
        ed.run("w " + p)
        self.assertFalse(ed.dirty)
        self.assertEqual(ed.path, p)
        ed.run("export " + os.path.join(tmp.name, "m.pyxra"))
        self.assertTrue(os.path.exists(os.path.join(tmp.name, "m.pyxra")))
        self.assertEqual(ed.path, p)
        with self.assertRaises(QuitRequested):
            ed.run("q")

    def test_opening_text_suggests_native_path(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        src = os.path.join(tmp.name, "art.txt")
        with open(src, "w") as f:
            f.write("#@\n")
        ed = Editor()
        ed.open(src)
        self.assertIsNone(ed.path)
        ed.run("w")
        self.assertEqual(ed.path, os.path.join(tmp.name, "art.pixra.json"))
        with open(src) as f:
            self.assertEqual(f.read(), "#@\n")  # the source text is left alone


if __name__ == "__main__":
    unittest.main()
