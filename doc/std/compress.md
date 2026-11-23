# std.compress

`std.compress` compresses and decompresses DEFLATE (RFC 1951), gzip (RFC 1952) and zlib (RFC 1950), and decompresses Zstandard (RFC 8878). Use the one-call functions when the whole input is in memory, and the streaming types when data arrives in pieces from a file or a socket. Every decoder holds bounded memory and stack, whatever its input. `Unzlib` and the Zstandard decoder never panic on their input, and every decoder stops at a limit you give it, so they are safe on untrusted data.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.compress
```

## Contents

- [The main pieces](#the-main-pieces)
- [Limits and errors](#limits-and-errors)
- [Streaming](#streaming)
- [Zstandard](#zstandard)
- [HTTP content codings](#http-content-codings)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [round trip in memory](#round-trip-in-memory), [handle bad input](#handle-bad-input), [compress and read a stream](#compress-and-read-a-stream), [decode Zstandard](#decode-zstandard)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Compress a buffer | `Deflate` (raw), `Gzip`, `Zlib` |
| Decompress a buffer | `Inflate` (raw), `Gunzip`, `Unzlib` |
| Compress a stream | `Encoder.New` with a `Framing`, then `Write`, `Flush`, `Finish` |
| Decompress a stream | `Inflater.New` (raw), `Inflater.NewGzip`, `Inflater.NewZlib`, then `Read` or `Reader` |
| Decompress Zstandard | `ZstdDecoder.New` or `ZstdDecoder.NewLazy`, then `Read`, `Reader` or `ReaderLazy` |
| Checksum zlib data | `Adler32`, or `Adler32State` piece by piece |
| Decode an HTTP body | `GzipContent`, `DeflateContent`, `ZstdContent` |

There is one compression level. `Encoder.New` takes a level, but level 0 means stored blocks only and every other level compresses as `Deflate` does. No block is larger than its stored form. So `Deflate` of `n` bytes writes at most `n + n / 2048 + 64` bytes; `Gzip` adds 18 bytes of header and trailer, `Zlib` 6. There is no Zstandard or Brotli encoder.

## Limits and errors

Every decoder of a whole buffer takes a `limit`: the most bytes of output it may produce. A negative limit acts as 0. Pick a limit from what your program can hold; a few hundred bytes of input can claim gigabytes of output.

`compress.Error` has four members:

| Member | Meaning |
|---|---|
| `Corrupt(offset)` | The input is not a valid stream; `offset` is the input byte where the first bad element starts |
| `Truncated` | The input ends inside the stream |
| `TooLarge` | The output would pass the limit, or a Zstandard window would pass the window limit |
| `OutOfMemory` | The arena could not give the output or the decoder's buffers |

- `Inflate` and `Unzlib` take all of `data` as one stream, so any byte after the end of the stream is `Corrupt`.
- `Gunzip` reads gzip members back to back, as `gzip -d` does with concatenated files. A byte after a member that does not start another one is `Corrupt`.
- For DEFLATE, `Corrupt`'s offset is the byte that holds the first bit that cannot be decoded.
- gzip trailers (CRC-32 and length) and zlib trailers (Adler-32) are checked; a mismatch is `Corrupt`.
- The one-call compressors (`Deflate`, `Gzip`, `Zlib`) fail only with `OutOfMemory`.

`compress.Error` is open: it may gain members ([errors](README.md#5-errors)).

## Streaming

An `Inflater` decodes one stream from a `Reader`. `Read(r, buf)` writes at most `buf.Len()` bytes and returns their count, 0 at the end. It takes no `Io`, because `r` carries one.

- Bytes decoded before an error are returned first; the error then repeats on every later call.
- A `ReadError` from `r` is passed through, and `Read` may be called again: nothing was lost.
- `Inflater.Reader(r, buf)` wraps the decoder as a `Reader`, for `Line`, `Until` and the rest. A failure there is a plain `ReadError`, and every later read fails too, so a stream never resumes past a failure. `Inflater.Cause` says which `compress.Error` ended it, once you stop using the `Reader`.
- The `Inflater` treats no byte past the end of its stream as data, though it may have read some of them into its buffer.

An `Encoder` compresses to a `Writer`. It is linear: a path through your code that ends without `Finish` or `Abandon` does not compile, so a stream is never cut short in silence.

- `Write` compresses and writes each 64 KiB of input as it fills; the rest waits.
- `Flush` is a sync flush: a reader of the output can decode everything written so far. Each one costs a few bytes.
- `Finish` writes the final block and the trailer and consumes the encoder. It does not flush the `Writer`.
- After a failed `Write`, `Flush` or `Finish`, an unknown part of the stream reached the `Writer`. Every later call fails; end the encoder with `Abandon`, which writes nothing.

`Adler32State` streams `Adler32` as `hash.Crc32State` streams `hash.Crc32` ([hash](hash.md)): feed it the input in any split and `Sum` gives the one-shot value. `Sum` takes the state by value, so the state keeps absorbing. A zero-filled `Adler32State` is the empty input's state.

## Zstandard

`ZstdDecoder` reads Zstandard frames back to back, skippable frames among them, and checks each frame's content size and content checksum. Frames that need a dictionary are `Corrupt`: dictionaries are not supported. A match offset past the window plus the block's output so far is `Corrupt`.

A Zstandard frame names the window it needs, and the decoder must hold that much history. `window_limit` caps it; 0 or less means 8 MiB, the limit RFC 8878 asks of an HTTP client. A frame over the limit fails with `TooLarge`; a `NewLazy` decoder fails before it takes that frame's window.

| Constructor | Window | Reader |
|---|---|---|
| `ZstdDecoder.New(a, limit)` | `limit + 256 KiB`, taken from `a` when the decoder is made | `Reader` or `ReaderLazy` |
| `ZstdDecoder.NewLazy(a, limit)` | Sized to each frame at its header, so a small frame costs a small window; over a stream, less than three times the largest window | `ReaderLazy` only; `Reader` panics |

With `NewLazy`, a window the arena cannot give is `OutOfMemory` from `Read`, and the next `Read` tries again.

## HTTP content codings

A `Content` pairs a token of `Accept-Encoding` and `Content-Encoding` with a function that wraps a body's `Reader` in a decoder. `GzipContent` (`gzip`), `DeflateContent` (`deflate`) and `ZstdContent` (`zstd`) are the values to list in an HTTP client's decoders ([http](http.md)). The `deflate` coding is a zlib stream, so a raw DEFLATE body is `Corrupt` at its first byte. `ZstdContent` decodes through `NewLazy` with the 8 MiB limit, so there a window the arena cannot give is the reader's `ReadError`.

## Memory and cost

Everything std.compress allocates comes from the `&Arena` you pass; nothing touches the heap behind your back.

- The one-call decoders return a slice that lives in `a` (`from a`). They start with a buffer of four times the input and double it as needed, leaving outgrown buffers in the arena. `Gunzip` sizes its buffer from the last member's length field when that is plausible.
- The one-call compressors allocate the output bound in `a`, and 448 KiB of scratch in a sub-arena that they free before they return.
- An `Inflater` holds about 11.5 KiB inline, and a 98,570-byte window and a 16 KiB input buffer in its arena.
- An `Encoder` holds about 610 KiB in its arena; at level 0 about 162 KiB.
- A `ZstdDecoder` holds about 17 KiB inline, and in its arena a 132 KiB input buffer, a 128 KiB literal buffer and its window.
- The decoders' constructors (`Inflater.New`, `NewGzip`, `NewZlib`, `ZstdDecoder.New`, `NewLazy`) cannot fail: when the arena cannot give the buffers, the first `Read` fails with `OutOfMemory` and the next one tries again. `Encoder.New` fails with `OutOfMemory` at once.

Every operation is linear in its input plus its output. None of them blocks or takes an `Io` of its own, so none can be cancelled: cancellation reaches a stream through the `Reader` or `Writer` it uses.

## Examples

Each program runs as written with `mk run`.

### Round trip in memory

```mk
// verdict: ok
import std.compress

