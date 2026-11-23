# Mk cookbook

Short programs for everyday tasks. Each recipe states the task, shows a program, and lists the few things to know about it. Every program compiles as written; the few that need a line in `pkg.mk` say which.

Read the [overview](overview.md) first. For the rules behind the recipes, see [the reference](reference.md) and [the memory model](memory.md). For larger programs, see the [examples](examples.md).

**Programs and output:** [Print the command-line arguments](#print-the-command-line-arguments), [Parse command-line options](#parse-command-line-options), [Print a table](#print-a-table), [Implement Format for your own type](#implement-format-for-your-own-type), [Report errors with a line number](#report-errors-with-a-line-number).

**Files and text:** [Read a file line by line](#read-a-file-line-by-line), [Read a whole file and count its lines](#read-a-whole-file-and-count-its-lines), [Write a file and report a close error](#write-a-file-and-report-a-close-error), [Hash a file in pieces](#hash-a-file-in-pieces), [Count words and print the ten most common](#count-words-and-print-the-ten-most-common), [Split text into tokens without copying](#split-text-into-tokens-without-copying), [Compare words without case, and measure columns](#compare-words-without-case-and-measure-columns), [Filter lines with a regular expression](#filter-lines-with-a-regular-expression), [Parse JSON into a struct](#parse-json-into-a-struct), [Parse a URL and resolve a link](#parse-a-url-and-resolve-a-link).

**Memory and data structures:** [Use one arena per request](#use-one-arena-per-request), [Pass capabilities together in a struct](#pass-capabilities-together-in-a-struct), [Return heap-owned data that outlives an arena](#return-heap-owned-data-that-outlives-an-arena), [Write a generic container](#write-a-generic-container), [Write a heap-owned generic container](#write-a-heap-owned-generic-container), [Link tree nodes to their parents](#link-tree-nodes-to-their-parents), [Build an expression tree in an arena](#build-an-expression-tree-in-an-arena), [Sort with a comparison that reads locals](#sort-with-a-comparison-that-reads-locals), [Keep a callback for later](#keep-a-callback-for-later), [Queue jobs whose states have different types](#queue-jobs-whose-states-have-different-types).

**Tasks and time:** [Serve each connection in its own task](#serve-each-connection-in-its-own-task), [Shut down on a signal](#shut-down-on-a-signal), [Handle errors inside tasks](#handle-errors-inside-tasks), [Share state between tasks](#share-state-between-tasks), [Process a slice in parallel](#process-a-slice-in-parallel), [Time out a read, an idle connection and a retry](#time-out-a-read-an-idle-connection-and-a-retry), [Run tasks with a deadline](#run-tasks-with-a-deadline), [Finish a write during shutdown, with a time limit](#finish-a-write-during-shutdown-with-a-time-limit), [Report from many tasks](#report-from-many-tasks).

**Network, processes and the terminal:** [Serve HTTP with routes](#serve-http-with-routes), [Restart workers that panic](#restart-workers-that-panic), [Download a large file over HTTP](#download-a-large-file-over-http), [Make an HTTPS request](#make-an-https-request), [Run a program and read its output](#run-a-program-and-read-its-output), [Talk to a running child, build a pipeline, and kill on timeout](#talk-to-a-running-child-build-a-pipeline-and-kill-on-timeout), [Read keys from the terminal](#read-keys-from-the-terminal).

**Testing, speed and foreign code:** [Write a test and a property](#write-a-test-and-a-property), [Test crash safety under simulation](#test-crash-safety-under-simulation), [Write a vector kernel once and pick it by CPU](#write-a-vector-kernel-once-and-pick-it-by-cpu), [Write a kernel that widens its lanes](#write-a-kernel-that-widens-its-lanes), [Call a function in a Windows DLL](#call-a-function-in-a-windows-dll), [Load a library at run time](#load-a-library-at-run-time).

## Print the command-line arguments

Print each argument with its position.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    for i, arg in sys.args {
        try sys.Print("{i}: {arg}\n", (i, arg))
    }
}
```

```text
$ mk run args.mk -- red green
0: ...\args.exe
1: red
2: green
```

- `sys.args[0]` is the program's path, as it was started.
- `sys` holds what the program may use: its arguments, the standard streams, the heap, an arena and `io`.
- `Print` takes a format string and a tuple. Each hole, `{i}`, names a field of the tuple, and `(i, arg)` takes its field names from the variables.
- Printing can fail, so `Main` returns `WriteError!void` and each call is a `try`.

## Read a file line by line

Open a file, read it one line at a time, and print the lines that are not comments.

```mk
// verdict: ok
import std.fs

export Main(sys: &System): fs.Error|ReadError|OutOfMemory|WriteError!void {
    try fs.WriteAll(sys.io, "notes.txt", "# shopping\nbread\n# done\nmilk\n".AsBytes())
    var f := try fs.Open(sys.io, "notes.txt")
    onerr f.Abandon()
    var buf: [4096]u8 = [0; 4096]
    var input := f.Reader(sys.io, mut buf[..]) // reads through buf
    var n := 0
    for line := try input.Line(sys.arena) { // none at the end ends the loop
        n += 1
        if line.StartsWith("#") {
            continue
        }
        try sys.Print("{n}: {line}\n", (n, line))
    }
    try f.Close(sys.io)
    try fs.Remove(sys.io, "notes.txt")
}
```

```text
$ mk run lines.mk
2: bread
4: milk
```

- `f.Reader` reads through the buffer you give it. `Line` returns `none` at the end of the file, and the loop ends.
- Each line is copied into the arena you pass. `sys.arena` lives as long as the program; for a large file, pass an arena you reset on each turn.
- `onerr f.Abandon()` releases the file if anything after it fails. `f.Close` reports a close error.
- The `Reader` borrows `f` until its last use, so `f.Close` comes after the loop.

## Read a whole file and count its lines

Read a small file into memory at once.

```mk
// verdict: ok
import std.fs

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    text := try fs.ReadText(sys.arena, sys.io, "notes.txt")
    var n := 0
    for line in text.Lines() {
        _ = line
        n += 1
    }
    try sys.Print("{n} lines\n", (n))
}
```

With the `notes.txt` of the recipe above:

```text
$ mk run count.mk
4 lines
```

- `fs.ReadText` allocates the text in the arena, so nothing needs a release.
- `Lines()` yields views of `text`. Nothing is copied.

## Write a file and report a close error

Write data to a file so that every failure, including a failed close, reaches the caller.

```mk
// verdict: ok
import std.fs

Save(io: Io, path: str, data: []u8): fs.Error!void {
    var f := try fs.Create(io, path)
    onerr f.Abandon()
    try f.WriteAll(io, data)
    try f.Sync(io)
    try f.Close(io)
}
```

- A close can fail, so it is a `try` at the end, not a `defer`.
- `onerr` runs only when the function fails. A failed `Close` leaves `f` open and owned, so `onerr` covers that failure too.
- `Sync` asks the system to put the data on disk before the close.

## Hash a file in pieces

Compute the SHA-256 of a file of any size with one fixed buffer.

```mk
// verdict: ok
import std.fs
import std.hash

FileSha256(a: &Arena, io: Io, path: str): fs.Error|OutOfMemory![32]u8 {
    var scratch := try a.Sub()
    defer scratch.Free()
    buf := try scratch.Slice[u8](65536)
    var f := try fs.Open(io, path)
    onerr f.Abandon()
    var h := hash.Sha256State.New()
    for {
        n := try f.Read(io, buf)
        if n == 0 {
            break
        }
        h.Bytes(buf[..n])
    }
    try f.Close(io)
    return h.Sum()
}
```

- The buffer comes from a sub-arena that `defer` frees when the function returns.
- `buf` is a `mut []u8`, which does not pass where a `[]u8` is expected. `buf[..n]` is the shared view that `h.Bytes` takes.
- The result is a plain array, so nothing of the scratch arena outlives the call.

## Count words and print the ten most common

Count each word of a file in a map, then sort the counts.

```mk
// verdict: ok
import std.fs

struct Entry {
    word: str
    count: int
}

ByCount(a: &Entry, b: &Entry): bool {
    if a.count != b.count {
        return a.count > b.count
    }
    return a.word < b.word
}

Count(a: &Arena, text: str into a): OutOfMemory!List[Entry] from a, text {
    var counts := Map[str, int].New(a)
    for word in text.Words() {
        n := try counts.Ensure(word, 0)
        *n += 1
    }
    var out := List[Entry].New(a)
    for (key, value) in counts.Iter() {
        try out.Push(Entry(word: *key, count: *value))
    }
    out.Sort(ByCount)
    return out
}

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    text := try fs.ReadText(sys.arena, sys.io, "input.txt")
    entries := try Count(sys.arena, text)
    for i, e in entries {
        if i == 10 {
            break
        }
        try sys.Print("{word} {count}\n", (word: e.word, count: e.count))
    }
}
```

For an `input.txt` of

```text
the cat and the dog
saw the bird in the tree
```

it prints

```text
the 4
and 1
bird 1
cat 1
dog 1
in 1
saw 1
tree 1
```

- `Ensure` returns a reference to the value for a key, inserting the given value first when the key is missing.
- The keys are views of `text`, so the map and the result hold a loan of it. The signature says so: `text: str into a` and `from a, text`.
- `ByCount` breaks ties by word, so equal counts print in the same order on every run.

## Split text into tokens without copying

Walk a string and hand out its words as slices of it.

```mk
// verdict: ok
struct Parser {
    src: str
    pos: int
}

Parser.Next(mut self): ?str {
    for self.pos < self.src.Len() && self.src.Byte(self.pos) == b' ' {
        self.pos += 1
    }
    if self.pos == self.src.Len() {
        return none
    }
    start := self.pos
    for self.pos < self.src.Len() && self.src.Byte(self.pos) != b' ' {
        self.pos += 1
    }
    return some(self.src[start..self.pos])
}

Tokens(a: &Arena, text: str into a): OutOfMemory!List[str] from a, text {
    var out := List[str].New(a)
    for tok in Parser(src: text, pos: 0) {
        try out.Push(tok)
    }
    return out
}
```

- A `Parser` stores no text of its own, only a view, so each token is a slice of the source.
- Tokens borrow the source, not the parser. They outlive the loop, and `Next` needs no clause.
- `for tok in Parser(...)` calls `Next` until it returns `none`.

## Compare words without case, and measure columns

Compare two words as a reader would, and find how wide some text is on a terminal.

```mk
// verdict: ok
import std.text

SameWord(a: &Arena, x: str, y: str): OutOfMemory!bool {
    var scratch := try a.Sub()
    defer scratch.Free()
    fx := try text.FoldCase(&scratch, x)
    fy := try text.FoldCase(&scratch, y)
    return fx == fy
}

Columns(s: str): int {
    var widest := 0
    for line in s.Lines() {
        w := text.Width(line)
        if w > widest {
            widest = w
        }
    }
    return widest
}
```

- `text.FoldCase` is full case folding: `"Straße"` and `"STRASSE"` compare equal, which `ToLower` does not give.
- `text.Width` counts terminal columns per grapheme cluster. A wide character takes two, a combining mark none. `Len()` counts bytes.

## Filter lines with a regular expression

Print the lines of standard input that match a pattern given on the command line.

```mk
// verdict: ok
import std.proc
import std.regex

struct ErrorLine {
}

gen regex.Matcher(ErrorLine, "^ERROR [0-9]+")

CountErrors(text: str): int {
    var n := 0
    for line in text.Lines() {
        if ErrorLine.IsMatch(line) {
            n += 1
        }
    }
    return n
}

export Main(sys: &System): regex.Error|ReadError|WriteError!void {
    if sys.args.Len() < 2 {
        try sys.PrintErr("usage: grep PATTERN\n")
        proc.Exit(2)
    }
    re := try regex.Compile(sys.arena, sys.args[1])
    var input := sys.Stdin()
    for line := try input.Line(sys.arena) {
        if re.IsMatch(line) {
            try sys.Print("{line}\n", (line))
        }
    }
}
```

```text
$ printf 'ERROR 12 disk full\ninfo: started\nERROR 7 retry\n' | mk run grep.mk -- '^ERROR'
ERROR 12 disk full
ERROR 7 retry
```

- `regex.Compile` takes a pattern at run time. Matching is linear in the input.
- A pattern known when you write the program becomes code with `gen regex.Matcher`, which needs no arena. `CountErrors` uses one.

## Parse JSON into a struct

Read a configuration file into a struct, and write the struct back as JSON.

```mk
// verdict: ok
import std.fs
import std.json

struct Config {
    export name: str
    export port: int
    export debug: ?bool
}

gen json.Codec(Config)

Load(a: &Arena, io: Io, path: str): fs.Error|json.Error!json.Value from a {
    text := try fs.ReadText(a, io, path)
    return try json.Parse(a, text)
}

export Main(sys: &System): fs.Error|json.Error|WriteError!void {
    v := try Load(sys.arena, sys.io, "config.json")
    c := try Config.FromJson(sys.arena, &v)
    try sys.Print("{name} on port {port}\n", (name: c.name, port: c.port))
    var out := sys.Stdout()
    try c.FormatJson(mut out)
}
```

For a `config.json` of

```json
{"name": "api", "port": 8080}
```

it prints

```text
api on port 8080
{"name":"api","port":8080,"debug":null}
```

- `gen json.Codec(T)` writes `T.FromJson` and `T.FormatJson` from the fields. Every field must be exported. An optional field, `?bool`, may be missing.
- `json.Parse` keeps each string without an escape as a view of the text, so the `Value` borrows the arena.
- `FromJson` copies each string it reads into the arena. Its result borrows the arena alone, so the `Value` may end first.

## Parse a URL and resolve a link

Turn a link found on a page into an absolute URL.

```mk
// verdict: ok
import std.url

Link(a: &Arena, page: str into a, href: str into a): url.Error|OutOfMemory!str from a {
    base := try url.Parse(a, page, none)
    u := try url.Parse(a, href, some(&base))
    return try a.Dup(u.Href())
}
```

- `url.Parse` follows the WHATWG URL Standard. Pass a base URL to resolve a relative link.
- `Href()` is the exact URL and borrows its `Url`, so a result that outlives it is a copy. `{u}` prints the URL without its password.

## Print a table

Print rows of a struct in aligned columns.

```mk
// verdict: ok
struct Row {
    export name: str
    export qty: int
    export price: f64
}

export Main(sys: &System): WriteError!void {
    rows := [Row(name: "bolt", qty: 12, price: 0.25), Row(name: "washer", qty: 300, price: 0.05)]
    try sys.Print("{h:<12}{q:>6}{p:>10}\n", (h: "name", q: "qty", p: "price"))
    for r in rows[..] {
        try sys.Print("{r.name:<12}{r.qty:>6}{r.price:>10.2}\n", (r))
    }
}
```

```text
name           qty     price
bolt            12      0.25
washer         300      0.05
```

- A hole may name a field path, `{r.name}`, so a struct prints by its exported fields with no `Format` of its own.
- The spec after `:` sets the layout: `<12` pads to 12 columns aligned left, `>6` aligns right, and `.2` prints two decimals.

## Implement Format for your own type

Make your type print with `{p}` in any format string.

```mk
// verdict: ok
struct Vec2 {
    x: i32
    y: i32
}

impl Format for Vec2 {
    Format(&self, w: mut Writer): WriteError!void {
        try w.Str("(")
        try self.x.Format(w)
        try w.Str(", ")
        try self.y.Format(w)
        try w.Str(")")
    }
}

export Main(sys: &System): WriteError!void {
    p := Vec2(x: 3, y: -1)
    try sys.Print("p = {p}\n", (p)) // p = (3, -1)
}
```

- `Format` writes the value to a `Writer`. Each write can fail, so each is a `try`.
- `self.x.Format(w)` prints a field the way a `{x}` hole would.

## Parse command-line options

Describe the options as a struct and let the compiler write the parser and the help text.

```mk
// verdict: ok
import std.cli
import std.proc

struct Options {
    /// Number each line
    export numbers: bool
    /// Stop after this many lines
    export limit: ?int
    /// Files to read
    export files: []str
}

gen cli.Args(Options)

export Main(sys: &System): WriteError!void {
    opts := Options.Parse(sys.arena, sys.args[1..]) else {
        var err := sys.Stderr()
        try Options.Usage(mut err)
        proc.Exit(2)
    }
    for f in opts.files {
        try sys.Print("{f}\n", (f))
    }
}
```

```text
$ mk run cat.mk -- --numbers a.txt b.txt
a.txt
b.txt
$ mk run cat.mk -- --bogus
Options:
  --numbers      Number each line
  --limit <int>  Stop after this many lines
Arguments:
  <files>...     Files to read
```

- `gen cli.Args(T)` writes `T.Parse` and `T.Usage` from the exported fields. The `///` comments are the help text.
- A `bool` is a flag. `int` and `str` take a value, `?T` is optional, and `[]str` holds the remaining arguments.
- A field `dry_run` is the option `--dry-run`, written `--dry-run 3` or `--dry-run=3`. There are no short options.
- Add `export help: bool` and `--help` sets it and skips the required options, so the program can call `Usage` and exit 0.

## Report errors with a line number

Fail with an error that says where the input was wrong.

```mk
// verdict: ok
error BadLine(line: int)
error Missing

type ConfigError = BadLine | Missing

ParsePort(text: str): ConfigError!int {
    var n := 0
    for line in text.Lines() {
        n += 1
        if line.StartsWith("port=") {
            value := line[5..].ParseInt() else {
                fail BadLine(line: n)
            }
            return Trunc[int](value)
        }
    }
    fail Missing
}
```

- An error's payload is plain data, such as a line number. Printing the error prints it: `BadLine(line: 3)`.
- You declare an error set with `type`. A `try` needs the callee's set to be part of the caller's.
- `else { ... }` handles a failed parse in place.

## Use one arena per request

Give each request its own arena and release everything it allocated at once.

```mk
// verdict: ok
import std.net

Serve(conn: net.Stream, heap: &Heap, io: Io) {
    defer conn.Close()
    var arena := Arena.New(heap) else {
        return
    }
    defer arena.Free()
    var buf: [4096]u8 = [0; 4096]
    var input := conn.Reader(io, mut buf[..])
    for {
        read := input.Line(&arena) else {
            return
        }
        line := read else {
            return // the client closed the connection
        }
        reply := arena.Fmt("{n} bytes\n", (n: line.Len())) else {
            return
        }
        conn.WriteAll(io, reply.AsBytes()) else {
            return
        }
        arena.Reset() // frees the line and the reply
    }
}
```

- `Reset` frees everything the request allocated, here the line and the reply. It is rejected while any borrow from the arena is still live.
- `defer arena.Free()` returns the arena's memory to the heap when `Serve` returns.
- To serve many connections at once, run `Serve` in a task per connection, as in [Serve each connection in its own task](#serve-each-connection-in-its-own-task).

## Pass capabilities together in a struct

When several capabilities travel together, put them in a struct.

```mk
// verdict: ok
struct Ctx {
    arena: &Arena
    io: Io
}

Greeting(c: &Ctx, name: str): OutOfMemory!str from c.arena {
    return try c.arena.Fmt("hello {name}", (name))
}
```

- The result comes from the arena held in `c`. `name` could also supply a `str`, so the `from c.arena` clause says which.

## Return heap-owned data that outlives an arena

Build a list on the heap and hand it to the caller.

```mk
// verdict: ok
Squares(a: &Alloc, n: int): OutOfMemory!HeapList[int] {
    var out := HeapList[int].New(a)
    onerr out.Free(a)
    for i in 0..n {
        try out.Push(a, i * i)
    }
    return out
}

export Main(sys: &System): OutOfMemory!void {
    a := sys.heap.Alloc()
    xs := try Squares(a, 10)
    defer xs.Free(a)
    Assert(xs[3] == 9)
}
```

- `HeapList` is linear: someone must `Free` it, and the compiler rejects a path that drops it.
- `onerr` frees the list when a push fails partway. On success the caller owns it.

## Write a generic container

A binary min-heap over any ordered plain type.

```mk
// verdict: ok
struct MinHeap[T: Plain + Ord] {
    items: List[T]
}

MinHeap[T].New(a: &Arena): MinHeap[T] {
    return MinHeap[T](items: List[T].New(a))
}

MinHeap[T].Push(mut self, x: T into self): OutOfMemory!void {
    try self.items.Push(x)
    var i := self.items.Len() - 1
    for i > 0 {
        parent := (i - 1) / 2
        if !self.items[i].Less(&self.items[parent]) {
            break
        }
        self.items.Swap(i, parent)
        i = parent
    }
}
```

- Bounds sit on the type, `T: Plain + Ord`. Methods name its parameters: `MinHeap[T].Push`.
- The `List` holds its arena, so `Push` needs no allocator.
- The order comes from the `Ord` bound. A type cannot take a `const` `fn` parameter; a function can, as `Slice[T].Sort` does.

## Write a heap-owned generic container

A small LRU cache that owns its entries on the heap.

```mk
// verdict: ok
struct Entry[K: Plain + Eq, V: Plain] {
    k: K
    v: V
}

struct Lru[K: Plain + Eq, V: Plain] {
    capacity: int
    entries: HeapList[Entry[K, V]]
}

Lru[K, V].New(a: &Alloc, capacity: int): Lru[K, V] {
    return Lru[K, V](capacity: capacity, entries: HeapList[Entry[K, V]].New(a))
}

Lru[K, V].Put(mut self, a: &Alloc, e: Entry[K, V] into self): OutOfMemory!void {
    if self.entries.Len() == self.capacity {
        _ = self.entries.Remove(0)
    }
    try self.entries.Push(a, e)
}

Lru[K, V].Get(&self, k: &K): ?&V {
    for e in &self.entries {
        if e.k.Eq(k) {
            return some(&e.v)
        }
    }
    return none
}

impl[K: Plain + Eq, V: Plain] Free for Lru[K, V] {
    Free(self, a: &Alloc) {
        Lru(entries, ..) := self
        entries.Free(a)
    }
}

test "evicts the oldest" (sys: &System) {
    a := sys.heap.Alloc()
    var c := Lru[int, int].New(a, 1)
    defer c.Free(a)
    try c.Put(a, Entry[int, int](k: 1, v: 10))
    try c.Put(a, Entry[int, int](k: 2, v: 20))
    Assert(c.Get(&1).IsNone())
}

test "a list of caches frees each cache" (sys: &System) {
    a := sys.heap.Alloc()
    var ls := HeapList[Lru[int, int]].New(a)
    defer ls.Free(a)
    var c := Lru[int, int].New(a, 2)
    onerr c.Free(a)
    try ls.Push(a, c)
}
```

- Every call that allocates takes the allocator `a`, and every path releases what it took.
- A type releases what it owns with `impl Free`. A `HeapList` of caches then frees each cache.
- `Lru(entries, ..) := self` takes the struct apart; `..` skips the plain fields.

## Link tree nodes to their parents

Build a tree in an arena where each node can reach its parent.

```mk
// verdict: ok
struct Node {
    name: str
    parent: Cell[?&Node]
    first: Cell[?&Node]
}

AddChild(a: &Arena into parent, parent: &Node into a, name: str into a): OutOfMemory!&Node from a {
    child := try a.Put(Node(name: name, parent: Cell.New(some(parent)), first: Cell.New(none)))
    parent.first.Store(some(&*child))
    return &*child
}
```

- `Cell` allows the back edge: a node is shared and still updated.
- `a.Put` returns `mut T`, and `&*child` shares it.
- The signature states what happens: the child goes into the parent, and the parent goes into the arena. Freeing the arena ends every loan in it, cycles included.

## Build an expression tree in an arena

Allocate every node of a tree in one arena and free them together.

```mk
// verdict: ok
enum Expr {
    Num(v: i64)
    Add(l: &Expr, r: &Expr)
}

const NoTerms: []i64 = []
const Terms: [3]i64 = [1, 2, 3]

Chain(a: &Arena, terms: []i64): OutOfMemory!&Expr from a {
    var e: &Expr = &*try a.Put(Expr.Num(v: 0))
    for t in terms {
        leaf := try a.Put(Expr.Num(v: t))
        e = &*try a.Put(Expr.Add(l: e, r: &*leaf))
    }
    return e
}

Eval(e: &Expr): i64 {
    return match *e {
        .Num(v) { v }
        .Add(l, r) { Eval(l) + Eval(r) }
    }
}

test "a chain adds its terms" (sys: &System) {
    var a := try Arena.New(sys.heap)
    defer a.Free()
    Assert(Eval(try Chain(&a, Terms[..])) == 6)
    Assert(Eval(try Chain(&a, NoTerms)) == 0)
}
```

- A node in an arena may hold references to other nodes of the same arena. That needs no clause, since they all end with the arena.
- The test builds the tree in a local arena, which ends when the test returns.
- An empty slice is `[]` only in a `const` initializer (E0711 elsewhere), so the test names a `const`.

## Sort with a comparison that reads locals

Sort one slice by values held in another.

```mk
// verdict: ok
struct Entry {
    word: str
    count: int
}

ByCount(order: mut []int, entries: []Entry) {
    order.SortStableInPlace(fn(a: &int, b: &int): bool {
        return entries[*a].count > entries[*b].count
    })
}

Ascending(xs: mut []i32) {
    xs.Sort(fn(a: &i32, b: &i32): bool { return *a < *b })
}
```

- A `fn` literal passed as a `const` argument, as `Sort`'s comparison is, can read the caller's locals.
- It cannot write them and cannot be stored. To keep a callback, see the next recipe.

## Keep a callback for later

Store a function together with the state it needs.

```mk
// verdict: ok
struct Threshold {
    limit: i32
}

struct Filter {
    keep: fn(&Threshold, i32): bool
    state: Threshold
}

Above(t: &Threshold, x: i32): bool {
    return x > t.limit
}

Filter.Count(&self, xs: []i32): int {
    var n := 0
    for x in xs {
        if self.keep(&self.state, x) {
            n += 1
        }
    }
    return n
}

CountAbove(xs: []i32, limit: i32): int {
    f := Filter(keep: Above, state: Threshold(limit: limit))
    return f.Count(xs)
}
```

- A `fn` literal that captures locals cannot be stored (E0609).
- Store a named function whose first parameter takes the state by reference, and store the plain state beside it.
- For an open set of types behind one interface, use `gen dyn.Of(I)` instead.

## Queue jobs whose states have different types

Keep a list of jobs, each with state of its own type.

```mk
// verdict: ok
import std.dyn

struct Job {
    run: fn(mut dyn.Ref): int
    state: dyn.Ref
}

struct Counter {
    n: int
}

struct Greeting {
    times: int
}

Bump(r: mut dyn.Ref): int {
    c := r.Mut[Counter]()
    c.n += 1
    return c.n
}

Twice(r: mut dyn.Ref): int {
    return r.Get[Greeting]().times * 2
}

RunJobs(a: &Arena): OutOfMemory!int {
    var jobs := List[Job].New(a)
    c := try a.Put(Counter(n: 41))
    try jobs.Push(Job(run: Bump, state: dyn.Ref.Of(c)))
    g := try a.Put(Greeting(times: 3))
    try jobs.Push(Job(run: Twice, state: dyn.Ref.Of(g)))
    var total := 0
    for j in mut jobs {
        total += j.run(mut j.state)
    }
    return total
}
```

- `dyn.Ref.Of` erases the state's type. `Get` and `Mut` give it back only as the type it was made from.
- The queue holds loans of the states, so they live in the same arena as the queue.

## Serve each connection in its own task

Accept connections and echo each one in a task of its own.

```mk
// verdict: ok
import std.net

Echo(conn: net.Stream, io: Io) {
    defer conn.Close()
    var buf: [4096]u8 = [0; 4096]
    for {
        n := conn.Read(io, mut buf[..]) else {
            return
        }
        if n == 0 {
            return
        }
        conn.WriteAll(io, buf[..n]) else {
            return
        }
    }
}

export Main(sys: &System): net.Error!void {
    listener := try net.Listen(sys.io, "0.0.0.0:8080")
    defer listener.Close()
    scope s := sys.io.Scope() {
        for {
            conn := try listener.Accept(sys.io)
            s.go Echo(conn, sys.io)
        }
    }
}
```

- Every task belongs to a `scope`. The block ends only when all its tasks have finished.
- `s.go` moves `conn` into the task.
- `&Heap` can cross into tasks; an arena cannot. A task that allocates makes its own arena.

## Shut down on a signal

Stop accepting connections on Ctrl-C and let the open ones finish.

```mk
// verdict: ok
import std.net
import std.proc

Watch(s: &Scope, io: Io) {
    proc.WaitSignal(io, proc.Signal.Interrupt) else {
        return
    }
    s.Cancel()
}

Echo(conn: net.Stream, io: Io) {
    defer conn.Close()
    var buf: [4096]u8 = [0; 4096]
    for {
        n := conn.Read(io, mut buf[..]) else {
            return
        }
        if n == 0 {
            return
        }
        conn.WriteAll(io, buf[..n]) else {
            return
        }
    }
}

export Main(sys: &System): net.Error!void {
    listener := try net.Listen(sys.io, "0.0.0.0:8080")
    defer listener.Close()
    scope s := sys.io.Scope() {
        s.go Watch(&s, sys.io)
        for {
            conn := listener.Accept(sys.io) else {
                break
            }
            s.go Echo(conn, sys.io)
        }
        s.Cancel()
    }
}
```

- `s.Cancel()` reaches the block's own body too, so `Accept` fails with `Canceled`, a normal error, and the loop ends.
- The tasks finish before the block exits.
- The `s.Cancel()` after the loop covers an accept that fails for another reason. Without it, the block would wait for `Watch` forever.

## Handle errors inside tasks

Run work that can fail in tasks, and report each failure after they finish.

```mk
// verdict: ok
import std.fs

Stamp(io: Io, path: str): fs.Error!void {
    try fs.WriteAll(io, path, b"done\n")
}

StampTask(io: Io, path: str, failed: mut ?fs.Error) {
    Stamp(io, path) else e {
        *failed = some(e)
    }
}

export Main(sys: &System): WriteError!void {
    var a: ?fs.Error = none
    var b: ?fs.Error = none
    scope s := sys.io.Scope() {
        s.go StampTask(sys.io, "a.stamp", mut a)
        s.go StampTask(sys.io, "b.stamp", mut b)
    }
    if e := a {
        try sys.PrintErr("a.stamp: {e}\n", (e))
    }
    if e := b {
        try sys.PrintErr("b.stamp: {e}\n", (e))
    }
}
```

- A task under `io.Scope()` returns `void`. Write the work as a function returning `E!void`, and spawn a `void` wrapper.
- A task cannot print through `sys`: `&System` holds an arena and is not `Share` (E1002). The wrapper keeps the error in a slot the parent owns.
- The parent reads the slots after the block, when every task has finished. To fail the whole group on the first error, use `io.ScopeOf[E]()`, as in [Run tasks with a deadline](#run-tasks-with-a-deadline).

## Share state between tasks

Keep one word count that every connection updates.

```mk
// verdict: ok
import std.net

Bump(a: &Alloc, io: Io, counts: &Mutex[TextMap[int]], word: str): Canceled|OutOfMemory!int {
    var g := try counts.Lock(io)
    defer g.Unlock()
    n := try g.Mut().Ensure(a, word, 0)
    *n += 1
    return *n
}

Serve(conn: net.Stream, heap: &Heap, io: Io, counts: &Mutex[TextMap[int]]) {
    defer conn.Close()
    arena := Arena.New(heap) else {
        return
    }
    defer arena.Free()
    a := heap.Alloc()
    var buf: [4096]u8 = [0; 4096]
    var input := conn.Reader(io, mut buf[..])
    for {
        line := input.Line(&arena) else {
            return
        }
        word := line else {
            return
        }
        n := Bump(a, io, counts, word) else {
            return
        }
        reply := arena.Fmt("{n}\n", (n)) else {
            return
        }
        conn.WriteAll(io, reply.AsBytes()) else {
            return
        }
    }
}

export Main(sys: &System): net.Error!void {
    a := sys.heap.Alloc()
    counts := Mutex.New(TextMap[int].New(a))
    defer counts.Into().Free(a)
    listener := try net.Listen(sys.io, "0.0.0.0:9000")
    defer listener.Close()
    scope s := sys.io.Scope() {
        for {
            conn := listener.Accept(sys.io) else {
                break
            }
            s.go Serve(conn, sys.heap, sys.io, &counts)
        }
    }
}
```

- A `Map` lives in an arena, so it stays on one task. Shared data is owned by the heap and sits behind a `Mutex`.
- `Lock` can fail with `Canceled`, so a task waiting for the lock still stops when its scope is cancelled.
- Each task makes its own arena for its own temporary data.

## Process a slice in parallel

Scale every element of a large slice, one piece per task.

```mk
// verdict: ok
Scale(xs: mut []f64, k: f64) {
    for x in mut xs {
        *x *= k
    }
}

ScaleAll(io: Io, xs: mut []f64, k: f64) {
    scope s := io.Scope() {
        for piece in xs.ChunksMut(4096) {
            s.go Scale(piece, k)
        }
    }
}
```

- `ChunksMut` hands out disjoint `mut` pieces, and each goes to its own task.
- The pieces hold the loan on `xs`, so nothing else touches it until the scope ends. This is the parallel `for` loop of Mk.

## Time out a read, an idle connection and a retry

Give one operation a time limit, close a connection that stays idle, and retry a dial until a deadline.

```mk
// verdict: ok
import std.net
import std.time

ReadFor(io: Io, conn: &net.Stream, buf: mut []u8, d: time.Duration): net.Error!int {
    var n := 0
    scope s := io.Scope() {
        s.CancelAfter(d)
        n = conn.Read(io, buf) else e {
            if s.Expired() {
                fail time.TimedOut
            }
            fail e
        }
    }
    return n
}

Idle(io: Io, conn: &net.Stream, buf: mut []u8): net.Error!void {
    for {
        n := try ReadFor(io, conn, buf, time.Seconds(30))
        if n == 0 {
            return
        }
    }
}

Connect(io: Io, addr: str): net.Error!net.Stream {
    var wait := time.Millis(100)
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(10))
        for {
            c := net.Dial(io, addr) else {
                if s.Expired() {
                    fail time.TimedOut
                }
                io.Sleep(wait) else e {
                    if s.Expired() {
                        fail time.TimedOut
                    }
                    fail e
                }
                wait = time.Nanos(wait.Nanos() * 2)
                continue
            }
            return c
        }
    }
}
```

- A deadline belongs to a scope: `CancelAfter` cancels the scope when the time runs out. Open one scope per wait.
- `Expired()` tells a timeout from another cancellation, so you can fail with `time.TimedOut`.
- In `Connect`, one deadline bounds the whole retry loop, sleeps included.

## Run tasks with a deadline

Give a group of tasks one deadline, and find out afterwards whether it was missed.

```mk
// verdict: ok
import std.fs
import std.time

type JobError = fs.Error | time.TimedOut

Touch(io: Io, path: str) {
    fs.WriteAll(io, path, b"x") else {
        return
    }
}

TouchAll(io: Io, paths: []str): bool {
    var late := false
    scope outer := io.Scope() {
        outer.CancelAfter(time.Seconds(2))
        scope inner := io.Scope() {
            for p in paths {
                inner.go Touch(io, p)
            }
        }
        late = outer.Expired()
    }
    return !late
}

Job(s: &Scope[JobError], io: Io, path: str): JobError!void {
    fs.WriteAll(io, path, b"x") else e {
        if s.Expired() {
            fail time.TimedOut
        }
        fail e
    }
}

RunAll(io: Io, paths: []str): JobError!void {
    try scope s := io.ScopeOf[JobError]() {
        s.CancelAfter(time.Seconds(2))
        for p in paths {
            s.go Job(&s, io, p)
        }
    }
}
```

There are two ways to see the deadline after the tasks finish:

- `TouchAll` puts the deadline on an outer scope and the tasks in an inner one, then reads `Expired()` after the inner block.
- `RunAll` uses `ScopeOf[E]`, whose tasks return `E!void`. Each task reports `time.TimedOut` when its scope has expired. The first error cancels the other tasks, and `try scope` returns it.

## Finish a write during shutdown, with a time limit

Save state while the program is being cancelled, but give up after two seconds.

```mk
// verdict: ok
import std.fs
import std.time

SaveOnExit(io: Io, path: str, data: []u8): fs.Error!void {
    q := io.NoCancel()
    scope s := q.Scope() {
        s.CancelAfter(time.Seconds(2))
        try fs.WriteAll(q, path, data)
    }
}
```

- `NoCancel` gives an `Io` that ignores the cancellation of the caller's scope.
- A scope opened from it is cancelled only by its own deadline.

## Report from many tasks

A `log.Sink` is shared by tasks with no lock of the caller's, writes each record whole, and still writes while the scope is being cancelled.

```mk
// verdict: ok
import std.fs
import std.log

Clean(io: Io, sink: &log.Sink, path: str) {
    fs.Remove(io, path) else e {
        sink.Warn(io, "{path}: {e}", (path, e)) else {
            return
        }
    }
}

export Main(sys: &System) {
    sink := log.Stderr(log.Level.Info)
    scope s := sys.io.Scope() {
        for p in sys.args[1..] {
            s.go Clean(sys.io, &sink, p)
        }
    }
}
```

- Any number of tasks can share one sink. Each record is written whole, so records of different tasks never interleave.
- `log.Stderr(log.Level.Info)` drops records below `Info`.

## Serve HTTP with routes

Route requests by method and path to handler functions.

```mk
// verdict: ok
import std.http

struct App {
    greeting: str
}

Hello(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    name := req.Param("name") else {
        fail http.Status(code: 400)
    }
    var w := res.Writer()
    try Print(mut w, "{greeting}, {name}\n", (greeting: c.greeting, name))
}

Health(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"ok\n")
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/hello/{name}", handler: Hello),
    http.Route[App](method: "GET", pattern: "/health", handler: Health),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

export Main(sys: &System): http.Error!void {
    app := App(greeting: "hello")
    try http.Serve(sys, "127.0.0.1:8080", 1_000_000, &app, Handle)
}
```

- The route table is a `const`, so every connection's task can read it.
- Each request gets its own arena, reset after the reply. The response copies what you give it into that arena.
- A handler fails with `http.Status` to send an error status.

## Restart workers that panic

Run several worker processes on one listener, and replace any that panics.

```mk
// verdict: ok
import std.net
import std.proc

Worker(sys: &System, l: &net.Listener): net.Error!void {
    for {
        conn := try l.Accept(sys.io)
        defer conn.Close()
        try conn.WriteAll(sys.io, b"hello\n")
    }
}

export Main(sys: &System): net.Error|proc.Error!void {
    l := try net.Listen(sys.io, "127.0.0.1:8080")
    defer l.Close()
    try proc.Supervise[net.Error](sys, 4, &l, Worker)
}
```

- `proc.Supervise` runs the program again in each worker, which inherits the listener. A worker that panics is restarted, and the others keep serving.
- Everything before `Supervise` runs again in each worker, so keep it to setup that can repeat.
- The type argument is the worker's error set.

## Download a large file over HTTP

Stream a response body to a file without holding it in memory.

```mk
// verdict: ok
import std.fs
import std.http
import std.url

const NoHeaders: []http.Header = []
const NoBody: []u8 = []

Download(c: &http.Client, a: &Arena, io: Io, link: str into a, path: str): http.Error|url.Error|fs.Error!void {
    u := try url.Parse(a, link, none)
    req := http.ClientRequest(method: "GET", url: &u, headers: NoHeaders, body: NoBody)
    var x := try c.Do(a, io, &req)
    onerr x.Abandon()
    var f := try fs.Create(io, path)
    onerr f.Abandon()
    var buf: [65536]u8 = [0; 65536]
    var body := x.Body(mut buf[..])
    var w: [65536]u8 = [0; 65536]
    for {
        n := try body.Read(mut w[..])
        if n == 0 {
            break
        }
        try f.WriteAll(io, w[..n])
    }
    try f.Close(io)
    try x.Close(io)
}
```

- `Client.Do` streams the body through a reader, so a download need not fit the size limit of `Fetch`.
- `x.Close` returns the connection to the client's pool. On failure, `onerr` abandons it instead.
- One `Client` serves many tasks.

## Make an HTTPS request

Fetch a page over TLS.

```mk
// verdict: ok
import std.http
import std.tls
import std.url

const NoHeaders: []http.Header = []
const NoBody: []u8 = []

Get(h: &Heap, a: &Arena, io: Io, link: str into a): http.Error|url.Error!http.Reply from a {
    u := try url.Parse(a, link, none)
    cfg := tls.DefaultConfig
    var limits := http.DefaultClientLimits
    limits.secure = some(tls.Connector(&cfg))
    c := http.Client.New(h, limits)
    req := http.ClientRequest(method: "GET", url: &u, headers: NoHeaders, body: NoBody)
    reply := c.Fetch(a, io, &req) else e {
        c.Close()
        fail e
    }
    c.Close()
    return reply
}
```

- `std.http` does not import `std.tls`. A client gets `https` when its limits name a `tls.Connector`; without one, an `https` URL fails with `NoSecureTransport`.
- `tls.DefaultConfig` checks the server's certificate against the system's roots.
- `std.tls` is experimental.

## Run a program and read its output

Run a command, wait for it, and print what it wrote.

```mk
// verdict: ok
import std.proc

export Main(sys: &System): proc.Error|WriteError!void {
    args := ["git", "rev-parse", "HEAD"]
    r := try proc.Run(sys.arena, sys.io, args[0..], b"", 1_000_000)
    if r.code != 0 {
        var err := sys.Stderr()
        try err.Bytes(r.stderr)
        proc.Exit(r.code)
    }
    var out := sys.Stdout()
    try out.Bytes(r.stdout)
}
```

- `proc.Run` waits for the child and returns its exit code with what it wrote, up to the limit you pass. The fourth argument is its standard input.
- An array literal is an array. Slice it, `args[0..]`, to pass a `[]str`.

## Talk to a running child, build a pipeline, and kill on timeout

Feed a child's input while reading its output, connect two children with a pipe, and stop a child that runs too long.

```mk
// verdict: ok
import std.proc
import std.time

CloseEnds(i: ?proc.PipeWriter, o: ?proc.PipeReader, e: ?proc.PipeReader) {
    if w := i {
        w.Close()
    }
    if r := o {
        r.Close()
    }
    if r := e {
        r.Close()
    }
}

Feed(io: Io, w: proc.PipeWriter, input: []u8) {
    w.WriteAll(io, input) else {
        w.Close()
        return
    }
    w.Close()
}

Forward(io: Io, child: &proc.Child) {
    for {
        proc.WaitSignal(io, proc.Signal.Interrupt) else {
            return
        }
        child.Signal(proc.Signal.Interrupt) else {
            return
        }
    }
}

Live(sys: &System, args: []str, input: []u8): proc.Error|WriteError!proc.Status {
    io := sys.io
    var c := proc.DefaultCommand
    c.args = args
    c.stdin = proc.Stdio.Pipe
    c.stdout = proc.Stdio.Pipe
    c.group = true
    (child, stdin, stdout, stderr) := try proc.Spawn(sys.arena, io, &c)
    onerr child.Detach()
    CloseEnds(none, none, stderr)
    r := stdout.Must()
    onerr r.Close()
    var out := sys.Stdout()
    var buf: [4096]u8 = [0; 4096]
    scope s := io.Scope() {
        s.go Feed(io, stdin.Must(), input)
        s.go Forward(io, &child)
        for {
            n := try r.Read(io, mut buf[..])
            if n == 0 {
                break
            }
            try out.Bytes(buf[..n])
            try out.Flush()
        }
        s.Cancel()
    }
    r.Close()
    return try child.Wait(io)
}

Pipeline(a: &Arena, io: Io, first: []str, second: []str): proc.Error!proc.Status {
    (r, w) := try proc.Pipe(io)
    onerr r.Close()
    onerr w.Close()
    var c1 := proc.DefaultCommand
    c1.args = first
    c1.stdout = proc.Stdio.Writer(w: &w)
    (child: p1, stdin: i1, stdout: o1, stderr: e1) := try proc.Spawn(a, io, &c1)
    onerr p1.Detach()
    CloseEnds(i1, o1, e1)
    w.Close()
    var c2 := proc.DefaultCommand
    c2.args = second
    c2.stdin = proc.Stdio.Reader(r: &r)
    (child: p2, stdin: i2, stdout: o2, stderr: e2) := try proc.Spawn(a, io, &c2)
    onerr p2.Detach()
    CloseEnds(i2, o2, e2)
    r.Close()
    _ = try p1.Wait(io)
    return try p2.Wait(io)
}

Reap(io: Io, child: proc.Child): ?proc.Status {
    child.Kill() else {
        child.Detach()
        return none
    }
    q := io.NoCancel()
    scope s := q.Scope() {
        s.CancelAfter(time.Seconds(5))
        st := child.Wait(q) else {
            child.Detach()
            return none
        }
        return some(st)
    }
}

RunFor(a: &Arena, io: Io, args: []str, limit: time.Duration): proc.Error|time.TimedOut!proc.Status {
    var c := proc.DefaultCommand
    c.args = args
    c.group = true
    (child, stdin, stdout, stderr) := try proc.Spawn(a, io, &c)
    CloseEnds(stdin, stdout, stderr)
    scope s := io.Scope() {
        s.CancelAfter(limit)
        st := child.Wait(io) else e {
            _ = Reap(io, child)
            if s.Expired() {
                fail time.TimedOut
            }
            fail e
        }
        return st
    }
}
```

- `Spawn` returns the child and the pipe ends as your own values, which you must close. `Wait` consumes the child when it returns a status; `Detach` lets it go.
- Read a pipe while the child runs, and close `stdin` once the input is written. A child that reads to the end of its input otherwise waits forever.
- In a pipeline, close the end you lent to one child before you wait on the other.
- On Linux a child in its own group (`c.group = true`) gets Ctrl+C only when the parent forwards it, as `Forward` does; see [process groups](std/proc.md#process-groups).

## Read keys from the terminal

Put the terminal in raw mode and handle one event at a time.

```mk
// verdict: ok
import std.term

EventLoop(sys: &System): term.Error|WriteError!void {
    io := sys.io
    var t := try term.Open(io)
    onerr t.Close(io)
    try t.Raw(io)
    var events := try Arena.New(sys.heap)
    onerr events.Free()
    var buf: [1024]u8 = [0; 1024]
    var keys := 0
    for {
        events.Reset()
        ev := try t.Read(&events, io)
        done := match ev {
            .Key(key, ch, mods) {
                match key {
                    .Escape { true }
                    _ { mods.ctrl && ch == 'q' }
                }
            }
            _ { false }
        }
        if done {
            break
        }
        keys += 1
        var w := t.Writer(io, mut buf[..])
        try term.Clear(mut w)
        try term.MoveTo(mut w, 1, 1)
        try Print(mut w, "{keys} events; Esc quits\r\n", (keys))
        try w.Flush()
    }
    try t.Restore(io)
    t.Close(io)
    events.Free()
}
```

- `term.Open` opens the terminal itself, even when standard input is a pipe.
- `Read` returns keys, pastes and resizes as one `Event`. The events live in an arena reset on each turn.
- `Restore` ends raw mode. If the program panics, exits or gets a fatal signal first, the runtime restores the mode.

## Write a test and a property

Put tests beside the code, and let the test runner check a function's contract with generated inputs.

```mk
// verdict: ok
Clamp(x: i32, lo: i32, hi: i32): i32
    requires lo <= hi
    ensures result >= lo && result <= hi
{
    if x < lo {
        return lo
    }
    if x > hi {
        return hi
    }
    return x
}

test "clamp" {
    Assert(Clamp(5, 0, 3) == 3)
}
```

- `mk test` runs every `test` block. A failed `Assert` fails the test.
- `requires` states what callers must pass, and `ensures` what the function promises, with `result` naming its return value.
- `mk test --prop` calls `Clamp` with generated inputs that meet `requires` and checks `ensures` on each.

## Test crash safety under simulation

Check that a write is either complete or absent after a crash at any point.

```mk
// verdict: ok
import std.fs
import std.sim

Put(io: Io, key: str, value: str): fs.Error!void {
    var f := try fs.Create(io, key)
    onerr f.Abandon()
    try f.WriteAll(io, value.AsBytes())
    try f.Sync(io)
    try f.Close(io)
}

test "a write is whole or absent after a crash" (sys: &System) {
    for seed in 0..100 {
        var s := try sim.Sim.New(sys.arena, Cast[u64](seed))
        defer s.Free()
        s.CrashAfterRandomOps()
        Put(s.Io(), "key", "value") else {
        }
        io := s.Restart()
        text := fs.ReadText(sys.arena, io, "key") else {
            continue
        }
        Assert(text == "value" || text == "")
    }
}
```

- A `Sim` is an `Io` over a simulated disk and clock. The code under test takes an `Io`, so it runs unchanged.
- The same seed gives the same run, so a failing seed replays exactly.
- `CrashAfterRandomOps` stops the run at a point drawn from the seed. `Restart` gives the next run an `Io` over what was synced.

## Write a vector kernel once and pick it by CPU

Write a SIMD sum generic over its width, then choose the widest version the CPU supports.

```mk
// verdict: ok
import std.simd

SumN[const N: int](xs: []f32): f32
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 {
    var acc := simd.Vec[f32, N].Zero()
    var i := 0
    for i + N <= xs.Len() {
        acc = acc + simd.Vec[f32, N].FromSlice(xs, i)
        i += N
    }
    var total := acc.ReduceSum()
    for i < xs.Len() {
        total += xs[i]
        i += 1
    }
    return total
}

Sum8(xs: []f32): f32 cpu "x86-64-v3" {
    return SumN[8](xs)
}

Sum4(xs: []f32): f32 {
    return SumN[4](xs)
}

Sum(xs: []f32): f32 {
    var f: fn([]f32): f32 = Sum4
    unsafe "Dispatch returns Sum8 only when Cpu() has x86-64-v3, its clause" {
        choices := [simd.Choice[fn([]f32): f32](needs: simd.CpuFeatures.Of("x86-64-v3"), f: Sum8)]
        f = simd.Dispatch(choices[0..], Sum4)
    }
    return f(xs)
}
```

- Give each width a plain function with its `cpu` clause. The instance `SumN[8]` that `Sum8` calls is compiled for `x86-64-v3`.
- The generic restates the lane counts that the `Vec` functions it calls accept, in its `requires` clause.
- Naming `Sum8` as a value needs `unsafe`, and the reason says what guards it. The module must be listed in `unsafe_modules` in `pkg.mk`.
- `simd.Dispatch` reads the CPU once, so a call costs a few compares.
- Each width adds in the same order, so the result can differ between widths only by rounding, never between CPUs at one width.

## Write a kernel that widens its lanes

Scale `u8` samples by a gain, widening to `u16` lanes for the arithmetic.

```mk
// verdict: ok
import std.math
import std.simd

GainN[const N: int, const M: int](src: []u8, dst: mut []u8, gain: u16)
    requires N == 16 || N == 32 || N == 64
    requires M == N / 2
{
    g := simd.Vec[u16, M].Splat(gain)
    var i := 0
    for i + N <= src.Len() {
        v := simd.Vec[u8, N].FromSlice(src, i)
        lo := (v.WidenLo[u16, M]() << 8).MulHigh(g)
        hi := (v.WidenHi[u16, M]() << 8).MulHigh(g)
        lo.NarrowSat[u8, N](hi).CopyTo(dst, i)
        i += N
    }
    for j in i..src.Len() {
        dst[j] = Trunc[u8](math.Min(int(src[j]) * int(gain) >> 8, 255))
    }
}

Gain(src: []u8, dst: mut []u8, gain: u16) {
    GainN[16, 8](src, dst, gain)
}
```

- The kernel has two lane counts, `N` and `M`, and the `simd` functions it calls relate them: `WidenLo` requires `M * 2 == N`.
- List the values of `N` and fix `M` with `requires M == N / 2`. The compiler then knows every pair and checks each call against it, so the kernel restates nothing.
- Write `M == N / 2`, not `M * 2 == N`: the product can overflow, and a clause whose arithmetic overflows does not limit `M`.
- A call with a pair the clauses rule out, such as `GainN[16, 16]`, is E0706.

## Call a function in a Windows DLL

Call `timeGetTime` from `winmm.dll`.

```mk
// verdict: ok
extern timeGetTime(): u32

UptimeMillis(): int {
    var ms := 0
    unsafe "timeGetTime takes nothing and reads a clock" {
        ms = int(timeGetTime())
    }
    return ms
}
```

Name the library in `pkg.mk`:

```text
links: [pkg.Link(name: "winmm.dll", kind: "dynamic", path: "", target: "x86_64-windows")]
```

- Declare the function `extern` and call it inside `unsafe`, with a reason. List the module in `unsafe_modules` in `pkg.mk`.
- mk makes the import library from the DLL's exports, or from a `.def` file the package ships.

## Load a library at run time

Open a system library while the program runs and call a function from it.

```mk
// verdict: ok
import std.dl
import std.raw

ScreenWidth(io: Io): dl.Error!int {
    lib := try dl.Open(io, "user32.dll", dl.Search.System)
    defer lib.Close(io)
    var width := 0
    if p := lib.Symbol("GetSystemMetrics") {
        unsafe "GetSystemMetrics takes and returns an i32, and lib is open" {
            f := raw.FnFromAddr[fn(i32): i32](raw.Addr(p))
            width = int(f(0))
        }
    }
    return width
}
```

- See [std.dl](std/dl.md) for the search rules and what Linux supports.
