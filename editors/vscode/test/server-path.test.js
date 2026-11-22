// Why: a file named mk can hide a usable language server later in the search.
// What: tests for automatic discovery and the mk.server.path override.
// How: search temporary workspace and PATH directories with real file permissions.

'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { findServer } = require('../server-path');

const windows = process.platform === 'win32';
const exe = windows ? 'mk.exe' : 'mk';

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mk-server-path-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const workspace = path.join(root, 'workspace');
  const bin = path.join(root, 'bin');
  fs.mkdirSync(workspace);
  fs.mkdirSync(bin);
  const installed = path.join(bin, exe);
  fs.writeFileSync(installed, '', { mode: 0o755 });
  return { workspace, local: path.join(workspace, exe), bin, installed };
}

test('automatic discovery prefers the workspace and respects an explicit setting', (t) => {
  const { workspace, local, bin, installed } = fixture(t);
  fs.writeFileSync(local, '', { mode: 0o755 });
  assert.equal(findServer('', [workspace], bin), local);
  assert.equal(findServer(installed, [workspace], bin), installed);
  assert.equal(findServer('mk', [workspace], bin), installed);
  assert.equal(findServer(path.join(bin, 'missing'), [workspace], bin), '');
});

test('discovery skips non-executable files in workspace folders and on PATH', { skip: windows }, (t) => {
  const { workspace, local, bin, installed } = fixture(t);
  fs.writeFileSync(local, '', { mode: 0o644 });
  assert.equal(findServer('', [workspace], bin), installed);
  assert.equal(findServer('', [workspace, bin], ''), installed);
  assert.equal(findServer('', [], [workspace, bin].join(path.delimiter)), installed);
  assert.equal(findServer('', [workspace], workspace), '');
  assert.equal(findServer(local, [], bin), '');
});
