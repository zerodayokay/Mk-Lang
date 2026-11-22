// Why: the first file named mk can lack permission to run.
// What: findServer locates the executable for mk lsp.
// How: use the configured command, or search the workspace folders then PATH.

'use strict';

const fs = require('fs');
const path = require('path');

function isExecutable(file) {
  try {
    if (!fs.statSync(file).isFile()) return false;
    if (process.platform !== 'win32') fs.accessSync(file, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function findServer(configured, folders, searchPath = process.env.PATH || '') {
  const windows = process.platform === 'win32';
  if (!configured) {
    for (const dir of folders) {
      const file = path.join(dir, windows ? 'mk.exe' : 'mk');
      if (isExecutable(file)) return file;
    }
  } else if (path.isAbsolute(configured) || configured.includes('/') || configured.includes('\\')) {
    return isExecutable(configured) ? path.resolve(configured) : '';
  }

  const command = configured || 'mk';
  const exts = windows ? ['', ...(process.env.PATHEXT || '.EXE;.CMD;.BAT').split(';')] : [''];
  for (const dir of searchPath.split(path.delimiter)) {
    if (!dir) continue;
    for (const ext of exts) {
      const file = path.resolve(dir, command + ext);
      if (isExecutable(file)) return file;
    }
  }
  return '';
}

module.exports = { findServer };
