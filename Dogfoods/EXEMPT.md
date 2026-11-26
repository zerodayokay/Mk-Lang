# Exempt lines

The library lines (`doc/reference.md`, "Library surface") that no dogfood can exercise, each with the reason and the place that tests it instead. Every other line of the library surface is named by some dogfood's source and executed by some dogfood case; `go run ./tools/dogfood cover` checks both and writes the result to `COVERAGE.md`.

`cover` reads this file. A `## ` heading names a module the way the library surface's `// std.x` comments do (`prelude` for the lines before the first of them), and each entry under it has this form:

```
- `<the line, word for word as the library surface writes it>` (execution|reference|both): <reason>. Covered instead: <where>.
```

`execution` exempts only the run-time half (some dogfood must still name the line, as `mk query uses` shows), `reference` only the static half (some case must still execute it), and `both` the whole line. `cover` exits 1 on an entry that names no library line, lacks "Covered instead:", or repeats a line.

Most entries are lines that only freestanding code reaches (a freestanding program writes no profile), lines that only a generator's output names, and lines that no dogfood calls. The "Covered instead" part says which dogfood or test exercises the line.

## prelude

- `Io.Random(self, buf: mut []u8): Canceled!void` (reference): the dogfoods take randomness through std's typed sources (`HashKey.New` in kvd, `math.Rng.FromIo` in raft, `crypto.Random` in pack), as a program that needs a key, a seed or a nonce does, so no realistic call site names `Io.Random` itself. Covered instead: its execution, which `cover` finds in kvd's cases (COVERAGE.md lists them under this line).
- `Arena.SliceAligned[T](&self, n: int, align: int): OutOfMemory!mut []T` (both): no dogfood calls it. Covered instead: std/core/tests_arena.mk's SliceAligned blocks and std/core_slicealigned_test.go.
- `Reader.Of(b: []u8): Reader from b` (both): no dogfood calls it. Covered instead: std/core/tests_rw.mk's Reader.Of blocks and std/core_readerof_test.go.
- `str.Replace(self, a: &Arena, old: str, new: str): OutOfMemory!str from a` (both): no dogfood calls it. Covered instead: std/core/tests_text.mk's D-833 blocks and std/core_replace_test.go, which compares it with Go's strings.ReplaceAll.
- `Text.PushReplaced(mut self, s: str, old: str, new: str): OutOfMemory!void` (both): no dogfood calls it. Covered instead: std/core/tests_text.mk's D-833 blocks and std/core_replace_test.go.

## std.mmio

Volatile accesses meant for device registers. `mmio.Load`, `mmio.Store` and `mmio.Fence` take any raw pointer, so hosted code in an `unsafe` module could call them on ordinary memory; but only `Dogfoods/kernel` uses `std.mmio`, and it runs freestanding, where no profile is written (a freestanding program links no `rt`, and `rt` writes the `.mkprof` file). `Reg[T]` stands for a register at a fixed physical address, which a hosted process cannot map.

- `mmio.Load[T: Plain](p: *T): T` (execution): owned only by the freestanding kernel, which writes no profile. Covered instead: Dogfoods/kernel must reference it and a QEMU case must show its effect on the serial output; internal/driver TestQemuKernelEssentials reads the APIC version with it.
- `mmio.Store[T: Plain](p: *T, x: T)` (execution): owned only by the freestanding kernel, which writes no profile. Covered instead: Dogfoods/kernel must reference it and a QEMU case must show its effect.
- `mmio.Fence()` (execution): owned only by the freestanding kernel, which writes no profile. Covered instead: Dogfoods/kernel must reference it in the code its QEMU cases run.
- `Reg[T].New(addr: uint): Reg[T]` (execution): a register at a fixed device address, which a hosted process cannot map. Covered instead: Dogfoods/kernel must reference it and a QEMU case must show its effect.
- `Reg[T].Read(&self): T` (execution): reads a register at a fixed device address. Covered instead: Dogfoods/kernel must reference it and a QEMU case must print what it reads.
- `Reg[T].Write(&self, x: T)` (execution): writes a register at a fixed device address. Covered instead: Dogfoods/kernel must reference it and a QEMU case must show its effect.

## std.free

- `free.Heap(): &Heap` (execution): std.free exists on x86_64-freestanding only, where no profile is written. Covered instead: Dogfoods/kernel must reference it and a QEMU case must allocate from it; internal/driver TestQemuKernelEssentials builds a HeapList on it.