export Main(sys: &System): compress.Error|Utf8Error|WriteError!void {
    var t := Text.New(sys.arena)
    for _ in 0..40 {
        try t.Push("the same words, again and again\n")
    }
    text := t.Str()
    packed := try compress.Gzip(sys.arena, text.AsBytes())
    back := try compress.Gunzip(sys.arena, packed, 1 << 20) // at most 1 MiB of output
    same := try str.From(back) == text
    try sys.Print("{n} bytes, {m} as gzip, round trip equal: {same}\n", (n: text.Len(), m: packed.Len(), same))
}
```

```text
$ mk run roundtrip.mk
1280 bytes, 58 as gzip, round trip equal: true
```

- `Gzip` and `Gunzip` put their results in `sys.arena`; nothing needs freeing.
- `Deflate` and `Inflate`, `Zlib` and `Unzlib` work the same way for the other two formats.

### Handle bad input

```mk
// verdict: ok
import std.compress

// Decode unpacks a zlib stream and says what happened.
Decode(sys: &System, what: str, data: []u8, limit: int): WriteError!void {
    match compress.Unzlib(sys.arena, data, limit) {
        ok(out) { try sys.Print("{what}: {n} bytes\n", (what, n: out.Len())) }
        err(e) {
            match e {
                compress.Corrupt(offset) { try sys.Print("{what}: Corrupt at byte {offset}\n", (what, offset)) }
                compress.Truncated { try sys.Print("{what}: Truncated\n", (what)) }
                compress.TooLarge { try sys.Print("{what}: TooLarge\n", (what)) }
                _ { try sys.Print("{what}: {e}\n", (what, e)) } // OutOfMemory, or a member added later
            }
        }
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    data := try compress.Zlib(sys.arena, "a line of text\n".AsBytes())
    try Decode(sys, "whole", data, 1024)
    try Decode(sys, "limit 4", data, 4) // at most 4 bytes of output
    try Decode(sys, "cut short", data[0..data.Len() - 3], 1024)
    var bad := try sys.arena.Slice[u8](data.Len())
    (mut bad[..]).CopyFrom(data)
    bad[bad.Len() - 1] ^= 0xFF // spoil the Adler-32 trailer
    try Decode(sys, "bad checksum", bad[..], 1024)
}
```

```text
$ mk run bad.mk
whole: 15 bytes
limit 4: TooLarge
cut short: Truncated
bad checksum: Corrupt at byte 19
```

- The trailer's checksum starts at byte 19 of the 23-byte stream, so that is where `Corrupt` points.

### Compress and read a stream

```mk
// verdict: ok
import std.compress
import std.text

export Main(sys: &System): compress.Error|ReadError|OutOfMemory|WriteError!void {
    // Compress lines as they arrive, into a buffer in memory.
    var space: [4096]u8 = [0; 4096]
    var sink := text.Capped.New(mut space[..])
    var w := sink.Writer()
    var enc := try compress.Encoder.New(sys.arena, compress.Framing.Gzip, 6)
    onerr enc.Abandon() // runs only if a later line fails
    for line in ["first", "second", "third"] {
        try enc.Write(mut w, line.AsBytes())
        try enc.Write(mut w, "\n".AsBytes())
    }
    try enc.Finish(mut w) // the final block and the gzip trailer; consumes enc
    gz := sink.Bytes()
    try sys.Print("{n} bytes of gzip\n", (n: gz.Len()))

    // Read it back, line by line.
    var src := Reader.Of(gz)
    var z := compress.Inflater.NewGzip(sys.arena)
    var buf: [256]u8 = [0; 256]
    var lines := z.Reader(mut src, mut buf[..])
    for line := try lines.Line(sys.arena) {
        try sys.Print("read: {line}\n", (line))
    }
}
```

```text
$ mk run stream.mk
39 bytes of gzip
read: first
read: second
read: third
```

- With a file, the `Writer` is `File.Writer` and the `Reader` is `File.Reader` ([fs](fs.md)); flush the file's `Writer` after `Finish`.
- `onerr enc.Abandon()` ends the encoder if a `Write` fails, which the compiler requires of a linear value.

### Decode Zstandard

```mk
// verdict: ok
import std.compress

// A Zstandard frame of the 13 bytes "hello, zstd!\n", in one raw block.
const Frame: [22]u8 = [
    0x28, 0xB5, 0x2F, 0xFD, // magic number
    0x20, 13,               // frame header: one segment, content size 13
    0x69, 0x00, 0x00,       // block header: last block, raw, 13 bytes
    0x68, 0x65, 0x6C, 0x6C, 0x6F, 0x2C, 0x20, 0x7A, 0x73, 0x74, 0x64, 0x21, 0x0A,
]

export Main(sys: &System): compress.Error|ReadError|Utf8Error|OutOfMemory|WriteError!void {
    frame := Frame // a local copy, which src borrows
    var src := Reader.Of(frame[..])
    var z := compress.ZstdDecoder.NewLazy(sys.arena, 0) // 0: the 8 MiB window limit; the window is sized to the frame
    var out: [5]u8 = [0; 5]
    var t := Text.New(sys.arena)
    var calls := 0
    for {
        n := try z.Read(mut src, mut out[..]) // at most 5 bytes per call
        if n == 0 {
            break // r ended right after a frame
        }
        calls += 1
        try t.Push(try str.From(out[0..n]))
    }
    try sys.Print("{calls} reads: {text}", (calls, text: t.Str()))
}
```

```text
$ mk run zstd.mk
3 reads: hello, zstd!
```

- `Read` fills at most `buf.Len()` bytes per call, here 5, 5 and 3.
- For data from a file or a socket, wrap the source `Reader` the same way; for an HTTP body, list `ZstdContent` in the client's decoders instead.

## API

<!-- api:begin -->
Stability: Stable.

### `Adler32`

```
export Adler32(data: []u8): u32
```

The Adler-32 checksum of data, the checksum of the zlib format (RFC
1950); 1 for no data. O(n); allocates nothing.

### `Adler32State`

```
export struct Adler32State
```

An Adler-32 in progress over the bytes so far, which streams Adler32 as
hash.Crc32State streams hash.Crc32. It holds the checksum xor 1, so its
zero value is the state of the empty input, whose checksum is 1: a
zero-filled Adler32State is a fresh one, and the type is zero-valid.

### `Adler32State.Bytes`

```
export Adler32State.Bytes(mut self, data: []u8)
```

Absorbs data. Feeding the input in any split gives the Adler32 of the
whole. O(data.Len()); allocates nothing and copies nothing.

### `Adler32State.New`

```
export Adler32State.New(): Adler32State
```

The state of the empty input. O(1); allocates nothing.

### `Adler32State.Sum`

```
export Adler32State.Sum(self): u32
```

The Adler-32 of everything absorbed. It takes the state by value, so
the state can keep absorbing. O(1); allocates nothing.

### `Content`

```
export struct Content
```

An HTTP content coding: a name and a way to decode a body. name is the
coding's token in Accept-Encoding and Content-Encoding. wrap makes a
Reader of the decoded body over the body's Reader src, with buf as its
inline buffer and the decoder's state in a. GzipContent, DeflateContent
and ZstdContent are the values to list in an HTTP client's decoders.

wrap fails only with OutOfMemory, when a cannot give the decoder its
buffers. The Reader it returns fails with ReadError where the stream is
bad or src fails, as Inflater.Reader does; for zstd also where a cannot
give a frame's window, which the decoder takes at the frame's header
(ZstdDecoder.NewLazy).

Fields:

- `name: str`: The coding's token in Accept-Encoding and Content-Encoding, such as
  "gzip".
- `wrap: (fn(a: &Arena, src: mut Reader, buf: mut []u8): OutOfMemory!Reader from a, src, buf)`: Makes a Reader of the decoded body over src, with buf as its inline
  buffer and the decoder's state in a.

### `Corrupt`

```
export error Corrupt(offset: int)
```

The input is not a valid stream. offset is the input byte where the
first bad element starts; each decoder's doc says which element that
is.

### `Deflate`

```
export Deflate(a: &Arena, data: []u8): OutOfMemory![]u8 from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

Compresses data as one raw DEFLATE stream (RFC 1951). There is one
compression level. No block is larger than its stored form, so the
output is at most n + n / 2048 + 64 bytes for n bytes of input.

Fails only with OutOfMemory. O(n); allocates that bound in a for the
output, and 448 KiB of scratch in a sub-arena of a, freed before it
returns.

### `DeflateContent`

```
export const DeflateContent: Content
```

The deflate content coding (RFC 9110): a zlib stream (RFC 1950), read by
Inflater.NewZlib. A raw DEFLATE stream sent as deflate is Corrupt at its
first byte.

### `Encoder`

```
export linear view struct Encoder
```

A streaming compressor that writes deflate, gzip or zlib to a Writer.
It is linear: a path that ends without Finish or Abandon does not
compile, so a stream is never cut short in silence. It holds about
610 KiB in its arena (history, match tables, symbol and output
buffers); at level 0 about 162 KiB.

### `Encoder.Abandon`

```
export Encoder.Abandon(self)
```

Ends the Encoder without finishing the stream, and writes nothing. Use
it on an error path and for an Encoder whose Write, Flush or Finish
failed. O(1); frees nothing, since the memory is its arena's.

### `Encoder.Finish`

```
export Encoder.Finish(self, w: mut Writer): WriteError!void
```

- Errors: WriteError

Compresses the pending input as the final block, writes the trailer
(gzip's CRC-32 and length, zlib's Adler-32) and consumes the Encoder.
It does not flush w.

Fails with WriteError when w does, or when an earlier write failed;
the Encoder is then still the caller's, to Abandon. O(pending input);
allocates nothing.

### `Encoder.Flush`

```
export Encoder.Flush(mut self, w: mut Writer): WriteError!void
```

- Errors: WriteError

A sync flush: compresses the pending input, ends the block, adds an
empty stored block so the stream is byte-aligned, writes it all to w
and flushes w. A reader of w can then decode every byte written so far.
Each call costs a block header and 5 bytes of output.

Fails as Write does. O(pending input); allocates nothing.

### `Encoder.New`

```
export Encoder.New(a: &Arena, framing: Framing, level: int): OutOfMemory!Encoder from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

An Encoder that writes framing around one deflate stream. Level 0
writes stored blocks only; every other level compresses as Deflate
does. It writes nothing until the first Write, Flush or Finish.

Fails only with OutOfMemory. O(1) plus the zero-filling of its
buffers; allocates them in a, as Encoder says.

### `Encoder.Write`

```
export Encoder.Write(mut self, w: mut Writer, data: []u8): WriteError!void
```

- Errors: WriteError

Compresses data into the stream. Each 64 KiB of input is compressed and
written to w as it fills; the rest waits for a later call.

Fails with WriteError when w does, and then on every later Write and
Flush: an unknown prefix of the stream reached w, so the stream can
only be abandoned. O(data.Len()), with one copy of data into the
history; allocates nothing.

### `Error`

```
export open type Error = Corrupt | Truncated | TooLarge | OutOfMemory
```

Every way a decode fails: Corrupt, Truncated, TooLarge or OutOfMemory.
The set is open, so a match on it outside std keeps a `_` arm.

### `Framing`

```
export enum Framing
```

The framing an Encoder writes around its deflate stream: none (raw
DEFLATE, RFC 1951), a gzip member (RFC 1952), or a zlib stream (RFC
1950).

Variants:

- `Raw`: Raw DEFLATE (RFC 1951), with no header or trailer.
- `Gzip`: A gzip member (RFC 1952), with its CRC-32 and size.
- `Zlib`: A zlib stream (RFC 1950), with its Adler-32.

### `Gunzip`

```
export Gunzip(a: &Arena, data: []u8, limit: int): Error![]u8 from a
```

- Flows: The result borrows from a.
- Errors: Error (Corrupt(offset: int), TooLarge, Truncated, OutOfMemory)

Decodes one or more gzip members (RFC 1952), back to back, into at most
limit bytes; a negative limit acts as 0.

Fails with Truncated when data ends inside a member, and TooLarge when
the output would pass limit. Fails with Corrupt at a bad header field
or header CRC, at a trailer whose CRC-32 or length mod 2^32 differs
from the member's output, at a bad element of the deflate stream, and
at a byte after a member that does not start another one.
O(data + output); allocates the output in a, sized by the last
member's length field when that is plausible, else as Inflate does.

### `Gzip`

```
export Gzip(a: &Arena, data: []u8): OutOfMemory![]u8 from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

Compresses data as one gzip member (RFC 1952): a 10-byte header with
mtime 0, XFL 0 and OS 255 (unknown), Deflate's stream, then the CRC-32
and the length mod 2^32.

Fails only with OutOfMemory. O(n); allocates as Deflate does, plus 18
bytes.

### `GzipContent`

```
export const GzipContent: Content
```

The gzip content coding (RFC 9110): gzip members back to back, read by
Inflater.NewGzip.

### `Inflate`

```
export Inflate(a: &Arena, data: []u8, limit: int): Error![]u8 from a
```

- Flows: The result borrows from a.
- Errors: Error (Corrupt(offset: int), TooLarge, Truncated, OutOfMemory)

Decodes a raw DEFLATE stream (RFC 1951) into at most limit bytes. All
of data is the one stream; a negative limit acts as 0.

Fails with Truncated when data ends inside the stream, and TooLarge
when the output would pass limit. Fails with Corrupt at the first bad
element, whose offset is the byte holding the first bit that cannot be
decoded, or at any byte after the final block. O(data + output);
allocates the output in a: a first buffer of 4 times the input (at
least 4 KiB, at most limit), doubled as needed, each outgrown buffer
left in a.

### `Inflater`

```
export view struct Inflater
```

A streaming DEFLATE, gzip or zlib decoder that reads its input from a
Reader. It holds about 11.5 KiB of decoder state inline, and its window
(98,570 bytes) and input buffer (16 KiB) in its arena. New, NewGzip and
NewZlib cannot fail: when the arena cannot give the buffers, Read fails
with OutOfMemory and tries again at its next call.

### `Inflater.Cause`

```
export Inflater.Cause(&self): ?Error
```

The compress.Error that ended the stream: Corrupt or Truncated once Read
or the Reader has reached it, OutOfMemory while the arena has not given
the buffers. none while the stream is good, after a clean end, and when
only the source failed. O(1); allocates nothing.

### `Inflater.New`

```
export Inflater.New(a: &Arena): Inflater
```

- Flows: The result borrows from a.

An Inflater that reads one raw DEFLATE stream (RFC 1951). It cannot
fail: when a cannot give the buffers, Read fails with OutOfMemory
instead. O(1) plus the zero-filling of the buffers; allocates the window
(98,570 bytes) and the input buffer (16 KiB) in a.

### `Inflater.NewGzip`

```
export Inflater.NewGzip(a: &Arena): Inflater
```

- Flows: The result borrows from a.

An Inflater that reads gzip members (RFC 1952) back to back, as Gunzip
does. Each header is checked as Gunzip checks it, and each trailer's
CRC-32 and length after the member's output is handed out. A byte after
a member that does not start another one is Corrupt. The stream ends
where r ends, after a member. It cannot fail, and costs and allocates
as New.

### `Inflater.NewZlib`

```
export Inflater.NewZlib(a: &Arena): Inflater
```

- Flows: The result borrows from a.

An Inflater that reads one zlib stream (RFC 1950), as Unzlib does. The
header is checked as Unzlib checks it, and the Adler-32 after the output
is handed out. The stream ends at its trailer: bytes after it are not
data, though Read may have buffered some. It cannot fail, and costs and
allocates as New.

### `Inflater.Read`

```
export Inflater.Read(mut self, r: mut Reader, buf: mut []u8): Error|ReadError!int
```

- Errors: Error|ReadError (Corrupt(offset: int), TooLarge, Truncated, OutOfMemory, ReadError)

Decodes the next bytes into buf and returns their count: at least 1,
unless buf is empty or the stream has ended (0). It writes at most
buf.Len() bytes. It takes no Io: r holds one. A raw stream ends at its
final block, a zlib stream at its trailer: bytes after it are not data,
though Read may have buffered some.

Bytes decoded before an error are returned first, and the error then
repeats on every call. It fails with Truncated when r ends inside the
stream, with Corrupt at the stream offset of the first bad element, and
with OutOfMemory while the arena has not given the window and the input
buffer; each Read tries again. A ReadError from r is returned as it is,
and Read may be called again. O(input read + buf.Len()) per call; it
allocates nothing, except the buffers when the constructor could not
take them.

### `Inflater.Reader`

```
export Inflater.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf
```

- Flows: The result borrows from self, r and buf.

A Reader of the decoded stream over r, with buf as its inline buffer.
An empty buf makes Line and Until take one read per byte. Its Io is
r's.

It fails with ReadError where Read fails, and from then on on every
read, a ReadError from r included, so a stream never resumes past a
failure. Cause says which compress.Error ended it, once the caller
stops using the Reader. O(1) per call plus the decoding; allocates
nothing.

### `TooLarge`

```
export error TooLarge
```

The output would pass the caller's limit, or a Zstandard frame's window
would pass the decoder's window limit.

### `Truncated`

```
export error Truncated
```

The input ends inside the stream.

### `Unzlib`

```
export Unzlib(a: &Arena, data: []u8, limit: int): Error![]u8 from a
```

- Flows: The result borrows from a.
- Errors: Error (Corrupt(offset: int), TooLarge, Truncated, OutOfMemory)

Decodes a zlib stream (RFC 1950) into at most limit bytes. All of data
is the one stream; a negative limit acts as 0.

Fails with Truncated when data ends inside the stream, and TooLarge
when the output would pass limit. Fails with Corrupt at a bad header
byte (a method other than deflate, a window over 32 KiB, a bad check,
a preset dictionary), at the first bad element of the deflate stream,
at an Adler-32 that differs from the output's, and at a byte after the
stream. O(data + output); allocates the output in a as Inflate does.

### `Zlib`

```
export Zlib(a: &Arena, data: []u8): OutOfMemory![]u8 from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

Compresses data as one zlib stream (RFC 1950): the header 0x78 0x9C,
Deflate's stream, then the Adler-32 of data.

Fails only with OutOfMemory. O(n); allocates Deflate's bound plus 6
bytes in a for the output, and 448 KiB of scratch in a sub-arena of a,
freed before it returns.

### `ZstdContent`

```
export const ZstdContent: Content
```

The zstd content coding (RFC 8878), token zstd: frames read by a
ZstdDecoder.NewLazy with an 8 MiB window limit, the limit RFC 8878 asks
of an HTTP client, so a small body costs a small window. A window the
arena cannot give is the Reader's ReadError.

### `ZstdDecoder`

```
export view struct ZstdDecoder
```

A streaming Zstandard decoder (RFC 8878) that reads its input from a
Reader. It holds about 17 KiB inline, and in its arena a 132 KiB input
buffer, a 128 KiB literal buffer and its window: from New the window
limit plus 256 KiB, from NewLazy one sized to the frames it reads.
There is no zstd encoder.

### `ZstdDecoder.Cause`

```
export ZstdDecoder.Cause(&self): ?Error
```

The compress.Error that ended the stream: Corrupt, Truncated or
TooLarge once Read or the Reader has reached it, OutOfMemory while the
arena has not given the buffers or a NewLazy decoder's frame window.
none while the stream is good, after a clean end, and when only the
source failed. O(1); allocates nothing.

### `ZstdDecoder.New`

```
export ZstdDecoder.New(a: &Arena, window_limit: int): ZstdDecoder
```

- Flows: The result borrows from a.

A decoder of Zstandard frames back to back, skippable frames among
them. Each frame's content size and content checksum are checked. A
frame that names a dictionary is Corrupt: dictionaries are not
supported. A frame whose window, or for a single-segment frame whose
content size, is over window_limit bytes fails with TooLarge; a
window_limit of 0 or less means 8 MiB.

It cannot fail: when a cannot give the buffers, Read fails with
OutOfMemory instead. O(1) plus the zero-filling of the buffers;
allocates window_limit + 256 KiB, 132 KiB and 128 KiB in a. NewLazy
sizes the window to each frame instead.

### `ZstdDecoder.NewLazy`

```
export ZstdDecoder.NewLazy(a: &Arena, window_limit: int): ZstdDecoder
```

- Flows: The result borrows from a.

As New, but the window is taken lazily, so a small frame costs a small
window. The constructor takes only the 132 KiB input and 128 KiB
literal buffers. Each frame's window is taken from a at the frame's
header, after the TooLarge check: the frame's window plus two of its
blocks. A later frame whose window does not fit the one held takes
twice the held one or its own, whichever is more, at most window_limit
+ 256 KiB, so a stream holds less than three times its largest window.

A window a cannot give is OutOfMemory from Read and Cause, and the next
Read tries again. Read it through Read or ReaderLazy; Reader panics on
it. It cannot fail. O(1) plus the zero-filling of the buffers;
allocates 132 KiB and 128 KiB in a.

### `ZstdDecoder.Read`

```
export ZstdDecoder.Read(mut self, r: mut Reader, buf: mut []u8): Error|ReadError!int
```

- Errors: Error|ReadError (Corrupt(offset: int), TooLarge, Truncated, OutOfMemory, ReadError)

Decodes the next bytes into buf and returns their count: at least 1,
unless buf is empty or the stream has ended (0), which is when r ends
right after a frame.

Bytes decoded before an error are returned first, and the error then
repeats on every call. It fails with Truncated when r ends inside a
frame, or before the first one. It fails with Corrupt at the stream
offset of the bad element: a compressed block's header for anything
wrong inside the block, the last block's for a content size that does
not match, the checksum's for a checksum that does not. A match offset
that reaches past the window plus the block's output so far is Corrupt
too. It fails with TooLarge for a window over the limit, and with
OutOfMemory while the arena has not given the buffers, or for a NewLazy
decoder the frame's window; each Read tries again.

A ReadError from r is returned as it is, nothing is consumed, and Read
may be called again. O(input read + buf.Len()) per call, a block's
decoding included; allocates nothing, except the buffers when the
constructor could not take them, and for a NewLazy decoder a frame's
window at its header when the one held does not fit it.

### `ZstdDecoder.Reader`

```
export ZstdDecoder.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf
```

- Flows: The result borrows from self, r and buf.

A Reader of the decoded stream over r, with buf as its inline buffer,
for a decoder from New. An empty buf makes Line and Until take one read
per byte. Its Io is r's. It panics on a decoder from NewLazy, whose
reads take windows: use ReaderLazy for that one.

It fails with ReadError where Read fails, and from then on on every
read, a ReadError from r included, so a stream never resumes past a
failure. Cause says which compress.Error ended it. O(1) per call plus
the decoding; allocates nothing.

### `ZstdDecoder.ReaderLazy`

```
export ZstdDecoder.ReaderLazy(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf
```

- Flows: The result borrows from self, r and buf.

A Reader of the decoded stream, as Reader, for a decoder from New or
NewLazy. Its reads take a NewLazy decoder's window from the decoder's
arena at each frame's header as Read takes it. Its Io is r's.

It fails with ReadError where Read fails, a window the arena cannot
give included, and from then on on every read. Cause says which
compress.Error ended it. O(1) per call plus the decoding; allocates
what Read allocates.

<!-- api:end -->
