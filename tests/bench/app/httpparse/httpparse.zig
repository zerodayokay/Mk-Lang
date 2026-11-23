// httpparse, Zig, std: the task of README.md, a strict RFC 9112 HTTP/1.1
// request parser over a splitmix64-generated batch of valid and malformed
// requests (GET/POST/PUT/DELETE, paths and query strings, headers, fixed
// Content-Length and chunked bodies, plus 10 malformed classes). Uses
// std.mem.indexOfScalar for delimiter scans, reused header array and chunked
// body ArrayList, and wrapping u64 FNV-1a digest. No forced inline, no hints,
// no @setRuntimeSafety.
// Usage: httpparse <num_requests> <rounds> <seed>.
const std = @import("std");

const FNV_BASIS: u64 = 0xcbf29ce484222325;
const FNV_PRIME: u64 = 0x100000001b3;
const MAX_HEADERS: usize = 64;

const Rng = struct {
    s: u64,
    fn next(self: *Rng) u64 {
        self.s +%= 0x9e3779b97f4a7c15;
        var z = self.s;
        z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9;
        z = (z ^ (z >> 27)) *% 0x94d049bb133111eb;
        return z ^ (z >> 31);
    }
};

fn pushU64(alloc: std.mem.Allocator, dst: *std.ArrayList(u8), v: u64) !void {
    if (v == 0) {
        try dst.append(alloc, '0');
        return;
    }
    var tmp: [24]u8 = undefined;
    var n: usize = 0;
    var x = v;
    while (x > 0) {
        tmp[n] = @intCast('0' + (x % 10));
        x /= 10;
        n += 1;
    }
    var i = n;
    while (i > 0) {
        i -= 1;
        try dst.append(alloc, tmp[i]);
    }
}

fn pushHex(alloc: std.mem.Allocator, dst: *std.ArrayList(u8), v: usize) !void {
    const hex = "0123456789abcdef";
    if (v == 0) {
        try dst.append(alloc, '0');
        return;
    }
    var tmp: [16]u8 = undefined;
    var n: usize = 0;
    var x = v;
    while (x > 0) {
        tmp[n] = hex[x & 15];
        x >>= 4;
        n += 1;
    }
    var i = n;
    while (i > 0) {
        i -= 1;
        try dst.append(alloc, tmp[i]);
    }
}

fn buildBatch(alloc: std.mem.Allocator, n: usize, seed: u64, data: *std.ArrayList(u8), starts: *std.ArrayList(usize)) !void {
    var rng = Rng{ .s = seed };
    for (0..n) |_| {
        try starts.append(alloc, data.items.len);
        const r = rng.next();
        if (r % 20 == 0) {
            const bad: []const u8 = switch ((r >> 8) % 10) {
                0 => "GET /api/v1/bad HTTP/2.0\r\nHost: api.example.com\r\n\r\n",
                1 => "GET /api/v1/bad HTTP/1.1\nHost: api.example.com\r\n\r\n",
                2 => "GET /api/v1/bad HTTP/1.1\r\nHost : api.example.com\r\n\r\n",
                3 => "GET /api/v1/bad HTTP/1.1\r\nUser-Agent: bench/1.0\r\n\r\n",
                4 => "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n0\r\n\r\n",
                5 => "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\nabcd",
                6 => "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 12abc\r\n\r\nabcd",
                7 => "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 20\r\n\r\nshort",
                8 => "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\nabcd\r\n0\r\n\r\n",
                else => "POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n",
            };
            try data.appendSlice(alloc, bad);
            continue;
        }
        const m = (r >> 5) % 4;
        switch (m) {
            0 => try data.appendSlice(alloc, "GET "),
            1 => try data.appendSlice(alloc, "POST "),
            2 => try data.appendSlice(alloc, "PUT "),
            else => try data.appendSlice(alloc, "DELETE "),
        }
        try data.appendSlice(alloc, "/api/v1/users/");
        try pushU64(alloc, data, (r >> 8) % 10000);
        try data.appendSlice(alloc, "/items/");
        try pushU64(alloc, data, (r >> 22) % 100000);
        if (((r >> 40) & 1) == 1) {
            try data.appendSlice(alloc, "?page=");
            try pushU64(alloc, data, (r >> 41) % 100);
            try data.appendSlice(alloc, "&limit=");
            try pushU64(alloc, data, 10 + ((r >> 48) % 90));
            try data.appendSlice(alloc, "&sort=asc");
        }
        try data.appendSlice(alloc, " HTTP/1.1\r\nHost: api");
        try pushU64(alloc, data, r & 7);
        try data.appendSlice(alloc, ".example.com\r\nUser-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) BenchAgent/1.0\r\nAccept: application/json, text/plain;q=0.9, */*;q=0.8\r\nAccept-Encoding: gzip, deflate, br\r\n");
        if (((r >> 39) & 1) == 1) {
            try data.appendSlice(alloc, "Connection: close\r\n");
        } else {
            try data.appendSlice(alloc, "Connection: keep-alive\r\n");
        }
        const r2 = rng.next();
        try data.appendSlice(alloc, "X-Request-Id: req-");
        try pushU64(alloc, data, r2 % 100000000);
        try data.appendSlice(alloc, "\r\n");
        if (m == 1 or m == 2) {
            try data.appendSlice(alloc, "Content-Type: application/json\r\n");
            if (((r2 >> 30) & 1) == 1) {
                try data.appendSlice(alloc, "Transfer-Encoding: chunked\r\n\r\n");
                const nchunks: usize = 2 + @as(usize, @intCast((r2 >> 32) % 4));
                for (0..nchunks) |ci| {
                    const shift: u6 = @intCast(ci * 7);
                    const clen: usize = 16 + @as(usize, @intCast(((r2 >> shift) ^ (@as(u64, ci) * 13)) % 64));
                    try pushHex(alloc, data, clen);
                    try data.appendSlice(alloc, "\r\n");
                    for (0..clen) |j| {
                        try data.append(alloc, @intCast('a' + ((ci * 7 + j) % 26)));
                    }
                    try data.appendSlice(alloc, "\r\n");
                }
                try data.appendSlice(alloc, "0\r\n\r\n");
            } else {
                const blen: usize = 32 + @as(usize, @intCast((r2 >> 32) % 224));
                try data.appendSlice(alloc, "Content-Length: ");
                try pushU64(alloc, data, blen);
                try data.appendSlice(alloc, "\r\n\r\n");
                const off: usize = @intCast(r2 % 26);
                for (0..blen) |j| {
                    try data.append(alloc, @intCast('a' + ((j + off) % 26)));
                }
            }
        } else {
            try data.appendSlice(alloc, "\r\n");
        }
    }
    try starts.append(alloc, data.items.len);
}