## std.text

The Writers that need no Arena and no Io are for a program with no heap or no Io yet: a freestanding kernel, which writes no profile. A hosted dogfood formats into a `Text` or a process stream instead.

- `Sink[S].New(write: fn(mut S, []u8): WriteError!void): Sink[S]` (execution): owned by the freestanding kernel, which writes no profile. Covered instead: Dogfoods/kernel must reference it, and its QEMU cases print every console line and every panic report through it; std.text's blocks (tests_writer.mk) run it on the hosted targets.
- `Sink[S].Writer(&self, s: mut S, buf: mut []u8): Writer from self, s, buf` (execution): owned by the freestanding kernel, which writes no profile. Covered instead: Dogfoods/kernel must reference it and its QEMU cases must show its output; std.text's blocks run it on the hosted targets.
- `Capped.New(buf: mut []u8): Capped from buf` (both): a bounded record target that no dogfood's output needs, since each formats into a `Text` or a stream it can grow. Covered instead: std.text's blocks (tests_writer.mk), and std.log, whose every record goes through std.core's Capped.
- `Capped.Writer(mut self): Writer from self` (both): as `Capped.New`. Covered instead: std.text's blocks and std.log's records.
- `Capped.Bytes(&self): []u8 from self` (both): as `Capped.New`. Covered instead: std.text's blocks and std.log's records.
- `Capped.Dropped(&self): int` (both): as `Capped.New`. Covered instead: std.text's blocks and std.log's records cut past 4 KiB.
- `Capped.Reset(mut self)` (both): as `Capped.New`. Covered instead: std.text's blocks.

## std.json

- `json.Codec(g: mut Gen, t: Type)` (execution): a generator; it runs at compile time inside `gen`, so no case can enter it at run time. Covered instead: the dogfoods that own std.json (notes, orderbook) must reference it with a `gen json.Codec` line and execute the generated code in their cases.

## std.regex

- `regex.Matcher(g: mut Gen, t: Type, pattern: str)` (execution): a generator; it runs at compile time inside `gen`. Covered instead: Dogfoods/mgrep must reference it with a `gen regex.Matcher` line and execute the generated matcher in its cases.

## std.cli

- `cli.Args(g: mut Gen, t: Type)` (execution): a generator; it runs at compile time inside `gen`. Covered instead: the dogfoods with a command line must reference it with a `gen cli.Args` line and execute the generated parser in their `--help` and bad-argument cases.

## std.dyn

- `dyn.Of(g: mut Gen, i: Type)` (execution): a generator; it runs at compile time inside `gen`. Covered instead: Dogfoods/lox must reference it with a `gen dyn.Of` line and call native functions through the generated table in its cases.

## std.dyn.erased

- `Key[T](): u64` (reference): only `dyn.Of`'s output names it; a user who names it writes the generator's job by hand. Covered instead: the cases of Dogfoods/lox must execute it through its `gen dyn.Of` output.

## std.net

