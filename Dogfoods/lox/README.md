# lox

A small dynamically typed scripting language in the manner of Lox from Crafting Interpreters: a lexer, a recursive-descent parser, a bytecode compiler, a stack VM with a garbage-collected heap, native functions and a REPL. It shows an interpreter in Mk: an arena AST linked by index, a collector over `mem.Pool`, and natives called through `gen dyn.Of`.

## Running it

From the program's folder:

```
cd Dogfoods/lox
mk run -- cases/scripts/functions.lox
```

```text
6765
nil
20
42
<fn double>
<native fn>
true
liftoff
true
true
42
```

`lox [options] [script]` runs a script; `--eval CODE` runs that code; with neither, it reads entries from standard input, one a line, and runs each in the same VM, so globals and functions persist. A REPL line starting with `:` is a command: `:globals [lo hi]` lists the globals (those in `[lo, hi)` when given), `:forget name` removes one, `:help` lists the commands.

| Option | Meaning |
|---|---|
| `--help` | Print the help and exit |
| `--disasm` | List each compiled function's constants and bytecode before it runs |
| `--stats` | Print heap and collection counts to stderr when done |
| `--profile` | Count the calls of each function and native, and print them to stderr when done |
| `--gc-log` | Print one line to stderr for every collection |
| `--gc-stress` | Collect before every allocation |
| `--max-frames n` | The deepest call nesting (default 256); one more is `Stack overflow.` |
| `--max-objects n` | The most live objects a collection may leave (default: no limit); more is `Out of memory` |
| `--seed n` | The seed of `random()` (default 1) |
| `--define name=value` | Define a global string before the script runs |

Exit codes are clox's (sysexits.h): 0 done, 64 a usage error, 65 a syntax or compile error, 70 a runtime error, 74 a file that cannot be read. A REPL session exits 0 at the end of its input, whatever its entries did, or 74 when standard input fails.

### The language

Numbers (`f64`), strings, booleans, `nil`; `var`, blocks with locals, assignment; `+ - * / %`, comparison, `==`, `!=`, `and`, `or`, `!`, unary `-`; `print`; `if`/`else`, `while`, `for`; `fun` with recursion and functions as values; `return`; lists `[a, b]` with index reads and writes. Natives: `len`, `str`, `num` (by `str.ParseFloat`), `int` (by `str.ParseInt`), `substr`, `chr`, `ord`, `push`, `pop`, `extend`, `random`, `seed`, `gc`, `objects`.

## Design

- The syntax tree lives in arena lists linked by index: a child is an index into `Ast.exprs` or `Ast.stmts`, so building it is a few appends and freeing it is one arena reset.
- The parser recovers in panic mode and resynchronises at the next statement, so one run reports every independent syntax error.
- The program keeps every function's code, lines and constants in program-wide lists, so the REPL keeps adding functions to one `Program`. Constant keys get `Eq`, `Hash` and `Format` from `gen derive`.
- The heap is a `mem.Pool` collected by mark and sweep with an epoch mark, so marks are never cleared; string bytes and list items live in two spaces that each collection compacts.
- Natives implement one interface and are called through the table `gen dyn.Of(Native)` writes; state several natives share sits in a registry of erased references keyed by `erased.Key`.
- The VM keeps suspended frames in a `coll.Deque` and globals in a `coll.BTree`, so `:globals` lists them in name order.

## Limits

- No classes.
- No closures over an enclosing function's locals: a nested function sees its own locals and the globals, and naming an outer local is a compile error.
- At most 256 locals and 255 constants a function.
- Numbers print as Mk's shortest round trip (`1e+21`, `0.30000000000000004`). Errors and traces follow clox's wording; a trace longer than 16 calls shows its 8 innermost and 8 outermost.
- lox runs no tasks and opens no socket; its memory budget is `--max-objects`.

## Tests

The cases in `cases/` run the scripts under `cases/scripts/` against golden output: the language, syntax, compile and runtime errors with their traces, the stack limit, the collector under allocation pressure, the REPL and the command line's errors. Run them from the repository root with `go run ./tools/dogfood run -only lox`.

