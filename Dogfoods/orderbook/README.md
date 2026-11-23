# orderbook

A limit order book matching engine. It replays a market-data feed, or a synthetic market of any size, through price-time priority matching and reports the book, the trades and statistics as text tables, JSON and CSV. It shows Mk's collections (B-trees, deques, priority queues, pools), CSV and JSON code generation, a memory budget and crash-safe file saves.

## Running it

From the program's folder:

```
cd Dogfoods/orderbook
mk run -- replay cases/data/basic.csv --quiet --band 99.00:101.00 --account 1
```

```text
commands 20 fills 8 checksum e40a9c4c962129d6
account 1 ranks 1 of 8 by volume
```

Without `--quiet`, `replay` prints the full report: counts, VWAP and percentiles, the depth table, the largest and most recent fills, and bars.

```
orderbook replay [options] FEED
orderbook replay [options] --synth N [--seed S]
orderbook synth --synth N [--seed S]
orderbook --help
```

- `replay FEED` replays a feed file; `--synth N --seed S` replays N synthetic commands instead.
- `synth` writes the synthetic feed as CSV.
- `--book PATH` loads an opening book, `--book-out PATH` writes the book before the close in the same format, so one session's closing book opens the next, and `--fills PATH` writes every fill; `-` is standard output.
- `--memory-limit` caps the engine's heap. `orderbook --help` lists every option and the file formats.

Exit status: 0 done, 1 an `--account` that did not trade, 2 a bad command line, 3 a bad feed or book record (`path:line: field N (name) is not valid`), 4 a file that cannot be read or written, 5 out of memory.

### The feed

RFC 4180 CSV, one command a record; a record whose first field starts with `#` is a comment. Prices have at most two decimals.

```
N,ts,acct/serial,B|S,qty,price            limit order
M,ts,acct/serial,B|S,qty                  market order
S,ts,acct/serial,B|S,qty,stop             stop order
T,ts,acct/serial,B|S,qty,stop,limit       stop-limit order
C,ts,acct/serial                          cancel
A,ts,acct/serial,qty,price                amend: new size and price
H,ts,acct                                 halt an account
R,ts,acct                                 resume an account
X,ts                                      bust the last trade
```

### Matching rules

- A limit order trades against the best opposite prices while they cross its limit, oldest order first at each price, at the resting order's price; the rest of it rests.
- A market order's remainder is cancelled; one that finds nothing is refused.
- A stop becomes a market order (`S`) or a limit order (`T`) when the last trade reaches its stop price, and runs before the next feed command; one already reached when it arrives runs at once.
- An amend is a new arrival: it loses its place in the queue.
- A halted account's new orders are refused.
- A bust reverses the last trade's record (log, tape, ranking, volume), not the orders.

## Design

- Orders live in a `mem.Pool` named by `mem.Handle`, with live flags in a `coll.Bitset`. Each side of the book is a `coll.HeapBTree` of price levels whose queues are `coll.HeapDeque`s, so the first key is the best price and the front of its queue the oldest order.
- Stop orders wait in two `coll.PriorityQueue`s; a `coll.Deque` work queue runs the stops a trade triggers before the next feed command.
- Prices are integer ticks of 0.01, so no fill rounds. A checksum folds every fill, bust, refusal and the final book through FNV-1a, and is the same on every target and profile.
- The book file's rows use `gen csv.Codec` and the JSON report `gen json.Codec`. Every save writes a temporary file, syncs it and renames it over the old one.
- The engine's heap comes through a `mem.Counting`, over a `mem.Fixed` when `--memory-limit` is given. Statistics run after the replay, with one task per bar over `ChunksMut`.

## Limits

- One instrument per replay.
- A comment is a CSV record too, so a `"` in it must be inside a quoted field.
- A feed field is at most 256 bytes and a record at most 64 fields; a book tag is at most 4 KiB. Tags come only from the opening book: the feed format has no tag column.
- Timestamps are taken as given: the feed's order is the event order.
- The synthetic market is a load generator, not a model of any real market.

## Tests

The cases in `cases/` cover the matching rules, every refusal, malformed CSV feeds and books, missing and unwritable files, a memory budget, a million-order replay, and saves under simulated crashes and full disks (`mk test --sim`). Run them from the repository root with `go run ./tools/dogfood run -only orderbook`.

## Section 18 lines

