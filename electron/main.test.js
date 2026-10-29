const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

for (const previousName of ['New-API-App', 'new-api-electron', 'New API', null]) {
  test(`keeps ${previousName || 'new installation'} data in its own profile`, () => {
    const paths = new Map();
    const appData = path.join(path.sep, 'test', 'profiles');
    const expected = path.join(appData, previousName || 'aiscoo');
    const app = {
      getPath: () => appData,
      setPath: (name, value) => paths.set(name, value),
      whenReady: () => ({ then() {} }),
      on() {},
    };
    vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'main.js'), 'utf8'), {
      require: (name) => {
        if (name === 'electron') return { app };
        if (name === 'fs') return {
          existsSync: (value) => previousName !== null && value === path.join(expected, 'data', 'new-api.db'),
          mkdirSync() {},
        };
        return require(name);
      },
      __dirname,
      process: { platform: process.platform, env: {} },
      console,
    });
    assert.equal(paths.get('userData'), expected);
  });
}
