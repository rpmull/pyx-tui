"""Minimal dependency-free PNG encoder/decoder (8-bit, non-interlaced)."""

import struct
import zlib

_SIG = b"\x89PNG\r\n\x1a\n"


def _chunk(kind, data):
    body = kind + data
    return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)


def encode_rgba(width, height, pixels, scale=1):
    """Encode RGBA bytes (width*height*4) as a PNG, optionally upscaled (nearest)."""
    if len(pixels) != width * height * 4:
        raise ValueError("pixel buffer is %d bytes, expected %d" % (len(pixels), width * height * 4))
    scale = max(1, int(scale))
    raw = bytearray()
    for y in range(height):
        row = pixels[y * width * 4:(y + 1) * width * 4]
        if scale > 1:
            row = b"".join(row[i:i + 4] * scale for i in range(0, len(row), 4))
        line = b"\x00" + bytes(row)
        for _ in range(scale):
            raw += line
    ihdr = struct.pack(">IIBBBBB", width * scale, height * scale, 8, 6, 0, 0, 0)
    return _SIG + _chunk(b"IHDR", ihdr) + _chunk(b"IDAT", zlib.compress(bytes(raw), 9)) + _chunk(b"IEND", b"")


def _paeth(a, b, c):
    p = a + b - c
    pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
    if pa <= pb and pa <= pc:
        return a
    return b if pb <= pc else c


def decode(data):
    """Decode a PNG to (width, height, rgba_bytearray).

    Supports bit depth 8 for grayscale, RGB, palette, gray+alpha and RGBA,
    including tRNS transparency. Interlaced images are rejected.
    """
    if data[:8] != _SIG:
        raise ValueError("not a PNG file")
    pos = 8
    ihdr = None
    plte = b""
    trns = b""
    idat = bytearray()
    while pos < len(data):
        (length,) = struct.unpack(">I", data[pos:pos + 4])
        kind = data[pos + 4:pos + 8]
        body = data[pos + 8:pos + 8 + length]
        pos += 12 + length
        if kind == b"IHDR":
            ihdr = struct.unpack(">IIBBBBB", body)
        elif kind == b"PLTE":
            plte = body
        elif kind == b"tRNS":
            trns = body
        elif kind == b"IDAT":
            idat += body
        elif kind == b"IEND":
            break
    if ihdr is None:
        raise ValueError("PNG has no IHDR chunk")
    w, h, depth, ctype, _comp, _filt, interlace = ihdr
    if depth != 8:
        raise ValueError("only 8-bit PNGs are supported (got %d-bit)" % depth)
    if interlace:
        raise ValueError("interlaced PNGs are not supported")
    channels = {0: 1, 2: 3, 3: 1, 4: 2, 6: 4}.get(ctype)
    if channels is None:
        raise ValueError("unsupported PNG colour type %d" % ctype)

    raw = zlib.decompress(bytes(idat))
    stride = w * channels
    bpp = channels
    prev = bytearray(stride)
    out = bytearray(w * h * 4)
    p = 0
    for y in range(h):
        ftype = raw[p]
        line = bytearray(raw[p + 1:p + 1 + stride])
        p += 1 + stride
        for i in range(stride):
            a = line[i - bpp] if i >= bpp else 0
            b = prev[i]
            c = prev[i - bpp] if i >= bpp else 0
            if ftype == 1:
                line[i] = (line[i] + a) & 0xFF
            elif ftype == 2:
                line[i] = (line[i] + b) & 0xFF
            elif ftype == 3:
                line[i] = (line[i] + ((a + b) >> 1)) & 0xFF
            elif ftype == 4:
                line[i] = (line[i] + _paeth(a, b, c)) & 0xFF
        prev = line
        for x in range(w):
            o = (y * w + x) * 4
            if ctype == 6:
                out[o:o + 4] = line[x * 4:x * 4 + 4]
            elif ctype == 2:
                r, g, b = line[x * 3:x * 3 + 3]
                alpha = 255
                if len(trns) >= 6 and struct.unpack(">HHH", trns[:6]) == (r, g, b):
                    alpha = 0
                out[o:o + 4] = bytes((r, g, b, alpha))
            elif ctype == 3:
                idx = line[x]
                r, g, b = plte[idx * 3:idx * 3 + 3]
                alpha = trns[idx] if idx < len(trns) else 255
                out[o:o + 4] = bytes((r, g, b, alpha))
            elif ctype == 0:
                v = line[x]
                alpha = 0 if len(trns) >= 2 and struct.unpack(">H", trns[:2])[0] == v else 255
                out[o:o + 4] = bytes((v, v, v, alpha))
            else:  # ctype 4
                v, alpha = line[x * 2:x * 2 + 2]
                out[o:o + 4] = bytes((v, v, v, alpha))
    return w, h, out
