// knucleotide, Zig: the reference algorithm of README.md (2-bit packed DNA
// k-mers in a fixed open-addressed linear-probing hash table, Fibonacci hash,
// 70% load factor). Wrapping u64 hashing and checksum (overflow class "wraps").
// ReleaseSafe checks bounds; ReleaseFast does not. No hints.
const std = @import("std");

// tools/bench flips this line in a copy for the counting build.
const bench_count = false;

const IM: i64 = 139968;
const IA: i64 = 3877;
const IC: i64 = 29573;
const FIB_MUL: u64 = 0x9e3779b97f4a7c15;

const Entry = struct {
    key: u64,
    val: u64,
};

const Table = struct {
    entries: []Entry,
    cap: usize,
    shift: u6,
    len: usize,

    fn init(alloc: std.mem.Allocator) !Table {
        const entries = try alloc.alloc(Entry, 64);
        @memset(entries, .{ .key = 0, .val = 0 });
        return .{ .entries = entries, .cap = 64, .shift = 6, .len = 0 };
    }

    fn deinit(self: *Table, alloc: std.mem.Allocator) void {
        alloc.free(self.entries);
    }

    fn grow(self: *Table, alloc: std.mem.Allocator, rehashes: *u64) !void {
        if (bench_count) {
            rehashes.* +%= 1;
        }
        const new_cap = self.cap * 2;
        const new_shift = self.shift + 1;
        const next = try alloc.alloc(Entry, new_cap);
        @memset(next, .{ .key = 0, .val = 0 });
        const mask = new_cap - 1;
        const sh: u6 = @intCast(64 - @as(u7, new_shift));
        for (0..self.cap) |i| {
            const e = self.entries[i];
            if (e.val != 0) {
                var slot: usize = @intCast(((e.key *% FIB_MUL) >> sh) & @as(u64, mask));
                while (true) {
                    if (next[slot].val == 0) {
                        next[slot] = e;
                        break;
                    }
                    slot = (slot + 1) & mask;
                }
            }
        }
        alloc.free(self.entries);
        self.entries = next;
        self.cap = new_cap;
        self.shift = new_shift;
    }

    fn inc(self: *Table, alloc: std.mem.Allocator, key: u64, probes: *u64, rehashes: *u64) !void {
        if ((self.len + 1) * 10 > self.cap * 7) {
            try self.grow(alloc, rehashes);
        }
        const mask = self.cap - 1;
        const sh: u6 = @intCast(64 - @as(u7, self.shift));
        var slot: usize = @intCast(((key *% FIB_MUL) >> sh) & @as(u64, mask));
        while (true) {
            if (bench_count) {
                probes.* +%= 1;
            }
            const v = self.entries[slot].val;
            if (v == 0) {
                self.entries[slot] = .{ .key = key, .val = 1 };
                self.len += 1;
                return;
            }
            if (self.entries[slot].key == key) {
                self.entries[slot].val = v +% 1;
                return;
            }
            slot = (slot + 1) & mask;
        }
    }

    fn get(self: *const Table, key: u64, probes: *u64) u64 {
        const mask = self.cap - 1;
        const sh: u6 = @intCast(64 - @as(u7, self.shift));
        var slot: usize = @intCast(((key *% FIB_MUL) >> sh) & @as(u64, mask));
        while (true) {
            if (bench_count) {
                probes.* +%= 1;
            }
            const v = self.entries[slot].val;
            if (v == 0) {
                return 0;
            }
            if (self.entries[slot].key == key) {
                return v;
            }
            slot = (slot + 1) & mask;
        }
    }
};

fn packSeq(s: []const u8) u64 {
    var key: u64 = 0;
    for (s) |c| {
        const code: u64 = if (c == 'A') 0 else if (c == 'C') 1 else if (c == 'G') 2 else 3;
        key = (key << 2) | code;
    }
    return key;
}

fn countK(alloc: std.mem.Allocator, seq: []const u8, k: usize, probes: *u64, rehashes: *u64) !u64 {
    var tab = try Table.init(alloc);
    defer tab.deinit(alloc);
    const n = seq.len;
    if (n >= k) {
        const shift: u6 = @intCast(2 * k);
        const mask: u64 = (@as(u64, 1) << shift) -% 1;
        var key: u64 = 0;
        for (0..k - 1) |i| {
            key = (key << 2) | @as(u64, seq[i]);
        }
        for (k - 1..n) |i| {
            key = ((key << 2) | @as(u64, seq[i])) & mask;
            try tab.inc(alloc, key, probes, rehashes);
        }
    }
    var h: u64 = @intCast(tab.len);
    var max_val: u64 = 0;
    for (0..tab.cap) |i| {
        const v = tab.entries[i].val;
        if (v > max_val) {
            max_val = v;
        }
    }
    h = h *% 31 +% max_val;
    if (k == 1) {
        for (0..4) |c| {
            h = h *% 31 +% tab.get(@intCast(c), probes);
        }
    } else if (k == 2) {
        for (0..16) |c| {
            h = h *% 31 +% tab.get(@intCast(c), probes);
        }
    } else if (k == 3) {
        h = h *% 31 +% tab.get(packSeq("GGT"), probes);
    } else if (k == 4) {
        h = h *% 31 +% tab.get(packSeq("GGTA"), probes);
    } else if (k == 6) {
        h = h *% 31 +% tab.get(packSeq("GGTATT"), probes);
    } else if (k == 12) {
        h = h *% 31 +% tab.get(packSeq("GGTATTTTAATT"), probes);
    } else if (k == 18) {
        h = h *% 31 +% tab.get(packSeq("GGTATTTTAATTTATAGT"), probes);
    }
    return h;
}

pub fn main(init: std.process.Init) !void {
    const alloc = init.gpa;
    var args = try init.minimal.args.iterateAllocator(alloc);
    defer args.deinit();
    _ = args.next();
    const arg1 = args.next() orelse return error.InvalidArguments;
    const arg2 = args.next() orelse return error.InvalidArguments;
    const n = try std.fmt.parseInt(usize, arg1, 10);
    const rounds = try std.fmt.parseInt(usize, arg2, 10);
    const seq = try alloc.alloc(u8, n);
    defer alloc.free(seq);
    const p0: f64 = 0.3029549426680;
    const p1: f64 = p0 + 0.1979883004921;
    const p2: f64 = p1 + 0.1975473066391;
    var seed: i64 = 42;
    for (0..n) |i| {
        seed = @rem(seed * IA + IC, IM);
        const r = @as(f64, @floatFromInt(seed)) / @as(f64, @floatFromInt(IM));
        if (r < p0) {
            seq[i] = 0;
        } else if (r < p1) {
            seq[i] = 1;
        } else if (r < p2) {
            seq[i] = 2;
        } else {
            seq[i] = 3;
        }
    }
    const ks = [_]usize{ 1, 2, 3, 4, 6, 12, 18 };
    var probes: u64 = 0;
    var rehashes: u64 = 0;
    var total: u64 = 0;
    for (0..rounds) |_| {
        for (ks) |k| {
            total = total *% 31 +% try countK(alloc, seq, k, &probes, &rehashes);
        }
    }
    var out_buf: [256]u8 = undefined;
    var out = std.Io.File.stdout().writer(init.io, &out_buf);
    try out.interface.print("{d}\n", .{total});
    if (bench_count) {
        try out.interface.print("counts probes={d} rehashes={d}\n", .{ probes, rehashes });
    }
    try out.interface.flush();
}
