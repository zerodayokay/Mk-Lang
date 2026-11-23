// hashmap-heap, Zig, idiomatic std: the task of README.md with
// std.AutoHashMap(u64, u64) on the default general-purpose allocator
// (init.gpa, no arena; wyhash-based autoHash with seed 0); grows from empty.
const std = @import("std");

fn mix(x: u64) u64 {
    var z = x;
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9;
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb;
    return z ^ (z >> 31);
}

fn round(gpa: std.mem.Allocator, n: usize, seed: u64) !u64 {
    var m = std.AutoHashMap(u64, u64).init(gpa);
    defer m.deinit();
    var st = seed;
    var i: usize = 0;
    while (i < n) : (i += 1) {
        st +%= 0x9e3779b97f4a7c15;
        try m.put(mix(st), @intCast(i));
    }
    var sum: u64 = 0;
    st = seed;
    i = 0;
    while (i < n) : (i += 1) {
        st +%= 0x9e3779b97f4a7c15;
        const k = mix(st);
        if (m.get(k)) |v| sum +%= v;
        if (m.contains(mix(st ^ 0x5555555555555555))) sum +%= 1;
        if (i % 2 == 0) _ = m.remove(k);
    }
    st = seed;
    i = 0;
    while (i < n) : (i += 1) {
        st +%= 0x9e3779b97f4a7c15;
        if (m.contains(mix(st))) sum +%= 3;
    }
    return sum +% m.count();
}

pub fn main(init: std.process.Init) !void {
    const gpa = init.gpa;
    var args = try init.minimal.args.iterateAllocator(gpa);
    defer args.deinit();
    _ = args.next();
    const arg1 = args.next() orelse return error.Usage;
    const arg2 = args.next() orelse return error.Usage;
    const arg3 = args.next() orelse return error.Usage;
    const n = try std.fmt.parseInt(usize, arg1, 10);
    const rounds = try std.fmt.parseInt(u64, arg2, 10);
    const seed = try std.fmt.parseInt(u64, arg3, 10);
    var total: u64 = 0;
    var r: u64 = 0;
    while (r < rounds) : (r += 1) total +%= try round(gpa, n, seed +% r);
    var buf: [64]u8 = undefined;
    var w = std.Io.File.stdout().writer(init.io, &buf);
    try w.interface.print("{d}\n", .{total});
    try w.interface.flush();
}