## Section 18 lines
The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```
str.ParseInt(self): ParseError!i64
str.ParseFloat(self): ParseError!f64
str.Scalars(self): Scalars
f64.Bits(self): u64
f64.FromBits(b: u64): f64
Scalars.Next(mut self): ?char
Slice[T].Sort(mut self, const less: fn(&T, &T): bool)
Slice[T].SortStableInPlace(mut self, const less: fn(&T, &T): bool)
Slice[T].Search[K](&self, key: &K, const less: fn(&T, &K): bool): int
List[T].New(a: &Arena): List[T]
List[T].Push(mut self, x: T into self): OutOfMemory!void
List[T].Pop(mut self): ?T
List[T].Len(&self): int
List[T].Clear(mut self)
List[T].Insert(mut self, i: int, x: T into self): OutOfMemory!void
List[T].Remove(mut self, i: int): T
Map[K, V].New(a: &Arena): Map[K, V]
Map[K, V].Get(&self, k: &K): ?&V
Map[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void
Map[K, V].Ensure(mut self, k: K into self, v: V into self): OutOfMemory!mut V from self
Map[K, V].Len(&self): int
Map[K, V].Iter(&self): MapIter[K, V]
MapIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.map
Set[T].New(a: &Arena): Set[T]
Set[T].Add(mut self, x: T into self): OutOfMemory!bool
Set[T].Has(&self, x: &T): bool
Set[T].Remove(mut self, x: &T): bool
Text.New(a: &Arena): Text
Text.Push(mut self, s: str): OutOfMemory!void
Text.PushChar(mut self, c: char): OutOfMemory!void
Text.Str(&self): str
Text.Clear(mut self)
Text.Writer(mut self): Writer
Arena.New(h: &Heap): OutOfMemory!Arena
Arena.Put[T](&self, x: T into self): OutOfMemory!mut T from self
Arena.Sub(&self): OutOfMemory!Arena
Arena.Free(self) ends self
Reader.Line(mut self, a: &Arena): ReadError|OutOfMemory!?str from a
struct Cell[T: Plain] {
Cell[T].New(x: T): Cell[T]
Cell[T].Load(&self): T
Cell[T].Store(&self, x: T into self)
// std.mem
Pool[T].New(a: &Arena): Pool[T]
Pool[T].Add(mut self, x: T into self): OutOfMemory!Handle
Pool[T].Get(&self, h: Handle): ?&T
Pool[T].GetMut(mut self, h: Handle): ?mut T
Pool[T].Len(&self): int
Pool[T].Iter(&self): PoolIter[T] from self
PoolIter[T].Next(mut self): ?(handle: Handle, value: &T) from self.pool
Pool[T].Sweep(mut self, const keep: fn(Handle, &T): bool): int
Pool[T].GetPair(mut self, h: Handle, k: Handle): ?(a: mut T, b: mut T) from self
// std.coll
Deque[T].New(a: &Arena): Deque[T]
Deque[T].Push(mut self, x: T into self): OutOfMemory!void
Deque[T].Pop(mut self): ?T
Deque[T].Get(&self, i: int): ?&T
Deque[T].Len(&self): int
BTree[K, V].New(a: &Arena): BTree[K, V]
BTree[K, V].Get(&self, k: &K): ?&V
BTree[K, V].GetMut(mut self, k: &K): ?mut V
BTree[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void
BTree[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V
BTree[K, V].Has(&self, k: &K): bool
BTree[K, V].Remove(mut self, k: &K): ?V
BTree[K, V].Len(&self): int
BTree[K, V].Iter(&self): BTreeIter[K, V]
BTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self
BTreeIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.tree
// std.dyn
dyn.Of(g: mut Gen, i: Type)
Ref.Of[T](x: mut T): Ref from x
Ref.Mut[T](mut self): mut T from self
Ref.Get[T](&self): &T from self
Ref.Is[T](&self): bool
// std.dyn.erased
Key[T](): u64
```