The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```
Slice[T].Len(&self): int
Slice[T].Get(&self, i: int): ?&T
Slice[T].GetMut(mut self, i: int): ?mut T
Slice[T].Sort(mut self, const less: fn(&T, &T): bool)
Slice[T].SortStable(mut self, a: &Alloc, const less: fn(&T, &T): bool): OutOfMemory!void
Slice[T].Search[K](&self, key: &K, const less: fn(&T, &K): bool): int
Slice[T].Swap(mut self, i: int, j: int)
Slice[T].Reverse(mut self)
Slice[T].Fill(mut self, x: T into self)
Slice[T].CopyFrom(mut self, src: []T) *src into self
Slice[T].Has(&self, x: &T): bool
Slice[T].Index(&self, x: &T): ?int
Slice[T].SplitAt(mut self, i: int): (left: mut []T, right: mut []T) from self
Slice[T].GetPair(mut self, i: int, j: int): ?(a: mut T, b: mut T) from self
Slice[T].Chunks(&self, n: int): Chunks[T] from self
Slice[T].ChunksMut(mut self, n: int): ChunksMut[T] from self
Chunks[T].Next(mut self): ?[]T from self.xs
ChunksMut[T].Next(mut self): ?mut []T from self.ptr
Map[K, V].New(a: &Arena): Map[K, V]
Map[K, V].NewKeyed(a: &Arena, key: HashKey): Map[K, V]
Map[K, V].Get(&self, k: &K): ?&V
Map[K, V].GetMut(mut self, k: &K): ?mut V
Map[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void
Map[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V
Map[K, V].Ensure(mut self, k: K into self, v: V into self): OutOfMemory!mut V from self
Map[K, V].Has(&self, k: &K): bool
Map[K, V].Remove(mut self, k: &K): ?V
Map[K, V].Len(&self): int
Map[K, V].Iter(&self): MapIter[K, V]
MapIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.map
Set[T].New(a: &Arena): Set[T]
Set[T].NewKeyed(a: &Arena, key: HashKey): Set[T]
Set[T].Add(mut self, x: T into self): OutOfMemory!bool
Set[T].Has(&self, x: &T): bool
Set[T].Remove(mut self, x: &T): bool
HeapList[T].New(a: &Alloc): HeapList[T]
HeapList[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void
HeapList[T].Pop(mut self): ?T
HeapList[T].Len(&self): int
HeapList[T].Remove(mut self, i: int): T
HeapList[T].Insert(mut self, a: &Alloc, i: int, x: T into self): OutOfMemory!void
HeapList[T].Clear(mut self)
HeapList[T].FreeEmpty(self, a: &Alloc)
HeapList[T].Free(self, a: &Alloc)
Box[T].New(a: &Alloc, x: T): OutOfMemory!Box[T] from a, x
Box[T].Ref(&self): &T
Box[T].Mut(mut self): mut T
Box[T].Take(self, a: &Alloc): T
Box[T].Free(self, a: &Alloc)
Print[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void
HashKey.Of(k0: u64, k1: u64): HashKey
// std.mem
Counting.New(inner: &Alloc): Counting
Counting.Alloc(&self): &Alloc from self
Counting.Bytes(&self): int
Counting.Live(&self): int
Counting.Peak(&self): int
Pool[T].New(a: &Arena): Pool[T]
Pool[T].Add(mut self, x: T into self): OutOfMemory!Handle
Pool[T].Get(&self, h: Handle): ?&T
Pool[T].GetMut(mut self, h: Handle): ?mut T
Pool[T].Free(mut self, h: Handle): bool
Pool[T].Len(&self): int
Pool[T].Iter(&self): PoolIter[T] from self
PoolIter[T].Next(mut self): ?(handle: Handle, value: &T) from self.pool
Pool[T].Sweep(mut self, const keep: fn(Handle, &T): bool): int
Pool[T].GetPair(mut self, h: Handle, k: Handle): ?(a: mut T, b: mut T) from self
// std.coll
*
// std.csv. Records, Record, Options and Error below are csv.Records, csv.Record, csv.Options and csv.Error
error Quote(line: i64, column: int)
error TooLarge(line: i64)
type Error = Quote | TooLarge | ReadError
Records.New(r: mut Reader, buf: mut []u8, o: &Options): Records from r, buf
Records.Next(mut self): csv.Error!?Record from self
Record.Len(&self): int
Record.Field(&self, i: int): ?str from self
Record.Line(&self): i64
csv.WriteRecord(w: mut Writer, values: []str, o: &Options): WriteError!void
csv.Codec(g: mut Gen, t: Type)
struct Options {
// std.json. Value, Member and Error below are json.Value, json.Member and json.Error
json.Codec(g: mut Gen, t: Type)
interface Json {
```
