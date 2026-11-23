// hashmap-str, Zig, idiomatic std: the task of README.md with
// std.StringHashMap(u64) (its default hash, std.hash.Wyhash with seed 0:
// fixed, no DoS resistance) over an ArenaAllocator per round; keys are
// 8-24 byte ASCII slices from one contiguous buffer, and the map grows
// from empty.
const std = @import("std");

const alph = "abcdefghijklmnopqrstuvwxyz012345";

fn mix(x: u64) u64 {
    var z = x;
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9;
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb;
    return z ^ (z >> 31);
}

fn fillKey(buf: []u8, pos: usize, idx: usize, tag: u8, st: *u64, seed: u64) usize {
    st.* +%= 0x9e3779b97f4a7c15;
    const z = mix(st.*);
    const length: usize = 8 + @as(usize, @intCast(z % 17));
    const u = (@as(u64, @intCast(idx)) *% 0x9e3779b97f4a7c15) ^ (seed *% 0x517cc1b727220a95);
    for (0..7) |j| {
        const shift: u6 = @intCast(5 * j);
        buf[pos + j] = alph[@as(usize, @intCast((u >> shift) & 31))];
    }
    buf[pos + 7] = tag;
    var w = z >> 8;
    for (8..length) |j| {
        if (j == 16) {
            st.* +%= 0x9e3779b97f4a7c15;
            w = mix(st.*);
        }
        buf[pos + j] = alph[@as(usize, @intCast(w & 31))];
        w >>= 5;
    }
    return pos + length;
}

fn round(keys: []const []const u8, misses: []const []const u8, r: u64) !u64 {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    var m = std.StringHashMap(u64).init(arena.allocator());
    for (keys, 0..) |k, i| {
        try m.put(k, @as(u64, @intCast(i)) +% r);
    }
    var sum: u64 = 0;
    for (keys, 0..) |k, i| {
        if (m.get(k)) |v| sum +%= v;
        if (m.contains(misses[i])) sum +%= 1;
        if (i % 2 == 0) _ = m.remove(k);
    }
    for (keys) |k| {
        if (m.contains(k)) sum +%= 3;
    }
    return sum +% m.count();
}

pub fn main(init: std.process.Init) !void {
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 4) return error.Usage;
    const n = try std.fmt.parseInt(usize, args[1], 10);
    const rounds = try std.fmt.parseInt(u64, args[2], 10);
    const seed = try std.fmt.parseInt(u64, args[3], 10);
    const pa = std.heap.page_allocator;
    const buf = try pa.alloc(u8, 2 * n * 24);
    defer pa.free(buf);
    const keys = try pa.alloc([]const u8, n);
    defer pa.free(keys);
    const misses = try pa.alloc([]const u8, n);
    defer pa.free(misses);
    var st = seed;
    var pos: usize = 0;
    for (0..n) |i| {
        const lo = pos;
        pos = fillKey(buf, pos, i, 'k', &st, seed);
        keys[i] = buf[lo..pos];
    }
    for (0..n) |i| {
        const lo = pos;
        pos = fillKey(buf, pos, i, 'm', &st, seed);
        misses[i] = buf[lo..pos];
    }
    var total: u64 = 0;
    var r: u64 = 0;
    while (r < rounds) : (r += 1) total +%= try round(keys, misses, r);
    var obuf: [64]u8 = undefined;
    var w = std.Io.File.stdout().writer(init.io, &obuf);
    try w.interface.print("{d}\n", .{total});
    try w.interface.flush();
}
