# archive (app)

**Task.** A signed, sealed archive of a directory tree, created and then opened again: the kind of program a user writes to back up or ship a tree (tests/bench/README.md "Applications"). `archive create <dir> <out> [<files>]` writes the archive of the tree `<dir>` to `<out>`, taking only the first `<files>` files of step 2's order when given. `archive open <archive>` checks an archive and prints its manifest. Every language does these steps, in this order, with its own standard library or, for Rust, the libraries named below.

`create`:

1. **Walk.** Walk `<dir>` recursively and keep every regular file, hidden, empty and binary files included; directories are not listed, and nothing follows a link (the tree has none). A file's name in the archive is its path relative to `<dir>`, with `/` between its parts.
2. **Sort.** Sort the files by that name, in byte order. The walk's own order is never used: it differs by OS and library (Go's `WalkDir` is lexical per directory, which is not full-path byte order, since `.` sorts before `/`).
3. **Read and digest.** In that order, read each file whole into memory, compute its SHA-256, append one manifest line `<sha256 in lowercase hex> <size in bytes, decimal> <name>\n`, and append the file's bytes to one payload buffer.
4. **Compress.** Compress the payload as one raw DEFLATE stream (RFC 1951) with the library's default level.
5. **Sign.** Sign the manifest's bytes with Ed25519 under the key made from the fixed seed (Format).
6. **Seal and write.** Build the body (Format), seal it with AES-256-GCM under the fixed key and nonce, with the 8-byte magic as the associated data, and write the magic and the sealed bytes to `<out>`. No program syncs the file. Then print `archive: <files> files, <bytes> bytes`.

`open`:

1. Read the archive whole, check the magic, and open the seal (a wrong tag fails).
2. Read the manifest's length, the manifest and the signature, and verify the signature with the fixed key's public half.
3. Read every manifest line, and inflate the payload, which must hold exactly the manifest's total size: a short or a longer payload fails.
4. Check every file's SHA-256 against its line, in order.
5. Print the manifest exactly as stored, then `ok: <files> files, <bytes> bytes`, through a buffered stdout.

Any failure exits nonzero. `open` is specified, and compared, only on archives the row's `create` writes. On other well-sealed and signed archives the columns may differ, and the row does not align them (probes of 2026-11-16): mk alone refuses bytes after the final DEFLATE block, the Zig cells alone accept a size written with `_` and a manifest with no final line feed, and rust alone refuses a digest in uppercase hex. Where columns accept, they print the same. Each program holds the payload, the compressed bytes and the sealed archive in memory at once, as each one-shot AEAD interface (Go's `cipher.AEAD`, ring's `seal_in_place_append_tag`, Zig's `encrypt`, Mk's `AesGcm.Seal`) needs the whole plaintext.

**Format.** Little-endian, the same in every language, so any column's `open` reads any column's archive:

```
file      = magic (8 bytes: "MKARCH01") || sealed
sealed    = AES-256-GCM(key, nonce, aad = magic, body), the ciphertext then the 16-byte tag
body      = u64 manifest length || manifest || Ed25519 signature of the manifest (64 bytes) || DEFLATE payload (to the end)
manifest  = one line per file, in step 2's order: "<64 hex digits> <size> <name>\n"
```

The fixed test material: the AES-256 key is the bytes 0 to 31, the nonce the bytes 160 to 171, and the Ed25519 seed the bytes 64 to 95. A fixed nonce under a fixed key is a benchmark fixture, safe only because every archive the row writes is of the same tree with the same key; a real tool draws a fresh nonce and a real key.

**Checksum.** `"digest": true` with output check `then`. The archive's bytes legitimately differ by library, since four DEFLATE encoders make four streams, so they are never compared. After each checked `create`, tools/bench runs `archive open <out>` on the column's own archive, untimed, and the checksum is the SHA-256 of that run's whole stdout: the manifest, which holds only names, sizes and SHA-256 digests, and the summary line. No compressed size appears in it. The archive's size is printed beside each cell.

