---
description: Build mk from source, run the compiler's tests and the documentation corpus, find your way around the repository, and work on this website.
---

# Building and testing

This section is for people who work on Mk itself: the compiler, the standard library, the editor support or this website. To use Mk, start with the [guide](/guide/).

## Build mk

You need Go 1.27 or newer and LLVM 21 or newer (`clang` and `lld`). [Building from source](/guide/installation#building-from-source) covers installing LLVM on each platform.

```sh
git clone https://github.com/zerodayokay/Mk-Lang
cd Mk-Lang
go build -o mk ./cmd/mk        # on Windows: go build -o mk.exe ./cmd/mk
./mk version
```

`mk` is one binary, written in Go with the standard library only: no cgo and no third-party modules. The standard library and runtime are Mk source embedded in the binary.

## Run the tests

```sh
go vet ./...
go test ./...                  # every Go test
go run ./cmd/mk corpus         # compile every mk block in doc/ against its verdict
go run ./cmd/mk corpus --all   # the same, plus the units in tests/conformance
```

- `go test ./std` builds and runs many Mk programs and takes a long time. While you work on one package, test that package: `go test ./internal/borrow`.
- To judge one document's examples: `go run ./cmd/mk corpus --only "doc/memory.md:*"`.
- Run `go test -p 2` so a test run leaves the machine usable.
- `go run ./tools/gate -tier quick` runs every check in a fixed order and writes one report.

Some tests read large outside data from an *evidence directory* and skip without it; others need WSL or wall-clock timing. [Contributing](./contributing#building-and-testing) explains each.

## Every example is a test

Every `mk` code block in `doc/` begins with a verdict line, and `mk corpus` compiles each one against it:

| Verdict | Meaning |
|---|---|
| `// verdict: ok` | The example compiles |
| `// verdict: reject E0501` | Compiling fails with exactly the listed codes, each on a line marked `// <- E0501` |
| `// verdict: parse` | The example parses; it may use names that are not declared |

This website hides the verdict line and labels a rejected example with its error codes. Because the reference pages here are generated from `doc/`, they are exactly the examples the test suite checks.

## How the compiler is built

`mk` compiles modules in import order, in parallel where it can, through fixed stages:

| # | Stage | Package | Output |
|---|---|---|---|
| 1 | Load | `driver` | The module graph and source files |
| 2 | Parse | `syntax` | A syntax tree per file |
| 3 | Resolve | `resolve` | What each name means |
| 4 | Generate | `gen` | The items `gen` lines emit |
| 5 | Signatures | `check` | Types, signatures with flows, the impl table |
| 6 | Bodies | `check`, `lower` | Typed bodies, then MIR per function |
| 7 | Ownership | `borrow` | Moves, linear values and loans checked |
| 8 | Instances | `mono` | Monomorphic MIR from the roots |
| 9 | Code generation | `codegen` | Textual LLVM IR |
| 10 | Native | `toolchain` | Objects from `clang`, the executable from `lld` |

`mk check` runs stages 1 to 7, which makes it the fastest loop. [The compiler](./compiler) describes each stage, the cache and incremental builds, and [contributing](./contributing#repository-layout) maps every directory of the repository.

## The VS Code extension

From `editors/vscode`, after `npm ci`, `npm test` checks the TextMate grammar against every keyword, operator and literal form and every `.mk` file in `std/`. To try a change, open the folder in VS Code and press F5. This website highlights Mk with the same grammar, so a grammar fix shows up here too.

## Work on this website

The site lives in `website/` and is built with [VitePress](https://vitepress.dev). It needs Node.js 20 or newer.

```sh
cd website
npm install
npm run dev          # http://localhost:5173, reloads as you edit
npm run build        # the static site, in .vitepress/dist
npm run preview      # serve the built site
```

| Path | What it holds |
|---|---|
| `src/guide/`, `src/examples/index.md`, `src/reference/index.md`, `src/development/index.md`, `src/index.md` | Pages written for the site |
| `.vitepress/sync.ts` | Generates the reference, standard library, tools and contributor pages from `doc/`, `CONTRIBUTING.md` and `CHANGELOG.md` at every build |
| `.vitepress/markdown.ts` | Mk highlighting from `editors/vscode`, verdict lines, labels for rejected examples |
| `.vitepress/theme/` | The site's styles and the 404 page |

The generated pages are not committed: to change one, edit its source in `doc/` and the site follows. Before sending a change to the site, run its checks:

```sh
npm run check                         # types, the production build and every internal link and anchor
MK=/path/to/mk npm run check:examples # compile and run the site's own examples
```

`check:examples` judges every `mk` block on the hand-written pages with `mk corpus`, then runs each program that shows its output and compares what it prints with the page.

To serve the site from a subpath, as GitHub Pages does, set `MK_DOCS_BASE`, for example `MK_DOCS_BASE=/Mk-Lang/ npm run build`. The output in `.vitepress/dist` is static files that any web server can host.
