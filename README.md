# Pixra

Pixra is a terminal (TUI) editor for building pixel maps out of ASCII characters.

A Pixra map uses the same layout as the Pyxra editor's `pixels` op. `rows` holds
one string per pixel row, and `legend` maps each character to a colour:

```json
{"op": "pixels", "layer": "Heart", "frame": 1, "x": 0, "y": 0,
 "legend": {"R": "#e03a3e", "K": "#1d2b53"},
 "rows": [".KK..KK.",
          "KRRKKRRK",
          "KRRRRRRK",
          ".KRRRRK.",
          "..KRRK..",
          "...KK..."]}
```

You draw with characters, and Pixra shows each one in its legend colour as you
type. It can write the result as a Pyxra edit-ops file, a `.pyxra` document, a
PNG, plain text, or its own `.pixra.json` format.

## Install and run

You need Python 3.8 or later. Pixra has no third-party dependencies, except
`windows-curses` on Windows, which pip installs for you.

```
pip install .            # installs the `pixra` command
pixra heart.pixra.json   # open a map, or create it on the first save
pixra --size 32x24       # start a new, unnamed 32x24 map
python -m pixra ...      # run without installing
```

Other commands:

```
pixra convert art.txt art.pyxra             # ASCII art -> Pyxra document
pixra convert heart.pixra.json heart.png --scale 16
pixra convert heart.pixra.json heart.ops.json --layer Heart --origin 10,4
pixra convert sprite.pyxra sprite.pixra.json --frame 3
pixra show heart.pixra.json                 # preview in the terminal (true colour)
pixra show heart.pixra.json --ascii         # print the legend and rows
```

## The editor

```
 PIXRA  heart.pixra.json [+]  8x8  layer 'Heart' @0,0  frame 1
+----------------+                                    LEGEND
|. K K . . K K . |                                    >1 R  ##  #e03a3e   22
|K R W K K R R K |                                     2 r  ##  #a4202a    5
|K W R R R R r K |                                     3 W  ##  #fff1e8    2
...
 NORMAL   2,1 'W'   brush 'R' #e03a3e   color view
```

Each pixel shows its character on a block of its colour. Press `v` to show
coloured characters instead, and `z` to switch between one and two columns per
pixel. Large maps scroll with the cursor.

### Keys

| Key | Action |
|---|---|
| arrows, `h j k l` | move (`H J K L` moves 8) |
| `0` `$` `g` `G` | row start, row end, top, bottom |
| Space, Enter | paint the brush at the cursor |
| `x`, Delete | erase (set to `.`) |
| `d` | pen down/up: moving paints |
| `i` | **type mode**: typed characters become pixels. Enter goes to the next row, Space writes `.`, Esc leaves |
| `b` + char | choose the brush character |
| `p` | pick the brush from under the cursor |
| `1`-`9`, `[` `]`, Tab | choose or cycle legend entries |
| `c` | set the brush character's colour |
| `f` | flood fill |
| `/` | line tool |
| `r` / `R` | rectangle, outline / filled |
| `o` / `O` | ellipse, outline / filled |
| `u`, `U` / Ctrl-R | undo, redo |
| `s` / Ctrl-S | save |
| `e` | export |
| `:` | command line (Tab completes file names) |
| `?` | help |
| `q` | quit (press twice to discard unsaved changes) |
| mouse | left button paints (drag to draw), right button picks the brush |

The line, rectangle and ellipse tools work the same way. Press the tool key,
move to the opposite corner while a preview is shown, then press Space or Enter
to draw. Esc cancels.

### Commands

| Command | Action |
|---|---|
| `:w [file]` | save; `.pixra.json` becomes the working file, other extensions are exported |
| `:wq`, `:x`, `:q`, `:q!` | save and quit, quit, quit without saving |
| `:e <file>` | open a `.pixra.json`, `.ops.json`, `.pyxra` or text file |
| `:new <WxH>` | start a new map |
| `:export <file> [scale]` | write `.pyxra`, `.png`, `.ops.json`, `.txt` or `.json` |
| `:resize <WxH>` | change the canvas size (content stays top-left) |
| `:color <c> <#hex\|null>` | set a legend colour; `null` erases in Pyxra |
| `:uncolor <c>` | remove a legend entry |
| `:brush <c>` | choose the brush |
| `:palette [pico8\|gameboy\|nes\|c64]` | palette that new characters take colours from |
| `:autocolor` | give a colour to every used character that has none |
| `:replace <a> <b>`, `:fill <c>` | replace a character everywhere, fill the canvas |
| `:shift <dx> <dy>`, `:flip h\|v`, `:crop` | move, mirror, or trim the drawing |
| `:layer <name>`, `:origin <x> <y>`, `:frame <n>` | where the map goes in Pyxra |
| `:clear on\|off` | add `clear: true` so the op empties the cel first |
| `:name <name>` | document name |
| `:set autolegend on\|off` | turn automatic colouring of new characters on or off |

## How characters map to colours

- `.` is empty. It is transparent in PNG and `.pyxra` exports, and leaves
  pixels unchanged when the ops file is applied in Pyxra.
- With `autolegend` on (the default), the first time you use a character it gets
  the next unused colour from the current palette. Change it with `c` or
  `:color`.
- A character with no legend entry also leaves Pyxra pixels unchanged. The
  editor shows it underlined and dimmed.
- A `null` colour erases those pixels in Pyxra.
- When a `.pyxra` document is imported, its visible layers are combined at the
  chosen frame and each distinct colour is given a character (`A`-`Z`, then
  `a`-`z`, digits and symbols, up to 93 colours).

## File formats

The extension decides the format.

| Extension | Contents |
|---|---|
| `.pixra.json`, `.json` | the native map: name, size, layer, frame, origin, `clear`, legend, rows |
| `.ops.json` | `[ {"op": "pixels", ...} ]`, ready for the Pyxra bridge: `edit {ops_file: "heart.ops.json"}` |
| `.pyxra` | a Pyxra document with one layer and one frame, containing the map at its origin |
| `.png` | an RGBA image, optionally upscaled by a whole number (export only) |
| `.txt` and others | plain ASCII art; spaces become `.` when read |

## Examples and tests

`examples/` holds a coloured heart (`heart.pixra.json`) and a plain ASCII house
(`house.txt`). Try `pixra examples/house.txt`.

```
python -m unittest discover -s tests
```