**Every column opens every column's archive.** That alone would show only that each column agrees with itself: a `create` and an `open` wrong in the same way would pass. So `verify` also runs each column's `open` on every other column's archive, untimed, and each of those runs must print the checksum too (tests/bench/README.md "Output files"): 30 more runs for the six columns, at whichever size `verify` checks, counted in one line of its output. A failed pair is laid on the column that the other languages' readers point to, each language counted once, so the two Zig cells, which build one source, are one vote; with no other language to decide, both columns are marked.

**Every column refuses a broken archive.** The row's tool `testdata/tamper.go` (no column's program; `go run tamper.go <in.mka> <out.mka> <mode> [<n>]`, its comment lists the modes) writes broken copies of an archive, re-signed and resealed so that only the step a mode names fails: a ciphertext byte flipped (`seal`), a manifest byte changed (`sig`), a payload byte changed (`data`) or added (`long`), a body too short for a length and a signature (`short`), a manifest length with its top bit set (`mtop`), sizes that are negative (`neg`), wrap a u64 (`wrap`) or add up past the i64 range (`i64sum`), one size of 2^62 (`huge`), and sizes that add up to `<n>` (`total`). By hand, every column's `open` refused every such copy of the four programs' quick archives, with `total` at 2^62 and 2^45, with an error of its own and exit 1: 288 of 288 runs, each under a 10 s timeout (2026-11-16). tools/bench's TestAppArchiveRefusals runs the modes on the Go twin. Go does not allocate the manifest's total before it inflates: it reads the payload through `io.ReadAll` limited to one byte past the total, as Mk's `compress.Inflate` takes the total as its limit. Rust (`try_reserve_exact`) and the Zig cells (`alloc`) still reserve the total first, which fails at once, with exit 1, for a total the machine cannot commit (2^45, 2^62); a total it can commit is reserved and then refused when the payload comes up short (read from the sources, not run).

**Cells.**

