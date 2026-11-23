// Why: lower's corpus tests compile doc/'s blocks without the real std,
// which this package's tests cannot load.
// What: the corpus helpers copied from internal/check's tests: docBlocks
// (every fenced mk block of doc/**/*.md but the std module pages),
// stdStubs (the std modules blocks import, declared as far as they need),
// testPrelude and corpusPrelude.
// How: each stub declares only what a block uses, mostly as bodiless
// intrinsics, so a block compiles through lower against them.

package lower

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/syntax"
)

// stdStubs are std modules a judged block imports, declared as far as the
// block needs (the real std is not loaded here).
var stdStubs = map[string]string{
	// fs.Error is a set holding NotFound, and fs.Kind is open, as std
	// declares them: the codebase guide's open-set blocks match both.
	"std.fs": "export error NotFound\nexport error Other\nexport open type Error = NotFound | Other\n" +
		"export open enum Kind {\n\tFile\n\tDir\n\tLink\n\tOther\n}\n" +
		"export Remove(io: Io, path: str): Error!void\n" +
		"export ReadText(a: &Arena, io: Io, p: str): Error|OutOfMemory!str from a\n" +
		// std's File, linear, as far as the three Save blocks (the
		// reference, the cookbook and the memory model) use it.
		"export linear struct File {\n\th: int\n}\n" +
		"export Create(io: Io, p: str): Error!File\n" +
		"export File.WriteAll(mut self, io: Io, data: []u8): Error!void\n" +
		"export File.Sync(mut self, io: Io): Error!void\n" +
		"export File.Close(self, io: Io): Error!void\n" +
		"export File.Abandon(self)\n" +
		// The cookbook's FileSha256 reads a File in pieces.
		"export Open(io: Io, p: str): Error!File\n" +
		"export File.Read(mut self, io: Io, buf: mut []u8): Error!int\n" +
		// The examples' log file and the cookbook's line reader.
		"export File.ReadAt(&self, io: Io, pos: i64, buf: mut []u8): Error!int\n" +
		"export File.WriteAt(&self, io: Io, pos: i64, data: []u8): Error!int\n" +
		"export File.Reader(mut self, io: Io, buf: mut []u8): Reader from self, buf\n" +
		// The cookbook's Stamp (a task whose work can fail).
		"export WriteAll(io: Io, p: str, data: []u8): Error!void\n",
	// std's Logger, as far as the reference's logging block uses it.
	"std.log": "export enum Level {\n\tDebug\n\tInfo\n\tWarn\n\tError\n}\n" +
		"export view struct Logger {\n\tw: mut Writer\n}\n" +
		"export Logger.New(w: mut Writer, level: Level): Logger from w\n" +
		"export Logger.Info[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void\n" +
		// std's Sink, as far as the cookbook's shared-sink block uses it.
		"export struct Sink {\n\th: int\n}\n" +
		"export Stderr(level: Level): Sink\n" +
		"export Sink.Warn[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void\n",
	// std's Notify, as far as the reference's notify block uses it.
	"std.sync": "export struct Notify {\n\tw: int\n}\n" +
		"export Notify.NotifyOne(&self)\nexport Notify.Wait(&self, io: Io): Canceled!void\n",
	// std's json, as far as the cookbook's config block and the
	// codebase's generic Emit use it.
	"std.json": "export error Syntax(offset: int)\nexport error Mismatch(offset: int)\nexport error Depth(offset: int)\n" +
		"export type Error = Syntax | Mismatch | Depth | OutOfMemory\n" +
		"export enum Value {\n\tNull\n\tBool(b: bool)\n\tNumber(text: str)\n\tText(s: str)\n}\n" +
		"export interface Json {\n\tFormatJson(&self, w: mut Writer): WriteError!void\n}\n" +
		"export Parse(a: &Arena, text: str): Error!Value from a, text\n" +
		"export Codec(g: mut Gen, t: Type)\n",
	// std's time, as far as the reference's ReadSome uses it.
	"std.time": "import std.core\n\nexport type Duration = core.Duration\nexport Seconds(n: i64): Duration\nexport error TimedOut\n",
	// std's net, as far as the cookbook's server blocks and the
	// reference's ReadSome use it.
	"std.net": `export error Refused
export error Reset
export error AddrInUse
export error Unreachable
export error InvalidAddress
export error Os(code: i64)
export type Error = Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed
export linear struct Listener {
	h: uint
}
export linear struct Stream {
	h: uint
}
export Listen(io: Io, addr: str): Error!Listener
export Listener.Accept(&self, io: Io): Error!Stream
export Listener.Close(self)
export Stream.Read(&self, io: Io, buf: mut []u8): Error!int
export Stream.WriteAll(&self, io: Io, data: []u8): Error!void
export Stream.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
export Stream.Close(self)
`,
	// std's http, as far as the cookbook's routes block uses it.
	"std.http": `import std.net

export error BadRequest(offset: int)
export error TooLarge
export error Status(code: int)
export type Error = BadRequest | TooLarge | Status | net.Error | OutOfMemory | ReadError | WriteError
export struct Request {
	export method: str
	export path: str
}
export view struct Response {
	arena: &Arena
}
export struct Route[C] {
	export method: str
	export pattern: str
	export handler: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void
}
export Request.Param(&self, name: str): ?str from self
export Response.Body(mut self, data: []u8): OutOfMemory!void
export Response.Writer(mut self): Writer from self
export Dispatch[C](a: &Arena, io: Io, routes: []Route[C], c: &C, req: &Request, res: mut Response): Error!void
export Serve[C: Share](sys: &System, addr: str, limit: int, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void): Error!void
`,
	// std's proc, as far as the cookbook's blocks use it.
	"std.proc": `import std.net

export error NotFound
export error Denied
export error TooLarge
export error Os(code: i64)
export error Unsupported
export type Error = NotFound | Denied | TooLarge | Os | Canceled | OutOfMemory | Unsupported
export enum Signal {
	Interrupt
	Terminate
	Hangup
}
export WaitSignal(io: Io, s: Signal): Canceled!void
export linear struct PipeReader {
	h: uint
}
export linear struct PipeWriter {
	h: uint
}
export Pipe(io: Io): Error!(r: PipeReader, w: PipeWriter)
export PipeReader.Read(&self, io: Io, buf: mut []u8): Error!int
export PipeReader.Close(self)
export PipeWriter.WriteAll(&self, io: Io, data: []u8): Error!void
export PipeWriter.Close(self)
export enum Env {
	Inherit
	Replace(vars: []str)
	Extend(vars: []str)
}
export enum Stdio {
	Inherit
	Null
	Pipe
	Reader(r: &PipeReader)
	Writer(w: &PipeWriter)
	JoinStdout
}
export struct Command {
	export args: []str
	export env: Env
	export dir: ?str
	export stdin: Stdio
	export stdout: Stdio
	export stderr: Stdio
	export group: bool
}
export const DefaultCommand: Command = Command(args: [], env: Env.Inherit, dir: none, stdin: Stdio.Inherit,
	stdout: Stdio.Inherit, stderr: Stdio.Inherit, group: false)
export struct Status {
	export code: ?int
	export signal: ?Signal
}
export linear struct Child {
	pid: i64
}
export Spawn(a: &Arena, io: Io, c: &Command): Error!(child: Child, stdin: ?PipeWriter, stdout: ?PipeReader, stderr: ?PipeReader)
export Child.Wait(self, io: Io): Error!Status
export Child.Signal(&self, s: Signal): Error!void
export Child.Kill(&self): Error!void
export Child.Detach(self)
export struct Output {
	export code: int
	export stdout: []u8
	export stderr: []u8
}
export Exit(code: int): never
export Run(a: &Arena, io: Io, args: []str, input: []u8, limit: int): Error!Output from a
export Supervise[E](sys: &System, workers: int, l: &net.Listener, const worker: fn(&System, &net.Listener): E!void): E|Error!void
`,
	// std's regex, as far as the cookbook's grep block uses it.
	"std.regex": `export error Syntax(offset: int)
export error TooLarge
export type Error = Syntax | TooLarge | OutOfMemory
export view struct Regex {
	text: str
}
export Compile(a: &Arena, pattern: str): Error!Regex from a
export Regex.IsMatch(&self, s: str): bool
export Matcher(g: mut Gen, t: Type, pattern: str)
`,
	// std's cli, as far as the cookbook's options block uses it.
	"std.cli": `export error Unknown(arg: int)
export error Missing(field: int)
export error Invalid(arg: int)
export type Error = Unknown | Missing | Invalid | OutOfMemory
export Args(g: mut Gen, t: Type)
`,
	// std's Sim, as far as the cookbook's crash test uses it.
	"std.sim": `export linear struct Sim {
	h: uint
}
export Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim
export Sim.Io(&self): Io
export Sim.CrashAfterRandomOps(mut self)
export Sim.Restart(mut self): Io
export Sim.Free(self)
`,
	// std's hash, as far as the cookbook's file hash uses it.
	"std.hash": `export struct Sha256State {
	n: u64
}
export Sha256State.New(): Sha256State
export Sha256State.Bytes(mut self, data: []u8)
export Sha256State.Sum(self): [32]u8
`,
	// std's text, as far as the cookbook's case-folding block and the
	// reference's LineBreaks block use it.
	"std.text": `export FoldCase(a: &Arena, s: str): OutOfMemory!str from a
export Width(s: str): int
export view struct Breaks {
	text: str
	pos: int
}
export LineBreaks(s: str): Breaks
export Breaks.Next(mut self): ?(at: int, mandatory: bool)
`,
}

