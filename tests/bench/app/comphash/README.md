# app/comphash: compress-then-hash pipeline

An `app` row (tests/bench/README.md "Applications"). A two-pass streaming storage pipeline combining RFC 1950 zlib compression/decompression with FIPS 180-4 SHA-256 integrity checking: read an input file in 64 KiB chunks while streaming its SHA-256 and compressing it to `<out>` with zlib at level 6, then read `<out>` back, decompress it with full header and Adler-32 validation while computing a second SHA-256, verify length and digest match the input, and print `<bytes> <sha256>`.

## Command line

```
comphash <in> <out>
```

Prints one line to stdout:

```
<input_bytes> <lowercase_64_hex_sha256>
```

Exits nonzero if `<out>` fails RFC 1950 header validation (FCHECK/FDICT), is truncated, fails Adler-32 trailer validation, or decompresses to a different byte count or SHA-256 than `<in>`.

## Input

`tests/bench/data/app-comphash-256mb.bin`, from `tools/bench gen` kind `mixed` (seed 23, bytes 268,435,456: 64 KiB blocks rotating among lowercase ASCII text, structured little-endian binary records with zero runs, pseudorandom bytes, and byte runs). Because the file mixes compressible, match-heavy, and incompressible blocks, both stored and compressed DEFLATE paths and streaming SHA-256 are exercised in one run. Sized so the fastest cell clears the 1 s floor.

## The work, every step

1. **Pass 1 (compress + hash):** open `<in>` and read sequentially in 64 KiB chunks. Feed every chunk into a streaming SHA-256 hasher and into a zlib encoder (RFC 1950 framing, compression level 6) writing through a 64 KiB buffered writer to `<out>`. Finish the zlib stream (writing the final block and Adler-32 trailer), flush and close `<out>`, close `<in>`, and finalize the input SHA-256.
2. **Pass 2 (decompress + verify):** open `<out>` through a 64 KiB buffered reader and inflate the RFC 1950 zlib stream into 64 KiB chunks, checking the header's FCHECK and rejecting FDICT, checking stream termination (no silent truncation), and verifying the Adler-32 trailer. Feed every inflated chunk into a second streaming SHA-256 hasher and count total inflated bytes.
3. **Cross-check & report:** verify Pass 2's byte count and SHA-256 equal Pass 1's (failing with nonzero exit otherwise), and print `<input_bytes> <hex_sha256>\n`.

## The check

- `"digest": true`: stdout's SHA-256 must match across all cells (exact input byte count and input SHA-256).
- `"output": {"file": "out.zlib", "check": "zlib"}`: `tools/bench verify` independently decompresses each cell's `<out>` file through Go's `compress/zlib` and checks that its decompressed bytes hash to the exact SHA-256 of `<in>`. Compressed byte streams differ by DEFLATE encoder and are not compared byte-for-byte.

## Hardware paths

At the row's `x86-64-v2` target:
- **Mk** (`std.hash.Sha256State`), **Rust** (`sha2 0.10.9` with `cpufeatures`), **Go** (`crypto/sha256`), and **C#** (`System.Security.Cryptography.IncrementalHash` / OS crypto) dispatch SHA instructions at run time when the CPU supports them.
- **Zig** (`std.crypto.hash.sha2.Sha256`) chooses SHA instructions at compile time from the target CPU features; `x86-64-v2` has none, so `zig-fast` and `zig-safe` run Zig's software SHA-256 (marked in `bench.json` `labels.zig`).

## Cells

| Language | Kind | Program | Libraries |
|---|---|---|---|
| Mk | `std` | `mk/comphash.mk`: `std.compress.Encoder` (zlib, level 6), `std.compress.Inflater.NewZlib`, `std.hash.Sha256State`, 64 KiB buffers | std only |
| C | `skip` | skipped: no std zlib or SHA-256 in C (no C SHA-256 library is vendored) | — |
| Rust | `eco` | `rs/src/main.rs`: `flate2::write::ZlibEncoder` (level 6, `miniz_oxide`), `flate2::Decompress` (explicit `Status::StreamEnd`), `sha2::Sha256`, 64 KiB buffers | `flate2 1.1.1`, `sha2 0.10.9` (`ring 0.17.14` and `walkdir 2.5.0` are present in `Cargo.lock` only to match the shared vendored crate tree and are unused) |
| Zig | `std` | `comphash.zig`: `std.compress.flate.Compress` (`.zlib`, `.level_6`), `std.compress.flate.Decompress` + RFC 1950 FCHECK/FDICT and `flate.Container.Hasher` Adler-32 check, `std.crypto.hash.sha2.Sha256` | std only |
| Go | `std` | `testdata/comphash.go`: `compress/zlib` (level 6), `crypto/sha256`, 64 KiB `bufio` | std only |
| C# | `std` | `comphash.cs`: `System.IO.Compression.ZLibStream` (level 6) + `StrictEofStream` on decompress, `IncrementalHash(SHA256)`, 64 KiB buffers | std only |

- **Overflow class:** `never`.
- **Environment:** `MK_CARRIERS=1` (single-threaded); `publish`'s one-CPU affinity.
