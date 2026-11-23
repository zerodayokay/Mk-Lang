# app/zlib-d: decompress a zlib stream into a file

An `app` row (tests/bench/README.md "Applications"). Its compressing pair is `app/zlib-c`: a compress and decompress pair is two rows, because a variant has no data file of its own. Every twin below is checked against this file before its number counts.

## The task

- **Command line:** `zlibd IN OUT`. tools/bench runs it as `zlibd {data} {out}`, with `{out}` the column's own output file under the work directory, deleted before every run.
- **Input:** `tests/bench/data/zlib-words-200mb.zz`, from `tools/bench gen` kind `zlib` of `words` (seed 3, bytes 200,000,000: whole words up to that size, then a newline), compressed once by Go's `compress/zlib` at level 6. Every language reads the same compressed bytes. The size is provisional: `verify -publish-size` on a quiet machine fixes it.
- **The work, every step:**
  1. open `IN` for reading and create `OUT`;
  2. read `IN` through a 64 KiB buffer;
  3. inflate it with the language's zlib decompressor (RFC 1950: the header, the DEFLATE blocks), into a 64 KiB output buffer; **check the header** as RFC 1950 2.3 requires of a compliant decompressor: CM 8, CINFO at most 7, FCHECK (the two bytes a multiple of 31), and no preset dictionary (FDICT clear, since the task has none); a bad header is an error;
  4. write each output buffer to `OUT`;
  5. **check the stream's end:** the final block must arrive before the input ends (a truncated stream is an error), and the trailer's Adler-32 must equal the Adler-32 of the decompressed bytes (a mismatch is an error);
  6. close `OUT`, and print the count of decompressed bytes, in decimal, and a newline.
- **Output:** the line on standard output, and the file `OUT`. A failed check exits non-zero.

## The check

`"digest": true` with `output.check` `digest`: the checksum is the SHA-256 of the standard output (CR LF read as LF) and the SHA-256 of `OUT`, which is the plain text, the same bytes in every column.

Step 5 was checked by hand on each built column (2026-11-15): a 1 MiB stream made by Go, the same stream with its last trailer byte flipped, and the same stream with its last 100 bytes cut. Every column wrote the plain bytes for the first and exited 1 on the other two. The header check of step 3 was checked (2026-11-15) on two more streams: `badheader` (`78 9d`, a wrong FCHECK) and `fdict` (`78 bb`, a valid FCHECK with FDICT set). Mk, C, Rust and Go exited 1 on both, and the Zig cells exited 0 with the plain bytes, because Zig 0.17's `Decompress` checks CM and CINFO only; the Zig program now checks the two bytes itself, and after that change both Zig columns exit 1 on both (2026-11-16). flate2's `Read` adapters returned end of input on the truncated stream with no error, so the Rust program drives `flate2::Decompress` and requires `Status::StreamEnd`, as the C program requires `Z_STREAM_END`.

## The cells

| Language | Kind | Program | Libraries |
|---|---|---|---|
| Mk | `std` | `mk/zlibd.mk`: `compress.Inflater.NewZlib` reading from `fs.File.Reader` (64 KiB buffer) into a 64 KiB arena slice, `File.WriteAll` per buffer; the Inflater checks the trailer (`Corrupt`) and the end (`Truncated`) | std only |
| C | `eco` | `zlibd.c`: `fread` 64 KiB chunks, `inflate` into a 64 KiB buffer until `Z_STREAM_END` (zlib checks the Adler-32: `Z_DATA_ERROR`), `fwrite` | zlib 1.3.1 from Mk-evidence/vendor, its eight C files built on the C column's line; portable C, no assembly or intrinsics |
| Rust | `eco` | `rs/` (a crate): a 64 KiB `BufReader`, `flate2::Decompress::new(true)` into a 64 KiB buffer until `Status::StreamEnd` (miniz_oxide checks the Adler-32), `write_all` | flate2 1.1.1 with its default `rust_backend`: miniz_oxide 0.8.8 and adler2 2.0.0 (both `#![forbid(unsafe_code)]`), crc32fast 1.4.2 (linked; unsafe SIMD for gzip's CRC-32, not run here). The crates come from a `cargo vendor` tree in Mk-evidence (`crates/flate2-1.1.1`) by directory source replacement |
| Zig | `std` | `zlibd.zig`: `std.compress.flate.Decompress` (`.zlib`) over a streaming `File.Reader` with a 64 KiB buffer, `readSliceShort` into 64 KiB, written through a streaming `File.Writer`. Zig 0.17's `Decompress` reads the trailer without comparing it, so the program checks it with std's own `flate.Container.Hasher` (`std.hash.Adler32`), as the gzip row's Zig program does; it checks CM and CINFO of the header only, so the program peeks the two header bytes first and refuses a wrong FCHECK or a set FDICT (`error.BadZlibHeader`) | std only |
| Go | `std` | `testdata/zlibd.go`: `zlib.NewReader` over a 64 KiB `bufio.Reader` (it checks the Adler-32: `zlib.ErrChecksum`), 64 KiB reads, `File.Write` | std only |
| C# | `std` | `zlibd.cs`: `ZLibStream` (`CompressionMode.Decompress`) over a 64 KiB buffered `FileStream` (checks header, termination and Adler-32 trailer), 64 KiB reads into a 64 KiB buffered output `FileStream` | std only |

- **Environment:** `MK_CARRIERS=1` (the row is single-threaded); `publish`'s one-CPU affinity.
- **Columns:** `mk`, `c`, `rust`, `rust-ovf`, `zig-fast`, `zig-safe`, `go`. The `eco` cells are printed "eco (not classed)".
- **Reported:** each cell's time, with its ratios; there is no MB/s column. tools/bench refuses `bytes` on an `app` row (`checkApp` in tools/bench/app.go: GB/s and the fill subtraction are the SIMD table's), so the row reports time only, as every app row does. Throughput is the output size over the time, and the output is the same bytes for every cell, so it would rank the cells exactly as time does.
- **The defect line:** the Mk cell against the fastest `std` cell of the other languages (Zig and Go here).
- **Bans:** every program's own source keeps the other tables' bans (`appRowViolations`).