// The stubs of std.tls, std.x509 and std.websocket, as far as the
// reference's blocks use them.
func init() {
	stdStubs["std.net"] += "export linear struct Conn {\n\th: uint\n}\nexport struct Secure {\n\tp: uint\n}\n"
	stdStubs["std.http"] += "export error NoSecureTransport\n"
	stdStubs["std.x509"] = "export error Malformed\nexport type Error = Malformed | Canceled | OutOfMemory\n"
	stdStubs["std.tls"] = `import std.net
import std.x509

export error Alert(code: u8)
export error Protocol
export error Unsupported
export type Error = Alert | Protocol | Unsupported | x509.Error | net.Error
export struct Config {
	export min_version: int
}
export const DefaultConfig: Config = Config(min_version: 0)
export linear struct Conn {
	h: uint
}
export Client(h: &Heap, io: Io, conn: net.Conn, c: &Config, host: str): Error!Conn from h, conn
`
	stdStubs["std.url"] = `export error BadUrl(offset: int)
export error BadHost
export error IdnaUnsupported
export type Error = BadUrl | BadHost | IdnaUnsupported
export view struct Url {
	href: str
}
export Parse(a: &Arena, input: str, base: ?&Url): Error|OutOfMemory!Url from a
`
	stdStubs["std.websocket"] = `import std.http
import std.net
import std.url

export error Protocol(code: u16)
export error TooLarge
export error PeerClosed(code: u16)
export error Handshake(status: int)
export type Error = Protocol | TooLarge | PeerClosed | Handshake | net.Error | http.NoSecureTransport | OutOfMemory
export struct Options {
	export frame_bytes: int
}
export const DefaultOptions: Options = Options(frame_bytes: 0)
export linear struct Conn {
	h: uint
}
export Dial(h: &Heap, a: &Arena, io: Io, u: &url.Url, o: &Options): Error!Conn from h
`
	stdStubs["std.math"] = "export const Nan: f64 = 0.0\nexport Sqrt(x: f64): f64\nexport Log(x: f64): f64\nexport Acos(x: f64): f64\n" +
		// The cookbook's widening kernel clamps its
		// tail with math.Min, as std/math/math.mk writes it.
		"export Min[T: Ord + Plain](a: T, b: T): T {\n    if b.Less(&a) {\n        return b\n    }\n    return a\n}\n"
	// std.dl and the std.raw intrinsics, as far as the cookbook's "Load a
	// library at run time" uses them.
	stdStubs["std.dl"] = `export error NotFound
export error Invalid
export error InitFailed
export error BadName
export error Unsupported
export error Os(code: i64)
export type Error = NotFound | Invalid | InitFailed | BadName | Unsupported | Os
export enum Search {
	System
	Application
	Path
}
export linear struct Library {
	h: uint
}
export Open(io: Io, name: str, search: Search): Error!Library
export Library.Symbol(&self, name: str): ?*u8
export Library.Close(self, io: Io)
export Library.Detach(self)
`
	stdStubs["std.raw"] = "export Of[T](r: &T): *T\nexport OfMut[T](r: mut T): *T\nexport PtrCast[T, U](p: *T): *U\n" +
		"export Read[T](p: *T): T\nexport Addr[T](p: *T): uint\n" +
		"export Null[T](): *T\nexport Offset[T](p: *T, n: int): *T\nexport Data[T](s: []T): *T\n" +
		"export DataMut[T](s: mut []T): *T\nexport StrData(s: str): *u8\nexport FnFromAddr[F](addr: uint): F\n" +
		"export IsNull[T](p: *T): bool\nexport Write[T](p: *T, x: T)\n"
}

