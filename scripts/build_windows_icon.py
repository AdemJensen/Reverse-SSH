#!/usr/bin/env python3
"""Build the Windows icon and its x64/ARM64 Go resource objects.

Run with llvm-rc and llvm-cvtres on PATH, or pass --llvm-bin DIR.
"""

import argparse
from pathlib import Path
import struct
import subprocess
import tempfile
import zlib


SIZES = (16, 24, 32, 48, 64, 128, 256)
MINT = (187, 231, 213)
TEAL = (19, 63, 53)


def arrow_segments():
    lines = (
        ((0.26, 0.377), (0.74, 0.377)),
        ((0.63, 0.272), (0.74, 0.377)),
        ((0.74, 0.377), (0.63, 0.482)),
        ((0.74, 0.623), (0.26, 0.623)),
        ((0.37, 0.518), (0.26, 0.623)),
        ((0.26, 0.623), (0.37, 0.728)),
    )
    segments = []
    for (x, y), (end_x, end_y) in lines:
        dx, dy = end_x - x, end_y - y
        segments.append((x, y, dx, dy, 1 / (dx * dx + dy * dy)))
    return segments


def inside_stroke(x, y, segments, radius_squared):
    for start_x, start_y, dx, dy, inverse_length in segments:
        offset_x, offset_y = x - start_x, y - start_y
        progress = (offset_x * dx + offset_y * dy) * inverse_length
        progress = max(0, min(1, progress))
        distance_x = offset_x - progress * dx
        distance_y = offset_y - progress * dy
        if distance_x * distance_x + distance_y * distance_y <= radius_squared:
            return True
    return False


def render(size):
    """Return straight-alpha RGBA pixels in top-down row order."""
    sampling = 4
    samples = [(index + 0.5) / (size * sampling) for index in range(size * sampling)]
    segments = arrow_segments()
    radius = 0.18
    inset = 0.445 - radius
    stroke_radius = 0.048 if size <= 24 else 0.039
    output = bytearray()
    for y in range(size):
        rows = samples[y * sampling : (y + 1) * sampling]
        for x in range(size):
            columns = samples[x * sampling : (x + 1) * sampling]
            red = green = blue = total = 0
            for row in rows:
                corner_y = max(abs(row - 0.5) - inset, 0)
                if size >= 48:
                    tint = (0.5 - row) * 28
                    background = (
                        round(MINT[0] + tint),
                        round(MINT[1] + tint / 2),
                        round(MINT[2] + tint * 0.7),
                    )
                else:
                    background = MINT
                for column in columns:
                    corner_x = max(abs(column - 0.5) - inset, 0)
                    if corner_x * corner_x + corner_y * corner_y > radius * radius:
                        continue
                    color = background
                    if 0.2 <= column <= 0.8 and 0.22 <= row <= 0.78:
                        if inside_stroke(column, row, segments, stroke_radius * stroke_radius):
                            color = TEAL
                    red += color[0]
                    green += color[1]
                    blue += color[2]
                    total += 1
            if total:
                alpha = round(total * 255 / (sampling * sampling))
                output.extend((round(red / total), round(green / total), round(blue / total), alpha))
            else:
                output.extend((0, 0, 0, 0))
    return bytes(output)


def png_chunk(name, data):
    return (
        struct.pack(">I", len(data))
        + name
        + data
        + struct.pack(">I", zlib.crc32(name + data))
    )


def png_image(size, rgba):
    row_bytes = size * 4
    scanlines = b"".join(
        b"\x00" + rgba[start : start + row_bytes] for start in range(0, len(rgba), row_bytes)
    )
    header = struct.pack(">II5B", size, size, 8, 6, 0, 0, 0)
    return (
        b"\x89PNG\r\n\x1a\n"
        + png_chunk(b"IHDR", header)
        + png_chunk(b"IDAT", zlib.compress(scanlines, 9))
        + png_chunk(b"IEND", b"")
    )


def bitmap_image(size, rgba):
    pixels = bytearray()
    mask = bytearray()
    mask_stride = (size + 31) // 32 * 4
    for y in range(size - 1, -1, -1):
        row_mask = bytearray(mask_stride)
        for x in range(size):
            index = (y * size + x) * 4
            red, green, blue, alpha = rgba[index : index + 4]
            pixels.extend((blue, green, red, alpha))
            if alpha == 0:
                row_mask[x // 8] |= 0x80 >> (x % 8)
        mask.extend(row_mask)
    # An ICO bitmap includes both the color image and a monochrome AND mask.
    header = struct.pack("<IiiHHIIiiII", 40, size, size * 2, 1, 32, 0, len(pixels), 0, 0, 0, 0)
    return header + pixels + mask


def build_ico(path):
    directory = bytearray(struct.pack("<3H", 0, 1, len(SIZES)))
    images = bytearray()
    offset = len(directory) + 16 * len(SIZES)
    for size in SIZES:
        rgba = render(size)
        image = png_image(size, rgba) if size == 256 else bitmap_image(size, rgba)
        # In an ICO directory entry, a zero dimension represents 256 pixels.
        dimension = size % 256
        directory.extend(struct.pack("<4B2H2I", dimension, dimension, 0, 0, 1, 32, len(image), offset))
        images.extend(image)
        offset += len(image)
    path.write_bytes(directory + images)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--llvm-bin", type=Path, help="directory containing llvm-rc and llvm-cvtres")
    args = parser.parse_args()
    scripts = Path(__file__).resolve().parent
    app = scripts.parent
    compiler = "llvm-rc"
    converter = "llvm-cvtres"
    if args.llvm_bin:
        compiler = str(args.llvm_bin.resolve() / compiler)
        converter = str(args.llvm_bin.resolve() / converter)
    build_ico(scripts / "reverse-ssh.ico")
    with tempfile.TemporaryDirectory() as staging:
        resource = Path(staging) / "reverse-ssh.res"
        subprocess.run(
            [compiler, "/no-preprocess", "/FO", str(resource), "windows-icon.rc"],
            cwd=scripts,
            check=True,
        )
        for architecture, machine in (("amd64", "X64"), ("arm64", "ARM64")):
            output = app / f"icon_windows_{architecture}.syso"
            subprocess.run(
                [converter, f"/MACHINE:{machine}", "/TIMESTAMP:0", f"/OUT:{output}", str(resource)],
                check=True,
            )


if __name__ == "__main__":
    main()