const SliceSpan = struct { off: usize = 0, len: usize = 0 };
const HeaderSpan = struct { name: SliceSpan = .{}, value: SliceSpan = .{} };
const Parsed = struct {
    method: SliceSpan = .{},
    path: SliceSpan = .{},
    query: SliceSpan = .{},
    nhdrs: usize = 0,
    body_off: usize = 0,
    body_len: usize = 0,
    body_from_buf: bool = false,
    chunked: bool = false,
    keep_alive: bool = false,
};

fn isTchar(b: u8) bool {
    if ((b >= '0' and b <= '9') or (b >= 'A' and b <= 'Z') or (b >= 'a' and b <= 'z')) return true;
    return switch (b) {
        '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~' => true,
        else => false,
    };
}

fn lowerAscii(b: u8) u8 {
    return if (b >= 'A' and b <= 'Z') b + 32 else b;
}

fn eqIgnoreAscii(a: []const u8, lit: []const u8) bool {
    if (a.len != lit.len) return false;
    for (a, lit) |x, y| {
        if (lowerAscii(x) != y) return false;
    }
    return true;
}

fn hexVal(b: u8) ?usize {
    if (b >= '0' and b <= '9') return b - '0';
    if (b >= 'a' and b <= 'f') return b - 'a' + 10;
    if (b >= 'A' and b <= 'F') return b - 'A' + 10;
    return null;
}