// The std of several cookbook recipes, as the tree declares it, as far as
// they use it: the deadline retry, the URL link, the streamed download, the
// terminal loop and the https client.
func init() {
	stdStubs["std.time"] += "export Millis(n: i64): Duration\nexport Nanos(n: i64): Duration\n"
	stdStubs["std.net"] = "import std.time\n\n" + strings.Replace(stdStubs["std.net"],
		"export type Error = Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed\n",
		"export error Unsupported\n"+
			"export type Error = Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed | time.TimedOut | Unsupported | OutOfMemory\n", 1) +
		"export Dial(io: Io, addr: str): Error!Stream\n"
	stdStubs["std.url"] = strings.Replace(strings.Replace(stdStubs["std.url"], "\thref: str\n", "\ttext: str\n", 1),
		"export Parse(a: &Arena, input: str, base: ?&Url): Error|OutOfMemory!Url from a\n",
		"export Parse(a: &Arena, input: str into a, base: ?&Url): Error|OutOfMemory!Url from a, input\n"+
			"export Url.Href(&self): str from self\n", 1)
	stdStubs["std.tls"] += "export Connector(c: &Config): net.Secure from c\n"
	stdStubs["std.http"] = strings.Replace(stdStubs["std.http"], "import std.net\n", "import std.net\nimport std.url\n", 1) + `export struct Header {
	export name: str
	export value: str
}
export struct Reply {
	export status: int
	export headers: []Header
	export body: []u8
}
export struct ClientRequest {
	export method: str
	export url: &url.Url
	export headers: []Header
	export body: []u8
}
export struct ClientLimits {
	export redirects: int
	export secure: ?net.Secure
}
export const DefaultClientLimits: ClientLimits = ClientLimits(redirects: 10, secure: none)
export linear view struct Client {
	h: &Heap
}
export Client.New(h: &Heap, l: ClientLimits): Client from h, l
export Client.Close(self)
export Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): Error!Exchange from a, self
export Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): Error!Reply from a
export linear struct Exchange {
	h: uint
}
export Exchange.Body(mut self, buf: mut []u8): Reader from self, buf
export Exchange.Close(self, io: Io): Error!void
export Exchange.Abandon(self)
`
	// The reference's renamed import uses text.Width, the cookbook's
	// word count text.FoldCase.
	stdStubs["std.text"] = "export Width(s: str): int\nexport FoldCase(a: &Arena, s: str): OutOfMemory!str from a\n"
	stdStubs["std.term"] = `export error NotTerminal
export error Device(code: i64)
export error Unsupported
export type Error = NotTerminal | Device | Unsupported | Canceled | OutOfMemory
export enum Key {
	Char
	Enter
	Escape
}
export struct Mods {
	export shift: bool
	export alt: bool
	export ctrl: bool
}
export enum Event {
	Key(key: Key, ch: char, mods: Mods)
	Paste(text: str)
	Resize(cols: int, rows: int)
}
export linear struct Term {
	h: uint
}
export Open(io: Io): Error!Term
export Term.Raw(mut self, io: Io): Error!void
export Term.Restore(mut self, io: Io): Error!void
export Term.Read(mut self, a: &Arena, io: Io): Error!Event from a
export Term.Writer(mut self, io: Io, buf: mut []u8): Writer from self, buf
export Term.Close(self, io: Io)
export MoveTo(w: mut Writer, row: int, col: int): WriteError!void
export Clear(w: mut Writer): WriteError!void
`
}

