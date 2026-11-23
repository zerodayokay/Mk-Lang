# app/orderbook: the matching engine on a recorded feed

The row replays a market-data feed through a price-time priority matching engine, then runs batch statistics over the result. It is the most allocator- and container-heavy real program the repository has. The Mk side is the dogfood `Dogfoods/orderbook`, unchanged. The four twins, in C, Rust, Zig and Go, are written to this file.

## Command line

```
orderbook replay FEED --quiet --snapshot 0
```

The program prints one line, which is the checksum of the row:

```
commands <n> fills <n> checksum <16 hex digits>
```

- `--snapshot 0` turns off the periodic snapshot of the top of the book. A snapshot costs a popcount over the live flags, so with it off the row times the replay rather than the snapshot.
- The row's `env` sets `MK_CARRIERS=1`, because the table is single-threaded. The dogfood's statistics run one task per bar of 1,000 fills, so with one carrier they run one after another. The twins sum the bars inline.
- Every twin also accepts `replay FEED [--snapshot N]` without `--quiet`, which prints the dogfood's text report. The section "Checking the twins" below says why.

## Input

The input is the `orderfeed` kind of `tools/bench gen` (`tools/bench/gen_orderfeed.go`, which states its distribution). It writes a CSV file in the dogfood's feed format (`orderbook --help`), one command a line:

```
N,ts,acct/serial,B|S,qty,price          limit order
M,ts,acct/serial,B|S,qty                market order
S,ts,acct/serial,B|S,qty,stop           stop order
T,ts,acct/serial,B|S,qty,stop,limit     stop-limit order
C,ts,acct/serial                        cancel
A,ts,acct/serial,qty,price              amend size and price
H,ts,acct                               halt an account
R,ts,acct                               resume an account
X,ts                                    bust the last trade
```

- A line whose first field starts with `#` is a comment.
- A price has at most two decimals and is read as integer ticks of 0.01. `100`, `100.5` and `100.50` are all valid.
- The generated file uses LF line ends and contains no quote.
- The generator is not the dogfood's `synth`. Its mix is chosen so that a replay meets every command kind and every refusal reason.

On the provisional publish feed (64,000,000 bytes, seed 11), the dogfood's text report counts:
- 2,245,940 commands and 1,623,428 orders;
- 1,277,976 fills, 215,117 cancels and 22,654 busts;
- refusals of every kind: 17,040 duplicate ids, 41,794 halted accounts, 29,699 unknown ids, 237,712 already done, 68 with no liquidity, 13,174 bad quantities and 1 with nothing to bust.

These are counts from `orderbook replay tests/bench/data/orderfeed-64mb.csv --snapshot 0`, not times. `tools/bench verify -publish-size` fixes the size in the quiet window, and the checksum is recorded with it.

## The work, every step

Each twin must do all of the following. The checksum checks steps 1 to 3, and the text report of "Checking the twins" checks steps 4 to 7, with three exceptions that reach neither:
- a cancel's level compaction (step 2) changes no fill, no refusal and no count;
- neither does the sweep every 4,096 commands (step 2);
- the timed `--quiet` path computes the values of steps 4 to 7 but prints none of them; only the untimed text mode prints them.

These three are checked by reading the sources: every twin does them, and none of it is guarded by `--quiet`. Output cannot catch them: a C mutant whose compaction returns at once, and one with no sweep, each printed the dogfood's quiet line and text report on four feeds.

### 1. Read the feed a record at a time

- Fields are trimmed of white space.
- A comment or blank record is skipped.
- A record of an unknown type, with the wrong field count, or with a field that does not parse ends the program with status 3. A file that cannot be read gives status 4. Only the status is compared; the message is not.
- The bitset of live arrival numbers is sized from the file: a third of its bytes, plus two. This is the dogfood's `FeedBound` plus one.
- An integer (a time, a quantity, an account or a serial) is an optional `+` or `-` and then decimal digits, nothing else, in 64 bits: the dogfood's `str.ParseInt`, which is Go's `strconv.ParseInt`. Every twin parses exactly that, so `+15` and `-9223372036854775808` are accepted and `1_0` is refused everywhere (C reads the magnitude in 64 unsigned bits with a limit per sign).

