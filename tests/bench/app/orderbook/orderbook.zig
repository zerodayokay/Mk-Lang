// orderbook, Zig, std plus a hand CSV splitter: the task of README.md, the
// replay of Dogfoods/orderbook (`orderbook replay FEED [--quiet]
// [--snapshot N]`). The feed is read through std.Io.File.reader with a
// 64 KiB buffer, a line at a time (takeDelimiter), and split on commas by
// hand with std.mem.splitScalar (the feed has no quoted field; a line
// holding a quote is refused with status 3), fields trimmed by
// std.mem.trim of ASCII white space, integers by a sign-and-digits loop
// (the dogfood's str.ParseInt; std.fmt.parseInt also takes '_'). Memory
// comes from std.heap.smp_allocator, in both modes. Orders live
// in a slot pool (an ArrayList of slots, each with a generation, and an
// ArrayList of vacant slot indices used last in first out), named by (slot,
// generation) handles; a []u64 bitset of live arrival numbers sized from
// the file (a third of its bytes, plus two); an AutoArrayHashMapUnmanaged from
// client id to handle (swapRemove avoids AutoHashMapUnmanaged's tombstone
// saturation under steady-state insert/remove churn) and an AutoHashMapUnmanaged
// as the set of the ids that are done (std's default hash, Wyhash, fixed seed). Each side of the book is an ArrayList
// of price levels sorted by key (price times -1 for bids, times 1 for
// asks) in descending order, the best level last, so a new or emptied best
// level moves no other level; it is searched by std.sort.lowerBound and
// kept sorted by insert and orderedRemove: Zig's std has no ordered map but a Treap, and the sorted
// list is the labelled choice, as the Go twin's; a level's FIFO of handles,
// the work queue and the 8-fill tape are std.Deques. Stops wait in two
// std.PriorityQueues ordered by (key, arrival number). Trades are an
// ArrayList of every fill, one of the five largest, the tape, and an
// AutoHashMapUnmanaged of volume by price. The checksum is FNV-1a over the
// bytes of 64-bit words, its multiply wrapping (*%); every other integer is
// i64 or usize, checked in zig-safe, and no value of a feed overflows.
// After the replay, the statistics run whether or not --quiet is given: a
// sorted copy (std.mem.sortUnstable) of the fill prices for percentiles,
// the VWAPs of all fills and of each aggressor side (a copy partitioned in
// place), bars of 1,000 fills (running totals, a short last bar merged),
// the fill-size histogram and the volume by account (a hash map, then
// sortUnstable by account and the stable std.mem.sort by volume); the depth
// of 5 levels a side; the report's values; then the close, which counts
// the live orders left in each side's queues. Without --quiet the program
// prints the dogfood's text report but its "engine heap" line, which reads
// the dogfood's own counting allocator.
const std = @import("std");
const Allocator = std.mem.Allocator;

const Side = enum(u8) {
    buy,
    sell,
    fn name(s: Side) []const u8 {
        return if (s == .buy) "Buy" else "Sell";
    }
};

const Kind = enum(u8) {
    limit,
    market,
    stop,
    stop_limit,
    fn name(k: Kind) []const u8 {
        return switch (k) {
            .limit => "Limit",
            .market => "Market",
            .stop => "Stop",
            .stop_limit => "StopLimit",
        };
    }
};

const ClientId = struct { account: u32, serial: u32 };

const Order = struct {
    id: ClientId,
    seq: usize,
    side: Side,
    kind: Kind,
    price: i64,
    stop: i64,
    qty: i64,
    left: i64,
    ts: i64,
};

const Fill = struct {
    taker: ClientId,
    maker: ClientId,
    taker_seq: usize,
    maker_seq: usize,
    side: Side,
    price: i64,
    qty: i64,
    ts: i64,
};

const Handle = struct { idx: u32, gen: u32 };

const Command = union(enum) {
    add: struct { ts: i64, id: ClientId, side: Side, kind: Kind, qty: i64, price: i64, stop: i64 },
    cancel: struct { ts: i64, id: ClientId },
    amend: struct { ts: i64, id: ClientId, qty: i64, price: i64 },
    halt: struct { ts: i64, account: u32 },
    @"resume": struct { ts: i64, account: u32 },
    bust: struct { ts: i64 },
    trigger: Handle,
};

const r_duplicate = 0;
const r_halted = 1;
const r_unknown = 2;
const r_already_done = 3;
const r_no_liquidity = 4;
const r_bad_quantity = 5;
const r_nothing_to_bust = 6;
const reject_names = [7][]const u8{ "duplicate id", "account halted", "unknown id", "already done", "no liquidity", "bad quantity", "nothing to bust" };

var gpa: Allocator = undefined;

// u is a count or a size, never negative, as the unsigned integer Zig prints
// without a sign: a signed one printed with a width gains a + sign.
fn u(v: i64) u64 {
    return @intCast(v);
}

// ---- the order store: a slot pool, live flags, the index and the done set

const Slot = struct { gen: u32, used: bool, o: Order };

