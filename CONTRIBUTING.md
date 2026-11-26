# Contributing to Mk

This page says how the repository is laid out, how to build and test it, and the rules a change follows. To learn the language, start with the [documentation](doc/README.md). To learn how the compiler works, read [doc/compiler.md](doc/compiler.md).

Mk is released under the MIT License (see [LICENSE](LICENSE)). By contributing, you agree that your contributions are released under the same license.

## Repository layout

| Path | What |
|---|---|
| `cmd/mk` | The `mk` command line |
| `internal/source`, `diag`, `ast`, `types`, `mir` | The data types every stage shares: source files, diagnostics, the syntax tree, types and the mid-level IR (MIR). They hold no logic |
| `internal/syntax`, `resolve`, `check`, `lower` | The front end: lexer and parser, name resolution, type checking, lowering to MIR |
| `internal/borrow` | Moves, linear values and the loan checker |
| `internal/eval`, `gen` | Compile-time evaluation and generators |
| `internal/mono`, `abi`, `llvm`, `codegen`, `pgo` | Instantiation of generics, the C ABI, LLVM IR, code generation, profile-guided optimization |
| `internal/iface`, `cache`, `driver` | Module interfaces, the build cache, the pipeline that ties the stages together |
| `internal/toolchain`, `cimport` | Finding and running `clang` and `lld`; C import through Clang |
| `internal/fetch`, `git`, `release` | Package fetching, the git client, release archives |
| `internal/lsp`, `repl`, `format`, `fmtspec` | The language server, the REPL, the formatter, format strings |
| `internal/corpus` | The runner that compiles every Mk example in `doc/` and the conformance units |
| `internal/archtest` | The tests that enforce the package graph and the rules for Go files |
| `std/` | The standard library, the runtime and the scheduler, written in Mk. Its Go tests (`std/*_test.go`) build and run Mk programs |
| `kits/` | First-party frameworks written in Mk (`kits/web`) |
| `tests/programs` | Programs with their expected output, built and run by the Go tests |
| `tests/conformance` | One unit per error code and memory rule, judged by `mk corpus --all` |
| `tests/soundness` | The borrow checker's soundness tests and witnesses |
| `tests/bench` | Benchmarks: Mk against C, Rust, Zig and Go |
| `tools/gate` | Runs every check in a fixed order and writes one report |
| `tools/bench` | Builds and times the benchmarks |
| `tools/dogfood` | Runs the cases of the programs in `Dogfoods/` |
| `Dogfoods/` | Larger programs written as an outside user would write them, each with its cases and the defects it found |
| `editors/vscode` | The VS Code extension |
| `doc/` | The documentation. Every `mk` block in it is a test |

## Building and testing

