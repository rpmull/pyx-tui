# Pixra

Pixra is a fast, single-binary terminal editor for Pyxra pixel-art documents. It
represents every colour as an ASCII character, so you can draw by typing, read
a layer as text, and script any edit from the shell.

```
 PIXRA  world.pyxra [+]  32x16  layer 4/5 "Trees"  frame 1/1  zoom 2  mixed
┌────────────────────────────────────────────────────────────────┐   LAYERS
│A A A A A A A A A A A A A A A A A A A A A A A A A A A S S A A A │     5 ●  House
│A A A A A A A A A A A A A A A A A A A A A A A A A A S S S S A A │   > 4 ●  Trees
│A A A A A A A A A A A A A A # # A A A A A A A T T A A A A A A A │     3 ●  Ground
│A A A A A T A A A A A A A # # # # A A A A A T T T T A A A A A A │     2 ●  Sun
│A A A A T T T A A A A A # # # # # # A A A A T T T T A A A A A A │     1 ●  Sky
│A A A T T T T T A A A # # # # # # # # A A A A T T A A A A A A A │
│A A A A A | A A A A A A H H H H H H A A A A A | | A A A A A A A │   LEGEND
│G G G G G G G G G G G G G G G G G G G G G G G G G G G G G G G G │   >1 A  ██ #29adff
└────────────────────────────────────────────────────────────────┘    2 G  ██ #008751
 NORMAL  5,6 T #1d7a3a   brush T ██ #1d7a3a
```

- **Edits any Pyxra document.** Pixra reads and writes `.pyxra` files with all
  their layers, frames, tags, palette and cels. Fields it doesn't use are written
  back unchanged, and cels you didn't touch keep their original PNG data, byte
  for byte.
- **Builds worlds from layers.** You can add, delete, rename, reorder,
  duplicate, hide, lock, merge, blend and set the opacity of layers. Frames and
  tags can be added and edited too.
- **Colour and plain-text views.** `color` shows true-colour blocks, `mixed`
  shows the blocks with their characters, and `text` shows plain ASCII.
- **Zoom.** There are 7 levels from 1/8 to 8. Zoomed out, each character cell
  shows two pixels, one above the other, which keeps pixels square.
- **Every operation is a command.** The same commands work on the editor's `:`
  line, from the shell (`pixra rect world.pyxra 0 0 9 9 '#'`), and in scripts
  (`pixra do world.pyxra -f build.txt`).
- **Fast and portable.** Pixra is written in Go. It builds to one static binary
  for Linux, macOS, Windows and FreeBSD on amd64 and arm64, with no runtime
  dependencies. Cels are decoded and encoded in parallel. A 1024×1024 document
  with 8 layers and 6 frames opens in about 0.25 s.

## Install

You need Go 1.22 or later to build Pixra.

```
go install github.com/rpmull/pyx-tui/cmd/pixra@latest   # or:
make build                       # ./pixra for this machine
make dist                        # every platform into dist/
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o pixra.exe ./cmd/pixra
```

`make dist` builds `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`,
`windows/amd64`, `windows/arm64` and `freebsd/amd64`. Cross-compiling needs
nothing beyond Go.

## How characters map to colours

- Each colour in a document has one character, and `.` means transparent.
- The palette's colours and any colours already used in the document get
  characters automatically, starting with `A`-`Z`, then `a`-`z`, then digits and
  symbols (93 characters in all).
- The mapping is saved in the `.pyxra` file under a `pixra` key. If another
  tool removes that key, Pixra rebuilds the same mapping from the palette and
  pixels.
- Typing a character that has no colour yet gives it the next unused palette
  swatch, then the next colour from the built-in palette (`pico8` by default).
- `color T #1d7a3a` sets the colour for `T`. If `T` was already drawn, those
  pixels change colour across every layer and frame.
- `char T t` changes which character stands for a colour.
- Colour arguments accept a character or a hex colour (`#rgb`, `#rrggbb` or
  `#rrggbbaa`).

## The editor

```
pixra world.pyxra              # open, or create on first save
pixra --size 64x32             # new, unsaved document
```

| Key | Action |
|---|---|
| arrows, `h j k l` (`H J K L` ×8) | move; at zoom-out, moves one cell |
| `0 $ g G` | row start, row end, top, bottom |
| Space, Enter / `x`, Delete | paint the brush / erase |
| `d` | pen down/up: moving paints |
| `i` | **type mode**: typed characters become pixels (Enter = next row, Space or `.` erases, Esc leaves) |
| `b` + char, `p` | choose the brush / pick it from the canvas |
| `1`-`9`, `[ ]`, Tab | choose or cycle legend characters |
| `c` | set the brush character's colour |
| `f`, `/`, `r R`, `o O` | flood fill, line, rectangle, ellipse (capitals fill) |
| `u`, `U` / Ctrl-R | undo, redo |
| `+ -`, mouse wheel | zoom |
| `v` | view: color → mixed → text |
| `{ }`, `A`, `V`, `S` | layer down/up, add layer, show/hide, solo |
| `< >` | previous / next frame |
| `s` / Ctrl-S, `e` | save, export |
| `:` | command line, with history and Tab completion of commands, layers and files |
| `?`, `q` | help, quit |
| mouse | left button paints (drag to draw), right button picks a colour |