const Orders = struct {
    slots: std.ArrayList(Slot) = .empty,
    vacant: std.ArrayList(u32) = .empty,
    flags: []u64,
    nbits: usize,
    index: std.AutoArrayHashMapUnmanaged(ClientId, Handle) = .empty,
    done: std.AutoHashMapUnmanaged(ClientId, void) = .empty,
    seq: usize = 0,

    fn get(s: *const Orders, h: Handle) ?*Order {
        if (h.idx >= s.slots.items.len) return null;
        const sl = &s.slots.items[h.idx];
        return if (sl.used and sl.gen == h.gen) &sl.o else null;
    }

    fn hasFlag(s: *const Orders, q: usize) bool {
        return s.flags[q >> 6] >> @intCast(q & 63) & 1 != 0;
    }

    fn setFlag(s: *Orders, q: usize) void {
        if (q >= s.nbits) @panic("orders past the capacity");
        s.flags[q >> 6] |= @as(u64, 1) << @intCast(q & 63);
    }

    fn clearFlag(s: *Orders, q: usize) void {
        s.flags[q >> 6] &= ~(@as(u64, 1) << @intCast(q & 63));
    }

    fn live(s: *const Orders, h: Handle) ?*Order {
        const o = s.get(h) orelse return null;
        return if (s.hasFlag(o.seq)) o else null;
    }

    fn poolAdd(s: *Orders, o: Order) !Handle {
        if (s.vacant.pop()) |i| {
            const sl = &s.slots.items[i];
            sl.used = true;
            sl.o = o;
            return .{ .idx = i, .gen = sl.gen };
        }
        try s.slots.append(gpa, .{ .gen = 0, .used = true, .o = o });
        return .{ .idx = @intCast(s.slots.items.len - 1), .gen = 0 };
    }

    fn release(s: *Orders, i: u32) !void {
        const sl = &s.slots.items[i];
        sl.used = false;
        sl.gen +%= 1;
        try s.vacant.append(gpa, i);
    }

    fn add(s: *Orders, order: Order) !Handle {
        var o = order;
        o.seq = s.seq;
        const h = try s.poolAdd(o);
        try s.index.put(gpa, o.id, h);
        s.setFlag(o.seq);
        s.seq += 1;
        _ = s.done.remove(o.id);
        return h;
    }

    fn reissue(s: *Orders, order: Order) !Handle {
        var o = order;
        o.seq = s.seq;
        const h = try s.poolAdd(o);
        if (try s.index.fetchPut(gpa, o.id, h)) |old| {
            if (s.get(old.value)) |prev| s.clearFlag(prev.seq);
        }
        s.setFlag(o.seq);
        s.seq += 1;
        return h;
    }

    fn retire(s: *Orders, h: Handle) !void {
        const o = s.get(h) orelse return;
        s.clearFlag(o.seq);
        _ = s.index.swapRemove(o.id);
        try s.done.put(gpa, o.id, {});
    }

    fn liveCount(s: *const Orders) usize {
        var c: usize = 0;
        for (s.flags) |w| c += @popCount(w);
        if (c != s.index.count()) @panic("live flags and index disagree");
        return c;
    }

    fn sweep(s: *Orders) !void {
        for (s.slots.items, 0..) |sl, i| {
            if (sl.used and !s.hasFlag(sl.o.seq)) try s.release(@intCast(i));
        }
    }
};

// ---- trades and the checksum

const tape_length = 8;
const top_fills = 5;
const fnv_prime: u64 = 0x100000001b3;

fn mix(h0: u64, x0: u64) u64 {
    var h = h0;
    var x = x0;
    for (0..8) |_| {
        h = (h ^ (x & 0xff)) *% fnv_prime;
        x >>= 8;
    }
    return h;
}

fn mixInt(h: u64, x: i64) u64 {
    return mix(h, @bitCast(x));
}

const Trades = struct {
    fills: std.ArrayList(Fill) = .empty,
    top: std.ArrayList(Fill) = .empty,
    tape: std.Deque(Fill) = .empty,
    volume: std.AutoHashMapUnmanaged(i64, i64) = .empty,
    last: ?i64 = null,
    traded: i64 = 0,
    notional: i64 = 0,
    busted: usize = 0,
    checksum: u64 = 0xcbf29ce484222325,

    fn record(t: *Trades, f: Fill) !void {
        try t.fills.append(gpa, f);
        var at: usize = 0;
        while (at < t.top.items.len and t.top.items[at].qty >= f.qty) at += 1;
        if (at < top_fills) {
            try t.top.insert(gpa, at, f);
            if (t.top.items.len > top_fills) t.top.shrinkRetainingCapacity(top_fills);
        }
        try t.tape.pushBack(gpa, f);
        if (t.tape.len > tape_length) _ = t.tape.popFront();
        const v = try t.volume.getOrPut(gpa, f.price);
        if (!v.found_existing) v.value_ptr.* = 0;
        v.value_ptr.* += f.qty;
        t.last = f.price;
        t.traded += f.qty;
        t.notional += f.qty * f.price;
        t.checksum = mixInt(mixInt(mixInt(mix(mix(t.checksum, f.taker_seq), f.maker_seq), f.price), f.qty), f.ts);
    }

    fn bust(t: *Trades) bool {
        const f = t.fills.pop() orelse return false;
        _ = t.tape.popBack();
        for (t.top.items, 0..) |x, i| {
            if (std.meta.eql(x, f)) {
                _ = t.top.orderedRemove(i);
                break;
            }
        }
        if (t.volume.getPtr(f.price)) |v| {
            v.* -= f.qty;
            if (v.* == 0) _ = t.volume.remove(f.price);
        }
        t.traded -= f.qty;
        t.notional -= f.qty * f.price;
        t.busted += 1;
        t.checksum = mix(t.checksum, 0xb057);
        return true;
    }
};

// ---- one side of the book

const Level = struct { key: i64, qty: i64, live: usize, orders: std.Deque(Handle) };

const Depth = struct { price: i64, qty: i64, orders: usize };

const Expired = struct { orders: usize, qty: i64 };

const compact_slack = 8;

// The levels are in descending key order, so lowerBound's "less" is a
// greater key: it returns the first level whose key is at or below key.
fn keyOrder(key: i64, l: Level) std.math.Order {
    return std.math.order(l.key, key);
}

