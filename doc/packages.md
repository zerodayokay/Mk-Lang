# Packages

A package is a directory with a manifest, `pkg.mk`, and the Mk files under it. This guide starts with `mk new` and grows the package step by step: modules, dependencies, versions, `pkg.sum`, the cache, workspaces and libraries. Package errors are E0202 with a `reason` fact, unless another code is named; [the last section](#17-package-errors) lists the reasons.

## 1. A new package

`mk new` writes a package that builds:

```sh
mk new notes
cd notes
mk run
```

It writes two files. The manifest:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "notes",
    mk: "0.7",
    bins: [pkg.Bin(name: "notes", root: "src", subsystem: "console")],
)
```

and `src/main.mk`:

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    try sys.Print("hello from notes\n")
}
```

`mk build` writes the executable `notes` (`notes.exe` on Windows) to the current directory. `mk run` builds and runs it, and `mk test` runs the package's `test` blocks. Add more files to `src/`, and they join the same module.

## 2. The manifest

`pkg.mk` holds one declaration, `const Package`, a `pkg.Package` value. It is ordinary Mk, checked as a one-file module whose only import is `pkg`, and it cannot do I/O. A manifest may leave out any field whose value would be `""`, `[]` or `false`.

| `pkg.Package` field | Meaning |
|---|---|
| `name` | The package's name |
| `mk` | The oldest mk that builds the package, such as `"0.1"` ([section 14](#14-the-mk-minimum)) |
| `bins` | The executables and libraries to build, each a `pkg.Bin` |
| `deps` | Dependencies, each a `pkg.Dep` ([section 4](#4-dependencies)) |
| `test_deps` | Dependencies used only by `test` blocks ([section 12](#12-test-only-dependencies)) |
| `unsafe_modules` | The modules allowed to write `unsafe`, by directory relative to the manifest: `"src/ffi"`, or `"."` for the root module (E1102 otherwise). For a module chosen per target, name the chosen directory, such as `"src/plat/windows"`, not the import name |
| `unsafe_deps` | Fetched packages allowed to use unsafe code, C and assembly ([section 11](#11-grants-for-unsafe-code)) |
| `c_sources`, `c_imports`, `asm_sources`, `links` | C files, C headers, assembly files and native libraries; see [the reference's section on unsafe code and C](reference.md#14-unsafe-and-c) |
| `targets` | Modules chosen per target |
| `members` | Workspace member directories ([section 13](#13-workspaces)) |
| `replace` | Version and source overrides ([section 5](#5-choosing-versions)) |

| `pkg.Bin` field | Meaning |
|---|---|
| `name` | The output's name |
| `root` | The directory of the module that holds `Main` (or the library's exports) |
| `subsystem` | `"console"`, or `"windows"` for an `x86_64-windows` program that opens no console window |
| `cpu` | `""` for the target's default, `"baseline"`, or `"general-regs-only"` (freestanding only) |
| `link_script` | A linker script, package-relative, for `x86_64-freestanding` only |
| `kind` | `""` for an executable, `"dynamic"` or `"static"` for a library ([section 15](#15-libraries)) |
| `abi` | A library's ABI number, `""` meaning `"1"` |

| `pkg.CImport` field | Meaning |
|---|---|
| `name` | The import path's segment: `import c.<name>` |
| `headers`, `defines`, `macros` | The headers, the macros they are read with, the prototypes of function-like macros to bind |
| `vendor` | Read the committed `gen/<target>/c/` views when the headers cannot be read |
| `include_dirs` | Package directories searched for headers after the root |
| `sdk` | `true`: read the headers with the host's SDK (the Windows SDK, or `/usr/include` on Linux), for a library whose headers include the system's ([C imports](reference.md#c-imports)), and link its Windows SDK functions from the SDK's import libraries; `mk sdk` shows which. Left out, a C import sees only `MK_SYSROOT_<target>`, as before |

A field left out and a field written empty are the same. A positional call or a misspelled field name is E0305. `mk fmt` keeps omitted fields omitted, and `mk fix --canonical` removes fields that hold their empty value.

## 3. Modules

Each directory of `.mk` files is one module. Its import path is its directory relative to the package root, with `.` between the parts: the files in `src/text` are the module `src.text`.

```mk
// verdict: parse
// src/main.mk
import src.text

export Main(sys: &System): WriteError!void {
    title := text.Title()
    try sys.Print("{title}\n", (title,))
}
```

- A module whose path has a segment named `internal` can be imported only by modules of the same package (E0214). The message names the nearest public module.
- An import path never starts with `std`, `c` or `pkg`; those first segments are reserved.
- `vendor/`, and the directory of a path dependency inside the package tree, form no module of the package.

## 4. Dependencies

A `pkg.Dep` names a package by one of three sources:

| Form | Meaning |
|---|---|
| `pkg.Dep(name: "textutil", path: "../textutil")` | A package in a directory, relative to the manifest that names it |
| `pkg.Dep(name: "toml", source: "github.com/someone/toml", version: "0.9.3")` | A git repository, at the tag `v0.9.3` or a higher version that another manifest asks for |
| `pkg.Dep(name: "ini", url: "https://example.com/ini-1.2.tar.gz")` | A `.tar.gz`, `.tar` or `.zip` archive. `https`, or `http` to the loopback address only |

```mk
// verdict: parse
const Package = pkg.Package(
    name: "notes",
    mk: "0.1",
    bins: [pkg.Bin(name: "notes", root: "src", subsystem: "console")],
    deps: [
        pkg.Dep(name: "textutil", path: "../textutil"),
        pkg.Dep(name: "toml", source: "github.com/someone/toml", version: "0.9.3"),
    ],
    test_deps: [pkg.Dep(name: "golden", source: "github.com/someone/golden", version: "2.0.0")],
)
```

`mk install` adds a `source` dependency for you ([section 16](#16-package-commands)).

### Rules for a `Dep`

- Exactly one of `path`, `url` and `source` is set.
- `version` is set exactly when `source` is. It is the oldest version you accept, `X.Y.Z` with an optional pre-release, as in semantic versioning 2.0.
- A `source` is `host/path`. The host holds a dot. It has no scheme, no query and no trailing `.git`.
- `hash` is allowed only with `url`. Leave it out, and mk records the archive's tree hash in `pkg.sum` ([section 7](#7-pkgsum-and-hashes)). A `hash: "sha256:<64 hex digits>"`, the SHA-256 of the archive, is accepted, and `mk fix` replaces it with a `pkg.sum` line.
- A fetched package names no `path` dependency.

Each violation is E0202 `invalid`, reported at the field.

### Importing a dependency

The `name` of a `Dep` is the name you import the package by. A dependency's root directory is the module `textutil`; its directory `src/x` is the module `textutil.src.x`.

```mk
// verdict: parse
import textutil

export Main(sys: &System): WriteError!void {
    for i in 0..textutil.Repeat(2) {
        try sys.Print("line {i}\n", (i,))
    }
}
```

- The name is local to the manifest that writes it. Two manifests may call one package by different names, and two packages may each call a different package `json`.
- A local name may not be a reserved first segment (`std`, `c`, `pkg`), or the first segment of one of the package's own modules.
- Only direct dependencies can be imported. Inside a dependency, its own modules are imported by their path in it (`import src.x`).
- A dependency's `bins` are not built. Its own `deps` and `targets` apply to it, and so do its `unsafe_modules`, `asm_sources`, `c_sources`, `c_imports` and `links`, once granted ([section 11](#11-grants-for-unsafe-code)).
- A dependency cycle is E0202 `cycle`.

Diagnostics, `mk query` and `mk tree` print a package as its local name with its source and version: `toml (github.com/someone/toml v0.9.3)`.

### Package identity

mk identifies a fetched package by its source and major version, and a path package by its directory relative to the workspace root. From that identity it derives a canonical id. Symbol names and cache keys use the canonical id, so they do not depend on what a manifest calls the package. You see canonical ids only in symbol names and under `vendor/`.

| Package | Canonical id |
|---|---|
| Fetched from a `source` | The source's last element, made an identifier, `_`, then 8 hex digits of SHA-256 over `<source>@v<major>`: `toml_3f2a9c1e` |
| From a `url` | The last element of the url's path without its archive extension, `_`, then 8 hex digits of SHA-256 over the url |
| A path package outside the workspace's members | Its manifest name, `_`, then 8 hex digits of SHA-256 over its relative directory |
| The root package and workspace members | Their manifest names, which must be unique in the workspace |

Two identities with one canonical id are E0202 `id-clash`; resolve it with a `replace` or a rename.

Each major version is a separate identity, 0 and 1 included, so two majors of one source can coexist under different local names. They may not both define one C symbol (an `export extern`, or a global of a C or assembly source): the second `Dep` that brings one in is E0202 `symbol-clash`, naming the symbol and both versions.

## 5. Choosing versions

mk chooses versions by minimal version selection, Go's algorithm. Each manifest states the oldest version it accepts. There are no upper bounds and no lock file.

1. The requirements are the root's `deps` and `test_deps` that have a `source`, and in a workspace every member's.
2. mk reads the manifest of every version those requirements reach, once each, and follows its requirements in turn. Where the server supports it, mk fetches only `pkg.mk` for a version that may not be chosen. The manifests read are hashed in `pkg.sum`, so an offline build repeats the same walk.
3. The build uses, for each identity, the highest version visited. Path and `url` packages are fixed.

The result depends only on the manifests reached. Versions compare by semantic versioning 2.0, and a pre-release is used only when a manifest names it exactly. A requirement on a version with no tag is E0202 `no-version`.

### `replace`

`replace` in the root manifest points every use of a source at a directory or another source, or pins its version. A `replace` entry is a `pkg.Dep` whose `name` holds the source it replaces:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "notes",
    bins: [pkg.Bin(name: "notes", root: "src", subsystem: "console")],
    deps: [pkg.Dep(name: "toml", source: "github.com/someone/toml", version: "0.9.3")],
    replace: [
        pkg.Dep(name: "github.com/someone/toml", path: "../toml"),
        pkg.Dep(name: "github.com/someone/golden", source: "github.com/someone/golden", version: "2.1.0"),
    ],
)
```

- A replacement by `path` makes a path dependency, relative to the root.
- Naming its own source with a `version` pins that version, as Go's `replace x => x v1.1.0` does.
- Replacements apply before the version walk, and chain whatever their order. A cycle of replacements, or a source that reaches two identities through `replace`, is E0202 `replace`.
- `replace` and `members` in a fetched package's manifest are ignored, with a note that `mk why` repeats.

## 6. Fetching from git

mk speaks git's smart HTTP protocol, version 2, itself; it needs no `git` installed. It lists the tags `vX.Y.Z` and fetches the one commit a tag names, at depth 1. A manifest writes the version without the `v`.

- Only SHA-1 repositories are read. A SHA-256 repository, or a server without protocol version 2, is E0202 `protocol`; use the `url` form for it.
- mk treats the fetched data as untrusted. It refuses a pack of more than 1,000,000 objects, 1 GiB of inflated data, a delta chain longer than 4,095, or more than 256 nested directories, with E0202 `fetch` and the reason.

**Credentials.** For a private host, mk reads a netrc file: `NETRC`, else `~/.netrc` or `%USERPROFILE%\_netrc`. The login and password are sent as basic authentication over https, to the host the entry names only, and dropped on a redirect to another host. A credential never appears in a diagnostic, a cache key, `pkg.sum` or a log.

**Offline.** `--offline`, or `MK_OFFLINE=1`, never fetches. A build that needs bytes it does not have is E0202 `offline`. `--no-cache` keeps nothing it fetched.

## 7. `pkg.sum` and hashes

`pkg.sum` sits beside the root manifest, and you commit it. It records hashes, not choices. mk adds the lines it lacks when it fetches, and writes the file atomically.

```text
github.com/someone/toml v0.9.3 1f0c...e2 tree-sha256:9b4d...07
github.com/someone/toml v0.9.3/pkg.mk 1f0c...e2 sha256:51aa...c3
https://example.com/ini-1.2.tar.gz - - tree-sha256:0c7e...91
```

| Line | Records |
|---|---|
| `<source> v<version> <commit> tree-sha256:<hex>` | A package version in the build, test dependencies included |
| `<source> v<version>/pkg.mk <commit> sha256:<hex>` | A manifest the version walk read, as `go.sum`'s `/go.mod` lines do |
| `<url> - - tree-sha256:<hex>` | A `url` package |

Lines are sorted by source, then version. Concurrent builds merge their lines under `pkg.sum.lock`, so none is lost. A failure to write `pkg.sum` is a note, not an error.

| Fault | Code |
|---|---|
| A fetched tree's hash differs from its line | E0202 `sum-mismatch`, with both hashes |
| A version with no line, in an offline build or under `--locked` | E0202 `sum-missing` |
| A tag that names a different commit than its line | E0202 `tag-moved` |
| A `url` archive whose bytes differ from its manifest `hash` | E0202 `hash-mismatch` |

`--locked` refuses any version without a line and never writes `pkg.sum`.

**The tree hash.** mk turns an archive or a git tree into a set of regular files in memory, and hashes that set: `tree-sha256:` is SHA-256 over one line per file, `<SHA-256 of its bytes, 64 hex>  <slash path relative to the package root>\n`, sorted by path. File modes and directories do not enter. An archive's lone top directory is stripped.

A tree is refused, with E0202 `unsafe-archive`, when it holds a link, a submodule, a device, a path that escapes the root, a Windows device name, a path segment `.git` or `git~1` in any case, or two paths equal after case folding. Nothing reaches the disk before its hash is checked.

## 8. The cache

There is one cache per user:

| System | Location |
|---|---|
| Windows | `%LOCALAPPDATA%\mk\cache` |
| Linux | `$XDG_CACHE_HOME/mk`, else `~/.cache/mk` |

`MK_CACHE` names another directory. The cache holds fetched packages, at `pkg/<tree hash>/`, and build products.

- A package tree is written to a temporary directory, checked, renamed into place and made read-only. `MK_CACHE_VERIFY=1` re-hashes it each time it is used.
- Build products are evicted least recently used first once the cache passes 2 GiB. Package trees are never evicted.
- `mk cache size` reports package bytes and build bytes separately.
- `mk cache clean [path]` empties the build products of the cache a build of `path` would use, and keeps the packages.
- `mk cache prune --packages <dir>...` removes the package trees that no `pkg.sum` under the named directories refers to.

## 9. `vendor/`

`mk vendor` copies every package of the build into `vendor/`. A build then reads packages from there before it fetches, for dependencies of dependencies too.

```text
vendor/
    index.txt
    textutil_677ed16d/
        pkg.mk
        textutil.mk
```

- Each package is at `vendor/<canonical id>/`.
- `vendor/index.txt` has one line per package: `<source or path> v<version> <canonical id> tree-sha256:<hex>`. A path package's line names its directory relative to the workspace root, a `url` package's names its url, and a package with no version writes `-` for the version.
- A vendored build takes its versions from the index and skips the version walk. It reads `pkg.sum` and never writes it, and each source package's index line must agree with `pkg.sum`'s line for that version.
- `MK_CACHE_VERIFY=1` re-hashes vendored trees against the index. A mismatch is E0202 `sum-mismatch`.
- A vendored package keeps its source's identity, so it still needs its grant ([section 11](#11-grants-for-unsafe-code)).
- A `vendor/<name>` layout, with a directory per local name, is also read, with a note that suggests `mk vendor`.

## 10. `internal` modules

A module whose import path has a segment `internal` is importable only from modules of the package that holds it. Workspace members are separate packages. Any other import of it is E0214, naming the package and the nearest public module.

## 11. Grants for unsafe code

A fetched package can ask for unsafe code, assembly, C files, C imports and native libraries through its `unsafe_modules`, `asm_sources`, `c_sources`, `c_imports` and `links`. These take effect only when your root manifest's `unsafe_deps` names the package: by source, by url for a `url` package, or by local name for a direct dependency.

```mk
// verdict: parse
const Package = pkg.Package(
    name: "notes",
    bins: [pkg.Bin(name: "notes", root: "src", subsystem: "console")],
    deps: [pkg.Dep(name: "zlib", source: "github.com/someone/zlib", version: "1.3.1")],
    unsafe_deps: ["github.com/someone/zlib"],
)
```

Without the grant, the build stops at the `Dep` that brings the package in, with E0215. The message lists what the package asks for and the line to add.

- Path packages, workspace members and the root are your own code and need no grant.
- `mk query perimeter` lists, per package of the build, its local names, source and version, and each unsafe module, assembly file, C file, C import and link. `--json` gives the same as JSON.
- `mk update` prints how the perimeter changed. `mk install` prints the grants a new package would need, and never adds them.

## 12. Test-only dependencies

`test_deps` are chosen and hashed like `deps`. A module may import one, but may use its names only inside `test` blocks; a use anywhere else is E0217, reported where the name is used. Test dependencies are resolved whenever the package is checked, so `test` blocks always type-check, and they are linked only into the executables `mk test` builds.

## 13. Workspaces

A workspace is several packages developed together. The root manifest's `members` lists the member directories, each holding its own `pkg.mk`:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "suite",
    members: ["engine", "cli"],
)
```

- mk finds the workspace by walking up from a package to the first manifest whose `members` lists it.
- The members share one `pkg.sum`, one version choice over all their requirements, one set of grants and `replace` entries (the root's), and one cache.
- Members depend on each other by path.
- `mk build ./...`, `mk test ./...` and `mk check ./...` run once per member. The exit status is the highest of the runs.
- The root may itself be a package with bins.
- A member that is itself a workspace root is E0202 `nested-workspace`.

## 14. The `mk` minimum

`pkg.Package.mk` names the oldest mk that builds the package, such as `"0.1"`; `""` names none. A build needs the highest minimum of every package in it. An older mk stops before building with E0216, naming the version needed. mk never downloads a toolchain by itself.

## 15. Libraries

A `pkg.Bin` with `kind: "dynamic"` or `kind: "static"` builds a library for C and other languages instead of an executable. Libraries are built for `x86_64-windows`, `x86_64-linux` and `aarch64-linux`.

```mk
// verdict: parse
const Package = pkg.Package(
    name: "fastzip",
    bins: [pkg.Bin(name: "fastzip", root: "src", kind: "dynamic", abi: "2")],
)
```

**What it exports.** A library exports its own package's `export extern` functions and, as data, its `export extern var`s. Every other symbol is local, including std's and the dependencies' `export extern` functions. It also exports two runtime entries, `<name>_mk_start` and `<name>_mk_stop`.

**The boundary.** Every exported function uses the platform C ABI, and every struct has C layout. A parameter or result whose type has no C form, an error-union result, or a `const` parameter is E1109. A reference to a type with a C form is a C pointer, so it has a C form too. A private type in an exported signature is E0412, and a vector, or an aggregate holding one, is E1107.

**Dynamic libraries.** On Windows, mk writes an import library beside the DLL. On Linux, the library's soname is `lib<name>.so.<abi>`, its version script's node is `<NAME>_<abi>`, and it is compiled position-independent, std included.

**Static libraries.** A static library carries the Mk runtime and std, so at most one Mk static library can be linked into one program.

**The header.** `mk build` writes `<name>.h` beside every library. It holds an include guard, includes of `<stdint.h>`, `<stddef.h>` and `<stdbool.h>`, `extern "C"` under `__cplusplus`, one declaration per export, the options struct `<Name>MkOptions { carriers: u32, stack_reserve: u64 }` and the two entries.

- `<name>_mk_start` returns 0, 1 or 2. A library cannot start again after `<name>_mk_stop`.
- On Windows, an exported variable is declared `__declspec(dllimport)`.
- On ELF, an exported variable is protected, so a host must reach it through the GOT: compile the host with `-fPIC`, or clang's `-fPIE`. ld.lld refuses a host that would take a copy relocation (`-fno-pic -no-pie`, or `-fdirect-access-external-data`). Another linker may accept one and give the host a copy the library never sees. A `.so`'s header says so.

## 16. Package commands

| Command | Does |
|---|---|
| `mk new <name>` | Writes `<name>/pkg.mk`, with `mk` set to the running version and one bin, and `<name>/src/main.mk`, whose `Main` prints one line |
| `mk install <source>[@<version>]` | Adds a `source` dependency and its `pkg.sum` line. `@X.Y.Z` is the minimum; `@X.Y` and `@X` pick the newest tag within; no `@` picks the newest release. The local name is the source's last element, made an identifier, or `--name`. Prints the grants the package needs |
| `mk install <short name>` | Looks the name up in `seed/first-party.txt` beside the toolchain's `bin/` directory, and adds that package at the version listed there. A toolchain built from source has no such list, so use the full source |
| `mk remove <name>` | Removes the `Dep` from `deps` or `test_deps`, and the `pkg.sum` lines no longer needed |
| `mk update [name] [--dry-run]` | Raises each direct requirement, or the named one, to the newest tag of its major version. Prints every version and perimeter that changed |
| `mk tree` | The build list as a tree: each package's chosen version, and the minimum each requirer states |
| `mk why <name or source>` | The requirement paths from the root to the package, and the manifest whose minimum chose its version |
| `mk query perimeter` | Each package's local names, source, version and unsafe code ([section 11](#11-grants-for-unsafe-code)) |
| `mk vendor` | Copies the build's packages into `vendor/` ([section 9](#9-vendor)) |

With the path dependency of [section 4](#4-dependencies):

```text
$ mk tree
notes
  textutil (../textutil)
$ mk why textutil
notes -> textutil (../textutil)
$ mk vendor
vendor/textutil_677ed16d  ../textutil
```

The commands edit `pkg.mk` in place. They keep its comments, format the result, check it, and replace the file in one step, so a failed edit leaves the file as it was. A manifest whose `Package` is not a constructor call, or whose list is not an array literal, is refused with a message that names the edit to make by hand.

`mk install` adds a dependency, as npm's does. It never builds a tool into your path, as `go install` does.

## 17. Package errors

Package faults are E0202, reported at the `Dep`, field or line that causes them, with one of these `reason` facts:

| `reason` | Meaning |
|---|---|
| `invalid` | A manifest field breaks a rule of [section 4](#4-dependencies) |
| `conflict` | With a `vendor/<name>` layout, two `Dep`s give one name different sources |
| `cycle` | The dependencies form a cycle |
| `fetch` | The network or the server failed, or a fetch passed a limit |
| `offline` | An offline build needs bytes it does not have |
| `hash-mismatch` | A `url` archive's bytes differ from the manifest's `hash` |
| `unsafe-archive` | A fetched tree holds a link, an escaping path or another refused entry |
| `id-clash` | Two packages have one canonical id |
| `symbol-clash` | Two major versions of one source define one C symbol |
| `no-version` | No tag for the version required |
| `replace` | A `replace` cycle, or a source that reaches two identities |
| `sum-mismatch` | A tree's hash differs from its `pkg.sum` line |
| `sum-missing` | No `pkg.sum` line, offline or under `--locked` |
| `tag-moved` | A tag names a different commit than its `pkg.sum` line |
| `protocol` | The server does not speak git protocol version 2, or the repository uses SHA-256 |
| `nested-workspace` | A workspace member is itself a workspace root |

Four package faults have codes of their own: E0214 (an `internal` module), E0215 (a missing grant), E0216 (mk too old) and E0217 (a test dependency outside a test). [The error codes](errors.md) list the other E0202 reasons, for modules and targets.