fn parseRequest(alloc: std.mem.Allocator, buf: []const u8, hdrs: *[MAX_HEADERS]HeaderSpan, body_buf: *std.ArrayList(u8), out: *Parsed) !bool {
    const n = buf.len;
    const nl = std.mem.indexOfScalar(u8, buf, '\n') orelse return false;
    if (nl < 1 or buf[nl - 1] != '\r') return false;
    const line_end = nl - 1;
    const sp1 = std.mem.indexOfScalar(u8, buf[0..line_end], ' ') orelse return false;
    if (sp1 == 0) return false;
    for (buf[0..sp1]) |b| {
        if (!isTchar(b)) return false;
    }
    const rest1 = sp1 + 1;
    const sp2_rel = std.mem.indexOfScalar(u8, buf[rest1..line_end], ' ') orelse return false;
    if (sp2_rel == 0) return false;
    const sp2 = rest1 + sp2_rel;
    if (buf[rest1] != '/') return false;

    var qpos: ?usize = null;
    for (rest1..sp2) |i| {
        const c = buf[i];
        if (c < 0x21 or c > 0x7e) return false;
        if (c == '?' and qpos == null) qpos = i;
    }
    const ver_start = sp2 + 1;
    if (!std.mem.eql(u8, buf[ver_start..line_end], "HTTP/1.1")) return false;

    out.method = .{ .off = 0, .len = sp1 };
    if (qpos) |q| {
        out.path = .{ .off = rest1, .len = q - rest1 };
        out.query = .{ .off = q + 1, .len = sp2 - (q + 1) };
    } else {
        out.path = .{ .off = rest1, .len = sp2 - rest1 };
        out.query = .{ .off = 0, .len = 0 };
    }

    var pos = nl + 1;
    var nhdrs: usize = 0;
    var has_host = false;
    var has_cl = false;
    var cl_val: usize = 0;
    var chunked = false;
    var keep_alive = true;
    var head_done = false;

    while (pos < n) {
        const ln_rel = std.mem.indexOfScalar(u8, buf[pos..], '\n') orelse return false;
        if (ln_rel < 1) return false;
        const ln_end = pos + ln_rel - 1;
        if (buf[ln_end] != '\r') return false;
        if (ln_end == pos) {
            pos += ln_rel + 1;
            head_done = true;
            break;
        }
        if (buf[pos] == ' ' or buf[pos] == '\t') return false;
        const colon_rel = std.mem.indexOfScalar(u8, buf[pos..ln_end], ':') orelse return false;
        if (colon_rel == 0) return false;
        const name_start = pos;
        const name_len = colon_rel;
        for (buf[name_start .. name_start + name_len]) |b| {
            if (!isTchar(b)) return false;
        }
        var vs = name_start + name_len + 1;
        var ve = ln_end;
        while (vs < ve and (buf[vs] == ' ' or buf[vs] == '\t')) vs += 1;
        while (ve > vs and (buf[ve - 1] == ' ' or buf[ve - 1] == '\t')) ve -= 1;
        for (buf[vs..ve]) |c| {
            if ((c < 0x20 and c != '\t') or c == 0x7f) return false;
        }
        if (nhdrs >= MAX_HEADERS) return false;
        hdrs[nhdrs] = .{
            .name = .{ .off = name_start, .len = name_len },
            .value = .{ .off = vs, .len = ve - vs },
        };
        nhdrs += 1;

        const name = buf[name_start .. name_start + name_len];
        const val = buf[vs..ve];
        if (name_len == 4 and eqIgnoreAscii(name, "host")) {
            if (has_host or val.len == 0) return false;
            has_host = true;
        } else if (name_len == 14 and eqIgnoreAscii(name, "content-length")) {
            if (val.len == 0) return false;
            var v: usize = 0;
            for (val) |c| {
                if (c < '0' or c > '9') return false;
                v = v * 10 + (c - '0');
                if (v > 1_073_741_824) return false;
            }
            if (has_cl and v != cl_val) return false;
            has_cl = true;
            cl_val = v;
        } else if (name_len == 17 and eqIgnoreAscii(name, "transfer-encoding")) {
            if (chunked or !eqIgnoreAscii(val, "chunked")) return false;
            chunked = true;
        } else if (name_len == 10 and eqIgnoreAscii(name, "connection")) {
            if (eqIgnoreAscii(val, "close")) {
                keep_alive = false;
            } else if (eqIgnoreAscii(val, "keep-alive")) {
                keep_alive = true;
            }
        }
        pos += ln_rel + 1;
    }

    if (!head_done or !has_host or (has_cl and chunked)) return false;
    out.nhdrs = nhdrs;
    out.chunked = chunked;
    out.keep_alive = keep_alive;

    if (chunked) {
        body_buf.clearRetainingCapacity();
        while (true) {
            if (pos >= n) return false;
            const sz_rel = std.mem.indexOfScalar(u8, buf[pos..], '\n') orelse return false;
            if (sz_rel < 1) return false;
            const sz_end = pos + sz_rel - 1;
            if (buf[sz_end] != '\r') return false;
            var sz: usize = 0;
            var digits: usize = 0;
            for (buf[pos..sz_end]) |c| {
                if (c == ';') break;
                const hv = hexVal(c) orelse return false;
                sz = sz * 16 + hv;
                if (sz > 16_777_216) return false;
                digits += 1;
            }
            if (digits == 0) return false;
            pos += sz_rel + 1;
            if (sz == 0) {
                if (pos + 2 != n or buf[pos] != '\r' or buf[pos + 1] != '\n') return false;
                break;
            }
            if (pos + sz + 2 > n) return false;
            try body_buf.appendSlice(alloc, buf[pos .. pos + sz]);
            pos += sz;
            if (buf[pos] != '\r' or buf[pos + 1] != '\n') return false;
            pos += 2;
        }
        out.body_off = 0;
        out.body_len = body_buf.items.len;
        out.body_from_buf = true;
    } else if (has_cl) {
        if (n - pos != cl_val) return false;
        out.body_off = pos;
        out.body_len = cl_val;
        out.body_from_buf = false;
    } else {
        if (pos != n) return false;
        out.body_off = 0;
        out.body_len = 0;
        out.body_from_buf = false;
    }
    return true;
}