const Ladder = struct {
    levels: std.ArrayList(Level) = .empty, // descending key: best price last, so a new or emptied best level moves nothing
    sign: i64,

    fn find(l: *const Ladder, key: i64) struct { i: usize, found: bool } {
        const i = std.sort.lowerBound(Level, l.levels.items, key, keyOrder);
        return .{ .i = i, .found = i < l.levels.items.len and l.levels.items[i].key == key };
    }

    fn best(l: *const Ladder) ?i64 {
        if (l.levels.items.len == 0) return null;
        return l.levels.items[l.levels.items.len - 1].key * l.sign;
    }

    fn drop(l: *Ladder, i: usize) void {
        var lvl = l.levels.orderedRemove(i);
        lvl.orders.deinit(gpa);
    }

    fn rest(l: *Ladder, price: i64, qty: i64, h: Handle) !void {
        const f = l.find(price * l.sign);
        if (!f.found) try l.levels.insert(gpa, f.i, .{ .key = price * l.sign, .qty = 0, .live = 0, .orders = .empty });
        const lvl = &l.levels.items[f.i];
        try lvl.orders.pushBack(gpa, h);
        lvl.qty += qty;
        lvl.live += 1;
    }

    fn withdraw(l: *Ladder, price: i64, qty: i64, h: Handle) void {
        const f = l.find(price * l.sign);
        if (!f.found) return;
        const lvl = &l.levels.items[f.i];
        lvl.qty -= qty;
        lvl.live -= 1;
        if (lvl.orders.back()) |b| {
            if (std.meta.eql(b, h)) _ = lvl.orders.popBack();
        }
        if (lvl.qty == 0) l.drop(f.i);
    }

    fn compact(l: *Ladder, price: i64, book: *const Orders) !void {
        const f = l.find(price * l.sign);
        if (!f.found) return;
        const old = &l.levels.items[f.i];
        if (old.orders.len <= 2 * old.live + compact_slack) return;
        var fresh: std.Deque(Handle) = .empty;
        for (0..old.orders.len) |j| {
            const h = old.orders.at(j);
            if (book.live(h) != null) try fresh.pushBack(gpa, h);
        }
        old.orders.deinit(gpa);
        old.orders = fresh;
    }

    fn close(l: *Ladder, book: *const Orders) Expired {
        var e: Expired = .{ .orders = 0, .qty = 0 };
        for (l.levels.items) |*lvl| {
            e.qty += lvl.qty;
            for (0..lvl.orders.len) |j| {
                if (book.live(lvl.orders.at(j)) != null) e.orders += 1;
            }
            lvl.orders.deinit(gpa);
        }
        l.levels.clearRetainingCapacity();
        return e;
    }

    fn match(l: *Ladder, book: *Orders, taker: Handle, now: i64, out: *Trades) !void {
        while (true) {
            const t = book.live(taker) orelse return;
            if (t.left == 0) return;
            const limited = t.kind == .limit or t.kind == .stop_limit;
            const limit = t.price;
            if (l.levels.items.len == 0) return;
            const key = l.levels.items[l.levels.items.len - 1].key;
            const p = key * l.sign;
            if (limited and !(if (l.sign > 0) p <= limit else p >= limit)) return;
            try l.sweepLevel(key, book, taker, now, out);
        }
    }

    fn sweepLevel(l: *Ladder, key: i64, book: *Orders, taker: Handle, now: i64, out: *Trades) !void {
        const price = key * l.sign;
        const f = l.find(key);
        if (!f.found) return;
        const lvl = &l.levels.items[f.i];
        while (lvl.qty > 0) {
            const maker = lvl.orders.popFront() orelse break;
            if (book.live(maker) == null or std.meta.eql(maker, taker)) continue;
            const t = book.get(taker).?;
            const m = book.get(maker).?;
            const q = @min(t.left, m.left);
            t.left -= q;
            m.left -= q;
            lvl.qty -= q;
            try out.record(.{ .taker = t.id, .maker = m.id, .taker_seq = t.seq, .maker_seq = m.seq, .side = t.side, .price = price, .qty = q, .ts = now });
            if (m.left > 0) {
                try lvl.orders.pushFront(gpa, maker);
            } else {
                lvl.live -= 1;
                try book.retire(maker);
            }
            if (t.left == 0) break;
        }
        if (lvl.qty == 0) l.drop(f.i);
    }

    fn top(l: *const Ladder, out: []Depth) []Depth {
        const n = @min(out.len, l.levels.items.len);
        const items = l.levels.items;
        for (0..n) |i| {
            const lvl = items[items.len - 1 - i];
            out[i] = .{ .price = lvl.key * l.sign, .qty = lvl.qty, .orders = lvl.live };
        }
        return out[0..n];
    }

    fn quantity(l: *const Ladder) i64 {
        var s: i64 = 0;
        for (l.levels.items) |lvl| s += lvl.qty;
        return s;
    }
};

// ---- the engine

const StopEntry = struct { key: i64, seq: usize, h: Handle };

fn stopOrder(_: void, a: StopEntry, b: StopEntry) std.math.Order {
    if (a.key != b.key) return std.math.order(a.key, b.key);
    return std.math.order(a.seq, b.seq);
}

const StopQueue = std.PriorityQueue(StopEntry, void, stopOrder);

const sweep_every = 4096;