For the line, rectangle and ellipse tools, press the key at one corner, move to
the other corner (a preview follows), then press Space to draw.

## Commands

You can run any command in three ways:

```
:rect 0 0 9 9 # --fill                     # in the editor
pixra rect world.pyxra 0 0 9 9 '#' --fill  # from the shell: COMMAND FILE ARGS
pixra layer add world.pyxra Trees          # two-word commands work the same way
pixra do world.pyxra 'layer Trees' 'text 2 2 TT.TT' 'export world.png --scale 8'
pixra do world.pyxra -f build.txt          # one command per line, # for comments
```

From the shell, Pixra saves the file when a command changes it. Shell options:
`-o OUT` saves somewhere else, `-n` is a dry run, `-l LAYER` and `-F N` choose
the starting layer and frame, and `-q` runs quietly. Separate several commands on
one line with `;`. `pixra help` lists every command, and `pixra help COMMAND`
explains one.

| Group | Commands |
|---|---|
| File | `new WxH`, `open FILE`, `save [FILE]`, `export FILE [--scale N] [--layer L] [--frame N] [--all-frames] [--all-layers] [--clear] [--add-layers]`, `import FILE [--at X,Y] [--name N]`, `apply OPS.json`, `info`, `rename NAME`, `quit`, `quit!`, `wq` |
| Canvas | `resize WxH [X Y] [--center]`, `crop [X Y W H]` |
| Layers | `layers`, `layer L`, `layer add NAME [--below] [--top]`, `layer del`, `layer rename`, `layer up`, `layer down`, `layer move L POS`, `layer dup`, `layer show`, `layer hide`, `layer toggle`, `layer lock`, `layer unlock`, `layer opacity N\|N%`, `layer blend MODE`, `layer merge`, `layer clear [--all]` |
| Frames | `frames`, `frame N`, `frame add`, `frame dup`, `frame del`, `frame move N POS`, `frame duration [N] MS`, `tags`, `tag add NAME FROM TO`, `tag del NAME` |
| Colours | `legend`, `color C #HEX`, `char OLD NEW`, `brush C`, `palette`, `palette add`, `palette rm`, `palette load pico8\|gameboy\|nes\|c64`, `recolor FROM TO` |
| Draw | `px X Y [C]`, `erase X Y`, `line`, `rect`, `ellipse X0 Y0 X1 Y1 [C] [--fill]`, `fill X Y [C]`, `text X Y STRING`, `pixels X Y ROW... [--erase]`, `paste FILE [X Y]`, `clear`, `replace A B`, `shift DX DY`, `flip h\|v` (`--all` every frame, `--doc` every layer) |
| History | `undo`, `redo` |
| View | `print [--layer L]`, `goto X Y`, `zoom in\|out\|1/8..8`, `view color\|mixed\|text`, `solo`, `set autolegend on\|off`, `set palette NAME`, `help` |

Layers can be named by name, `#id`, or position (1 is the bottom layer). Frames
are numbered from 1.

Other shell commands: `pixra new FILE [WxH] [LAYER]`, `pixra info FILE`,
`pixra show FILE [--view color|mixed|text] [--zoom 1|2] [--layer L] [--frame N]`
(a true-colour preview in the terminal), and `pixra convert IN OUT [export options]`.

## File formats

Pixra picks the format from the file extension.

| Extension | Read | Write |
|---|---|---|
| `.pyxra` | the whole document | the whole document; untouched cels and unknown fields are kept as they were |
| `.pixra.json`, `.json` | the readable world: legend plus ASCII rows for every layer and frame. Also reads single maps (`rows` + `legend`) and bare Pyxra `pixels` ops | the readable world |
| `.ops.json` | Pyxra edit ops: `pixels`, `rect`, `line`, `ellipse`, `fill`, `clear`, `add_layer`, `set_layer`, `delete_layer`, `duplicate_layer`, `move_layer` and `recolor`. Other op types are skipped with a note. | one `pixels` op per layer and frame, for `edit {ops_file}`; `--add-layers` and `--clear` reproduce the document exactly |
| `.png` | a single-layer document | the composite image, or one `--layer`; `--scale N` enlarges it, and `--all-frames` lays out a horizontal sprite sheet |
| `.txt` and others | ASCII rows, coloured from the palette | the frame's rows: the topmost visible character, or one `--layer` |

`apply` runs an ops file as a single undo step, and `import` adds another file's
layers above the current layer.

## Examples

```
pixra show examples/world.pixra.json            # the world in the screenshot
pixra examples/house.txt                        # plain ASCII art, auto-coloured
pixra convert examples/world.pixra.json world.pyxra
pixra convert examples/heart.pixra.json heart.png --scale 16
```

## Development

```
make test        # go vet + go test
```

The code is in three packages:

- `internal/pyxra`: the document model, the `.pyxra` reader and writer, and the
  drawing routines (rasterisers).
- `internal/session`: the legend, undo history, file formats and the command
  registry shared by the editor and the CLI.
- `internal/tui`: the editor, built on tcell.
