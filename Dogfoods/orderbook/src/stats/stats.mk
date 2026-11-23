// Why: a replay's report needs summary figures over its fills.
// What: Summarize, Percentile, Vwap, Rank and Newest; Bar, AccountRow and
// Summary.
// How: computed in batches after the replay: price percentiles, VWAP by
// aggressor side, bars of a fixed number of trades, sizes, account volume.

import src.book

/// A bar: the fills of one run of `n` consecutive trades.
export struct Bar {
    export open: i64
    export high: i64
    export low: i64
    export close: i64
    export volume: i64
    export trades: int
}

/// What one account traded, as taker and as maker together.
export struct AccountRow {
    export account: u32
    export bought: i64
    export sold: i64
    export fills: int
}

/// The size classes of the fill-size histogram: up to 10, 50, 100, 500,
/// 1,000, 5,000 and above.
export const SizeClasses: int = 7

const SizeLimits: [6]i64 = [10, 50, 100, 500, 1_000, 5_000]

/// The batch results.
export struct Summary {
    export fills: int
    export p50: ?i64
    export p90: ?i64
    export p99: ?i64
    export vwap: ?i64
    export buy_vwap: ?i64
    export sell_vwap: ?i64
    export at_or_below_vwap: int
    export bars: List[Bar]
    export sizes: []int
    export accounts: List[AccountRow]
}

/// Computes the summary of `fills`, oldest first, with bars of `per_bar`
/// trades each (a short last bar is merged into the one before it). The
/// bars' volumes are summed by tasks under `io`, one per bar. Fails with
/// OutOfMemory when `a` cannot hold the working copies, which are
/// O(len(fills)). O(n log n).
export Summarize(a: &Arena, io: Io, fills: []book.Fill, per_bar: int): OutOfMemory!Summary from a {
    n := fills.Len()
    var prices := try a.Slice[i64](n)
    for i, f in fills {
        prices[i] = f.price
    }
    var sorted := try a.Slice[i64](n)
    sorted.CopyFrom(prices[..])
    sorted.Sort(fn(x: &i64, y: &i64): bool { return *x < *y })
    vwap := Vwap(fills)
    var below := 0
    if v := vwap {
        below = sorted.Search(&v, fn(x: &i64, k: &i64): bool { return *x <= *k })
    }
    (buy: buy_vwap, sell: sell_vwap) := try SideVwaps(a, fills)
    return Summary(
        fills: n,
        p50: Percentile(sorted[..], 50),
        p90: Percentile(sorted[..], 90),
        p99: Percentile(sorted[..], 99),
        vwap: vwap,
        buy_vwap: buy_vwap,
        sell_vwap: sell_vwap,
        at_or_below_vwap: below,
        bars: try Bars(a, io, fills, per_bar),
        sizes: try Sizes(a, fills),
        accounts: try Accounts(a, fills),
    )
}

/// The value at percentile `p` of `sorted`, nearest rank; none when empty.
export Percentile(sorted: []i64, p: int): ?i64 {
    if sorted.Len() == 0 {
        return none
    }
    rank := (sorted.Len() * p + 99) / 100
    at := if rank == 0 { 0 } else { rank - 1 }
    v := sorted.Get(at) else {
        return none
    }
    return some(*v)
}

/// The volume-weighted average price of `fills`, rounded down.
export Vwap(fills: []book.Fill): ?i64 {
    var qty: i64 = 0
    var notional: i64 = 0
    for f in fills {
        qty += f.qty
        notional += f.qty * f.price
    }
    if qty == 0 {
        return none
    }
    return some(notional / qty)
}

/// The VWAP of the fills a buyer started and of those a seller started. A
/// copy of the fills is partitioned in place, buys first, then split.
SideVwaps(a: &Arena, fills: []book.Fill): OutOfMemory!(buy: ?i64, sell: ?i64) {
    var scratch := try a.Sub()
    defer scratch.Free()
    var xs := try scratch.Slice[book.Fill](fills.Len())
    xs.CopyFrom(fills)
    var cut := 0
    for i in 0..xs.Len() {
        if xs[i].side == book.Side.Buy {
            xs.Swap(i, cut)
            cut += 1
        }
    }
    (left, right) := xs.SplitAt(cut)
    return (buy: Vwap(left[..]), sell: Vwap(right[..]))
}