| Column | Kind | Walk | Read | SHA-256 | DEFLATE | Ed25519 | AES-256-GCM |
|---|---|---|---|---|---|---|---|
| mk | std | `fs.Walk` | `fs.ReadBytes` into an arena reset per file, `HeapList.Extend` | `hash.Sha256` | `compress.Deflate` (std's one level), `compress.Inflate` with the total as its limit | `crypto.Ed25519.FromSeed`, `Sign`, `Verify` | `crypto.AesGcm.New`, `Seal`, `Open`; written by `fs.WriteAll` |
| rust, rust-ovf | eco | walkdir `WalkDir` | `std::fs::read`, `extend_from_slice` | sha2 `Sha256::digest` | flate2 `DeflateEncoder` at `Compression::default()` (6), `DeflateDecoder` | ring `Ed25519KeyPair::from_seed_unchecked`, `UnparsedPublicKey` | ring `LessSafeKey` over `AES_256_GCM`, in place |
| zig-fast, zig-safe | std | `std.Io.Dir.walk` | `Dir.readFileAlloc`, `ArrayList.appendSlice` | `std.crypto.hash.sha2.Sha256` | `std.compress.flate.Compress` at `Options.default` (`level_6`), `flate.Decompress` | `std.crypto.sign.Ed25519.KeyPair.generateDeterministic` | `std.crypto.aead.aes_gcm.Aes256Gcm` |
| go | std | `filepath.WalkDir` | `os.ReadFile`, `append` | `crypto/sha256` | `compress/flate` at `DefaultCompression` (6), `flate.NewReader` read by `io.ReadAll` with the total as its limit | `crypto/ed25519` | `crypto/aes`, `cipher.NewGCM` |
| c | skip | | | | | | no std hash, compression or cryptography in C, and no C crypto library is vendored (zlib alone would leave a partial task) |
| cs, cs-aot | skip | | | | | | no std Ed25519 in .NET BCL |

- **Rust's libraries** (`rs/Cargo.toml`, pinned by `rs/Cargo.lock`; cargo reads them from Mk-evidence's `crates/flate2-1.1.1-ring-0.17.14-sha2-0.10.9-walkdir-2.5.0`, a `cargo vendor` tree of the lockfile's 36 registry packages that tools/bench checks against each package's crates.io archive before every build, bench.json's `crates`; nine of them, wasi 0.11.1, windows-sys 0.52.0 and seven windows_* 0.52.6 import-library crates, each for a target the x86_64-pc-windows-msvc build does not read, were fetched separately, its SOURCE.txt says): sha2 0.10.9, flate2 1.1.1 over miniz_oxide 0.8.8, ring 0.17.14 and walkdir 2.5.0. bench.json's `libs` says what each holds; ring is C and assembly as much as Rust. The cell is `eco (not classed)`.
- **DEFLATE effort.** Each column uses its library's default level. The reference gives no level for Mk's one-shot `compress.Deflate` (doc/std/compress.md: "There is one compression level"). The archives that a publish-size `verify` run wrote (2026-11-15) differ by 1.2 percent at most: mk 47,932,137 bytes, rust 47,997,609, go 48,004,025 and zig 48,490,733, for a payload of 100,005,475 bytes. So no column buys its speed with a much weaker compression.

**Hardware paths.** The row's target is `x86-64-v2` in every column, and that level has no AES, PCLMULQDQ or SHA instructions, so each library either chooses them at run time or does without. They were counted on 2026-11-15 in the executables that `bench verify` built with that day's compiler and std, with `llvm-objdump -d --no-show-raw-insn` (the counts are of instructions in the binary, not of instructions run):

| Column | `aesenc` | `vaesenc` | `pclmulqdq` | `vpclmulqdq` | `sha256rnds2` | `cpuid` | Path |
|---|---|---|---|---|---|---|---|
| mk | 555 | 0 | 24 | 0 | 32 | 6 | AES-NI, PCLMULQDQ and SHA chosen at run time inside std (std's kernels behind `simd.Cpu()`) |
| rust | 193 | 206 | 48 | 250 | 32 | 14 | ring: AES-NI with PCLMULQDQ, and a VAES and VPCLMULQDQ AVX2 form, chosen at run time from CPUID; sha2: SHA chosen at run time by cpufeatures |
| zig-fast, zig-safe | 0 | 0 | 0 | 0 | 0 | 0 | software AES, GHASH and SHA-256: Zig 0.17 chooses them at compile time from the target (`std/crypto/aes.zig` needs `aes` and `avx`, `ghash_polyval.zig` `pclmul` and `avx`, `sha2.zig` `sha` and `avx2`), and x86-64-v2 has none |
| go | 696 | 0 | 139 | 0 | 32 | 7 | AES-NI, PCLMULQDQ and SHA chosen at run time by the runtime's CPU detection |

The machine's CPU (an AMD Ryzen 7 9800X3D, `znver5` to `zig build-exe -mcpu=native`) has all of them, VAES and VPCLMULQDQ included. So the Zig cells do the same work without the crypto instructions the others use, and rust may run a wider AES-GCM than mk and go; which form each run took was not traced. A Zig cell built for those features would need a target that tools/bench does not offer: its `cpu` levels are v2 and v3, and v3 has none of them either. The smallest target that turns Zig's hardware paths on is x86-64-v3 with `aes`, `pclmul` and `sha` (2026-11-16: `zig build-exe -O ReleaseFast -mcpu=x86_64_v3+aes+pclmul+sha` gave 260 `vaesenc`, 178 `vpclmulqdq` and 32 `sha256rnds2`, and `x86_64_v2+aes+pclmul+sha` none). How much of each column's time AES-GCM and SHA-256 take was not measured. bench.json's `labels` prints this beside the zig-fast and zig-safe cells.

**Input.** `tools/bench gen` kind `tree`, seed 36, 100,000,000 bytes (`tools/bench/gen_app.go` `genTree`): 18,507 files holding 100,005,475 bytes in 2,967 directories (the walk, the open summary and `find`, read on 2026-11-15), with hidden names, empty and binary files, CR LF files and names starting with a capital letter.

**Sizes.** Quick `create {data} {out} 500` (the first 500 files of step 2's order: 2,672,582 bytes), publish `create {data} {out}` (all of them). Overflow class `never`: sizes and offsets are far below every column's integer range. The size is provisional: in a publish-size `verify` run on a shared machine (2026-11-15), every cell's checked `create` took over the 1 s floor; the publish window confirms it with `verify -publish-size`, as every app row's. `open` is not timed for now: the row times `create`, and tools/bench deletes the output before every timed run, so a timed `open` would need the column's archive kept between runs, which it does not do yet.
