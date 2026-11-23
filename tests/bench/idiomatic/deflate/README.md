# deflate (idiomatic)

**Task.** Compress a generated mixed text buffer into memory with each language's standard raw DEFLATE (RFC 1951) encoder at its default level, then decompress it back into memory and verify byte-exact length and FNV-1a 64 checksum against the source buffer. `deflate <bytes> <rounds> <seed>`: generate a deterministic `bytes`-byte mixed structured log and prose buffer from SplitMix64 (`seed`). Each round computes `want = fnv64(src)`, compresses `src` into memory, decompresses back, checks `dec.len == bytes` and `fnv64(dec) == want` (failing non-zero on mismatch), folds `want +% bytes` into `total`, and flips `src[(r * 1009) % bytes] ^= 0x01` so no round can be hoisted.

**Cells.**

| Column | Compressor & Decompressor | Default level |
|---|---|---|
| mk | `std.compress.Deflate` + `std.compress.Inflate` in one scratch arena reset per round | Mk's single level (Go 1.27 level-6 matcher) |
| zig-fast, zig-safe | `std.compress.flate.Compress` (`.raw`, `.default`) + `flate.Decompress` (`.raw`), buffers reused | `.default` (level 6) |
| go | `compress/flate.NewWriter` + `flate.NewReader` (`flate.Resetter`), buffers reused | `flate.DefaultCompression` (level 6) |
| cs, cs-aot | `System.IO.Compression.DeflateStream` over reusable `MemoryStream` and `byte[]` | `CompressionLevel.Optimal` |
| c | no std deflate/gzip in C | |
| rust | no std deflate/gzip in Rust (`flate2` is a crate) | |

Checksums: quick `262144 4 1` `10654187985128517874`, publish `4194304 52 1` `14370483003890191482`. Overflow class `wraps`.
