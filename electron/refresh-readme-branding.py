#!/usr/bin/env python3
"""Refresh only the wordmark blocks in the eight simulated README screenshots.

Run after sync-brand-assets.py. --check does not write; --output-root previews
changes in isolation. Requires the sibling Aiscoo renderer, Pillow and jpegtran.
"""
import argparse
from io import BytesIO
import importlib.util
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET

from PIL import Image, ImageChops, JpegImagePlugin, __version__ as pillow_version

ROOT = Path(__file__).resolve().parent.parent
ET.register_namespace("", "http://www.w3.org/2000/svg")
SCREENSHOTS = tuple(f"{kind}.{language}.jpg"
                    for kind in ("dashboard", "models", "plugin-marketplace", "usage-logs")
                    for language in ("en", "zh-CN"))


def refresh(source_root, output_root, *, check=False):
    # 原因：-I 不加载脚本目录；显式复用同步脚本的来源选择器，避免两处布局规则漂移。
    sync_spec = importlib.util.spec_from_file_location(
        "admin_brand_sync", ROOT / "electron/sync-brand-assets.py")
    sync = importlib.util.module_from_spec(sync_spec)
    sync_spec.loader.exec_module(sync)
    _, _, generator = sync.resolve_brand_source(source_root)
    spec = importlib.util.spec_from_file_location("aiscoo_assets", generator)
    assets = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(assets)
    if pillow_version != assets.PILLOW_VERSION:
        raise RuntimeError(f"Expected Pillow {assets.PILLOW_VERSION}, got {pillow_version}")
    jpegtran = shutil.which("jpegtran")
    if jpegtran is None:
        raise RuntimeError("jpegtran is required for block-local JPEG replacement")
    svg = ET.fromstring((ROOT / "web/public/brand/wordmark-light.svg").read_text())
    # 原因：截图只有约 90px 的字标位；裁去资源画布留白，保留原有字形与胶囊眼。
    svg.set("viewBox", "40 35 501.15 129")
    svg.set("width", "501.15")
    svg.set("height", "129")
    art = ET.tostring(svg, encoding="unicode")
    mismatches = []
    with tempfile.TemporaryDirectory(prefix="aiscoo-readme-brand-") as temporary:
        work = Path(temporary)
        for name in SCREENSHOTS:
            source = ROOT / "assets/screenshots" / name
            with Image.open(source) as image:
                if image.size != (1920, 1080) or JpegImagePlugin.get_sampling(image) != 0:
                    raise ValueError(f"Unexpected screenshot canvas/subsampling: {name}")
                original = image.convert("RGB")
                qtables = image.quantization
            public = name.startswith("models.")
            x, y, width, height = (354, 20, 94, 25) if public else (58, 14, 89, 23)
            # 8px 对齐匹配现有 4:4:4 JPEG 的 MCU；其他块不重压缩。
            left, top, right, bottom = (344, 8, 456, 56) if public else (48, 8, 152, 48)
            patch = original.crop((left, top, right, bottom))
            # 原因：两张模型广场有渐变底；按每一行左右无字区域插值，不能覆盖纯白。
            clear_left, clear_top = left, top
            clear_right, clear_bottom = right, bottom
            for py in range(clear_top, clear_bottom):
                start = original.getpixel((clear_left - 1, py))
                end = original.getpixel((clear_right, py))
                for px in range(clear_left, clear_right):
                    fraction = (px - clear_left + 1) / (clear_right - clear_left + 1)
                    color = tuple(round(a + (b - a) * fraction) for a, b in zip(start, end))
                    patch.putpixel((px - left, py - top), color)
            rendered = assets.rasterize([{"svg": art, "width": width * 4, "height": height * 4}])[0]
            logo = assets.clean_image(rendered).resize((width, height), Image.Resampling.LANCZOS)
            patch.paste(logo, (x - left, y - top), logo)
            drop = work / "wordmark.jpg"
            patch.save(drop, format="JPEG", qtables=qtables, subsampling=0)
            result = subprocess.run(
                [jpegtran, "-copy", "all", "-drop", f"+{left}+{top}", str(drop), str(source)],
                check=True, capture_output=True, timeout=15,
            )
            with Image.open(BytesIO(result.stdout)) as image:
                updated = image.convert("RGB")
            if updated.size != original.size:
                raise RuntimeError(f"Screenshot canvas changed: {name}")
            difference = ImageChops.difference(original, updated)
            difference.paste((0, 0, 0), (left, top, right, bottom))
            if difference.getbbox() is not None:
                raise RuntimeError(f"Pixels outside brand blocks changed: {name}")
            if source.read_bytes() != result.stdout:
                mismatches.append(name)
            if not check:
                target = output_root / "assets/screenshots" / name
                if not target.is_file() or target.read_bytes() != result.stdout:
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(result.stdout)
    if check and mismatches:
        sys.stderr.write("README screenshot branding is stale:\n" + "\n".join(mismatches) + "\n")
        return 1
    print(f"{'Verified' if check else 'Refreshed'} {len(SCREENSHOTS)} README brand regions; "
          "pixels outside header blocks preserved")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-root", type=Path, default=ROOT.parent / "aiscoo")
    parser.add_argument("--output-root", type=Path, default=ROOT)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    return refresh(args.source_root, args.output_root, check=args.check)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError) as error:
        sys.stderr.write(f"README brand refresh failed: {error}\n")
        sys.exit(1)