- `error StreamReset(code: u64)` (both): the dogfoods check cancellation at the HTTP exchange boundary. Covered instead: std/net/pipe/tests_pipe.mk's reset cases.
- `error MuxClosed(code: u64, app: bool, peer: bool)` (both): the dogfoods check reconnects and closure at the HTTP exchange boundary. Covered instead: std/net/pipe/tests_pipe.mk's connection close cases.
- `Secure.Of[T: Share](c: core.ErasedCall): Secure from c` (both): the constructor of a connector that negotiates nothing; its argument is the erased call std.core builds, so a program names it only when it writes a TLS stack of its own, and std.tls's connectors are made by `Secure.OfAlpn`. Covered instead: the test connectors of std/http (tests_secure.mk, tests_limits.mk, tests_proxy.mk) and std/websocket (tests_sweep.mk), and std/std-net_secure_test.go, which also checks that a mismatched call panics.
- `Secure.OfAlpn[T: Share](c: core.TypedCall[fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str): net.Error!Conn from h, conn], n: core.TypedCall[fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): net.Error!(conn: Conn, chosen: int) from h, conn]): Secure from c, n` (reference): the constructor a TLS library calls to hand its connector to `std.http` and `std.websocket`; its arguments are typed calls std.core builds, so a program names it only when it writes a TLS stack of its own, and the dogfoods take `tls.Connector`, as a program that wants https does. Covered instead: its execution, which `cover` finds in fetch's https and wss cases, and std/std-net_secure_test.go, which also checks that a call of the wrong type is E0301.
- `Secure.Negotiate(&self, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): net.Error!(conn: Conn, chosen: int) from h, conn` (reference): Fetch and Chat exercise HTTP/2 over TLS through std.http. Covered instead: std/tls/tests_negotiate.mk, std/std-tls_negotiate_test.go (against Go's crypto/tls in both roles) and std/std-net_secure_test.go.
- `Stream.SendHandles(&self, io: Io, data: []u8, handles: []Pass): net.Error!void` (both): no dogfood calls it. Covered instead: std/net/tests_unix.mk's SendHandles and RecvHandles blocks.
- `Stream.RecvHandles(&self, a: &Alloc, io: Io, buf: mut []u8, into: mut HeapList[Passed]): net.Error!int` (both): no dogfood calls it. Covered instead: std/net/tests_unix.mk's SendHandles and RecvHandles blocks.
- `Passed.Close(self)` (both): no dogfood calls it. Covered instead: std/net/tests_unix.mk's RecvHandles blocks and std/proc/tests_handles.mk.
- `Passed.Adopt(p: rt.Passed): Passed` (both): no dogfood calls it. Covered instead: std/net/tests_unix.mk's RecvHandles blocks and std/proc/tests_handles.mk's Inherited blocks.
- `enum Pass {` (both): no dogfood calls it. Covered instead: std/net/tests_unix.mk's SendHandles blocks.
- `enum Passed {` (both): no dogfood calls it. Covered instead: std/net/tests_unix.mk and std/proc/tests_handles.mk.

## std.fs

- `File.Adopt(p: rt.Passed): File` (both): no dogfood calls it. Covered instead: std/net/tests_unix.mk's RecvHandles blocks and std/proc/tests_handles.mk's Inherited blocks, which reach it through net.Passed.Adopt.

## std.proc

- `proc.Inherited(a: &Alloc, io: Io, into: mut HeapList[net.Passed]): proc.Error!int` (both): no dogfood calls it. Covered instead: the Command.handles and proc.Inherited tests in std/proc/tests_handles.mk.
- `enum Pass {` (both): no dogfood calls it. Covered instead: the Command.handles and proc.Inherited tests in std/proc/tests_handles.mk.

## std.text

- `text.ParagraphBounds(s: str): Bounds` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk's UAX 9 blocks.
- `text.BidiLevels(a: &Arena, s: str, dir: Direction): OutOfMemory!Levels from a, s` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk's UAX 9 blocks.
- `Levels.Base(&self): u8` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk's UAX 9 blocks.
- `Levels.At(&self, i: int): u8` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk's UAX 9 blocks.
- `Levels.Line(&self, a: &Arena, start: int, end: int): OutOfMemory![]Run from a` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk's UAX 9 blocks.
- `text.Mirror(c: char): ?char` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk.
- `text.LineBreaks(s: str): Breaks` (both): no dogfood calls it. Covered instead: std/text/tests_linebreak.mk's UAX 14 blocks.
- `Breaks.Next(mut self): ?(at: int, mandatory: bool)` (both): no dogfood calls it. Covered instead: std/text/tests_linebreak.mk's UAX 14 blocks.
- `enum Direction {` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk.
- `struct Run {` (both): no dogfood calls it. Covered instead: std/text/tests_bidi.mk's Levels.Line blocks.

## std.hash

- `Sip13State.New(key: HashKey): Sip13State` (both): no dogfood calls it. Covered instead: std/std-defects_sip13state_test.go, which reads Sum after every piece against hash.Sip13 and the Go model.
- `Sip13State.Bytes(mut self, data: []u8)` (both): no dogfood calls it. Covered instead: std/std-defects_sip13state_test.go's split cases.
- `Sip13State.Sum(self): u64` (both): no dogfood calls it. Covered instead: std/std-defects_sip13state_test.go's split cases and Rust's DefaultHasher vectors.

## std.http

- `error ProxyRefused(code: int)` (both): no dogfood calls it. Covered instead: std/http/tests_proxy.mk's CONNECT blocks.
- `http.ServeChan[C: Share, T: Send](h: &Heap into ch, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, ch: &Chan[T], handle: fn(c: &C, h: &Heap into ch, ch: &Chan[T], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void): http.Error!void` (both): no dogfood calls it. Covered instead: std/http/tests_servechan.mk's blocks.

## std.crypto

- `error BadCost` (both): no dogfood calls it. Covered instead: std/crypto/tests_argon2.mk's blocks.
- `crypto.Argon2id(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, out: mut []u8): crypto.BadLength|crypto.BadCost|OutOfMemory!void` (both): no dogfood calls it. Covered instead: std/crypto/tests_argon2.mk's RFC 9106 blocks.
- `crypto.Argon2idVerify(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, tag: []u8): crypto.BadLength|crypto.BadCost|OutOfMemory!bool` (both): no dogfood calls it. Covered instead: std/crypto/tests_argon2.mk's blocks.
- `struct Argon2Cost {` (both): no dogfood calls it. Covered instead: std/crypto/tests_argon2.mk's blocks.
- `crypto.ChaCha20Block(key: &[32]u8, counter: u32, nonce: &[12]u8): [64]u8` (both): QUIC applies this header protection for the ChaCha20 suite. The HTTP/3 dogfoods leave suite selection to QUIC. Covered instead: std/crypto/tests_block.mk's RFC 8439 2.3.2 and RFC 9001 A.5 blocks.
- `crypto.HkdfExpandLabel(prk: &Prk, label: str, context: []u8, out: mut []u8): crypto.BadKey|crypto.BadLength!void` (reference): QUIC derives packet keys for the HTTP/3 dogfoods. Covered instead: std/crypto/tests_block.mk's RFC 9001 A.1 and A.5 blocks and its length blocks.
- `Aes.New(key: []u8): crypto.BadLength!Aes` (reference): QUIC sets up AES header protection for Initial packets in the HTTP/3 dogfoods. Covered instead: std/crypto/tests_block.mk's FIPS 197 and RFC 9001 A.2 and A.3 blocks.
- `Aes.Encrypt(&self, block: &[16]u8): crypto.BadKey![16]u8` (reference): QUIC applies AES header protection to Initial packets in the HTTP/3 dogfoods. Covered instead: std/crypto/tests_block.mk's FIPS 197 and RFC 9001 A.2 and A.3 blocks.
- `Aes.Wipe(mut self)` (reference): QUIC wipes temporary AES state internally in the HTTP/3 dogfoods. Covered instead: std/crypto/tests_block.mk's Wipe block.

## std.compress

- `Adler32State.New(): Adler32State` (both): no dogfood calls it. Covered instead: std/compress/tests_adler.mk's split blocks.
- `Adler32State.Bytes(mut self, data: []u8)` (both): no dogfood calls it. Covered instead: std/compress/tests_adler.mk's split blocks.
- `Adler32State.Sum(self): u32` (both): no dogfood calls it. Covered instead: std/compress/tests_adler.mk's split blocks.
- `ZstdDecoder.NewLazy(a: &Arena, window_limit: int): ZstdDecoder` (reference): std runs it for a dogfood, but no dogfood's own source names it. Covered instead: its execution, which cover finds in fetch's decoders case, and std/compress/tests_zstd.mk's window-cap blocks.
- `ZstdDecoder.ReaderLazy(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf` (reference): std runs it for a dogfood, but no dogfood's own source names it. Covered instead: its execution, which cover finds in fetch's decoders case, and std/compress/tests_zstd.mk's window-cap blocks.

## std.encoding

- `PercentSet.Has(self, b: u8): bool` (both): no dogfood calls it. Covered instead: std/encoding/tests_text.mk and std/url/tests_sets.mk.
- `Encoding.DecodeKeepBom(self, a: &Arena, bytes: []u8): OutOfMemory!str from a` (both): no dogfood calls it. Covered instead: std/encoding/tests_text.mk's KeepBom blocks.
- `Encoding.Of(codec: Codec): Encoding` (both): no dogfood calls it. Covered instead: std/encoding/tests_codec.mk's blocks.
- `struct Codec {` (both): no dogfood calls it. Covered instead: std/encoding/tests_codec.mk's blocks.

## std.encoding.cjk

- `cjk.Lookup(label: str): encoding.UnknownLabel!encoding.Encoding` (both): no dogfood calls it. Covered instead: std/encoding/cjk/tests_cjk.mk's blocks.

## std.x509

- `x509.SystemPool(h: &Heap, io: Io): x509.Error!&Pool` (reference): std runs it for a dogfood, but no dogfood's own source names it. Covered instead: its execution, which cover finds in fetch's handshake cases through std.tls, and std/x509/tests_system.mk's blocks.