const Engine = struct {
    bids: Ladder = .{ .sign = -1 },
    asks: Ladder = .{ .sign = 1 },
    orders: Orders,
    log: Trades = .{},
    buy_stops: StopQueue = .empty,
    sell_stops: StopQueue = .empty,
    work: std.Deque(Command) = .empty,
    fired: std.ArrayList(Handle) = .empty,
    halted: std.AutoHashMapUnmanaged(u32, void) = .empty,
    rejects: [7]usize = @splat(0),
    snap_event: usize = 0,
    snap_live: usize = 0,
    snap_fills: usize = 0,
    every: usize,
    now: i64 = 0,
    events: usize = 0,
    cancels: usize = 0,

    fn submit(e: *Engine, c: Command) !void {
        try e.work.pushBack(gpa, c);
        while (e.work.popFront()) |x| try e.apply(x);
        e.events += 1;
        if (e.events % sweep_every == 0) try e.orders.sweep();
        if (e.every > 0 and e.events % e.every == 0) {
            e.snap_event = e.events;
            e.snap_live = e.orders.liveCount();
            e.snap_fills = e.log.fills.items.len;
        }
    }

    fn apply(e: *Engine, c: Command) !void {
        switch (c) {
            .add => |a| {
                e.now = a.ts;
                try e.add(.{ .id = a.id, .seq = 0, .side = a.side, .kind = a.kind, .price = a.price, .stop = a.stop, .qty = a.qty, .left = a.qty, .ts = a.ts });
            },
            .cancel => |a| {
                e.now = a.ts;
                try e.cancel(a.id);
            },
            .amend => |a| {
                e.now = a.ts;
                try e.amend(a.ts, a.id, a.qty, a.price);
            },
            .halt => |a| {
                e.now = a.ts;
                try e.halted.put(gpa, a.account, {});
            },
            .@"resume" => |a| {
                e.now = a.ts;
                _ = e.halted.remove(a.account);
            },
            .bust => |a| {
                e.now = a.ts;
                if (!e.log.bust()) e.refuse(r_nothing_to_bust);
            },
            .trigger => |h| try e.fire(h),
        }
    }

    fn refuse(e: *Engine, code: usize) void {
        e.rejects[code] += 1;
        e.log.checksum = mix(e.log.checksum, 0x7e00 + @as(u64, code));
    }

    fn add(e: *Engine, o: Order) !void {
        if (o.qty <= 0 or (o.kind != .market and o.kind != .stop and o.price <= 0)) return e.refuse(r_bad_quantity);
        if (e.halted.contains(o.id.account)) return e.refuse(r_halted);
        if (e.orders.index.contains(o.id)) return e.refuse(r_duplicate);
        const h = try e.orders.add(o);
        if (o.kind == .stop or o.kind == .stop_limit) return e.arm(h);
        try e.execute(h);
    }

    fn arm(e: *Engine, h: Handle) anyerror!void {
        const o = (e.orders.live(h) orelse return).*;
        if (e.log.last) |last| {
            const reached = if (o.side == .buy) last >= o.stop else last <= o.stop;
            if (reached) return e.fire(h);
        }
        if (o.side == .buy) {
            try e.buy_stops.push(gpa, .{ .key = o.stop, .seq = o.seq, .h = h });
        } else {
            try e.sell_stops.push(gpa, .{ .key = -o.stop, .seq = o.seq, .h = h });
        }
    }

    fn fire(e: *Engine, h: Handle) anyerror!void {
        if (e.orders.live(h) == null) return;
        const o = e.orders.get(h).?;
        o.kind = if (o.kind == .stop_limit) .limit else .market;
        try e.execute(h);
    }

    fn execute(e: *Engine, h: Handle) anyerror!void {
        const before = e.log.fills.items.len;
        const side = (e.orders.live(h) orelse return).side;
        if (side == .buy) {
            try e.asks.match(&e.orders, h, e.now, &e.log);
        } else {
            try e.bids.match(&e.orders, h, e.now, &e.log);
        }
        const o = (e.orders.live(h) orelse return).*;
        if (o.left == 0) {
            try e.orders.retire(h);
        } else if (o.kind == .limit) {
            if (side == .buy) try e.bids.rest(o.price, o.left, h) else try e.asks.rest(o.price, o.left, h);
        } else {
            // The rest of a market order is cancelled.
            try e.orders.retire(h);
            if (e.log.fills.items.len == before) {
                if (e.orders.get(h) != null) try e.orders.release(h.idx);
                e.refuse(r_no_liquidity);
            }
        }
        if (e.log.fills.items.len > before) try e.triggers();
    }

    fn triggers(e: *Engine) !void {
        const last = e.log.last orelse return;
        while (e.buy_stops.peek()) |top| {
            if (top.key > last) break;
            const x = e.buy_stops.pop().?;
            if (e.orders.live(x.h) != null) try e.fired.append(gpa, x.h);
        }
        while (e.sell_stops.peek()) |top| {
            if (top.key > -last) break;
            const x = e.sell_stops.pop().?;
            if (e.orders.live(x.h) != null) try e.fired.append(gpa, x.h);
        }
        var i = e.fired.items.len;
        while (i > 0) {
            i -= 1;
            try e.work.pushFront(gpa, .{ .trigger = e.fired.items[i] });
        }
        e.fired.clearRetainingCapacity();
    }

    fn doneOrUnknown(e: *Engine, id: ClientId) void {
        e.refuse(if (e.orders.done.contains(id)) r_already_done else r_unknown);
    }

    fn cancel(e: *Engine, id: ClientId) !void {
        const h = e.orders.index.get(id) orelse return e.doneOrUnknown(id);
        if (e.orders.live(h)) |p| {
            const o = p.*;
            try e.orders.retire(h);
            if (o.kind == .limit) {
                const l = if (o.side == .buy) &e.bids else &e.asks;
                l.withdraw(o.price, o.left, h);
                try l.compact(o.price, &e.orders);
            }
        }
        e.cancels += 1;
    }

    fn amend(e: *Engine, ts: i64, id: ClientId, qty: i64, price: i64) !void {
        const h = e.orders.index.get(id) orelse return e.doneOrUnknown(id);
        if (qty <= 0 or price <= 0) return e.refuse(r_bad_quantity);
        var o = (e.orders.live(h) orelse return).*;
        if (o.kind == .limit) {
            if (o.side == .buy) e.bids.withdraw(o.price, o.left, h) else e.asks.withdraw(o.price, o.left, h);
        }
        // An amended order is a new arrival.
        o.qty = qty;
        o.left = qty;
        if (o.kind == .stop) o.stop = price else o.price = price;
        o.ts = ts;
        const h2 = try e.orders.reissue(o);
        if (o.kind == .stop or o.kind == .stop_limit) return e.arm(h2);
        try e.execute(h2);
    }

    fn checksum(e: *const Engine) u64 {
        var h = e.log.checksum;
        h = mixInt(h, e.bids.best() orelse 0);
        h = mixInt(h, e.asks.best() orelse 0);
        h = mixInt(h, e.bids.quantity());
        h = mixInt(h, e.asks.quantity());
        return mix(h, e.orders.liveCount());
    }
};

// ---- the feed

fn digits(s: []const u8) ?i64 {
    if (s.len == 0 or s.len > 15) return null;
    var n: i64 = 0;
    for (s) |b| {
        if (b < '0' or b > '9') return null;
        n = n * 10 + (b - '0');
    }
    return n;
}

