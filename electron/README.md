# aiscoo Electron Desktop App

This directory contains the Electron wrapper for aiscoo, providing a native desktop application with system tray support for Windows, macOS, and Linux.

## Brand asset source and refresh

Brand geometry and animation are owned by the sibling `aiscoo` repository's
`docs/brand/aiscoo/source/ai.py` and `anim.py`. Admin keeps its existing asset paths,
component layouts, theme selection and reduced-motion fallback. Static variants use
the tucked capsule-eye pose; animated wordmarks use the same 10-second timeline
with filled smile eyes and a whole-head bounce, preserving the tuck-away motion.

| Admin assets | Source in `aiscoo` |
| --- | --- |
| `web/public/brand/{app,mark,wordmark,wordmark-animated}-{light,dark}.svg` | Matching files in `packages/ui/src/aiscoo/brand/assets/` (wordmark canvases are transparent) |
| `web/public/logo.png` | `docs/brand/aiscoo/app-icon/png/icon-light-1024.png` |
| `web/public/{favicon.ico,apple-touch-icon.png,icon-192.png,icon-512.png}` | Matching files in `packages/web/public/` |
| `electron/{icon.png,icon.ico,icon.icns,tray-icon-windows.png}` | `packages/desktop/build/icon.{png,ico,icns}`; Windows tray uses `icon.png`, retaining the dark tile and 1.2 mark scale |
| `electron/tray-mark.png`, `tray-iconTemplate*.png` | `docs/brand/aiscoo/mark/png/ai-light-512.png`, then the existing `electron/create-tray-icon.js` template export |

From this repository root, with Python 3, Pillow 12.1.0, macOS `iconutil` and
Aiscoo's installed `@napi-rs/canvas` 0.1.100 (set `NODE_BINARY` if needed):

```bash
python3 ../aiscoo/docs/brand/aiscoo/source/make.py
python3 -I -B ../aiscoo/docs/brand/aiscoo/source/export_rasters.py
python3 -I -B ../aiscoo/scripts/aiscoo/generate-icons.py
python3 -I -B electron/sync-brand-assets.py --source-root ../aiscoo
python3 -I -B electron/brand-assets.test.py
```

The raster exporter uses each existing design PNG as a template and replaces only
the old/new eye region through the SVG's original transforms and clips. It retains
all original alpha values and pixels outside that region, including tile gradients,
shadows, letter edges and image dimensions. Each original iconset/ICO size is patched
separately; ICO retains its six original sizes, and ICNS is repackaged with `iconutil`.
This avoids losing SVG shadows with the canvas renderer or leaving old wide eyes
under the new capsules. The exporter renders SVG at the output resolution before
compositing, keeping mark and wordmark eyes sharp.

`export_rasters.py --template-root <preserved-design-directory> --output-root <dir>`
can reproduce an export from a preserved design set. Both the raster exporter and
admin sync accept `--check` to compare without writing, and `--output-root` for an
isolated destination. `AISCOO_SOURCE_ROOT` selects another source checkout for the
regression test. Synchronization touches only the 20 existing brand assets above;
third-party payment/provider logos and generated build directories are excluded.

The eight illustrative README images in `assets/screenshots/` also use the current
capsule-eye wordmark. Their existing simulated data and layout are retained; only
the header brand area is refreshed. Run `python3 -I -B electron/refresh-readme-branding.py`
after asset synchronization, or append `--check` to detect a stale wordmark without
writing. This uses the sibling Aiscoo renderer, Pillow 12.1.0 and `jpegtran` to replace
only aligned JPEG blocks; pixels outside those header blocks remain identical.
The script supports `--output-root` for isolated previews. These remain illustrative
images, not a claim of an authenticated session or a fresh full-page capture.

## Prerequisites

### 1. Go Binary (Required)
The Electron app requires the compiled Go binary to function. You have two options:

**Option A: Use existing binary (without Go installed)**
```bash
# If you have a pre-built binary (e.g., new-api-macos)
cp ../aiscoo-macos ../aiscoo
```

**Option B: Build from source (requires Go)**
TODO

### 2. Electron Dependencies
```bash
cd electron
npm install
```

## Development

Start the backend, the frontend, and Electron in separate terminals:
```bash
# Repository root
go run main.go

# Repository root
make dev-web

# electron/
npm run dev-app
```

This will:
- Use the Go backend on port 3000
- Use the Rsbuild frontend development server on port 5173
- Open an Electron window with DevTools enabled
- Create a system tray icon (menu bar on macOS)
- Store database in `../data/new-api.db`

## Building for Production

### Quick Build
```bash
# From electron/, build the frontend, Go binary, and desktop package
./build.sh

# Or package an existing binary for the current platform
npm run build

# Platform-specific builds
npm run build:mac    # Creates .dmg and .zip
npm run build:win    # Creates .exe installer
npm run build:linux  # Creates .AppImage and .deb
```

### Build Output
- Built applications are in `electron/dist/`
- macOS: `.dmg` (installer) and `.zip` (portable)
- Windows: `.exe` (installer) and portable exe
- Linux: `.AppImage` and `.deb`

## Configuration

### Port
Default port is 3000. To change, edit `main.js`:
```javascript
const PORT = 3000; // Change to desired port
```

### Database Location
- **Development**: `../data/new-api.db` (project directory)
- **Production**:
  - macOS: `~/Library/Application Support/aiscoo/data/`
  - Windows: `%APPDATA%/aiscoo/data/`
  - Linux: `~/.config/aiscoo/data/`

Existing installations retain the profile containing `data/new-api.db` under `New-API-App`, `new-api-electron`, or `New API`. The database filename stays unchanged for compatibility.