#### Parser leniency outside the feed's alphabet

The twins' contract is the `orderfeed` kind's alphabet plus the dogfood's own cases. Outside it, two differences are known and stated rather than removed:
- The dogfood reads the feed with `csv.Records` under `FeedOptions` (`src/feed/parse.mk`): a field over 256 bytes, or a record of more than 64 fields, ends it with status 3, comments included. No twin has that limit, so a 302-byte comment or a quantity of 300 zeros is refused by the dogfood and read by every twin. The generator writes no field near either limit.
- The dogfood and Go's `encoding/csv` read quoted fields. The hand splitters of C, Rust and Zig refuse a line holding a quote, with status 3 ("Cells").

### 2. Run each command through the engine, in feed order, with these rules

The checksum folds arrival numbers, so the rules must hold exactly. A twin that guesses at textbook matching gets a different checksum.

**Arrival numbers.**
- Every accepted order takes the next arrival number (`seq`), from 0.
- So does every amend, which reissues the order.
- A market order refused for no liquidity has already taken one.
- `orders` in the report is the count of arrival numbers taken.

**Add (`N`, `M`, `S`, `T`).** The checks run in this order:
1. A quantity of 0 or less, or a price of 0 or less on a limit or stop-limit order, is refused: bad quantity.
2. An order from a halted account is refused: account halted.
3. An id that names a live order is refused: duplicate id.

Otherwise the order is stored live under its id, and the id leaves the done set. A stop or stop-limit order is armed, and the others execute.

**Arm.**
- If a trade has happened and its price has already reached the stop, the order fires at once. A buy stop fires when the last price is at or above its stop, a sell stop when it is at or below.
- Otherwise the order joins its side's stop queue. The buy queue is keyed by (stop, seq), the sell queue by (-stop, seq), and each queue pops its least key first.
- A bust does not change the last price.

**Fire.** If the order is still live:
- a stop-limit order becomes a limit order;
- a stop order becomes a market order;
- then it executes.

**Execute.** The order matches against the opposite side.
- The best price comes first, and within a price, the oldest order first.
- A limit order trades only while the price crosses its limit.
- Each fill is at the resting order's price, stamped with the time of the current command.
- A resting order that is only partly filled keeps its place at the front of its queue.
- A resting order that is filled is retired: its flag clears, its id leaves the index and joins the done set.
- A queue entry whose order is dead is skipped and dropped.
- A level is removed when its resting quantity reaches 0.

Then:
- a filled order is retired;
- a limit order with quantity left rests at the back of its price's queue;
- a market order with quantity left is retired, and if it made no fill at all, its slot is freed and it is refused: no liquidity.

**Triggers.** If the order made at least one fill, the stops that the last price has reached are taken:
- first the whole buy queue while its least key is at or below the last price;
- then the sell queue while its least key is at or below minus the last price.

Each one that is still live goes, in that order, to the front of the work queue, ahead of anything already there. The work queue runs to empty before the next feed command.

**Cancel (`C`).**
- An id that is not live is refused: already done if the id is in the done set, unknown otherwise.
- A live order is retired.
- A resting limit order also leaves its level: the level's quantity and live count go down. Its queue entry is popped if it is the newest, and otherwise stays as a dead entry.
- The level is then compacted: if its queue holds more than twice its live orders plus 8, it is rebuilt from the live entries alone, in order.
- A waiting stop leaves its queue lazily.
- The cancel counts in `cancels`.

**Amend (`A`).** The checks run in this order:
1. An id that is not live is refused: already done or unknown, as for a cancel.
2. A quantity or price of 0 or less is refused: bad quantity.

A halted account does not stop an amend. Then:
- A resting limit order leaves its level as for a cancel, but with no compaction.
- The order is copied with the new size as both its quantity and what is left of it.
- The new price becomes the trigger of a stop order and the limit of any other order, so a stop-limit order keeps its stop.
- The copy takes the command's time and is reissued under the same id with a new arrival number. The old order's flag clears.
- The new order is armed if it is a stop or stop-limit order, and executes otherwise. It is a new arrival, so it has lost its place.

**Halt and resume (`H`, `R`).** These add an account to the halted set or remove it.

