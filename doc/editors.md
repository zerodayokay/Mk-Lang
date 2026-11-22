# Editors

`mk lsp` is the Mk language server. It is part of the `mk` binary, so there is nothing else to install: any editor that speaks the Language Server Protocol over standard input and output can use it. It gives diagnostics, hover, completion, go to definition and implementation, references, rename, document and workspace symbols, semantic tokens, inlay hints, formatting and code actions. Its answers match what `mk check` reports.

Every editor needs the same three facts:

| Fact | Value |
|---|---|
| Server command | `mk lsp --stdio` |
| File extension | `.mk` |
| Package root marker | `pkg.mk` |

This page shows the setup for VS Code, Neovim, Helix, Emacs, Sublime Text and Zed.

## The server

Build `mk` as [Building from source](../README.md#building-from-source) says, and put it on `PATH`. Every editor below starts the same command:

```sh
mk lsp --stdio
```

`mk lsp --check-capabilities` prints what the server supports and exits. Run it from a terminal to check that the `mk` on `PATH` is the one you expect. When the server misbehaves, add `--log <file>`: it appends one line per message to the file. [`mk lsp`](tools.md#mk-lsp) lists the other flags, and [the server's requests](tools.md#12-editor-mk-lsp) what it answers.

The server is the compiler it was built with, so rebuild `mk` after you pull new commits.

Mk files end in `.mk`, which many editors give to Makefiles. Each section below includes the line that claims `*.mk` for Mk.

## VS Code

The extension in `editors/vscode` adds highlighting, snippets, bracket and comment settings and the client for `mk lsp`. It is not on the Marketplace. Each GitHub release has it attached as a `.vsix`, which `code --install-extension <file>.vsix` installs. To build it from the checkout instead, you need Node 22 or later and the `code` command on `PATH`:

```sh
cd editors/vscode
sh install.sh                  # Windows: pwsh install.ps1
```

The script runs `npm ci`, packages the extension into `mk-lang.vsix` with `vsce` and installs it with `code --install-extension`. By hand, the same steps are:

```sh
cd editors/vscode
npm ci
npx vsce package --out mk-lang.vsix
code --install-extension mk-lang.vsix
```

On Windows, use `install.ps1`: `vsce` runs `npm` through `cmd.exe`, which fails when `PATH` is longer than 8191 characters, and the script shortens it.

Reload VS Code and open a folder with `.mk` files. To remove the extension: `code --uninstall-extension zerodayokay.mk-lang`.

**Finding the server.** The extension runs `mk lsp --stdio` with the first `mk` it finds:

1. the `mk.server.path` setting;
2. `mk` (`mk.exe` on Windows) at the root of a workspace folder;
3. `mk` on `PATH`.

On Linux and macOS the search skips files without execute permission. If no executable is found, a message says so, explains how to build `mk`, and offers to open the setting. Highlighting and snippets work without the server. After you fix the path, the server restarts by itself when you change a `mk.server.*` setting, or run **Mk: Restart the language server**.

The server starts only in a trusted folder, because it runs the package's generators and reads its manifests. In Restricted Mode the Mk output channel says it is waiting; trust the folder with **Workspaces: Manage Workspace Trust**.

VS Code's Makefile support also claims `*.mk`. The extension sets `"files.associations": {"*.mk": "mk"}` as a default; if a workspace setting overrides it and the status bar shows "Makefile", set the association back.

**Settings.**

| Setting | Default | Meaning |
|---|---|---|
| `mk.server.path` | `""` | The `mk` executable. `${workspaceFolder}` and a leading `~` are expanded |
| `mk.server.std` | `""` | Passed as `--std`. Empty: the `std` directory when the workspace is the Mk repository, else the standard library built into `mk` |
| `mk.server.cache` | `""` | Passed as `--cache`. Empty: `MK_CACHE`, else the per-user cache |
| `mk.server.log` | `""` | Passed as `--log`: the server appends one line per message |
| `mk.server.extraArgs` | `[]` | More arguments after `mk lsp --stdio` |
| `mk.trace.server` | `off` | `messages` or `verbose` traces the protocol in the Mk output channel |

The commands **Mk: Restart the language server** and **Mk: Show the language server's output** are in the command palette.

With the standard library built into `mk`, Go to Definition into std opens a read-only copy under the cache. In the Mk repository the extension passes `--std <repo>/std`, so definitions open the real files.

**What the extension adds.** A TextMate grammar for every keyword, contextual word, literal (`"""` text, `r"raw"`, `b"bytes"`, characters, numbers), operator and comment form (`//` and `///`), with format holes in strings and `$name` splices in `quote` blocks; line comments with Ctrl+/, bracket matching and auto-closing, indentation, and `///` continued on Enter; and the snippets `main`, `func`, `method`, `struct`, `enum`, `interface`, `impl`, `test`, `match`, `forin`, `forr`, `scope`, `unsafe` and `import`. Everything else comes from the server.

**Known limits.** The methods of `?T` and `E!T` (`Must`, `IsSome`, `IsNone`) have no declaration to go to; hover shows their description from the reference. Names inside a `quote { }` block belong to the code a generator emits, and the names of a C import come from C headers, so neither has a declaration to go to.

## Neovim

Neovim 0.11 and later configure language servers without plugins. In `init.lua`:

```lua
vim.filetype.add({ extension = { mk = 'mk' } })

vim.lsp.config('mk', {
  cmd = { 'mk', 'lsp', '--stdio' },
  filetypes = { 'mk' },
  root_markers = { 'pkg.mk', '.git' },
})
vim.lsp.enable('mk')

vim.api.nvim_create_autocmd('FileType', {
  pattern = 'mk',
  callback = function()
    vim.bo.commentstring = '// %s'
  end,
})
```

`vim.filetype.add` takes precedence over Neovim's own rule that reads `*.mk` as `make`. Neovim has no Mk syntax file; the server's semantic tokens color the code once it is attached. Check the client with `:checkhealth vim.lsp`.

With nvim-lspconfig on Neovim 0.10, which has no `vim.lsp.config`, register the server as a custom configuration:

```lua
vim.filetype.add({ extension = { mk = 'mk' } })

local configs = require('lspconfig.configs')
if not configs.mk then
  configs.mk = {
    default_config = {
      cmd = { 'mk', 'lsp', '--stdio' },
      filetypes = { 'mk' },
      root_dir = require('lspconfig.util').root_pattern('pkg.mk', '.git'),
    },
  }
end
require('lspconfig').mk.setup({})
```

## Helix

In `~/.config/helix/languages.toml` (`%AppData%\helix\languages.toml` on Windows):

```toml
[language-server.mk]
command = "mk"
args = ["lsp", "--stdio"]

[[language]]
name = "mk"
scope = "source.mk"
file-types = ["mk"]
roots = ["pkg.mk"]
comment-token = "//"
indent = { tab-width = 4, unit = "    " }
language-servers = ["mk"]
```

Helix's built-in `make` language also lists `mk` among its file types. If `.mk` files still open as `make` (`:lang` shows it), set the language with `:set-language mk`, or redefine `make`'s file types in the same file without `mk`. There is no tree-sitter grammar for Mk, so Helix shows no syntax colors; diagnostics, hover, completion, navigation and formatting come from the server. `hx --health mk` checks the setup.

## Emacs

Eglot is built into Emacs 29 and later. Emacs has no Mk mode, so define a small one with comments and keywords, and point Eglot at the server. In `init.el`:

```elisp
(defvar mk-mode-syntax-table
  (let ((st (make-syntax-table)))
    (modify-syntax-entry ?/ ". 12" st)
    (modify-syntax-entry ?\n ">" st)
    (modify-syntax-entry ?_ "_" st)
    (modify-syntax-entry ?' "\"" st)
    st))

(defconst mk-keywords
  '("import" "export" "struct" "enum" "error" "interface" "impl" "type"
    "const" "var" "linear" "view" "gen" "quote" "test" "extern" "if" "else"
    "for" "in" "match" "return" "break" "continue" "defer" "onerr" "fail"
    "try" "scope" "go" "unsafe" "asm" "become" "mut" "fn" "is" "requires"
    "ensures" "invariant"))

(define-derived-mode mk-mode prog-mode "Mk"
  "Major mode for Mk source files."
  :syntax-table mk-mode-syntax-table
  (setq-local comment-start "// ")
  (setq-local comment-end "")
  (setq-local indent-tabs-mode nil)
  (setq-local font-lock-defaults
              `(((,(regexp-opt mk-keywords 'symbols) . font-lock-keyword-face)
                 ("\\_<\\(true\\|false\\|none\\)\\_>" . font-lock-constant-face)))))

(add-to-list 'auto-mode-alist '("\\.mk\\'" . mk-mode))

(with-eval-after-load 'eglot
  (add-to-list 'eglot-server-programs '(mk-mode "mk" "lsp" "--stdio")))
(add-hook 'mk-mode-hook #'eglot-ensure)
```

`add-to-list` puts the `.mk` entry ahead of the built-in one for `makefile-mode`. `M-x eglot-events-buffer` shows the messages if the server does not start.

## Sublime Text

Install the LSP package with Package Control. Sublime needs a syntax whose scope is `source.mk` before it will attach the server. Save this minimal one as `Packages/User/Mk.sublime-syntax` (Preferences > Browse Packages):

```yaml
%YAML 1.2
---
name: Mk
scope: source.mk
file_extensions: [mk]
contexts:
  main:
    - match: '//.*$'
      scope: comment.line.double-slash.mk
    - match: '"""'
      push: text
    - match: '\b[rb]"|"'
      push: string
    - match: '\b(import|export|struct|enum|error|interface|impl|type|const|var|linear|view|gen|quote|test|extern|if|else|for|in|match|return|break|continue|defer|onerr|fail|try|scope|go|unsafe|asm|become|mut|fn|is|requires|ensures|invariant)\b'
      scope: keyword.control.mk
    - match: '\b(true|false|none)\b'
      scope: constant.language.mk
    - match: '\b[0-9][0-9_]*(\.[0-9_]+)?\b|\b0[xob][0-9A-Fa-f_]+\b'
      scope: constant.numeric.mk
  string:
    - meta_scope: string.quoted.double.mk
    - match: '\\.'
      scope: constant.character.escape.mk
    - match: '"'
      pop: true
  text:
    - meta_scope: string.quoted.triple.mk
    - match: '"""'
      pop: true
```

Then add the client in Preferences > Package Settings > LSP > Settings (`LSP.sublime-settings`):

```json
{
  "clients": {
    "mk": {
      "enabled": true,
      "command": ["mk", "lsp", "--stdio"],
      "selector": "source.mk"
    }
  }
}
```

Sublime's Makefile syntax also claims `.mk`. Open a `.mk` file, then choose View > Syntax > Open all with current extension as... > User > Mk once.

## Zed

Zed adds a language and its server only through an extension, and an extension's language needs a tree-sitter grammar. Mk has no tree-sitter grammar, so Zed cannot run `mk lsp`. Settings can open `.mk` files as plain text, so that they do not open as Makefiles:

```json
{
  "file_types": {
    "Plain Text": ["*.mk"]
  }
}
```

## Other editors

Any LSP client works with the three facts at the top of this page: the command `mk lsp --stdio`, the file extension `.mk`, and `pkg.mk` as the root marker of a package. For comment settings: line comments are `//`, and `///` documents the next item. Mk has no block comments. Indent with four spaces, as `mk fmt` does.