fn parsePrice(s: []const u8) ?i64 {
    const dot = std.mem.indexOfScalar(u8, s, '.');
    const w = digits(s[0 .. dot orelse s.len]) orelse return null;
    var c: i64 = 0;
    if (dot) |d| {
        const f = s[d + 1 ..];
        if (f.len == 0 or f.len > 2) return null;
        c = digits(f) orelse return null;
        if (f.len == 1) c *= 10;
    }
    if (w > 9_999_999_999_999) return null;
    return w * 100 + c;
}

/// An optional + or - and then decimal digits, nothing else, the value in
/// i64: exactly what the dogfood's str.ParseInt (and Go's strconv.ParseInt)
/// accepts. std.fmt.parseInt is not used, since it also takes a '_' between
/// digits, which the dogfood refuses.
fn int(s: []const u8) ?i64 {
    var i: usize = 0;
    var neg = false;
    if (s.len > 0 and (s[0] == '+' or s[0] == '-')) {
        neg = s[0] == '-';
        i = 1;
    }
    if (i == s.len) return null;
    const limit: u64 = if (neg) 1 << 63 else (1 << 63) - 1;
    var v: u64 = 0;
    for (s[i..]) |b| {
        if (b < '0' or b > '9') return null;
        const d: u64 = b - '0';
        if (v > (limit - d) / 10) return null;
        v = v * 10 + d;
    }
    return if (neg) @bitCast(0 -% v) else @intCast(v);
}

fn account(s: []const u8) ?u32 {
    const n = int(s) orelse return null;
    if (n < 0 or n > 0xffffffff) return null;
    return @intCast(n);
}

fn clientId(s: []const u8) ?ClientId {
    const slash = std.mem.indexOfScalar(u8, s, '/') orelse return null;
    return .{ .account = account(s[0..slash]) orelse return null, .serial = account(s[slash + 1 ..]) orelse return null };
}

const BadRecord = error{BadRecord};

/// The command on one line: null for a comment or a blank line.
fn parse(line: []const u8) BadRecord!?Command {
    var cols: [7][]const u8 = @splat("");
    var count: usize = 0;
    var it = std.mem.splitScalar(u8, line, ',');
    while (it.next()) |raw| {
        const f = std.mem.trim(u8, raw, " \t\r\n\x0b\x0c");
        if (count == 0 and f.len > 0 and f[0] == '#') return null;
        if (count < 7) cols[count] = f;
        count += 1;
    }
    if (count == 1 and cols[0].len == 0) return null;
    const k = cols[0];
    const want: usize = if (k.len != 1) 0 else switch (k[0]) {
        'X' => 2,
        'C', 'H', 'R' => 3,
        'M', 'A' => 5,
        'N', 'S' => 6,
        'T' => 7,
        else => 0,
    };
    if (want == 0 or count != want) return error.BadRecord;
    const ts = int(cols[1]) orelse return error.BadRecord;
    switch (k[0]) {
        'X' => return .{ .bust = .{ .ts = ts } },
        'H' => return .{ .halt = .{ .ts = ts, .account = account(cols[2]) orelse return error.BadRecord } },
        'R' => return .{ .@"resume" = .{ .ts = ts, .account = account(cols[2]) orelse return error.BadRecord } },
        else => {},
    }
    const id = clientId(cols[2]) orelse return error.BadRecord;
    switch (k[0]) {
        'C' => return .{ .cancel = .{ .ts = ts, .id = id } },
        'A' => return .{ .amend = .{ .ts = ts, .id = id, .qty = int(cols[3]) orelse return error.BadRecord, .price = parsePrice(cols[4]) orelse return error.BadRecord } },
        else => {},
    }
    const side: Side = if (std.mem.eql(u8, cols[3], "B")) .buy else if (std.mem.eql(u8, cols[3], "S")) .sell else return error.BadRecord;
    const qty = int(cols[4]) orelse return error.BadRecord;
    return switch (k[0]) {
        'N' => .{ .add = .{ .ts = ts, .id = id, .side = side, .kind = .limit, .qty = qty, .price = parsePrice(cols[5]) orelse return error.BadRecord, .stop = 0 } },
        'M' => .{ .add = .{ .ts = ts, .id = id, .side = side, .kind = .market, .qty = qty, .price = 0, .stop = 0 } },
        'S' => .{ .add = .{ .ts = ts, .id = id, .side = side, .kind = .stop, .qty = qty, .price = 0, .stop = parsePrice(cols[5]) orelse return error.BadRecord } },
        else => .{ .add = .{ .ts = ts, .id = id, .side = side, .kind = .stop_limit, .qty = qty, .stop = parsePrice(cols[5]) orelse return error.BadRecord, .price = parsePrice(cols[6]) orelse return error.BadRecord } },
    };
}

// ---- the statistics

const Bar = struct { open: i64, high: i64, low: i64, close: i64, volume: i64, trades: usize };

const AccountRow = struct { account: u32, bought: i64, sold: i64, fills: usize };

const Summary = struct {
    p50: ?i64,
    p90: ?i64,
    p99: ?i64,
    vwap: ?i64,
    buy_vwap: ?i64,
    sell_vwap: ?i64,
    at_or_below: usize,
    bars: []Bar,
    sizes: [7]usize,
    accounts: []AccountRow,
};

fn percentile(sorted: []const i64, p: usize) ?i64 {
    if (sorted.len == 0) return null;
    const rank = (sorted.len * p + 99) / 100;
    return sorted[if (rank == 0) 0 else rank - 1];
}

fn vwap(fills: []const Fill) ?i64 {
    var q: i64 = 0;
    var n: i64 = 0;
    for (fills) |f| {
        q += f.qty;
        n += f.qty * f.price;
    }
    return if (q == 0) null else @divTrunc(n, q);
}

const size_limits = [6]i64{ 10, 50, 100, 500, 1000, 5000 };

fn atOrBelow(v: i64, x: i64) bool {
    return x <= v;
}

fn byAccount(_: void, a: AccountRow, b: AccountRow) bool {
    return a.account < b.account;
}

fn byVolume(_: void, a: AccountRow, b: AccountRow) bool {
    return a.bought + a.sold > b.bought + b.sold;
}

