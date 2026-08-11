#!/usr/bin/env python3
"""Regenerate the Windows icon resources from resources/unicom-icon.svg."""

from __future__ import print_function

import argparse
import io
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


ICON_SIZES = (16, 24, 32, 48, 64, 128, 256)
RENDER_SIZE = 1024


def load_pillow():
    try:
        from PIL import Image
    except ImportError:
        raise RuntimeError(
            "Pillow is required. Install it with: python -m pip install Pillow"
        )
    return Image


def render_with_pymupdf(svg_data, size, image_module):
    try:
        import fitz
    except ImportError:
        return None

    document = fitz.open(stream=svg_data, filetype="svg")
    try:
        page = document.load_page(0)
        bounds = page.rect
        if bounds.width <= 0 or bounds.height <= 0:
            raise RuntimeError("SVG has an invalid view box")
        matrix = fitz.Matrix(float(size) / bounds.width, float(size) / bounds.height)
        pixmap = page.get_pixmap(matrix=matrix, alpha=True)
        return image_module.open(io.BytesIO(pixmap.tobytes("png"))).convert("RGBA")
    finally:
        document.close()


def render_with_cairosvg(svg_data, size, image_module):
    try:
        import cairosvg
    except ImportError:
        return None

    png_data = cairosvg.svg2png(
        bytestring=svg_data, output_width=size, output_height=size
    )
    return image_module.open(io.BytesIO(png_data)).convert("RGBA")


def render_svg(svg_path, size, image_module):
    svg_data = svg_path.read_bytes()
    image = render_with_pymupdf(svg_data, size, image_module)
    if image is not None:
        return image, "PyMuPDF"

    image = render_with_cairosvg(svg_data, size, image_module)
    if image is not None:
        return image, "CairoSVG"

    raise RuntimeError(
        "An SVG renderer is required. Install one with: "
        "python -m pip install PyMuPDF"
    )


def write_ico(svg_path, ico_path):
    image_module = load_pillow()
    rendered, renderer = render_svg(svg_path, RENDER_SIZE, image_module)
    try:
        resampling = getattr(image_module, "Resampling", image_module).LANCZOS
        base = rendered.resize((256, 256), resampling)
        try:
            ico_path.parent.mkdir(parents=True, exist_ok=True)
            fd, temp_name = tempfile.mkstemp(
                prefix=ico_path.stem + "-", suffix=".ico", dir=str(ico_path.parent)
            )
            os.close(fd)
            temp_path = Path(temp_name)
            try:
                base.save(
                    str(temp_path),
                    format="ICO",
                    sizes=[(size, size) for size in ICON_SIZES],
                    bitmap_format="png",
                )
                os.replace(str(temp_path), str(ico_path))
            finally:
                if temp_path.exists():
                    temp_path.unlink()
        finally:
            base.close()
    finally:
        rendered.close()
    return renderer


def find_windres(explicit_path, project_root):
    candidates = []
    if explicit_path:
        candidates.append(Path(explicit_path).expanduser())

    environment_path = os.environ.get("WINDRES")
    if environment_path:
        candidates.append(Path(environment_path).expanduser())

    path_match = shutil.which("windres")
    if path_match:
        candidates.append(Path(path_match))

    if project_root.drive:
        candidates.append(
            Path(project_root.drive + os.sep)
            / "msys64"
            / "mingw64"
            / "bin"
            / "windres.exe"
        )
    candidates.extend(
        [
            Path("C:/msys64/mingw64/bin/windres.exe"),
            Path("D:/msys64/mingw64/bin/windres.exe"),
        ]
    )

    for candidate in candidates:
        if candidate.is_file():
            return candidate.resolve()

    raise RuntimeError(
        "windres was not found. Add it to PATH, set WINDRES, or pass --windres PATH."
    )


def compile_resource(windres, resource_dir):
    output_path = resource_dir / "unicom_windows_386.syso"
    fd, temp_name = tempfile.mkstemp(
        prefix="unicom-windows-386-", suffix=".syso", dir=str(resource_dir)
    )
    os.close(fd)
    temp_path = Path(temp_name)
    try:
        subprocess.run(
            [
                str(windres),
                "-i",
                "app.rc",
                "-o",
                temp_path.name,
                "-O",
                "coff",
                "--target=pe-i386",
            ],
            cwd=str(resource_dir),
            check=True,
        )
        os.replace(str(temp_path), str(output_path))
    finally:
        if temp_path.exists():
            temp_path.unlink()
    return output_path


def parse_args():
    parser = argparse.ArgumentParser(
        description="Regenerate unicom.ico and the 32-bit Windows resource from SVG."
    )
    parser.add_argument(
        "--windres", metavar="PATH", help="path to windres.exe (optional)"
    )
    return parser.parse_args()


def main():
    args = parse_args()
    project_root = Path(__file__).resolve().parent
    resource_dir = project_root / "resources"
    svg_path = resource_dir / "unicom-icon.svg"
    ico_path = resource_dir / "unicom.ico"

    if not svg_path.is_file():
        raise RuntimeError("SVG source not found: {0}".format(svg_path))
    if not (resource_dir / "app.rc").is_file():
        raise RuntimeError("Windows resource script not found: resources/app.rc")

    renderer = write_ico(svg_path, ico_path)
    windres = find_windres(args.windres, project_root)
    syso_path = compile_resource(windres, resource_dir)

    print("SVG renderer: {0}".format(renderer))
    print("Updated: {0}".format(ico_path.relative_to(project_root)))
    print("Updated: {0}".format(syso_path.relative_to(project_root)))
    print("Icon sizes: {0}".format(", ".join(str(size) for size in ICON_SIZES)))
    print("Next: ./build.sh")


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        print("update-icon.py: {0}".format(error), file=sys.stderr)
        sys.exit(1)