[Building from source](README.md#building-from-source) in the README lists what you need and how `mk` finds LLVM. To work on Mk itself:

```sh
go build ./...                 # build everything
go build -o mk ./cmd/mk        # the mk binary (mk.exe on Windows)
go vet ./...
go test ./...                  # every Go test
go run ./cmd/mk corpus         # compile every mk block in doc/ against its verdict
go run ./cmd/mk corpus --all   # the same, plus the units in tests/conformance
```

`go test ./std` builds and runs many Mk programs and takes a long time. While you work on one package, test that package: `go test ./internal/borrow`. To judge one document, run `go run ./cmd/mk corpus --only "doc/memory.md:*"`.

### The evidence directory

Some tests check Mk against outside data that is too large for the repository or under its own licence. They read it from the evidence directory: `$MK_EVIDENCE`, and in most tests, when it is unset, `Mk-evidence` beside the repository (the driver itself reads `$MK_EVIDENCE` only). It is not part of the repository, and the tests that need it skip without it, naming `MK_EVIDENCE`. Its parts:

| Directory | What | Source |
|---|---|---|
| `ucd/`, `unicode/idna/` | Unicode Character Database and IDNA data 18.0.0 | unicode.org/Public |
| `wycheproof/` | Wycheproof test vectors | github.com/C2SP/wycheproof, commit dab4ea2e |
| `x509/` | x509-limbo `limbo.json` | github.com/C2SP/x509-limbo, commit 519f4bcf |
| `fiat-crypto/` | Fiat Cryptography P-256 and P-384 field arithmetic | github.com/mit-plv/fiat-crypto, commit dd8f11d9 |
| `compress/zstd/` | zstd golden test files | github.com/facebook/zstd, tag v1.5.7 |
| `vendor/zstd-1.5.7/`, `vendor/zdec/` | zstd 1.5.7 source release, and `zdec.c`, the reference decoder `tools/gate` builds from it for std's zstd tests | github.com/facebook/zstd releases |
| `vendor/zlib-1.3.1/` | zlib 1.3.1 source release (benchmark reference) | github.com/madler/zlib releases |
| `vendor/sqlite/` | SQLite 3.54.1 amalgamation (`Dogfoods/notes` builds with it) | sqlite.org |
| `jsontestsuite/` | JSONTestSuite | github.com/nst/JSONTestSuite, commit 1ef36fa0 |
| `iana/` | `zoneinfo.zip`, IANA tz 2026c | Go 1.27.3's `lib/time` |
| `cldr/` | CLDR 49 `windowsZones.xml` | github.com/unicode-org/cldr, tag release-49 |
| `whatwg/` | Encoding and URL Standard data and web-platform-tests files | github.com/whatwg (encoding be17ac5c, url 845bafca), github.com/web-platform-tests/wpt (4561f062) |
| `rfc/` | RFC 8448 | rfc-editor.org |
| `math/` | correctly rounded reference values for std.math | generated with mpmath by `gen_math.py` in that directory |
| `abi-oracle/` | `probe.go`, which runs clang over `internal/abi/testdata/suite.txt` to regenerate the ABI oracle | written for Mk |
| `crates/` | vendored Rust crates for the Rust rows of `tests/bench` | crates.io, through `cargo vendor` |
| `nuget/` | offline .NET 11 NativeAOT and runtime `.nupkg` files for the C# (`cs`, `cs-aot`) columns of `tests/bench` | nuget.org, pinned by SHA-256 in `nuget/SOURCE.txt` |

### The gate

```sh
go run ./tools/gate -tier quick
```

The gate runs these steps one after another and writes one JSON report:

| Step | What it runs |
|---|---|
| build | `go build` and `go vet` for Windows and Linux, and `gofmt -l` |
| test | `go test` over every package; `./std` runs last, in its own `go test` |
| linux | the tests that need WSL, with `MK_LINUX=1`, under the WSL lock |
| corpus | `mk corpus`, `mk corpus --all --strict`, and `mk fmt` over the documentation's examples |
| dogfood | the cases of the dogfood programs whose files changed |
| timing | the tests with wall-clock assertions, with `MK_TIMING=1`, one at a time |
| fuzz | the fuzz targets of the packages that changed |
| apidiff | in the full tier only: `mk apidiff --std` against the last release, failing on a breaking change the release does not list |

The quick tier keeps Go's test cache, so a package whose inputs did not change is not run again. `-tier full` runs everything again. `-skip build,fuzz` leaves steps out, and `-only ./internal/borrow` limits the test, linux, timing and fuzz steps to the packages named.

### Work directories

`tools/gate`, `tools/dogfood` and `tools/bench` keep their builds, caches, logs and raw results outside the tree, under one work root:

| Tool | Work directory, first that applies |
|---|---|
| `tools/gate`, `tools/dogfood` | `-work`, then `MK_WORK`, then the default root |
| `tools/bench` | `-work`, then `MK_BENCH_WORK`, then `bench` under `MK_WORK` or the default root |

The work root:

- The default is `mk-work` in the user's cache directory (Go's `os.UserCacheDir`: `%LocalAppData%\mk-work` on Windows; on Linux `$XDG_CACHE_HOME/mk-work`, else `~/.cache/mk-work`). Without a cache directory, it is in the temporary directory.
- It sits beside the compiler's own cache (`mk` in the same directory), never inside it, and never inside the repository.
- The gate's work directory holds its own `GOCACHE` and test temp files, so it grows large. Point `MK_WORK` at another disk when the cache directory's disk is small.
- `MK_WORK` must be an absolute path, so every tool finds the same lock from any directory. The tools refuse a relative one.

The machine lock:

