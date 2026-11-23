// strbuild, Zig, idiomatic std: the task of README.md with std.ArrayList(u8)
// over std.heap.smp_allocator, growing from .empty per document, appending
// literals, slices, repeated chunks and formatted integers via std.fmt.bufPrint,
// then hashing the built bytes with FNV-1a 64 plus their length.
const std = @import("std");

const words = [16][]const u8{
    "alpha", "bravo", "charlie", "delta",
    "echo", "foxtrot", "golf", "hotel",
    "india", "juliet", "kilo", "lima",
    "mike", "november", "oscar", "papa",
};

const src = "the quick brown fox jumps over the lazy dog 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ ";

fn fnv(b: []const u8) u64 {
    var h: u64 = 0xcbf29ce484222325;
    for (b) |c| h = (h ^ c) *% 0x100000001b3;
    return h;
}

fn buildDoc(gpa: std.mem.Allocator, d: usize, lines: usize) !u64 {
    var b: std.ArrayList(u8) = .empty;
    defer b.deinit(gpa);
    var num: [24]u8 = undefined;
    for (0..lines) |i| {
        const x = d * 131 + i * 17;
        try b.appendSlice(gpa, "rec[");
        const s1 = try std.fmt.bufPrint(&num, "{d}", .{i});
        try b.appendSlice(gpa, s1);
        try b.appendSlice(gpa, "] ");
        try b.appendSlice(gpa, words[x & 15]);
        try b.appendSlice(gpa, ":");
        const off = (x * 7) % 40;
        const slen = 8 + (x % 25);
        try b.appendSlice(gpa, src[off .. off + slen]);
        const reps = (i & 3) + 1;
        for (0..reps) |_| try b.appendSlice(gpa, "-=");
        try b.appendSlice(gpa, " #");
        const s2 = try std.fmt.bufPrint(&num, "{d}", .{x});
        try b.appendSlice(gpa, s2);
        try b.appendSlice(gpa, "\n");
    }
    return fnv(b.items) +% b.items.len;
}

pub fn main(init: std.process.Init) !void {
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 3) return error.Usage;
    const docs = try std.fmt.parseInt(usize, args[1], 10);
    const lines = try std.fmt.parseInt(usize, args[2], 10);
    const gpa = std.heap.smp_allocator;
    var total: u64 = 0;
    for (0..docs) |d| total +%= try buildDoc(gpa, d, lines);
    var obuf: [64]u8 = undefined;
    var w = std.Io.File.stdout().writer(init.io, &obuf);
    try w.interface.print("{d}\n", .{total});
    try w.interface.flush();
}
