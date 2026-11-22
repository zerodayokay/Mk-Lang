// Why: VS Code needs a client to talk to mk lsp, the language server built
// into the mk command; without one, .mk files get colors and nothing else.
//
// What: activate and deactivate for the extension, the commands
// mk.restartServer and mk.showServerOutput, and the server arguments.
//
// How: finds the mk executable (the mk.server.path setting, then mk at a
// workspace folder's root, then mk on PATH), builds the arguments of
// `mk lsp --stdio` from the mk.server.* settings and hands them to
// vscode-languageclient. Hover, definition, diagnostics and the rest are the
// server's. When no mk is found, the user gets a message that says how to
// build it and where to point the extension, instead of a spawn error.

'use strict';

const fs = require('fs');
const path = require('path');
const vscode = require('vscode');
const { findServer } = require('./server-path');
const { LanguageClient, RevealOutputChannelOn } = require('vscode-languageclient/node');

/** @type {LanguageClient | undefined} */
let client;
/** @type {vscode.LogOutputChannel | undefined} */
let output;

function folders() {
  return (vscode.workspace.workspaceFolders || []).map((f) => f.uri.fsPath);
}

// expand replaces ${workspaceFolder} and a leading ~ in a setting.
function expand(value) {
  if (!value) return value;
  const first = folders()[0] || '';
  let v = value.replace(/\$\{workspaceFolder\}/g, first);
  if (v.startsWith('~')) v = path.join(require('os').homedir(), v.slice(1));
  return v;
}

function isFile(p) {
  try {
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
}

function isDir(p) {
  try {
    return fs.statSync(p).isDirectory();
  } catch {
    return false;
  }
}

const installURL = 'https://github.com/zerodayokay/Mk-Lang/blob/main/doc/editors.md';

// notFound tells the user that no mk executable was found, how to build one
// and how to point the extension at it.
async function notFound(configured) {
  const exe = process.platform === 'win32' ? 'mk.exe' : 'mk';
  const where = configured
    ? `mk.server.path is "${configured}", which could not be resolved to an executable.`
    : 'No mk executable found in the workspace or on PATH.';
  output.appendLine(`mk lsp not started: ${where}`);
  const pick = await vscode.window.showErrorMessage(
    `Mk: the language server was not found. ${where} ` +
      `Build it from a source checkout with "go build -o ${exe} ./cmd/mk", ` +
      'then put it on PATH or set mk.server.path to its full path, and run "Mk: Restart the language server". ' +
      'Highlighting and snippets work without it.',
    'Open Settings',
    'Install Steps',
  );
  if (pick === 'Open Settings') {
    vscode.commands.executeCommand('workbench.action.openSettings', 'mk.server.path');
  } else if (pick === 'Install Steps') {
    vscode.env.openExternal(vscode.Uri.parse(installURL));
  }
}

// stdDir is mk.server.std, else the std directory of a workspace folder that
// is the Mk repository itself, else "" (the std embedded in mk).
function stdDir(cfg) {
  const set = expand(cfg.get('server.std', ''));
  if (set) return set;
  for (const dir of folders()) {
    if (isFile(path.join(dir, 'std', 'pkg.mk')) && isDir(path.join(dir, 'cmd', 'mk'))) {
      return path.join(dir, 'std');
    }
  }
  return '';
}

function serverArgs(cfg) {
  const args = ['lsp', '--stdio'];
  const std = stdDir(cfg);
  if (std) args.push('--std', std);
  const cache = expand(cfg.get('server.cache', ''));
  if (cache) args.push('--cache', cache);
  const log = expand(cfg.get('server.log', ''));
  if (log) args.push('--log', log);
  for (const a of cfg.get('server.extraArgs', [])) args.push(expand(a));
  return args;
}

async function startClient() {
  // Restricted Mode (an untrusted folder): highlighting, snippets and the
  // language configuration still work, since package.json declares limited
  // support, but no executable starts until the folder is trusted, as VS
  // Code's workspace trust asks (mk lsp runs the folder's generators and
  // reads its manifests). onDidGrantWorkspaceTrust starts it then.
  if (!vscode.workspace.isTrusted) {
    output.appendLine('Restricted Mode: mk lsp starts once this folder is trusted');
    return;
  }
  if (client) return;
  const cfg = vscode.workspace.getConfiguration('mk');
  const configured = expand(cfg.get('server.path', ''));
  const resolved = findServer(configured, folders());
  if (!resolved) {
    await notFound(configured);
    return;
  }
  const args = serverArgs(cfg);
  output.appendLine(`starting: ${resolved} ${args.join(' ')}`);
  const executable = { command: resolved, args, options: { cwd: folders()[0] } };
  client = new LanguageClient(
    'mk',
    'Mk',
    { run: executable, debug: executable },
    {
      documentSelector: [{ scheme: 'file', language: 'mk' }],
      outputChannel: output,
      revealOutputChannelOn: RevealOutputChannelOn.Never,
    },
  );
  try {
    await client.start();
    output.appendLine('mk lsp is running');
  } catch (e) {
    output.appendLine(`mk lsp did not start: ${e && e.stack ? e.stack : e}`);
    client = undefined;
    const pick = await vscode.window.showErrorMessage(
      `Mk: could not start "${resolved} lsp": ${e && e.message ? e.message : e}. ` +
        'Check that it is a current mk build ("mk version"), and see the Mk output channel.',
      'Open Settings',
    );
    if (pick === 'Open Settings') {
      vscode.commands.executeCommand('workbench.action.openSettings', 'mk.server');
    }
  }
}

async function stopClient() {
  if (!client) return;
  const c = client;
  client = undefined;
  try {
    await c.stop();
  } catch {
    // The server may already have exited.
  }
}

async function activate(context) {
  output = vscode.window.createOutputChannel('Mk', { log: true });
  context.subscriptions.push(output);
  context.subscriptions.push(
    vscode.commands.registerCommand('mk.restartServer', async () => {
      await stopClient();
      await startClient();
    }),
    vscode.commands.registerCommand('mk.showServerOutput', () => output.show(true)),
    vscode.workspace.onDidChangeConfiguration(async (e) => {
      if (e.affectsConfiguration('mk.server')) {
        await stopClient();
        await startClient();
      }
    }),
  );
  context.subscriptions.push(vscode.workspace.onDidGrantWorkspaceTrust(() => startClient()));
  await startClient();
}

function deactivate() {
  return stopClient();
}

module.exports = { activate, deactivate, serverArgs, stdDir };
