// Why: a replay reports what it executed, and two runs must be comparable
// by one number.
// What: Trades: every fill in order, the largest fills, a tape of the most
// recent, traded volume by price, and a running checksum (Mix, MixInt).
// How: the checksum is FNV-1a over each fill's fields; the tape and the
// volume tree live in the replay's arena, the fills on the engine's.

import std.coll

/// How many fills the tape keeps.
export const TapeLength: int = 8

/// How many of the largest fills the leaderboard keeps.
export const TopFills: int = 5

/// The trades of one replay. `fills` and `top` are on the engine's
/// allocator; the tape and the volume tree live in the replay's arena.
export view linear struct Trades {
    fills: HeapList[Fill]
    top: HeapList[Fill]
    tape: coll.Deque[Fill]
    volume: coll.BTree[i64, i64]
    last_price: ?i64
    traded: i64
    notional: i64
    busted: int
    checksum: u64
}

/// An empty record whose tape and volume tree live in `arena`.
export Trades.New(a: &Alloc, arena: &Arena): Trades from a, arena {
    return Trades(
        fills: HeapList[Fill].New(a),
        top: HeapList[Fill].New(a),
        tape: coll.Deque[Fill].New(arena),
        volume: coll.BTree[i64, i64].New(arena),
        last_price: none,
        traded: 0,
        notional: 0,
        busted: 0,
        checksum: Fnv64Basis,
    )
}

impl Free for Trades {
    Free(self, a: &Alloc) {
        Trades(fills, top, tape, volume, ..) := self
        _ = (tape, volume)
        fills.Free(a)
        top.Free(a)
    }
}

const Fnv64Basis: u64 = 0xcbf29ce484222325
const Fnv64Prime: u64 = 0x100000001b3

/// Folds one 64-bit word into an FNV-1a style checksum, a byte at a time,
/// so the result does not depend on the target's byte order.
export Mix(h: u64, x: u64): u64 {
    var s := h
    var v := x
    for i in 0..8 {
        _ = i
        s = (s ^ (v & 0xff)) *% Fnv64Prime
        v = v >> 8
    }
    return s
}

/// Folds a signed word into a checksum, as Mix does its two's complement.
export MixInt(h: u64, x: i64): u64 {
    return Mix(h, Trunc[u64](x))
}

/// Records `f`: appends it, ranks it, puts it on the tape, adds its volume
/// and folds it into the checksum. Fails with OutOfMemory when a list
/// cannot grow; nothing is recorded then but what came before the failure.
export Trades.Record(mut self, a: &Alloc, f: Fill): OutOfMemory!void {
    try self.fills.Push(a, f)
    try self.Rank(a, f)
    try self.tape.Push(f)
    if self.tape.Len() > TapeLength {
        _ = self.tape.PopFront()
    }
    if self.volume.Has(&f.price) {
        v := self.volume.GetMut(&f.price).Must() // Has just saw the key
        *v += f.qty
    } else {
        try self.volume.Set(f.price, f.qty)
    }
    self.last_price = some(f.price)
    self.traded += f.qty
    self.notional += f.qty * f.price
    self.checksum = MixInt(MixInt(MixInt(Mix(Mix(self.checksum, Cast[u64](f.taker_seq)), Cast[u64](f.maker_seq)), f.price), f.qty), f.ts)
}

/// Keeps `top` the TopFills largest fills, largest first, and among equal
/// sizes the earlier first.
Trades.Rank(mut self, a: &Alloc, f: Fill): OutOfMemory!void {
    at := self.top.Search(&f, fn(x: &Fill, k: &Fill): bool { return x.qty >= k.qty })
    if at >= TopFills {
        return
    }
    try self.top.Insert(a, at, f)
    if self.top.Len() > TopFills {
        _ = self.top.Remove(TopFills)
    }
}

/// Reverses the most recent fill, as an exchange does when it busts a
/// trade: it leaves the log, the tape, the leaderboard and the volume.
/// Returns false when there is no fill to bust.
export Trades.Bust(mut self): bool {
    f := self.fills.Pop() else {
        return false
    }
    _ = self.tape.Pop()
    if at := self.top.Index(&f) {
        _ = self.top.Remove(at)
    }
    if v := self.volume.Get(&f.price) {
        left := *v - f.qty
        if left == 0 {
            _ = self.volume.Remove(&f.price)
        } else {
            // Replace of a present key allocates nothing, so it cannot fail.
            _ = self.volume.Replace(f.price, left) else {}
        }
    }
    self.traded -= f.qty
    self.notional -= f.qty * f.price
    self.busted += 1
    self.checksum = Mix(self.checksum, 0xb057)
    return true
}

/// The number of fills.
export Trades.Count(&self): int {
    return self.fills.Len()
}

/// Every fill, oldest first.
export Trades.All(&self): []Fill {
    return self.fills.Items()
}

/// The largest fills, largest first.
export Trades.Largest(&self): []Fill {
    return self.top.Items()
}

/// The most recent fills, newest first, in `a`.
export Trades.Recent(&self, a: &Arena): OutOfMemory!List[Fill] from a {
    var out := List[Fill].New(a)
    n := self.tape.Len()
    for i in 0..n {
        if f := self.tape.Get(n - 1 - i) {
            try out.Push(*f)
        }
    }
    return out
}

/// The traded volume at each price from `lo` to `hi`, both included, lowest
/// price first, in `a`.
export Trades.VolumeBand(&self, a: &Arena, lo: i64, hi: i64): OutOfMemory!List[Depth] from a {
    var out := List[Depth].New(a)
    top := hi + 1
    for (key, value) in self.volume.Range(&lo, &top) {
        try out.Push(Depth(price: *key, qty: *value, orders: 0))
    }
    return out
}

/// The `n` prices with the most traded volume, the most first and ties by
/// price, in `a`. O(prices log prices).
export Trades.Busiest(&self, a: &Arena, n: int): OutOfMemory!List[Depth] from a {
    var all := List[Depth].New(a)
    for (key, value) in self.volume.Iter() {
        try all.Push(Depth(price: *key, qty: *value, orders: 0))
    }
    try all.SortStable(a.Alloc(), fn(x: &Depth, y: &Depth): bool { return x.qty > y.qty })
    var out := List[Depth].New(a)
    for d in all {
        if out.Len() == n {
            break
        }
        try out.Push(d)
    }
    return out
}

/// The number of distinct traded prices.
export Trades.Prices(&self): int {
    return self.volume.Len()
}

/// The price of the last fill.
export Trades.Last(&self): ?i64 {
    return self.last_price
}

/// The total quantity traded.
export Trades.Quantity(&self): i64 {
    return self.traded
}

/// The volume-weighted average price in ticks, rounded down; none before
/// the first fill.
export Trades.Vwap(&self): ?i64 {
    if self.traded == 0 {
        return none
    }
    return some(self.notional / self.traded)
}

/// The number of busted fills.
export Trades.Busts(&self): int {
    return self.busted
}

/// The running checksum over every fill and bust.
export Trades.Sum(&self): u64 {
    return self.checksum
}

/// Folds a word that is not a fill, such as a refusal, into the checksum.
export Trades.Note(mut self, x: u64) {
    self.checksum = Mix(self.checksum, x)
}