fn fnvByte(h: u64, b: u8) u64 {
    return (h ^ @as(u64, b)) *% FNV_PRIME;
}

fn fnvBytes(h0: u64, s: []const u8) u64 {
    var h = h0;
    for (s) |b| h = (h ^ @as(u64, b)) *% FNV_PRIME;
    return h;
}

fn fnvBytesLower(h0: u64, s: []const u8) u64 {
    var h = h0;
    for (s) |b| h = (h ^ @as(u64, lowerAscii(b))) *% FNV_PRIME;
    return h;
}

pub fn main(init: std.process.Init) !void {
    const alloc = init.gpa;
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 4) std.process.exit(2);
    const num_req = try std.fmt.parseInt(usize, args[1], 10);
    const rounds = try std.fmt.parseInt(usize, args[2], 10);
    const seed = try std.fmt.parseInt(u64, args[3], 10);

    var data: std.ArrayList(u8) = .empty;
    defer data.deinit(alloc);
    var starts: std.ArrayList(usize) = .empty;
    defer starts.deinit(alloc);
    try buildBatch(alloc, num_req, seed, &data, &starts);

    var hdrs: [MAX_HEADERS]HeaderSpan = undefined;
    var body_buf: std.ArrayList(u8) = .empty;
    defer body_buf.deinit(alloc);
    var parsed: Parsed = .{};

    var ok_cnt: i64 = 0;
    var err_cnt: i64 = 0;
    var hdr_cnt: i64 = 0;
    var body_cnt: i64 = 0;
    var chunked_cnt: i64 = 0;
    var ka_cnt: i64 = 0;
    var h: u64 = FNV_BASIS;

    for (0..rounds) |_| {
        for (0..num_req) |i| {
            const req_buf = data.items[starts.items[i]..starts.items[i + 1]];
            if (try parseRequest(alloc, req_buf, &hdrs, &body_buf, &parsed)) {
                ok_cnt += 1;
                hdr_cnt += @intCast(parsed.nhdrs);
                body_cnt += @intCast(parsed.body_len);
                if (parsed.chunked) chunked_cnt += 1;
                if (parsed.keep_alive) ka_cnt += 1;
                h = fnvBytes(h, req_buf[parsed.method.off .. parsed.method.off + parsed.method.len]);
                h = fnvByte(h, 1);
                h = fnvBytes(h, req_buf[parsed.path.off .. parsed.path.off + parsed.path.len]);
                h = fnvByte(h, 2);
                h = fnvBytes(h, req_buf[parsed.query.off .. parsed.query.off + parsed.query.len]);
                h = fnvByte(h, 3);
                for (0..parsed.nhdrs) |k| {
                    const hs = hdrs[k];
                    h = fnvBytesLower(h, req_buf[hs.name.off .. hs.name.off + hs.name.len]);
                    h = fnvByte(h, 4);
                    h = fnvBytes(h, req_buf[hs.value.off .. hs.value.off + hs.value.len]);
                    h = fnvByte(h, 5);
                }
                if (parsed.body_from_buf) {
                    h = fnvBytes(h, body_buf.items);
                } else {
                    h = fnvBytes(h, req_buf[parsed.body_off .. parsed.body_off + parsed.body_len]);
                }
                h = fnvByte(h, 6);
            } else {
                err_cnt += 1;
                h = fnvByte(h, 0xff);
                h = fnvByte(h, @intCast(i % 251));
            }
        }
    }

    var out_buf: [256]u8 = undefined;
    var out = std.Io.File.stdout().writer(init.io, &out_buf);
    try out.interface.print("ok={d} err={d} hdrs={d} body={d} chunked={d} keepalive={d} fnv={x:0>16}\n", .{
        ok_cnt, err_cnt, hdr_cnt, body_cnt, chunked_cnt, ka_cnt, h,
    });
    try out.interface.flush();
}