fn summarize(fills: []const Fill, per: usize) !Summary {
    const n = fills.len;
    const prices = try gpa.alloc(i64, n);
    defer gpa.free(prices);
    for (fills, 0..) |f, i| prices[i] = f.price;
    const sorted = try gpa.dupe(i64, prices);
    defer gpa.free(sorted);
    std.mem.sortUnstable(i64, sorted, {}, std.sort.asc(i64));
    const v = vwap(fills);
    const below = if (v) |x| std.sort.partitionPoint(i64, sorted, x, atOrBelow) else 0;
    const xs = try gpa.dupe(Fill, fills);
    defer gpa.free(xs);
    var cut: usize = 0;
    for (0..xs.len) |i| {
        if (xs[i].side == .buy) {
            std.mem.swap(Fill, &xs[i], &xs[cut]);
            cut += 1;
        }
    }
    var sizes: [7]usize = @splat(0);
    for (fills) |f| {
        var c: usize = 0;
        for (size_limits) |l| {
            if (f.qty <= l) break;
            c += 1;
        }
        sizes[c] += 1;
    }
    return .{
        .p50 = percentile(sorted, 50),
        .p90 = percentile(sorted, 90),
        .p99 = percentile(sorted, 99),
        .vwap = v,
        .buy_vwap = vwap(xs[0..cut]),
        .sell_vwap = vwap(xs[cut..]),
        .at_or_below = below,
        .bars = try bars(fills, per),
        .sizes = sizes,
        .accounts = try accounts(fills),
    };
}

fn bars(fills: []const Fill, per: usize) ![]Bar {
    var out: std.ArrayList(Bar) = .empty;
    if (fills.len == 0 or per < 1) return out.items;
    const volumes = try gpa.alloc(i64, fills.len);
    defer gpa.free(volumes);
    for (fills, 0..) |f, i| volumes[i] = f.qty;
    var lo: usize = 0;
    while (lo < volumes.len) : (lo += per) {
        var sum: i64 = 0;
        for (volumes[lo..@min(lo + per, volumes.len)]) |*x| {
            sum += x.*;
            x.* = sum;
        }
    }
    lo = 0;
    while (lo < fills.len) : (lo += per) {
        const hi = @min(lo + per, fills.len);
        const c = fills[lo..hi];
        var b: Bar = .{ .open = c[0].price, .high = c[0].price, .low = c[0].price, .close = c[c.len - 1].price, .volume = volumes[hi - 1], .trades = c.len };
        for (c) |f| {
            b.high = @max(b.high, f.price);
            b.low = @min(b.low, f.price);
        }
        try out.append(gpa, b);
    }
    const n = out.items.len;
    if (n >= 2 and out.items[n - 1].trades * 2 < per) {
        const last = out.pop().?;
        const prev = &out.items[n - 2];
        prev.high = @max(prev.high, last.high);
        prev.low = @min(prev.low, last.low);
        prev.close = last.close;
        prev.volume += last.volume;
        prev.trades += last.trades;
    }
    return out.items;
}

fn accounts(fills: []const Fill) ![]AccountRow {
    var by: std.AutoHashMapUnmanaged(u32, AccountRow) = .empty;
    defer by.deinit(gpa);
    for (fills) |f| {
        const buyer = if (f.side == .buy) f.taker.account else f.maker.account;
        const seller = if (f.side == .buy) f.maker.account else f.taker.account;
        const b = try by.getOrPut(gpa, buyer);
        if (!b.found_existing) b.value_ptr.* = .{ .account = buyer, .bought = 0, .sold = 0, .fills = 0 };
        b.value_ptr.bought += f.qty;
        b.value_ptr.fills += 1;
        const s = try by.getOrPut(gpa, seller);
        if (!s.found_existing) s.value_ptr.* = .{ .account = seller, .bought = 0, .sold = 0, .fills = 0 };
        s.value_ptr.sold += f.qty;
        s.value_ptr.fills += 1;
    }
    const rows = try gpa.alloc(AccountRow, by.count());
    var it = by.valueIterator();
    var i: usize = 0;
    while (it.next()) |r| : (i += 1) rows[i] = r.*;
    std.mem.sortUnstable(AccountRow, rows, {}, byAccount);
    std.mem.sort(AccountRow, rows, {}, byVolume);
    return rows;
}

// ---- the report

const PriceText = struct {
    buf: [32]u8 = undefined,
    fn of(p: *PriceText, t: i64) []const u8 {
        const a: u64 = @abs(t);
        return std.fmt.bufPrint(&p.buf, "{s}{d}.{d:0>2}", .{ if (t < 0) "-" else "", a / 100, a % 100 }) catch unreachable;
    }
    fn opt(p: *PriceText, t: ?i64) []const u8 {
        return if (t) |v| p.of(v) else "-";
    }
};

fn idText(buf: []u8, c: ClientId) []const u8 {
    return std.fmt.bufPrint(buf, "{d}/{d}", .{ c.account, c.serial }) catch unreachable;
}

/// What the dogfood's JSON report holds, built whether or not it is
/// written, as the dogfood builds it: its prices and ids as text.
fn buildReport(e: *const Engine, s: *const Summary, bids: []const Depth, asks: []const Depth) !std.ArrayList([]u8) {
    var out: std.ArrayList([]u8) = .empty;
    const ps = [_]?i64{ s.vwap, s.buy_vwap, s.sell_vwap, s.p50, s.p99, e.log.last, e.bids.best(), e.asks.best() };
    var pt: PriceText = .{};
    for (ps) |p| if (p) |v| try out.append(gpa, try gpa.dupe(u8, pt.of(v)));
    for (bids) |d| try out.append(gpa, try gpa.dupe(u8, pt.of(d.price)));
    for (asks) |d| try out.append(gpa, try gpa.dupe(u8, pt.of(d.price)));
    var ib: [32]u8 = undefined;
    for (e.log.top.items) |f| {
        try out.append(gpa, try gpa.dupe(u8, idText(&ib, f.taker)));
        try out.append(gpa, try gpa.dupe(u8, idText(&ib, f.maker)));
        try out.append(gpa, try gpa.dupe(u8, pt.of(f.price)));
    }
    for (s.bars) |b| {
        for ([_]i64{ b.open, b.high, b.low, b.close }) |p| try out.append(gpa, try gpa.dupe(u8, pt.of(p)));
    }
    return out;
}

