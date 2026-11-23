# app/zlib-c: compress a file into a zlib stream

An `app` row (tests/bench/README.md "Applications"). Its decompressing pair is `app/zlib-d`: a compress and decompress pair is two rows, because a variant has no data file of its own and the round trip compares with the row's data file. Every twin below is checked against this file before its number counts.

## The task

- **Command line:** `zlibc IN OUT`. tools/bench runs it as `zlibc {data} {out}`, with `{out}` the column's own output file under the work directory, deleted before every run.
- **Input:** `tests/bench/data/wordfreq-50mb.txt`, from `tools/bench gen` kind `words` (seed 3, bytes 50,000,000; the generator writes whole words until it reaches the size, then a newline, here 50,000,001 bytes), the wordfreq row's text and the same file. The same bytes for every language. The size is provisional: `verify -publish-size` on a quiet machine fixes it.
- **The work, every step:**
  1. open `IN` for reading and create `OUT`;
  2. read `IN` in chunks of 64 KiB (a 64 KiB read buffer);
  3. give each chunk to the language's zlib compressor (RFC 1950 framing: the two-byte header, DEFLATE blocks, the Adler-32 trailer) at **level 6**, in one stream;
  4. write the compressed bytes to `OUT` through a buffer of 64 KiB (or a fixed 64 KiB output buffer, as zlib's C API has);
  5. at the end of the input, finish the stream (the final block and the trailer), flush and close `OUT`;
  6. print the count of input bytes, in decimal, and a newline.
- **Output:** the line on standard output, and the file `OUT`.
- **Level 6** is zlib's default and every library's level 6: zlib's `deflateInit(&s, 6)`, flate2's `Compression::new(6)` (its default), Go's `zlib.NewWriterLevel(w, 6)` (`DefaultCompression`, -1, selects level 6), Zig's `Compress.Options.level_6` (`Options.default`, whose parameters Compress.zig takes from zlib 1.3.1's `deflate.c`), and Mk's `compress.Encoder.New(a, compress.Framing.Zlib, 6)`, which takes the level explicitly. Each library's level 6 is its own trade between speed and size, so the compressed size, which tools/bench prints beside each cell, is part of the result: a faster cell that compresses less has not done the same work as well.

## The check

`"digest": true` with `output.check` `zlib`: the checksum is the SHA-256 of the standard output (CR LF read as LF) and the round trip: Go's `compress/zlib` decodes each column's `OUT`, the result must equal the input file byte for byte, no byte may follow the stream's Adler-32 trailer, and the checksum gains its SHA-256. Compressed bytes differ by library, so they are never compared.

## The cells

| Language | Kind | Program | Libraries |
|---|---|---|---|
| Mk | `std` | `mk/zlibc.mk`: `fs.File.Read` into a 64 KiB arena slice, `compress.Encoder` (zlib framing, level 6) writing through `File.Writer` with a 64 KiB buffer, `Finish`, `Flush` | std only |
| C | `eco` | `zlibc.c`: `fread` 64 KiB chunks, `deflate` with `Z_NO_FLUSH` and `Z_FINISH` into a 64 KiB buffer, `fwrite` | zlib 1.3.1 from Mk-evidence/vendor, its eight C files built on the C column's line; portable C, no assembly or intrinsics |
| Rust | `eco` | `rs/` (a crate): `File::read` 64 KiB chunks, `flate2::write::ZlibEncoder` over a 64 KiB `BufWriter`, `finish` | flate2 1.1.1 with its default `rust_backend`: miniz_oxide 0.8.8 and adler2 2.0.0 (both `#![forbid(unsafe_code)]`), crc32fast 1.4.2 (linked; unsafe SIMD for gzip's CRC-32, which the zlib framing does not run). The crates come from a `cargo vendor` tree in Mk-evidence (`crates/flate2-1.1.1`) by directory source replacement |
| Zig | `std` | `zlibc.zig`: a streaming `File.Reader` with a 64 KiB buffer streamed into `std.compress.flate.Compress` (`.zlib`, `.level_6`), writing through a streaming `File.Writer` with a 64 KiB buffer, `finish`, `flush` | std only |
| Go | `std` | `testdata/zlibc.go`: `File.Read` 64 KiB chunks into `zlib.NewWriterLevel(w, 6)` over a 64 KiB `bufio.Writer`, `Close`, `Flush` | std only |
| C# | `std` | `zlibc.cs`: `FileStream.Read` 64 KiB chunks into `ZLibStream` (`ZLibCompressionOptions.CompressionLevel = 6`) over a 64 KiB buffered output `FileStream`, dispose, `Flush` | std only |

- **Environment:** `MK_CARRIERS=1` (the row is single-threaded); `publish`'s one-CPU affinity.
- **Columns:** `mk`, `c`, `rust`, `rust-ovf`, `zig-fast`, `zig-safe`, `go`. The `eco` cells are printed "eco (not classed)": their libraries' code is not made checked by the column's flags.
- **Reported:** each cell's time, with its ratios, and its compressed size; there is no MB/s column. tools/bench refuses `bytes` on an `app` row (`checkApp` in tools/bench/app.go: GB/s and the fill subtraction are the SIMD table's), so the row reports time only, as every app row does. Throughput is the input size over the time, and the input size is the same for every cell, so it would rank the cells exactly as time does.
- **The defect line:** the Mk cell against the fastest `std` cell of the other languages (Zig and Go here). The function a profile of the Mk cell names is the std function the line is about (std.compress's encoder, or fs).
- **Bans:** every program's own source keeps the other tables' bans (`appRowViolations`): no unsafe in Rust, no hints, no `@setRuntimeSafety(false)`, no fast-math, no `std.simd` vector code or unsafe module in Mk.