**Bust (`X`).**
- The last fill is removed from the fill log, the tape (the newest entry), the five largest (its first equal entry), the volume by price (whose key goes at 0) and the totals.
- The bust is folded into the checksum.
- With no fill left, the bust is refused: nothing to bust.

**After each feed command:**
- Every 4,096 commands, the values of dead orders are swept from the order store.
- When `--snapshot N` is above 0, every N commands the snapshot records the command count, the live orders and the fills.

### 3. The checksum

The checksum is FNV-1a 64 (basis `0xcbf29ce484222325`, prime `0x100000001b3`, wrapping multiply) over the eight bytes, low byte first, of each 64-bit word folded in. Signed words are folded as two's complement. The words are, in event order:
- for each fill: taker seq, maker seq, price, quantity, time;
- for each bust: `0xb057`;
- for each refusal: `0x7e00` plus its code (duplicate 0, halted 1, unknown 2, already done 3, no liquidity 4, bad quantity 5, nothing to bust 6).

At the end, the following words are folded in, in this order:
1. the best bid, or 0;
2. the best ask, or 0;
3. the bid quantity;
4. the ask quantity;
5. the number of live orders.

### 4. Record each fill in the trade log

The dogfood's `Trades.Record` does all of this for each fill. The twins do it too, although `--quiet` prints none of it:
- the fill is appended to the log of every fill;
- it is ranked into the five largest: largest first, and among equal sizes the earlier first;
- it is pushed on an 8-fill tape;
- it is added to the volume by price;
- the last price, the traded quantity and the notional are updated.

### 5. Run the batch statistics