// stubDeps are the stubs a stub imports, added before it.
var stubDeps = map[string][]string{"std.net": {"std.time"}, "std.http": {"std.net", "std.url"}, "std.proc": {"std.net"},
	"std.tls": {"std.net", "std.x509"}, "std.websocket": {"std.http", "std.net", "std.url"}}

// The device-register blocks import std.mmio: its declarations are the
// tree's file, which holds bodiless intrinsics only. The interrupt recipe
// (doc/examples.md) imports std.atomic as far as AtomicInt goes. The
// cookbook's job queue imports std.dyn as far as
// Ref goes: a view over the erased object, made by Of and given back by Mut
// and Get (std/dyn/ref.mk), as bodiless intrinsics.
func init() {
	stdStubs["std.dyn"] = "export view struct Ref {\n\tp: *u8\n\tkey: u64\n}\n" +
		"export Ref.Of[T](x: mut T): Ref from x\n" +
		"export Ref.Mut[T](mut self): mut T from self\n" +
		"export Ref.Get[T](&self): &T from self\n"
	if b, err := os.ReadFile(filepath.Join("..", "..", "std", "mmio", "mmio.mk")); err == nil {
		stdStubs["std.mmio"] = strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	// std.simd is the tree's vec.mk and dispatch.mk (Choice, Dispatch).
	var simd strings.Builder
	for _, f := range []string{"vec.mk", "dispatch.mk"} {
		if b, err := os.ReadFile(filepath.Join("..", "..", "std", "simd", f)); err == nil {
			simd.WriteString(strings.ReplaceAll(string(b), "\r\n", "\n") + "\n")
		}
	}
	if simd.Len() > 0 {
		stdStubs["std.simd"] = simd.String()
	}
	stdStubs["std.atomic"] = "export struct AtomicInt {\n\tv: i64\n}\nunsafe impl Share for AtomicInt {\n}\n" +
		"export AtomicInt.New(n: i64): AtomicInt {\n\treturn AtomicInt(v: n)\n}\n" +
		"export AtomicInt.Add(&self, n: i64): i64\nexport AtomicInt.Load(&self): i64\nexport AtomicInt.Store(&self, n: i64)\n" +
		// AtomicBool, and the memory orders, their clause methods
		// and Atomic's Load and Store (internal/check's stub).
		"export struct AtomicBool {\n\tv: u8\n}\nunsafe impl Share for AtomicBool {\n}\n" +
		"export AtomicBool.Load(&self): bool\nexport AtomicBool.Store(&self, b: bool)\n" +
		"export struct Order {\n\tv: u8\n}\n" +
		"export const Relaxed: Order = Order(v: 0)\nexport const Acquire: Order = Order(v: 1)\n" +
		"export const Release: Order = Order(v: 2)\nexport const AcqRel: Order = Order(v: 3)\n" +
		"export const SeqCst: Order = Order(v: 4)\n" +
		"export Order.CanLoad(self): bool {\n\treturn self.v != 2 && self.v != 3\n}\n" +
		"export Order.CanStore(self): bool {\n\treturn self.v != 1 && self.v != 3\n}\n" +
		"export struct Atomic[T: Plain] {\n\tv: T\n}\n" +
		"export Atomic[T].Load(&self, const o: Order): T\n\trequires o.CanLoad() {\n\treturn self.v\n}\n" +
		"export Atomic[T].Store(&self, x: T, const o: Order)\n\trequires o.CanStore() {\n}\n" +
		"export Atomic[T].New(x: T): Atomic[T] {\n\treturn Atomic[T](v: x)\n}\n" +
		"export Atomic[T].FetchAdd(&self, x: T, const o: Order): T {\n\treturn self.v\n}\n"
}

// docBlocks reads every fenced mk block of the language documents
// (doc/**/*.md without the std module pages), keyed by "file:line" of its
// opening fence, in file and line order.
func docBlocks(t testing.TB) (keys []string, texts map[string]string) {
	t.Helper()
	dir := filepath.Join("..", "..", "doc")
	files, err := languageDocs(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no documents: %v", err)
	}
	texts = map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for i := 0; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "```mk" {
				continue
			}
			start := i + 1
			j := start
			for j < len(lines) && strings.TrimSpace(lines[j]) != "```" {
				j++
			}
			key := f + ":" + itoa(i+1)
			keys = append(keys, key)
			texts[key] = strings.Join(lines[start:j], "\n") + "\n"
			i = j
		}
	}
	return keys, texts
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// testPrelude is the reference's test prelude, which every doc block and
// every conformance unit with `// prelude: yes` compiles with.
func testPrelude(t testing.TB) string {
	keys, texts := docBlocks(t)
	for _, k := range keys {
		if strings.HasPrefix(k, "reference.md:") && strings.Contains(texts[k], "MakePoint(): Point\n") {
			return texts[k]
		}
	}
	t.Fatal("no test prelude block in doc/reference.md")
	return ""
}