fn fillsTable(w: *std.Io.Writer, title: []const u8, fills: []const Fill, newest_first: bool) !void {
    try w.print("{s}\n", .{title});
    if (fills.len == 0) return w.writeAll("  (none)\n");
    var pt: PriceText = .{};
    var tb: [32]u8 = undefined;
    var mb: [32]u8 = undefined;
    for (0..fills.len) |i| {
        const f = fills[if (newest_first) fills.len - 1 - i else i];
        try w.print("  {d:>10}  {s:<4} {d:>7} @ {s:>10}  taker {s:<9} maker {s}\n", .{ u(f.ts), f.side.name(), u(f.qty), pt.of(f.price), idText(&tb, f.taker), idText(&mb, f.maker) });
    }
}

fn lineInt(w: *std.Io.Writer, label: []const u8, v: anytype) !void {
    try w.print("{s:<18}{d:>12}\n", .{ label, v });
}

fn lineStr(w: *std.Io.Writer, label: []const u8, v: []const u8) !void {
    try w.print("{s:<18}{s:>12}\n", .{ label, v });
}

fn busiestFirst(_: void, a: Depth, b: Depth) bool {
    if (a.qty != b.qty) return a.qty > b.qty;
    return a.price < b.price;
}

fn bySeq(_: void, a: Order, b: Order) bool {
    return a.seq < b.seq;
}

fn textReport(w: *std.Io.Writer, e: *Engine, s: *const Summary, bids: []const Depth, asks: []const Depth, sum: []const u8) !void {
    const t = &e.log;
    var pt: PriceText = .{};
    try lineInt(w, "commands", e.events);
    try lineInt(w, "orders", e.orders.seq);
    try lineInt(w, "fills", t.fills.items.len);
    try lineInt(w, "cancels", e.cancels);
    try lineInt(w, "busts", t.busted);
    try lineInt(w, "live orders", e.orders.liveCount());
    try lineInt(w, "stop queue entries", e.buy_stops.count() + e.sell_stops.count());
    for (e.rejects, 0..) |n, i| {
        if (n > 0) try w.print("rejected: {s:<14}{d:>12}\n", .{ reject_names[i], n });
    }
    try lineInt(w, "volume", u(t.traded));
    try lineStr(w, "vwap", pt.opt(s.vwap));
    try lineStr(w, "vwap, buyers", pt.opt(s.buy_vwap));
    try lineStr(w, "vwap, sellers", pt.opt(s.sell_vwap));
    try lineStr(w, "median fill", pt.opt(s.p50));
    try lineStr(w, "90th percentile", pt.opt(s.p90));
    try lineStr(w, "99th percentile", pt.opt(s.p99));
    try lineInt(w, "fills <= vwap", s.at_or_below);
    try lineStr(w, "last", pt.opt(t.last));
    try lineInt(w, "traded prices", t.volume.count());
    try w.writeAll("\n");
    try w.print("{s:>10} {s:>6} {s:>12} | {s:<12} {s:>10} {s:>6}\n", .{ "bid qty", "orders", "bid", "ask", "ask qty", "orders" });
    const rows = @max(bids.len, asks.len);
    if (rows == 0) try w.print("{s:^60}\n", .{"(the book is empty)"});
    for (0..rows) |i| {
        if (i < bids.len) {
            try w.print("{d:>10} {d:>6} {s:>12}", .{ u(bids[i].qty), bids[i].orders, pt.of(bids[i].price) });
        } else {
            try w.print("{s:<30}", .{""});
        }
        try w.writeAll(" | ");
        if (i < asks.len) try w.print("{s:<12} {d:>10} {d:>6}", .{ pt.of(asks[i].price), u(asks[i].qty), asks[i].orders });
        try w.writeAll("\n");
    }
    try w.writeAll("\n");
    try fillsTable(w, "largest fills", t.top.items, false);
    var tape: [tape_length]Fill = undefined;
    for (0..t.tape.len) |i| tape[i] = t.tape.at(i);
    try fillsTable(w, "recent fills, newest first", tape[0..t.tape.len], true);
    var busiest: std.ArrayList(Depth) = .empty;
    var vi = t.volume.iterator();
    while (vi.next()) |kv| try busiest.append(gpa, .{ .price = kv.key_ptr.*, .qty = kv.value_ptr.*, .orders = 0 });
    std.mem.sortUnstable(Depth, busiest.items, {}, busiestFirst);
    try w.writeAll("most traded prices\n");
    for (busiest.items[0..@min(3, busiest.items.len)]) |d| try w.print("  {s:>12} {d:>10}\n", .{ pt.of(d.price), u(d.qty) });
    var resting: std.ArrayList(Order) = .empty;
    for (e.orders.slots.items) |sl| {
        if (sl.used and e.orders.hasFlag(sl.o.seq)) try resting.append(gpa, sl.o);
    }
    std.mem.sortUnstable(Order, resting.items, {}, bySeq);
    try w.print("oldest resting orders ({d} live)\n", .{resting.items.len});
    var ib: [32]u8 = undefined;
    for (resting.items[0..@min(3, resting.items.len)]) |r| {
        try w.print("  {s:<9} {s:<4} {s:<9} {d:>7} @ {s:>10}\n", .{ idText(&ib, r.id), r.side.name(), r.kind.name(), u(r.left), pt.of(if (r.kind == .stop) r.stop else r.price) });
    }
    if (s.bars.len > 0) {
        try w.print("bars, newest first\n{s:>5} {s:>10} {s:>10} {s:>10} {s:>10} {s:>10}\n", .{ "#", "open", "high", "low", "close", "volume" });
        var p2: PriceText = .{};
        var p3: PriceText = .{};
        var p4: PriceText = .{};
        for (0..@min(10, s.bars.len)) |i| {
            const b = s.bars[s.bars.len - 1 - i];
            try w.print("{d:>5} {s:>10} {s:>10} {s:>10} {s:>10} {d:>10}\n", .{ s.bars.len - i, pt.of(b.open), p2.of(b.high), p3.of(b.low), p4.of(b.close), u(b.volume) });
        }
    }
    const labels = [7][]const u8{ "<=10", "<=50", "<=100", "<=500", "<=1000", "<=5000", ">5000" };
    try w.writeAll("fill sizes\n");
    for (s.sizes, 0..) |c, i| try w.print("  {s:<7}{d:>9}\n", .{ labels[i], c });
    try w.print("accounts by volume\n{s:>4} {s:>8} {s:>10} {s:>10} {s:>7}\n", .{ "rank", "account", "bought", "sold", "fills" });
    for (s.accounts[0..@min(10, s.accounts.len)], 0..) |r, i| {
        try w.print("{d:>4} {d:>8} {d:>10} {d:>10} {d:>7}\n", .{ i + 1, r.account, u(r.bought), u(r.sold), r.fills });
    }
    try w.print("checksum {s}\n", .{sum});
}