- It keeps these from running at the same time: the gate's timing and fuzz steps, the whole of a full or soak gate run, `go run ./tools/gate lock -- <cmd>`, and the benchmark publishes.
- It is the file `machine.lock` in `MK_WORK`, or in the default root, whatever `-work` each tool is given, so every tool finds the same lock.
- The operating system releases it when the holding process exits, however it exits.
- `tools/gate -lock` and `tools/bench -lock` name another file. `gate lock` takes no flags and always uses `machine.lock` of the work root.
- The default lock is per user: two accounts exclude each other only when both set `MK_WORK` to one shared directory.
- The gate passes the resolved `MK_WORK` to every command it starts.

### Continuous integration

GitHub Actions runs three workflows ([.github/runners](.github/runners/README.md) says what runs where):

- On every push to `main` and every pull request, forks included, CI runs its fast 18-job tier on GitHub-hosted Linux and Windows runners (under GitHub's 20-job free concurrency cap): `go build ./...`, `go vet ./...`, `gofmt`, `go run ./cmd/mk corpus`, `-short` compiler unit/integration tests on both OSes, 12 `-short` `internal/driver` shards and `internal/lsp` on Linux, and a curated Windows-native `driver-win-smoke` suite (`MK_GATE=1`), driven by `tools/ci/test.sh`. It has a read-only token and no secrets. A pull request from outside the project waits for a maintainer to approve its run. Full non-short CI (including macOS and AArch64 Linux) can also be triggered manually via `workflow_dispatch` (`mode=full`).
- A nightly run on `main` runs full non-short 5-OS CI (`windows-2025`, `ubuntu-24.04`, `macos-15`, `macos-15-intel`, `ubuntu-24.04-arm`) on GitHub-hosted cloud runners, plus the PC-guarded self-hosted `tools/gate` quick tier (`./std` and `MK_SLOW=1` included).
- A `v*` tag of a commit on `main` runs full non-short CI on Windows and Linux, builds and signs the binary bundles, and drafts a GitHub release ([Releases](#releases)).

Run the same commands locally before you open a pull request.

### Releases

A release is a `v*` tag whose version is `Release` in `internal/driver/version.go` and `version` in `editors/vscode/package.json`, and has a section in [CHANGELOG.md](CHANGELOG.md). Set all three, merge them to `main`, then tag that commit and push the tag (`git tag v0.1.0`, `git push origin v0.1.0`). `.github/workflows/release.yml` then:

1. Fails at once unless the tagged commit is on `main`, the tag is mk's version, CHANGELOG.md has a section headed `## <version>` (a date in parentheses may follow), and `internal/release/keys.go` and the install scripts hold the same release key.
2. Runs CI. Beside it, on a Windows, an x86_64 Linux, an AArch64 Linux (`ubuntu-24.04-arm`) and an Apple silicon (`macos-15`) runner, builds a release build of mk (`go build -trimpath` with the release address compiled in) and its bundle with `go run ./tools/release build`: mk, clang, lld and clang's headers from LLVM's official 21.1.8 release (checked against their SHA-256), LLVM's licence and the documentation. There is no Intel Mac bundle: LLVM publishes no Intel macOS binaries, and Homebrew's link against its own shared libraries. It unpacks each bundle and runs a hello program with it. On macOS it also checks that the bundled programs load only the system's libraries and run on macOS 14, that they are validly signed, and runs install.sh's tests with only the system's tools on `PATH`.
3. Packages the VS Code extension in a job of its own, with npm's install scripts off.
4. When all of that passes, writes `SHA256SUMS` over the bundles, the extension, `install.ps1` and `install.sh`, signs it with `MK_RELEASE_KEY`, a secret of the `release` environment, and checks the signature against the keys built into mk. That job runs only the pinned GitHub actions and `tools/release`, and is the only one that sees the key.
5. A last job, the only one whose token can write and which holds no secret, checks the signature and every file again and drafts the GitHub release: the bundles, `SHA256SUMS`, `SHA256SUMS.sig`, `install.ps1`, `install.sh` and the extension, with the tag's CHANGELOG section as notes. A version with a pre-release suffix (`0.2.0-rc.1`) is drafted as a pre-release.

Read the draft and publish it. The install scripts and `mk upgrade` find only published releases. If a tag's run fails, fix `main`, then an admin deletes the tag (the ruleset below lets no one else) and tags the fixed commit, or reruns the failed jobs when the cause was outside the repository.

The `release` environment admits only `v*` tags, so a branch or a pull request never reaches the key, whatever its workflow files say. Since it checks only the tag's name, a tag ruleset on `refs/tags/v*` restricts creating, updating (moving, force-pushing) and deleting such tags; only the repository admin role may bypass it, and no GitHub App, the GitHub Actions app included, and no deploy key. Without the update rule, anyone with write access could move an existing tag to a commit of their own, whose workflow would then get the key.

To try everything but signing before the tag, run the Release workflow by hand on `main` (Actions > Release > Run workflow). It runs the checks that need no tag, CI, the four bundles and the extension, and builds `SHA256SUMS` as a release would; it neither signs nor drafts a release.

The release key is made once, offline: `go run ./tools/release keygen -out <file outside every repository>` writes the private key and prints the public key's line. Add that line to `trusted` in `internal/release/keys.go`, and the same key id and public key to `$MkTrustedKeys` in `tools/install/install.ps1` and `trusted_keys` in `tools/install/install.sh` (`go test ./tools/release -run TestInstallKeys` checks that the three agree). Store the private key file's text as the secret `MK_RELEASE_KEY` of the `release` environment (never as a repository secret, which every branch's workflows can read) and keep two copies offline, never on a drive the self-hosted runners' accounts can read. To change keys, release once with the next key listed beside the current one, then sign with the next and remove the old.

### The VS Code extension

From `editors/vscode`, after `npm ci`:

- `npm test` checks the grammar offline with VS Code's own tokenizer: every keyword and contextual word of the reference and the compiler's lexer, every operator and literal form, and every `.mk` file under `std/`.
- `npm run test:vscode` starts the installed VS Code with a throwaway profile and this folder as a development extension, opens files of the repository and checks hover, definition, implementation, inlay hints and symbols. It needs `mk` at the repository root or `MK_EXE`.
- To try a change, open the folder in VS Code and press F5.

[doc/editors.md](doc/editors.md#vs-code) is the extension's user documentation.

## Rules

### Dependencies

- The compiler uses the Go standard library only. No cgo, and no `unsafe` import.
- There is no C in the compiler, the runtime or the standard library. The exceptions are the C, Rust, Zig and Go baselines in `tests/bench`, and a user package's own `c_sources`, which link against `std.libc`, never a C runtime.
- Imports between compiler packages follow the package graph in `internal/archtest/dag.go`. `internal/archtest` fails on any import the graph does not allow, so a change that needs a new edge changes the graph in the same commit.
- `internal/archtest` also holds every Go file to at most 1,200 lines, and keeps reflection out of the compiler passes.

### Language changes

- A public fact (a signature, its flows and error set, a type's kind and layout) never depends on a function body, except through compile-time evaluation. A caller is checked against signatures alone.
- A new language feature has to pass the admission test. Nothing enters the language for one application unless three unrelated kinds of program need it, and anything a library can do is a library. A proposal answers eight questions:

  1. What is the problem, and how often does it occur?
  2. Does something that exists already do it?
  3. Which concepts does it add?
  4. How does it interact with the rest of the language?
  5. What does it cost the compiler, and what does an edit to it rebuild?
  6. Does code that uses it stay readable?
  7. Can an agent check it?
  8. Can it be explained in one paragraph?

  [doc/rationale.md](doc/rationale.md) explains why Mk has the features it has and leaves others out.

### Linux tests and WSL

On Windows, the tests that build and run `x86_64-linux` programs run them inside WSL. They run only with `MK_LINUX=1`. Anything that may start `wsl.exe` runs under the WSL lock, with `go test -p 1`:

```powershell
$env:MK_LINUX = "1"
pwsh -NoProfile -File tools/wsl-lock.ps1 go test -p 1 ./internal/corpus
```

Never run two WSL clients at once: parallel `wsl.exe` clients can hang WSL until the machine restarts. The gate's linux step takes the lock itself.

### Timing tests and benchmarks

- Tests that assert on wall-clock time run only with `MK_TIMING=1`, on a quiet machine, through `tools/gate` or `tools/bench`. A timing taken while other work runs does not count.
- Benchmark numbers come only from `tools/bench`. Do not write a number in a document unless it says who measured it, how, and on what machine. [tests/bench/README.md](tests/bench/README.md) states the method and how to run the tables.

### Full test variants

Some tests check a sample in a plain run and the whole set only with `MK_SLOW=1`. The nightly workflow sets it; the per-push CI and `tools/gate` do not. With it set:

- `TestDeterminismGOMAXPROCS` starts every program, target and profile at GOMAXPROCS 1, not only one combination.
- `TestCanonicalIdempotentOnTree` runs the second `mk fix --canonical` on units the first run left unchanged.
- `TestInvalidation_RestateImportedAlias` checks every step cold in a fresh cache, not each tree state once.
- On Windows without `MK_LINUX=1`, the corpus programs tests build their `x86_64-linux` cases (they still do not run them).

Set it locally when you change `mk fix`, build determinism or incremental checking:

```powershell
$env:MK_SLOW = "1"
go test -p 2 ./internal/driver -run '^TestDeterminismGOMAXPROCS$'
```

### GPU tests and goldens

The portable device blocks of `std.gpu` (names starting `device`) run on one adapter per run, chosen by `MK_GPU_ADAPTER`: `warp` (D3D12's software rasterizer, the default on Windows), `d3d12` (a hardware D3D12 adapter) or `vulkan`. The backend blocks (`dx`, `vk`, `gpu vulkan`) run on their own backend when it is present, unless `MK_GPU_ADAPTER` names the other one. A block whose adapter is absent prints `skip: <reason>` and passes. `TestGpu_Blocks` runs everything with the default and `TestGpu_DeviceAdapters` runs the device blocks on the two hardware backends:

```powershell
$env:MK_GPU_ADAPTER = "d3d12"
mk test --std std.gpu --run device
```

What the comparisons check:

- On WARP each of the nine golden scenes must equal its committed PNG byte for byte when the host's `d3d10warp.dll` has the version the goldens were made with (`GoldenWarpVersion` in `std/gpu/tests_goldens.mk`). On another version the block prints the measured difference and does not fail, until a bound for that version is accepted.
- On hardware each scene is compared with WARP rendering it in the same run, and D3D12 with Vulkan on the same GPU: no channel more than 2 apart except on at most 0.1 % of the pixels (0.5 % for the MSAA scene), each of them next to a pixel within 2.
- Device blocks open D3D12 in `DebugMode.Strict`, which needs the D3D12 debug layer (Windows' optional Graphics Tools feature), and fail on any debug-layer error or warning. Vulkan blocks use `DebugMode.Validate`: without `VK_LAYER_KHRONOS_validation` installed, that is the portable layer's own checks and the layout tracker, not the driver's synchronization validation.

When a scene or the WARP version changes, regenerate the goldens on Windows, look at the PNGs, and commit them with `std/gpu/tests_goldens.mk`:

```powershell
go run ./tools/gpugolden -mode generate -mk <mk.exe>
go run ./tools/gpugolden -mode verify -live -mk <mk.exe>
```

`tools/gpugolden` renders the scenes through the block `gpu dump warp goldens`, writes `tools/gpugolden/goldens/*.png` and the Mk file, and keys them by the version it reads from `C:\Windows\System32\d3d10warp.dll`. `go test ./tools/gpugolden` checks that the Mk file matches the PNGs. The test shaders come from `tools/gpushaders`: change the HLSL under `tools/gpushaders/hlsl`, then run `go run ./tools/gpushaders -dxc <dxc.exe>` with the DXC version the tool pins.

### Documentation

- Every fenced `mk` block in `doc/` is a test. Its first line is its verdict: `// verdict: ok`, `// verdict: parse`, or `// verdict: reject E0505` with the exact codes the compiler must report, and `// <- E0505` at the end of the line where the error must be reported. A block that holds on one architecture only, such as an `asm` example in x86 assembly, says so on its second line, `// arch: x86_64`, and `mk corpus --target` for another architecture skips it. Use a `text` or `sh` fence for anything that is not Mk.
- A change to the language, the library or a command changes the documentation in the same commit, and a user-visible change adds a line to [CHANGELOG.md](CHANGELOG.md).

### Comments

- Every source file, Go or Mk, starts with a header of three labelled parts: `Why:` (the problem the file solves), `What:` (what it provides, by name) and `How:` (the approach, and anything surprising).
- Comments explain the code as it is. They never cite tickets, plans, reviews or decision numbers, and never tell the history of a change; the commit message holds that.

### Style

- Go code is `gofmt`-clean and passes `go vet`.
- Mk code is a fixed point of `mk fmt` and `mk fix --canonical`, and follows [the code style](doc/style.md).