// corpusPrelude is fakePrelude() with the reference's std.core surface
// added: System's fields, every function line before the block's first
// "// std." group comment, and the std.sync group's Mutex, Guard and Chan
// lines (prelude types), exported and bodiless (std's intrinsic form). The
// names the fake prelude declares itself (Print, Arena.Fmt, Writer.Str,
// Cell's functions, the interfaces and Cell) keep the fake's text.
func corpusPrelude(t testing.TB) string {
	keys, texts := docBlocks(t)
	var lib string
	for _, k := range keys {
		if strings.HasPrefix(k, "reference.md:") && strings.Contains(texts[k], "System.Stdout(&self): Writer\n") {
			lib = texts[k]
		}
	}
	if lib == "" {
		t.Fatal("no library surface block in doc/reference.md")
	}
	core := fakePrelude()
	const fakeSystem = "export struct System {}\n"
	if !strings.Contains(core, fakeSystem) {
		t.Fatal("fakePrelude declares System differently")
	}
	var b strings.Builder
	depth := 0
	inSystem := false
	other := false // past the std.core lines, in another module's group
	for _, l := range strings.Split(lib, "\n") {
		if strings.HasPrefix(l, "// std.") {
			other = true
			continue
		}
		switch {
		case strings.HasPrefix(l, "struct System {"):
			inSystem = true
			b.WriteString("export " + l + "\n")
			continue
		case inSystem:
			b.WriteString(l + "\n")
			if l == "}" {
				inSystem = false
			}
			continue
		}
		if depth > 0 || strings.HasSuffix(l, "{") {
			depth += strings.Count(l, "{") - strings.Count(l, "}")
			continue
		}
		if l == "" || strings.HasPrefix(l, "//") || strings.HasPrefix(l, " ") || !strings.Contains(l, "(") {
			continue
		}
		// The std.sync group lists Mutex, Guard and Chan, which are
		// std.core's prelude types; every other group's lines are its
		// module's.
		if other && !strings.HasPrefix(l, "Mutex[") && !strings.HasPrefix(l, "Guard[") && !strings.HasPrefix(l, "Chan[") {
			continue
		}
		if strings.HasPrefix(l, "Print[") || strings.HasPrefix(l, "Arena.Fmt") || strings.HasPrefix(l, "Writer.Str(") || strings.HasPrefix(l, "Cell[") ||
			strings.HasPrefix(l, "Swap[") || strings.HasPrefix(l, "Replace[") {
			continue
		}
		if impl, ok := freeImplOf(l); ok {
			b.WriteString(impl)
			continue
		}
		b.WriteString("export " + l + "\n")
	}
	core = strings.Replace(core, fakeSystem, "", 1)
	// The iterators' fields as std lays them out, so the
	// surface's `from self.text` and `from self.map` clauses resolve.
	for _, r := range [][2]string{
		{"export struct Lines {}\n", "export view struct Lines {\n\ttext: str\n\tpos: int\n}\n"},
		{"export struct Split {}\n", "export view struct Split {\n\ttext: str\n\tpos: int\n}\n"},
		{"export struct Words {}\n", "export view struct Words {\n\ttext: str\n\tpos: int\n}\n"},
		{"export struct Scalars {}\n", "export view struct Scalars {\n\ttext: str\n\tpos: int\n}\n"},
		{"export view struct MapIter[K, V] {\n\tk: *K\n\tv: *V\n}\n", "export view struct MapIter[K, V] {\n\tmap: &Map[K, V]\n\ti: int\n}\n"},
		{"export view struct TextMapIter[V] {\n\tp: *V\n}\n", "export view struct TextMapIter[V] {\n\tmap: &TextMap[V]\n\ti: int\n}\n"},
		// std's enums, and Format for every integer width std implements it for.
		{"export enum Arch { A }\n", "export enum Arch { X64\n\tArm64\n\tRiscv64 }\n"},
		{"export enum Os { A }\n", "export enum Os { Linux\n\tWindows\n\tMacos\n\tFreestanding }\n" +
			"impl Format for i32 {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n"},
		// std's Hash, which a key type's impl must match (E0707).
		{"export interface Hash {}\n", "export interface Hash {\n\tHash(&self, h: mut Hasher)\n}\n"},
		// Limits and StackSize as std declares them, and a Mutex word whose
		// name does not fold to the method Lock's.
		{"export struct Limits {}\n", "export struct Limits {\n\texport tasks: int\n\texport stack: StackSize\n}\n"},
		{"export enum StackSize { A }\n", "export enum StackSize { Default\n\tLarge\n\tBytes(n: int) }\n"},
		{"export struct Mutex[T] {\n\tlock: u32\n\tv: T\n}\n", "export struct Mutex[T] {\n\tword: u32\n\tv: T\n}\n"},
	} {
		if !strings.Contains(core, r[0]) {
			t.Fatalf("fakePrelude has no %q", r[0])
		}
		core = strings.Replace(core, r[0], r[1], 1)
	}
	// Format for references, optionals, slices and the lists, as std
	// implements it.
	for _, t := range []string{"&T", "?T", "[]T", "List[T]", "HeapList[T]"} {
		core += "impl[T: Format] Format for " + t + " {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n"
	}
	// Format and Eq for the built-in scalars fakePrelude leaves out, as
	// std implements them.
	for _, t := range []string{"bool", "char", "i8", "i16", "u8", "u16", "u32", "u64", "uint", "f32"} {
		core += "impl Format for " + t + " {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n"
	}
	for _, t := range []string{"bool", "char", "i8", "i16", "i64", "u16", "u32", "u64", "f32", "f64"} {
		core += "impl Eq for " + t + " {\n\tEq(&self, other: &" + t + "): bool {\n\t\treturn true\n\t}\n}\n"
	}
	// Chan and Mutex are Send and Share when T is Send, and the heap
	// containers Send, as std declares them.
	for _, t := range []string{"Chan[T]", "Mutex[T]"} {
		core += "unsafe impl[T: Send] Send for " + t + " {}\nunsafe impl[T: Send] Share for " + t + " {}\n"
	}
	core += "unsafe impl[T: Send] Send for HeapList[T] {}\nunsafe impl[V: Send] Send for TextMap[V] {}\nunsafe impl[T: Send] Send for Box[T] {}\n"
	core += b.String()
	sm := source.NewSourceMap()
	if _, ds := syntax.ParseFile(sm.Add("core.mk", []byte(core)), source.NewInterner(), syntax.Options{AllowBodiless: true}); len(ds) > 0 {
		t.Fatalf("the corpus prelude does not parse: %s at %d", ds[0].Message, ds[0].At.Start)
	}
	return core
}

