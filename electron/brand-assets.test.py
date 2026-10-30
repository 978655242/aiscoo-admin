#!/usr/bin/env python3
"""Cross-repository brand contract: existing admin assets use Aiscoo's design.

Run: python3 -I -B electron/brand-assets.test.py
AISCOO_SOURCE_ROOT overrides the sibling Aiscoo checkout used by the sync script.
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent.parent
SOURCE = Path(os.environ.get("AISCOO_SOURCE_ROOT", ROOT.parent / "aiscoo"))
SCRIPT = ROOT / "electron/sync-brand-assets.py"
NS = "{http://www.w3.org/2000/svg}"


class BrandAssetsTests(unittest.TestCase):
    def run_sync(self, destination, *extra, source_root=SOURCE, expected=0):
        result = subprocess.run(
            [sys.executable, "-I", "-B", str(SCRIPT), "--source-root", str(source_root),
             "--output-root", str(destination), *extra],
            cwd=ROOT, capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        return result

    def source_layout(self, directory, *, legacy=False):
        brand = "aiscoo-brand" if legacy else "xwork-brand"
        ui = "aiscoo" if legacy else "xwork"
        scripts = "aiscoo" if legacy else "xcode"
        config = directory / f"packages/shared/src/{brand}/brand.json"
        config.parent.mkdir(parents=True)
        config.write_text(json.dumps({"sourceDirectory": "design"}))
        for target, source in (
            ("design", SOURCE / "docs/brand/aiscoo"),
            (f"packages/ui/src/{ui}/brand/assets", ROOT / "web/public/brand"),
            ("packages/desktop/build", SOURCE / "packages/desktop/build"),
            ("packages/web/public", SOURCE / "packages/web/public"),
        ):
            path = directory / target
            path.parent.mkdir(parents=True, exist_ok=True)
            path.symlink_to(source, target_is_directory=True)
        renderer = directory / f"scripts/{scripts}/generate-icons.py"
        renderer.parent.mkdir(parents=True)
        actual = SOURCE / "scripts/xcode/generate-icons.py"
        if not actual.is_file():
            actual = SOURCE / "scripts/aiscoo/generate-icons.py"
        renderer.symlink_to(actual)
        return renderer

    def test_published_brand_assets_match_source_and_existing_tray_generator(self):
        self.run_sync(ROOT, "--check")
        with tempfile.TemporaryDirectory(prefix="aiscoo-source-layouts-") as temporary:
            for legacy in (False, True):
                with self.subTest(legacy=legacy):
                    checkout = Path(temporary) / ("legacy" if legacy else "release")
                    renderer = self.source_layout(checkout, legacy=legacy)
                    self.run_sync(ROOT, "--check", source_root=checkout)
                    if not legacy:
                        old_config = checkout / "packages/shared/src/aiscoo-brand/brand.json"
                        old_config.parent.mkdir(parents=True)
                        old_config.write_text(json.dumps({"sourceDirectory": "missing-design"}))
                        self.run_sync(ROOT, "--check", source_root=checkout)
                    renderer.unlink()
                    result = self.run_sync(ROOT, "--check", source_root=checkout, expected=1)
                    self.assertIn(str(renderer), result.stderr)
                    self.assertNotIn("Traceback", result.stderr)

    def test_sync_is_repeatable_and_check_rejects_stale_assets_without_writing(self):
        with tempfile.TemporaryDirectory(prefix="aiscoo-brand-sync-test-") as temporary:
            directory = Path(temporary)
            self.run_sync(directory)
            first = {str(p.relative_to(directory)): hashlib.sha256(p.read_bytes()).hexdigest()
                     for p in directory.rglob("*") if p.is_file()}
            self.assertEqual(len(first), 20)
            target = directory / "web/public/brand/wordmark-light.svg"
            target.write_bytes(b"stale brand design")
            result = subprocess.run(
                [sys.executable, "-I", "-B", str(SCRIPT), "--source-root", str(SOURCE),
                 "--output-root", str(directory), "--check"],
                cwd=ROOT, capture_output=True, text=True, timeout=30,
            )
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn("web/public/brand/wordmark-light.svg", result.stderr)
            self.assertEqual(target.read_bytes(), b"stale brand design")
            self.run_sync(directory)
            current = {str(p.relative_to(directory)): hashlib.sha256(p.read_bytes()).hexdigest()
                       for p in directory.rglob("*") if p.is_file()}
            self.assertEqual(current, first)
            self.run_sync(directory, "--check")

    def test_static_svg_variants_share_capsule_eyes_without_changing_canvas_sizes(self):
        for theme in ("light", "dark"):
            for kind, view_box in (("mark", "0 0 172.00 172.00"),
                                   ("app", "0 0 1024.00 1024.00"),
                                   ("wordmark", "0 0 581.15 200.00")):
                with self.subTest(theme=theme, kind=kind):
                    tree = ET.parse(ROOT / f"web/public/brand/{kind}-{theme}.svg").getroot()
                    self.assertEqual(tree.get("viewBox"), view_box)
                    eyes = [e for e in tree.iter() if e.get("class") == "eye"]
                    self.assertEqual(len(eyes), 2)
                    for eye in eyes:
                        self.assertEqual(eye.tag, NS + "rect")
                        self.assertEqual(float(eye.get("width")), 7.4)
                        self.assertEqual(float(eye.get("height")), 17.2)
                    smiles = [e for e in tree.iter() if e.get("class") == "smile"]
                    self.assertEqual(len(smiles), 1)
                    self.assertEqual(smiles[0].get("opacity"), "0")

    def test_readme_screenshots_use_current_wordmark_and_check_does_not_write(self):
        screenshots = ROOT / "assets/screenshots"
        before = {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                  for path in screenshots.glob("*.jpg")}
        self.assertEqual(len(before), 8)
        result = subprocess.run(
            [sys.executable, "-I", "-B", str(ROOT / "electron/refresh-readme-branding.py"),
             "--source-root", str(SOURCE), "--check"],
            cwd=ROOT, capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        after = {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                 for path in screenshots.glob("*.jpg")}
        self.assertEqual(after, before)
        with tempfile.TemporaryDirectory(prefix="aiscoo-readme-layouts-") as temporary:
            for legacy in (False, True):
                with self.subTest(legacy=legacy):
                    checkout = Path(temporary) / ("legacy" if legacy else "release")
                    renderer = self.source_layout(checkout, legacy=legacy)
                    result = subprocess.run(
                        [sys.executable, "-I", "-B", str(ROOT / "electron/refresh-readme-branding.py"),
                         "--source-root", str(checkout), "--check"],
                        cwd=ROOT, capture_output=True, text=True, timeout=30,
                    )
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    renderer.unlink()
                    result = subprocess.run(
                        [sys.executable, "-I", "-B", str(ROOT / "electron/refresh-readme-branding.py"),
                         "--source-root", str(checkout), "--check"],
                        cwd=ROOT, capture_output=True, text=True, timeout=30,
                    )
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn(str(renderer), result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
        with tempfile.TemporaryDirectory(prefix="aiscoo-readme-preview-") as temporary:
            exported = subprocess.run(
                [sys.executable, "-I", "-B", str(ROOT / "electron/refresh-readme-branding.py"),
                 "--source-root", str(SOURCE), "--output-root", temporary],
                cwd=ROOT, capture_output=True, text=True, timeout=30,
            )
            self.assertEqual(exported.returncode, 0, exported.stdout + exported.stderr)
            outputs = Path(temporary) / "assets/screenshots"
            self.assertEqual({path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                              for path in outputs.glob("*.jpg")}, before)

    def test_animated_wordmarks_keep_the_shared_timeline_and_reduced_motion(self):
        for theme in ("light", "dark"):
            with self.subTest(theme=theme):
                data = (ROOT / f"web/public/brand/wordmark-animated-{theme}.svg").read_text()
                tree = ET.fromstring(data)
                self.assertEqual(tree.get("viewBox"), "0 0 615.58 200.00")
                self.assertIn("prefers-reduced-motion:reduce", data)
                self.assertIn("10.0s", data)
                self.assertIn("animation:bounce", data)
                self.assertIn("steps(1,end)", data)
                self.assertIn(".eye-open", data)
                self.assertIn(".smile", data)
                self.assertFalse(any(e.tag in (NS + "script", NS + "image") for e in tree.iter()))


if __name__ == "__main__":
    unittest.main()