// ---- main

fn usage(io: std.Io, msg: []const u8) u8 {
    var buf: [256]u8 = undefined;
    var ew = std.Io.File.stderr().writer(io, &buf);
    ew.interface.print("error: {s}\nusage: orderbook replay FEED [--quiet] [--snapshot N]\n", .{msg}) catch {};
    ew.interface.flush() catch {};
    return 2;
}

fn fail(io: std.Io, comptime f: []const u8, args: anytype, code: u8) u8 {
    var buf: [512]u8 = undefined;
    var ew = std.Io.File.stderr().writer(io, &buf);
    ew.interface.print(f, args) catch {};
    ew.interface.flush() catch {};
    return code;
}

pub fn main(init: std.process.Init) u8 {
    return run(init) catch |err| switch (err) {
        error.OutOfMemory => fail(init.io, "error: out of memory\n", .{}, 5),
        else => fail(init.io, "error: {s}\n", .{@errorName(err)}, 4),
    };
}

fn run(init: std.process.Init) !u8 {
    gpa = std.heap.smp_allocator;
    const io = init.io;
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len < 2 or !std.mem.eql(u8, args[1], "replay")) return usage(io, "the only command is replay");
    var quiet = false;
    var every: usize = 10_000;
    var path: ?[]const u8 = null;
    var i: usize = 2;
    while (i < args.len) : (i += 1) {
        const a = args[i];
        if (std.mem.eql(u8, a, "--quiet")) {
            quiet = true;
        } else if (std.mem.eql(u8, a, "--snapshot") and i + 1 < args.len) {
            const n = int(args[i + 1]) orelse return usage(io, "--snapshot does not take that value");
            if (n < 0 or n > 1_000_000_000_000) return usage(io, "--snapshot does not take that value");
            every = @intCast(n);
            i += 1;
        } else if (!std.mem.startsWith(u8, a, "--") and path == null) {
            path = a;
        } else {
            return usage(io, "unexpected argument");
        }
    }
    const p = path orelse return usage(io, "replay needs a feed file");
    const file = std.Io.Dir.cwd().openFile(io, p, .{}) catch return fail(io, "error: cannot read {s}\n", .{p}, 4);
    defer file.close(io);
    const st = file.stat(io) catch return fail(io, "error: cannot read {s}\n", .{p}, 4);
    if (st.kind != .file) return fail(io, "error: cannot read {s}\n", .{p}, 4);
    const nbits: usize = @intCast(st.size / 3 + 2);
    const flags = try gpa.alloc(u64, (nbits + 63) / 64);
    @memset(flags, 0);
    var e: Engine = .{ .orders = .{ .flags = flags, .nbits = nbits }, .every = every };
    var rbuf: [64 * 1024]u8 = undefined;
    var fr = file.reader(io, &rbuf);
    const r = &fr.interface;
    var n: usize = 0;
    while (true) {
        const raw = r.takeDelimiter('\n') catch |err| switch (err) {
            error.StreamTooLong => return fail(io, "{s}:{d}: not a valid record\n", .{ p, n + 1 }, 3),
            else => return fail(io, "error: cannot read {s}\n", .{p}, 4),
        } orelse break;
        n += 1;
        const l = if (raw.len > 0 and raw[raw.len - 1] == '\r') raw[0 .. raw.len - 1] else raw;
        if (std.mem.indexOfScalar(u8, l, '"') != null) return fail(io, "{s}:{d}: not a valid record\n", .{ p, n }, 3);
        const c = parse(l) catch return fail(io, "{s}:{d}: not a valid record\n", .{ p, n }, 3);
        if (c) |cmd| try e.submit(cmd);
    }
    var wbuf: [64 * 1024]u8 = undefined;
    var ow = std.Io.File.stdout().writer(io, &wbuf);
    const w = &ow.interface;
    const s = try summarize(e.log.fills.items, 1000);
    var bb: [5]Depth = undefined;
    var ab: [5]Depth = undefined;
    const bids = e.bids.top(&bb);
    const asks = e.asks.top(&ab);
    var sb: [16]u8 = undefined;
    const sum = std.fmt.bufPrint(&sb, "{x:0>16}", .{e.checksum()}) catch unreachable;
    if (quiet) {
        try w.print("commands {d} fills {d} checksum {s}\n", .{ e.events, e.log.fills.items.len, sum });
    } else {
        try textReport(w, &e, &s, bids, asks, sum);
    }
    // The report's values are built, as the dogfood builds its JSON report;
    // only its --json would write them.
    const rv = try buildReport(&e, &s, bids, asks);
    _ = rv;
    const eb = e.bids.close(&e.orders);
    const ea = e.asks.close(&e.orders);
    if (!quiet) {
        try w.print("last snapshot at command {d}: {d} live orders, {d} fills\n", .{ e.snap_event, e.snap_live, e.snap_fills });
        try w.print("expired at close: {d} bids ({d}), {d} asks ({d})\n", .{ eb.orders, eb.qty, ea.orders, ea.qty });
    }
    try w.flush();
    return 0;
}