// freeImplOf turns a surface line `HeapList[T].Free(self, a: &Alloc)` into
// the unbounded impl std writes for it (`impl[T] Free for HeapList[T]`),
// as internal/check's corpus prelude does.
func freeImplOf(l string) (string, bool) {
	const tail = ".Free(self, a: &Alloc)"
	if !strings.HasSuffix(l, tail) {
		return "", false
	}
	typ := strings.TrimSuffix(l, tail)
	gens := ""
	if i := strings.IndexByte(typ, '['); i >= 0 {
		gens = typ[i:]
	}
	return "impl" + gens + " Free for " + typ + " {\n\tFree(self, a: &Alloc) {\n\t}\n}\n", true
}

// stubPaths are stdStubs' keys in byte order, so modules register in a
// fixed order.
func stubPaths() []string {
	var out []string
	for p := range stdStubs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// docFiles lists the Markdown files under dir, doc/std's pages included, as
// paths relative to dir with '/', in byte order: "reference.md",
// "std/fs.md".
func docFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return err
	})
	sort.Strings(out)
	return out, err
}

// languageDocs is docFiles without the std module pages (doc/std/): their
// examples call the real std, which this package's stand-in std does not
// declare, so mk corpus judges them instead.
func languageDocs(dir string) ([]string, error) {
	all, err := docFiles(dir)
	var out []string
	for _, f := range all {
		if !strings.HasPrefix(f, "std/") {
			out = append(out, f)
		}
	}
	return out, err
}