`--quiet` still runs these. It is in `src/main/output.mk`: `Write` runs `stats.Summarize`, the depth and the reject list before its quiet branch, and builds the whole JSON report after it. Over the fills left after busts, the statistics are:
- a sorted copy of the prices, for the nearest-rank 50th, 90th and 99th percentiles;
- the VWAP of all fills, rounded down, and the count of fills at or below it;
- the VWAP of the fills a buyer started and of those a seller started, from a copy partitioned in place;
- bars of 1,000 fills: open, high, low, close, volume (running totals over each bar's sizes) and count, with a last bar of fewer than 500 merged into the one before it;
- the fill-size histogram, with classes up to 10, 50, 100, 500, 1,000, 5,000 and above;
- the bought, sold and fill count of each account (a hash map), sorted by account and then stably by volume, largest first.

### 6. Build the report's values

These are:
- the depth of 5 levels a side;
- the refusal counts;
- the checksum text;
- the prices of the report as text, the five largest fills, every bar and the top 10 accounts.

The dogfood builds these as its JSON report, which only `--json` writes. The twins build the same values as text and keep them.

### 7. Close the session

Every level's queue is walked, counting its live orders. The resting quantity is summed, and the book is released.

## Cells

| Column | Kind | What it uses |
|---|---|---|
| mk | std | the dogfood: `csv.Records`, `mem.Pool`, `coll.Bitset`, `Map`, `Set`, `coll.HeapBTree` of `coll.HeapDeque` levels, `coll.PriorityQueue`, `coll.Deque`, `coll.BTree`, `HeapList`, `Box`, `mem.Counting` |
| go | std | `encoding/csv`, maps, `container/heap`. Go's std has no ordered map, so the price levels are a sorted slice searched by `sort.Search`, which is labelled. The queues are plain slices: a level's FIFO is read at its front and advanced past each order it is done with, the work queue is a stack whose top is its front, and the tape is an 8-fill window that slides along a fixed slice of 1,024 fills and is copied back to its start once it reaches the end, so a new fill moves no other fill but once in 1,016 |
| rust | hand | std's `BTreeMap`, `VecDeque`, `BinaryHeap`, `HashMap` and `HashSet`, plus a hand CSV splitter for the unquoted fields |
| zig | hand | std's `AutoHashMapUnmanaged`, `PriorityQueue`, `Deque` and `ArrayList`, plus a hand CSV splitter. The price levels are a sorted `ArrayList`, labelled as Go's are: Zig's std has a Treap but no B-tree |
| c | hand | every container: a hash table, ring buffers (indexed by a mask, their capacity a power of two), sorted arrays, binary heaps, and the CSV splitter |
| cs, cs-aot | hand | .NET BCL `Dictionary`, `HashSet`, `SortedList` (descending key, best level last), `PriorityQueue`, `List`, plus a hand CSV splitter for the unquoted fields |

- **The price levels in a sorted array (C, Zig, Go).** Each side is kept in descending key order with the best level last, so a new or emptied best level, the common case, moves no other level. Mk's `coll.HeapBTree` and Rust's `BTreeMap` do O(log n) work there. An instrumented run counted the first form in C, best level first, on the publish feed: 64 percent of new levels and 97 percent of emptied ones were at the best, and `memmove` moved 4,756,698,912 bytes, about 2,118 bytes per command. With the best level last it would move 32,280,920 bytes. These are counts of bytes from an instrumented copy of the C twin, not times, and the probe is outside the tree.
- **The slot pool and the bitset of live arrival numbers are the task's own structure, not a facility.** Step 2's sweep needs a slot pool with generations, and the arrival-number flags need a bitset, so every twin writes both: the dogfood uses `mem.Pool` and `coll.Bitset`. No cell counts them as `hand` (`tests/bench/README.md`, "Applications"). Every other facility a twin writes is labelled: C's containers, the hand CSV splitters, and Zig's and Go's sorted level arrays.

- Every twin keeps orders in a slot pool with a generation per slot and a last-in-first-out list of vacant slots, as `mem.Pool` does.
- The Rust and Zig splitters, and C's, refuse a line holding a quote, with status 3. The feed has none. Go's `encoding/csv` and Mk's `csv.Records` read quoted fields too.
- Rust's reader checks each line as UTF-8. Mk's `csv.Records` checks each field. Go's, Zig's and C's readers do not check, since the generated feed is ASCII.
- The `hand` field of bench.json gives each hand facility and its line count.

## Checking the twins

The checksum covers the engine. It does not cover the statistics, the trade log's leaderboard and tape, or the close, which `--quiet` runs but does not print. So each twin, run without `--quiet`, prints the dogfood's text report byte for byte, except its `engine heap` line, which reads the dogfood's own counting allocator. This text mode is not timed.

A twin is checked by running both programs on the same feed and comparing their output. C's standard output is in text mode on Windows, so its CR is removed first.

```
orderbook replay FEED --quiet --snapshot 0          # the timed command, and its exit status
orderbook replay FEED --snapshot 7 | grep -v '^engine heap:'
```

On 2026-11-16 this check ran on all six twin builds, each built with its column's flags (c, rust, rust-ovf, zig-fast, zig-safe, and go with `GOAMD64=v2 CGO_ENABLED=0 go build -pgo=off -trimpath`), against `orderbook.exe` (the dogfood built by mk, release, `--cpu x86-64-v2`). Every build printed the dogfood's quiet line, exit status and text report on 114 feeds:
- the `orderfeed` kind at 2,000,000 bytes, seeds 1, 2, 3, 7, 11, 21, 42 and 99;
- the 64,000,000-byte publish feed, seed 11 (with `--snapshot 9973`, since a snapshot every 7 commands is slow at that size), which gave the row's checksum in every column;
- 66 adversarial feeds (a narrow price band, crossing amends, cancels behind the newest order, halts, bust runs, reused ids, and fill counts around the bar boundary);
- 23 of the dogfood's 24 `cases/data` feeds, all but `quoted.csv`, which the hand splitters refuse;
- the dogfood's own 400,000-command `synth` feed, seed 11, and a feed whose book ends empty;
- 14 of 16 edge feeds. The other two are the over-long field and comment of "Parser leniency".

The scripts and logs are outside the tree. The depth table of the text report is what checks the order of the price levels.

The replay is the whole 64,000,000-byte feed in every mode: `quick` equals `args`, as in `app/zlib-c`, because the dogfood takes no size argument and a row has one data file. So `verify` and `buildtime` replay all of it too.

The checksum's strength was checked on the Go twin. Each of four one-rule mutants changed the checksum on the 2,000,000-byte seed-1 feed:
- a stop-limit amend that sets the stop;
- a buy stop that triggers only above its stop;
- a stop queue ordered by seq descending;
- no price check on an add.

## Not timed here

No time in this directory was measured. The row's numbers come only from `tools/bench`, on a quiet machine.
