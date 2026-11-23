# strbuild (idiomatic)

**Task.** Repeatedly assemble structured ASCII text in each language's standard growable string builder, growing from empty on every document without pre-sizing. `strbuild <docs> <lines>`: for each document `d` in `0..docs`, start an empty builder and append `lines` records (`i` in `0..lines`, `x = d * 131 + i * 17`), each combining string literals (`"rec["`, `"] "`, `":"`, `" #"`, `"\n"`), decimal formatted integers (`i` and `x`), a 16-word lookup slice, a variable-length slice (`8..32` bytes) of a static ASCII sentence, and `1..4` repetitions of `"-="`. After building the document, compute FNV-1a 64 over its bytes plus its byte length and add (wrapping) to the total checksum.

**Cells.**

| Column | Builder | Integer formatting |
|---|---|---|
| mk | `Text.New(&a)` in one scratch arena reset per document | `Print` into `Text.Writer()` |
| rust, rust-ovf | `String::new()` | `std::fmt::Write` (`write!`) |
| zig-fast, zig-safe | `std.ArrayList(u8)` over `std.heap.smp_allocator` | `std.fmt.bufPrint` + `appendSlice` |
| go | `strings.Builder` | `strconv.AppendInt` into stack buffer + `Write` |
| cs, cs-aot | `System.Text.StringBuilder` (`GetChunks()` for zero-copy scan) | `StringBuilder.Append(int)` |
| c | no std growable string builder in C | |

Checksums: quick `2000 200` `15236428992153487888`, publish `100000 200` `4970838943839601395`. Overflow class `wraps`.
