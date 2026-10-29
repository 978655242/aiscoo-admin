// Run: node create-tray-icon.js. Uses the shipped transparent source mark.
const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');

const result = spawnSync(process.env.PYTHON || 'python3', ['-c', `
from pathlib import Path
from PIL import Image
root = Path(__import__('sys').argv[1])
source = Image.open(root / 'tray-mark.png').convert('RGBA')
source.putdata([(0, 0, 0, round(a * (1 - min(r, g, b) / 255))) for r, g, b, a in source.get_flattened_data()])
for size, name in [(22, 'tray-iconTemplate.png'), (44, 'tray-iconTemplate@2x.png')]:
    image = source.copy()
    image.thumbnail((size, size), Image.Resampling.LANCZOS)
    image.save(root / name)
`, __dirname], { stdio: 'inherit' });

if (result.error) throw result.error;
if (result.status !== 0) {
  throw new Error('Tray icon generation requires Python 3 and Pillow');
}
for (const name of ['tray-iconTemplate.png', 'tray-iconTemplate@2x.png']) {
  if (!fs.existsSync(path.join(__dirname, name))) throw new Error(`Missing ${name}`);
}
console.log('Created aiscoo 22px and 44px macOS tray templates.');
