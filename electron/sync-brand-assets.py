#!/usr/bin/env python3
"""Copy existing admin branding from the Aiscoo design and published assets.

Run after Aiscoo's source/export_rasters.py and scripts/xcode/generate-icons.py
(legacy checkouts use scripts/aiscoo/generate-icons.py).
No UI or packaging paths are changed. --check compares without writing.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
SOURCE_LAYOUTS = (
    ("packages/shared/src/xwork-brand/brand.json", "packages/ui/src/xwork/brand/assets",
     "scripts/xcode/generate-icons.py"),
    ("packages/shared/src/aiscoo-brand/brand.json", "packages/ui/src/aiscoo/brand/assets",
     "scripts/aiscoo/generate-icons.py"),
)
MAPPINGS = {"electron/tray-icon-windows.png": "packages/desktop/build/icon.png"}
for name in ("favicon.ico", "apple-touch-icon.png", "icon-192.png", "icon-512.png"):
    MAPPINGS[f"web/public/{name}"] = f"packages/web/public/{name}"
for name in ("icon.png", "icon.ico", "icon.icns"):
    MAPPINGS[f"electron/{name}"] = f"packages/desktop/build/{name}"


def resolve_brand_source(source_root):
    source_root = source_root.resolve()
    # 原因：release 已移动品牌模块；两脚本按同一配置选择完整布局，不能混用新旧路径。
    for config_path, assets_path, generator_path in SOURCE_LAYOUTS:
        config = source_root / config_path
        if not config.is_file():
            continue
        brand = json.loads(config.read_text(encoding="utf-8"))
        directory = brand.get("sourceDirectory")
        if not isinstance(directory, str) or not directory.strip():
            raise ValueError(f"Missing sourceDirectory in Aiscoo brand configuration: {config}")
        design = source_root / directory
        ui_assets = source_root / assets_path
        generator = source_root / generator_path
        required = [generator, design / "app-icon/png/icon-light-1024.png",
                    design / "mark/png/ai-light-512.png"]
        required.extend(ui_assets / f"{kind}-{theme}.svg"
                        for theme in ("light", "dark")
                        for kind in ("app", "mark", "wordmark", "wordmark-animated"))
        missing = [str(path) for path in required if not path.is_file()]
        if missing:
            raise FileNotFoundError("Missing Aiscoo brand source files:\n" + "\n".join(missing))
        return design, ui_assets, generator
    checked = "\n".join(str(source_root / layout[0]) for layout in SOURCE_LAYOUTS)
    raise FileNotFoundError("Missing Aiscoo brand configuration; checked:\n" + checked)


def outputs(source_root):
    design, ui_assets, _ = resolve_brand_source(source_root)
    sources = {Path(target): source_root / source for target, source in MAPPINGS.items()}
    sources[Path("web/public/logo.png")] = design / "app-icon/png/icon-light-1024.png"
    sources[Path("electron/tray-mark.png")] = design / "mark/png/ai-light-512.png"
    for theme in ("light", "dark"):
        for kind in ("app", "mark", "wordmark", "wordmark-animated"):
            name = f"{kind}-{theme}.svg"
            sources[Path("web/public/brand") / name] = ui_assets / name
    result = {target: source.read_bytes() for target, source in sources.items()}
    # 原因：托盘模板还有独立派生步骤；复用既有脚本，避免同步时漏掉眼型或重复算法。
    # 临时目录只包含本仓库脚本和明确的品牌输入，--check 不写实际发布文件。
    with tempfile.TemporaryDirectory(prefix="aiscoo-admin-tray-") as temporary:
        directory = Path(temporary)
        (directory / "create-tray-icon.js").write_bytes((ROOT / "electron/create-tray-icon.js").read_bytes())
        (directory / "tray-mark.png").write_bytes(result[Path("electron/tray-mark.png")])
        subprocess.run([os.environ.get("NODE_BINARY", "node"), str(directory / "create-tray-icon.js")],
                       cwd=ROOT, env={**os.environ, "PYTHON": sys.executable},
                       check=True, capture_output=True, text=True, timeout=30)
        for name in ("tray-iconTemplate.png", "tray-iconTemplate@2x.png"):
            result[Path("electron") / name] = (directory / name).read_bytes()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-root", type=Path, default=ROOT.parent / "aiscoo")
    parser.add_argument("--output-root", type=Path, default=ROOT)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    generated = outputs(args.source_root)
    mismatches = []
    for relative, data in sorted(generated.items()):
        target = args.output_root / relative
        if args.check:
            if not target.is_file() or target.read_bytes() != data:
                mismatches.append(str(relative))
        else:
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
    if mismatches:
        sys.stderr.write("Admin brand assets differ:\n" + "\n".join(mismatches) + "\n")
        return 1
    print(f"{'Verified' if args.check else 'Synced'} {len(generated)} existing admin brand assets")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError) as error:
        sys.stderr.write(f"Admin brand sync failed: {error}\n")
        sys.exit(1)