/// Bars of `per_bar` trades each, oldest first.
Bars(a: &Arena, io: Io, fills: []book.Fill, per_bar: int): OutOfMemory!List[Bar] from a {
    var bars := List[Bar].New(a)
    if fills.Len() == 0 || per_bar < 1 {
        return bars
    }
    var volumes := try a.Slice[i64](fills.Len())
    for i, f in fills {
        volumes[i] = f.qty
    }
    // Each task turns its bar's quantities into running totals, so a bar's
    // volume is its last element; the bars are disjoint, so the tasks need
    // no lock and the result does not depend on how many carriers run them.
    scope s := io.Scope() {
        for chunk in volumes.ChunksMut(per_bar) {
            s.go RunningTotal(chunk)
        }
    }
    var at := 0
    for chunk in fills.Chunks(per_bar) {
        at += chunk.Len()
        var bar := Bar(open: chunk[0].price, high: chunk[0].price, low: chunk[0].price, close: chunk[chunk.Len() - 1].price, volume: volumes[at - 1], trades: chunk.Len())
        for f in chunk {
            if f.price > bar.high {
                bar.high = f.price
            }
            if f.price < bar.low {
                bar.low = f.price
            }
        }
        try bars.Push(bar)
    }
    n := bars.Len()
    if n >= 2 && bars[n - 1].trades * 2 < per_bar {
        merge := bars.GetPair(n - 2, n - 1) else {
            return bars
        }
        (a: prev, b: last) := merge
        prev.high = if last.high > prev.high { last.high } else { prev.high }
        prev.low = if last.low < prev.low { last.low } else { prev.low }
        prev.close = last.close
        prev.volume += last.volume
        prev.trades += last.trades
        _ = bars.Pop()
    }
    return bars
}

/// Replaces each element of `xs` by the sum of it and those before it.
RunningTotal(xs: mut []i64) {
    var sum: i64 = 0
    for x in mut xs {
        sum += *x
        *x = sum
    }
}

/// How many fills fall in each size class.
Sizes(a: &Arena, fills: []book.Fill): OutOfMemory![]int from a {
    var counts := try a.Slice[int](SizeClasses)
    counts.Fill(0)
    for f in fills {
        var class := 0
        for limit in SizeLimits {
            if f.qty <= limit {
                break
            }
            class += 1
        }
        if c := counts.GetMut(class) {
            *c += 1
        }
    }
    return counts[..]
}

/// Each account's traded volume, the largest total first, ties by account.
Accounts(a: &Arena, fills: []book.Fill): OutOfMemory!List[AccountRow] from a {
    var by := Map[u32, AccountRow].New(a)
    for f in fills {
        sides := if f.side == book.Side.Buy { (buyer: f.taker.account, seller: f.maker.account) } else { (buyer: f.maker.account, seller: f.taker.account) }
        (buyer, seller) := sides
        b := try by.Ensure(buyer, AccountRow(account: buyer, bought: 0, sold: 0, fills: 0))
        b.bought += f.qty
        b.fills += 1
        if s := by.GetMut(&seller) {
            s.sold += f.qty
            s.fills += 1
        } else {
            try by.Set(seller, AccountRow(account: seller, bought: 0, sold: f.qty, fills: 1))
        }
    }
    var rows := List[AccountRow].New(a)
    for (key, value) in by.Iter() {
        _ = key
        try rows.Push(*value)
    }
    // Map order is unspecified: sort by account, then stably by volume.
    rows.Sort(fn(x: &AccountRow, y: &AccountRow): bool { return x.account < y.account })
    try rows.SortStable(a.Alloc(), fn(x: &AccountRow, y: &AccountRow): bool { return x.bought + x.sold > y.bought + y.sold })
    return rows
}

/// The rank, from 1, of `account` in `rows`, which Accounts made; none when
/// the account did not trade.
export Rank(a: &Arena, rows: []AccountRow, account: u32): OutOfMemory!?int {
    var ids := try a.Slice[u32](rows.Len())
    for i, r in rows {
        ids[i] = r.account
    }
    if !ids.Has(&account) {
        return none
    }
    at := ids.Index(&account) else {
        return none
    }
    return some(at + 1)
}

/// The bars newest first, for a tail view, in `a`.
export Newest(a: &Arena, bars: []Bar): OutOfMemory!mut []Bar from a {
    var out := try a.Slice[Bar](bars.Len())
    out.CopyFrom(bars)
    out.Reverse()
    return out
}

test "percentiles use the nearest rank" {
    xs: [5]i64 = [10, 20, 30, 40, 50]
    Assert(Percentile(xs[..], 50).Must() == 30)
    Assert(Percentile(xs[..], 99).Must() == 50)
    Assert(Percentile(xs[..0], 50).IsNone())
}

test "running totals turn quantities into cumulative volume" {
    var xs: [4]i64 = [1, 2, 3, 4]
    RunningTotal(mut xs[..])
    Assert(xs[3] == 10 && xs[0] == 1)
}
