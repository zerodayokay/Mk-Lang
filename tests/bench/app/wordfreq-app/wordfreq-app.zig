// wordfreq-app, Zig, std: the task of README.md. Streams the input file
// through a 64 KiB read buffer, tokenizes ASCII words ([A-Za-z], folded to
// lowercase) carrying partial words across chunk boundaries, counts frequencies
// in std.StringHashMap(i64) (Wyhash, keys duplicated into an arena on first
// insert only), sorts unique entries by (count desc, word asc) via
// std.mem.sort, and writes total, unique and top-K lines to a buffered stdout
// writer. No forced inline, no hints, no @setRuntimeSafety.
// Usage: wordfreq-app <file> <top_k> [<max_words>].
const std = @import("std");

const Entry = struct {
    word: []const u8,
    count: i64,
};

fn entryLess(_: void, a: Entry, b: Entry) bool {
    if (a.count != b.count) return a.count > b.count;
    return std.mem.order(u8, a.word, b.word) == .lt;
}

fn recordWord(arena: std.mem.Allocator, counts: *std.StringHashMap(i64), word: []const u8) !void {
    if (counts.getPtr(word)) |p| {
        p.* += 1;
        return;
    }
    const owned = try arena.dupe(u8, word);
    try counts.put(owned, 1);
}

pub fn main(init: std.process.Init) !void {
    const alloc = init.gpa;
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len < 3 or args.len > 4) std.process.exit(2);
    const top_k = try std.fmt.parseInt(usize, args[2], 10);
    const limit: i64 = if (args.len == 4) try std.fmt.parseInt(i64, args[3], 10) else -1;

    var key_arena = std.heap.ArenaAllocator.init(alloc);
    defer key_arena.deinit();
    const kalloc = key_arena.allocator();

    var counts = std.StringHashMap(i64).init(alloc);
    defer counts.deinit();

    const file = try std.Io.Dir.cwd().openFile(init.io, args[1], .{});
    defer file.close(init.io);

    var rbuf: [64 * 1024]u8 = undefined;
    var r = file.readerStreaming(init.io, &rbuf);

    var wbuf: std.ArrayList(u8) = .empty;
    defer wbuf.deinit(alloc);

    var chunk: [64 * 1024]u8 = undefined;
    var total: i64 = 0;
    var done = false;

    while (!done) {
        const n = try r.interface.readSliceShort(&chunk);
        if (n == 0) break;
        var i: usize = 0;
        while (i < n) {
            const c = chunk[i];
            if ((c >= 'a' and c <= 'z') or (c >= 'A' and c <= 'Z')) {
                if (wbuf.items.len == 0) {
                    const start = i;
                    var has_upper = (c >= 'A' and c <= 'Z');
                    i += 1;
                    while (i < n) {
                        const b = chunk[i];
                        if (b >= 'a' and b <= 'z') {
                            i += 1;
                        } else if (b >= 'A' and b <= 'Z') {
                            has_upper = true;
                            i += 1;
                        } else {
                            break;
                        }
                    }
                    if (i == n) {
                        for (chunk[start..i]) |b| {
                            try wbuf.append(alloc, if (b >= 'A' and b <= 'Z') b + 32 else b);
                        }
                    } else if (!has_upper) {
                        try recordWord(kalloc, &counts, chunk[start..i]);
                        total += 1;
                        if (limit >= 0 and total >= limit) {
                            done = true;
                            break;
                        }
                    } else {
                        wbuf.clearRetainingCapacity();
                        for (chunk[start..i]) |b| {
                            try wbuf.append(alloc, if (b >= 'A' and b <= 'Z') b + 32 else b);
                        }
                        try recordWord(kalloc, &counts, wbuf.items);
                        wbuf.clearRetainingCapacity();
                        total += 1;
                        if (limit >= 0 and total >= limit) {
                            done = true;
                            break;
                        }
                    }
                } else {
                    try wbuf.append(alloc, if (c >= 'A' and c <= 'Z') c + 32 else c);
                    i += 1;
                }
            } else {
                if (wbuf.items.len > 0) {
                    try recordWord(kalloc, &counts, wbuf.items);
                    wbuf.clearRetainingCapacity();
                    total += 1;
                    if (limit >= 0 and total >= limit) {
                        done = true;
                        break;
                    }
                }
                i += 1;
            }
        }
    }
    if (!done and wbuf.items.len > 0) {
        try recordWord(kalloc, &counts, wbuf.items);
        total += 1;
    }

    var entries: std.ArrayList(Entry) = .empty;
    defer entries.deinit(alloc);
    try entries.ensureTotalCapacity(alloc, counts.count());
    var it = counts.iterator();
    while (it.next()) |kv| {
        entries.appendAssumeCapacity(.{ .word = kv.key_ptr.*, .count = kv.value_ptr.* });
    }
    std.mem.sort(Entry, entries.items, {}, entryLess);

    const uniq = entries.items.len;
    const k = @min(top_k, uniq);
    var obuf: [64 * 1024]u8 = undefined;
    var out = std.Io.File.stdout().writer(init.io, &obuf);
    try out.interface.print("total {d}\nunique {d}\n", .{ total, uniq });
    for (entries.items[0..k]) |e| {
        try out.interface.print("{s} {d}\n", .{ e.word, e.count });
    }
    try out.interface.flush();
}
