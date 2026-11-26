// Why: the RFC 8448 oracle and the codec tests need many cases from one build,
// and they reach private entries std does not export.
// What: tlsDriver, the Mk driver program; tlsFixture, which exports the private
// entries; tlsExe, tlsBuild, tlsAsk and tlsRun, which build it and feed it.
// How: the driver reads one record a line on standard input and answers each
// with one line; MK_TLS_BASE builds over a snapshot of std with std/tls taken
// from this tree.

package std

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// tlsDriver is the Mk side. Arguments are lower-case hex or decimals;
// every answer is "ok ..." or "err <the error as {e} prints it>".
const tlsDriver = `import std.tls

Nib(c: u8): u8 {
    if c >= b'a' {
        return c - 87
    }
    return c - 48
}

Unhex(a: &Arena, s: str): OutOfMemory![]u8 from a {
    b := s.AsBytes()
    out := try a.Slice[u8](b.Len() / 2)
    for i in 0..out.Len() {
        out[i] = (Nib(b[2 * i]) << 4) | Nib(b[2 * i + 1])
    }
    return out[..]
}

const Digits: str = "0123456789abcdef"

Hex(a: &Arena, b: []u8): OutOfMemory!str from a {
    out := try a.Slice[u8](2 * b.Len())
    d := Digits.AsBytes()
    for i in 0..b.Len() {
        out[2 * i] = d[int(b[i] >> 4)]
        out[2 * i + 1] = d[int(b[i] & 15)]
    }
    return str.From(out[..]).Must()
}

Field(parts: &List[str], i: int): str {
    if i < parts.Len() {
        return parts[i]
    }
    return ""
}

Num(parts: &List[str], i: int): int {
    return Cast[int](Field(parts, i).ParseInt() else { -1 })
}

Case(a: &Arena, io: Io, line: str into a): OutOfMemory!str from a {
    var parts := List[str].New(a)
    for p in line.Split(" ") {
        try parts.Push(p)
    }
    op := Field(&parts, 0)
    // Room for a message as long as the line's hex, twice (FxFuzzMessage
    // rewrites into both halves): a 256 KiB Certificate fits (TLS-R11).
    out := try a.Slice[u8](70000 + line.Len())
    if op == "x" {
        salt := try Unhex(a, Field(&parts, 2))
        ikm := try Unhex(a, Field(&parts, 3))
        n := tls.FxExtract(Num(&parts, 1), salt, ikm, mut out[..])
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    if op == "l" {
        secret := try Unhex(a, Field(&parts, 2))
        label := str.From(try Unhex(a, Field(&parts, 3))) else { "" }
        ctx := try Unhex(a, Field(&parts, 4))
        n := Num(&parts, 5)
        tls.FxExpandLabel(Num(&parts, 1), secret, label, ctx, mut out[0..n])
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    if op == "k" {
        secret := try Unhex(a, Field(&parts, 2))
        n := tls.FxKeyIv(Num(&parts, 1), secret, mut out[..])
        return try a.Fmt("ok {k} {iv}", (k: try Hex(a, out[0..n]), iv: try Hex(a, out[32..44])))
    }
    if op == "f" {
        base := try Unhex(a, Field(&parts, 2))
        hash := try Unhex(a, Field(&parts, 3))
        n := tls.FxFinished(Num(&parts, 1), base, hash, mut out[..])
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    if op == "c" {
        shared := try Unhex(a, Field(&parts, 2))
        n := tls.FxSchedule(Num(&parts, 1), shared, mut out[..])
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    if op == "t" {
        ops := try Unhex(a, Field(&parts, 2))
        n := tls.FxTranscript(Num(&parts, 1), ops, mut out[..])
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    if op == "s" {
        key := try Unhex(a, Field(&parts, 2))
        iv := try Unhex(a, Field(&parts, 3))
        content := try Unhex(a, Field(&parts, 6))
        n := tls.FxSeal(Num(&parts, 1), key, iv, Cast[u64](Num(&parts, 4)), Trunc[u8](Num(&parts, 5)), content, mut out[..])
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    if op == "o" {
        key := try Unhex(a, Field(&parts, 2))
        iv := try Unhex(a, Field(&parts, 3))
        record := try Unhex(a, Field(&parts, 5))
        r := tls.FxOpen(Num(&parts, 1), key, iv, Cast[u64](Num(&parts, 4)), record, mut out[..])
        if r.alert != 0 {
            return try a.Fmt("err alert {c}", (c: r.alert))
        }
        return try a.Fmt("ok {t} {h}", (t: r.ty, h: try Hex(a, out[0..r.n])))
    }
    if op == "p" {
        msg := try Unhex(a, Field(&parts, 1))
        n := tls.FxRewrite(msg, mut out[..]) else e {
            return try a.Fmt("err {e}", (e))
        }
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    if op == "z" {
        msg := try Unhex(a, Field(&parts, 1))
        return tls.FxFuzzMessage(msg, mut out[..])
    }
    if op == "y" {
        b := try Unhex(a, Field(&parts, 1))
        return tls.FxFuzzRecord(io, b, mut out[..])
    }
    if op == "g" {
        msg := try Unhex(a, Field(&parts, 1))
        return tls.FxFuzzLogic(msg)
    }
    if op == "d" {
        secret := try Unhex(a, Field(&parts, 1))
        point := try Unhex(a, Field(&parts, 2))
        n := tls.FxX25519(secret, point, mut out[..])
        return try a.Fmt("ok {h}", (h: try Hex(a, out[0..n])))
    }
    return "err unknown op"
}

export Main(sys: &System): ReadError|WriteError|OutOfMemory!void {
    var r := sys.Stdin()
    var out := sys.Stdout()
    // One record is at most 1 MiB: a 256 KiB message's hex and its op
    // (TLS-R11; Reader.Line's 64 KiB buffer is too short for it).
    var lines := try Arena.New(sys.heap)
    defer lines.Free()
    big := try lines.Slice[u8](1 << 20)
    var arena := try Arena.New(sys.heap)
    defer arena.Free()
    for {
        // Until gives a long line in pieces: read until one ends it.
        var n := 0
        for n < big.Len() {
            k := try r.Until(0x0A, mut big[n..])
            if k == 0 {
                break
            }
            n += k
            if big[n - 1] == 0x0A {
                break
            }
        }
        if n == 0 {
            break
        }
        if big[n - 1] == 0x0A {
            n -= 1
        }
        if n > 0 && big[n - 1] == 0x0D {
            n -= 1
        }
        // A copy on the arena Case answers on, so big is free for the
        // next record.
        text := str.From(big[0..n]) else {
            fail ReadError
        }
        line := try arena.Dup(text)
        answer := try Case(&arena, sys.io, line)
        try out.Str(answer)
        try out.Str("\n")
        try out.Flush()
        arena.Reset()
    }
    try out.Flush()
}
`

// tlsFixture is written to std/tls in the driver's copy of std.
const tlsFixture = `// Fixture of the std-tls tests: private entries, exported.

import std.crypto
import std.net
import std.x509

FxHashOf(h: int): crypto.HashKind {
    return if h == 384 { crypto.HashKind.Sha384 } else { crypto.HashKind.Sha256 }
}

FxSuiteOf(s: int): Suite {
    return SuiteOf(Trunc[u16](s)) else { Suite.Aes128Gcm }
}

FxSecret(h: int, b: []u8): Secret {
    var s := Secret(kind: FxHashOf(h), b: [0; 48])
    (mut s.b[0..b.Len()]).CopyFrom(b)
    return s
}

/// FxExtract is Extract under the hash h (256 or 384).
export FxExtract(h: int, salt: []u8, ikm: []u8, out: mut []u8): int {
    s := Extract(FxHashOf(h), salt, ikm)
    n := HashLen(s.kind)
    (mut out[0..n]).CopyFrom(s.Bytes())
    return n
}

/// FxExpandLabel is ExpandLabel.
export FxExpandLabel(h: int, secret: []u8, label: str, ctx: []u8, out: mut []u8) {
    s := FxSecret(h, secret)
    ExpandLabel(&s, label, ctx, out)
}

/// FxKeyIv writes the key at out[0..] and the IV at out[32..44].
export FxKeyIv(suite: int, secret: []u8, out: mut []u8): int {
    su := FxSuiteOf(suite)
    s := FxSecret(if su == Suite.Aes256Gcm { 384 } else { 256 }, secret)
    k := TrafficKeyOf(su, &s)
    n := SuiteKeyLen(su)
    (mut out[0..n]).CopyFrom(k.key[0..n])
    (mut out[32..44]).CopyFrom(k.iv[..])
    return n
}

/// FxFinished is FinishedValue.
export FxFinished(h: int, base: []u8, hash: []u8, out: mut []u8): int {
    s := FxSecret(h, base)
    FinishedValue(&s, hash, out)
    return HashLen(s.kind)
}

/// FxSchedule writes the early, handshake and master secrets.
export FxSchedule(h: int, shared: []u8, out: mut []u8): int {
    var s := Schedule.New(FxHashOf(h))
    s.DeriveHandshake(shared)
    s.DeriveMaster()
    n := HashLen(s.kind)
    (mut out[0..n]).CopyFrom(s.early.Bytes())
    (mut out[n..2 * n]).CopyFrom(s.handshake.Bytes())
    (mut out[2 * n..3 * n]).CopyFrom(s.master.Bytes())
    return 3 * n
}

/// FxTranscript runs ops: 'm', a 24-bit length and a message to add; 'c'
/// to choose h; 'r' to restart after a HelloRetryRequest. It writes the
/// hash under h.
export FxTranscript(h: int, ops: []u8, out: mut []u8): int {
    k := FxHashOf(h)
    var t := Transcript.New()
    var i := 0
    for i < ops.Len() {
        op := ops[i]
        i += 1
        if op == b'm' {
            n := (int(ops[i]) << 16) | (int(ops[i + 1]) << 8) | int(ops[i + 2])
            t.Add(ops[i + 3..i + 3 + n])
            i += 3 + n
        } else if op == b'c' {
            t.Choose(k)
        } else if op == b'r' {
            t.Restart(k)
        }
    }
    t.Sum(k, out)
    return HashLen(k)
}

FxAead(suite: int, key: []u8, iv: []u8, seq: u64): Aead {
    su := FxSuiteOf(suite)
    var tk := TrafficKey(suite: su, key: [0; 32], iv: [0; 12])
    (mut tk.key[0..key.Len()]).CopyFrom(key)
    (mut tk.iv[..]).CopyFrom(iv)
    var x := Aead.New(&tk)
    x.seq = seq
    return x
}

/// FxSeal seals content of type ty as record number seq.
export FxSeal(suite: int, key: []u8, iv: []u8, seq: u64, ty: u8, content: []u8, out: mut []u8): int {
    var x := FxAead(suite, key, iv, seq)
    var inner: [16385]u8 = [0; 16385]
    return x.Seal(ty, content, mut inner[..], out)
}

/// FxOpen opens record (header and body) as record number seq.
export FxOpen(suite: int, key: []u8, iv: []u8, seq: u64, record: []u8, out: mut []u8): (ty: u8, n: int, alert: u8) {
    var x := FxAead(suite, key, iv, seq)
    var alert: u8 = 0
    if record.Len() < 5 {
        return (ty: 0, n: 0, alert: AlertDecodeError)
    }
    r := x.Open(record[0..5], record[5..], out, mut alert) else {
        return (ty: 0, n: 0, alert: alert)
    }
    return (ty: r.ty, n: r.n, alert: 0)
}

/// FxFuzzMessage rewrites msg (FxRewrite) and rewrites the result again:
/// "err" when msg does not parse, "same" when the rewrite is msg itself,
/// "lossy" when it differs but the second rewrite equals the first, and
/// "unstable" otherwise, which is a fault.
export FxFuzzMessage(msg: []u8, out: mut []u8): str from msg {
    (left, right) := out.SplitAt(out.Len() / 2)
    n1 := FxRewrite(msg, left) else {
        return "err"
    }
    n2 := FxRewrite(left[0..n1], right) else {
        return "unstable"
    }
    if n1 != n2 {
        return "unstable"
    }
    for i in 0..n1 {
        if left[i] != right[i] {
            return "unstable"
        }
    }
    if n1 != msg.Len() {
        return "lossy"
    }
    for i in 0..n1 {
        if left[i] != msg[i] {
            return "lossy"
        }
    }
    return "same"
}

/// FxFuzzRecord runs b through a record path chosen by its first byte: 0,
/// the rest opened as a record under a fixed key; 1, the rest read as the
/// handshake content of records after the handshake (KeyUpdate and
/// NewSessionTicket), in pieces of the second byte's length; 2, the rest
/// sealed as a whole inner plaintext and opened (FxFuzzInner). "ok",
/// "err", or "bad" for a fault; never a panic.
export FxFuzzRecord(io: Io, b: []u8, out: mut []u8): str from b {
    if b.Len() < 2 {
        return "short"
    }
    if b[0] == 2 {
        return FxFuzzInner(b[1..], out)
    }
    if b[0] == 0 {
        key: [16]u8 = [1; 16]
        iv: [12]u8 = [2; 12]
        r := FxOpen(4865, key[..], iv[..], 0, b[1..], out)
        return if r.alert == 0 { "ok" } else { "err" }
    }
    var rd := ReadHalf.New()
    sh := Mutex[Shared].New(Shared(fault: none, owe: false, notified: false, rshut: false, wshut: false, down: false,
        writing: false))
    piece := if b[1] == 0 { 1 } else { int(b[1]) }
    var at := 2
    for at < b.Len() {
        k := if b.Len() - at < piece { b.Len() - at } else { piece }
        (mut rd.plain[0..k]).CopyFrom(b[at..at + k])
        var alert: u8 = 0
        rd.PostHandshake(&sh, io, k, mut alert) else {
            _ = sh.Into()
            return "err"
        }
        at += k
    }
    _ = sh.Into()
    return "ok"
}

/// FxFuzzInner seals b[1..] as a whole TLSInnerPlaintext (its last byte
/// the type, so any padding and any type reach Open) under the suite b[0]
/// picks, opens it, and checks Open's answer against RFC 8446 5.4's
/// rule: the content type is the last nonzero byte, the content what
/// precedes it, an all-zero inner plaintext unexpected_message. "ok",
/// "err" (refused as the rule says) or "bad" (a fault).
FxFuzzInner(b: []u8, out: mut []u8): str from b {
    if b.Len() < 2 || b.Len() - 1 > 16385 {
        return "short"
    }
    suites: [3]int = [4865, 4866, 4867]
    suite := suites[int(b[0]) % 3]
    key: [32]u8 = [3; 32]
    iv: [12]u8 = [4; 12]
    inner := b[1..]
    ty := inner[inner.Len() - 1]
    var rec: [16500]u8 = [0; 16500]
    klen := if suite == 4865 { 16 } else { 32 }
    n := FxSeal(suite, key[0..klen], iv[..], 7, ty, inner[0..inner.Len() - 1], mut rec[..])
    r := FxOpen(suite, key[0..klen], iv[..], 7, rec[0..n], out)
    var i := inner.Len()
    for i > 0 && inner[i - 1] == 0 {
        i -= 1
    }
    if i == 0 {
        return if r.alert == AlertUnexpectedMessage { "err" } else { "bad" }
    }
    if r.alert != 0 || r.ty != inner[i - 1] || r.n != i - 1 {
        return "bad"
    }
    for j in 0..r.n {
        if out[j] != inner[j] {
            return "bad"
        }
    }
    return "ok"
}

/// FxFuzzLogic runs a handshake message through the checks a peer's
/// message reaches after its parser: a ClientHello through ReadOffer
/// (first and after a HelloRetryRequest for X25519 and for P-256), a
/// ServerHello through CheckServerHello (as an answer and as a retry),
/// EncryptedExtensions through CheckEncryptedExtensions; the client's
/// Hello takes the ServerHello's session id when it is 32 bytes, so a
/// mutation reaches past that check. "ok", "err", or "skip" for another
/// message; never a panic.
export FxFuzzLogic(msg: []u8): str {
    r := ParseHandshake(msg, 0xFFFFFF) else {
        return "err"
    }
    got := r else {
        return "err"
    }
    m := got.m
    seed: [32]u8 = [9; 32]
    key := crypto.PrivateKey.Ed25519(seed: seed)
    nochain: [0]x509.Certificate = []
    ids: [1]Identity = [Identity(chain: nochain[..], key: &key)]
    protos: [2]str = ["h2", "http/1.1"]
    var cfg := DefaultConfig
    cfg.identities = ids[..]
    cfg.alpn = protos[..]
    suites: [3]u16 = [0x1301, 0x1302, 0x1303]
    var hello := Hello(suites: suites[..], random: [0; 32], sid: [0; 32], sni: "example.com", alpn: protos[..],
        group: GroupX25519, cookie: b"")
    var alert: u8 = 0
    if m.ty == HsClientHello {
        ch := ParseClientHello(m.body) else {
            return "err"
        }
        retries: [3]u16 = [0, GroupX25519, GroupP256]
        var any := false
        for g in retries {
            _ = ReadOffer(mut alert, &ch, &cfg, g) else {
                continue
            }
            any = true
        }
        return if any { "ok" } else { "err" }
    }
    if m.ty == HsServerHello {
        sh := ParseServerHello(m.body) else {
            return "err"
        }
        if sh.legacy_id.Len() == 32 {
            (mut hello.sid[..]).CopyFrom(sh.legacy_id)
        }
        var any := false
        retry: [2]bool = [false, true]
        for x in retry {
            _ = CheckServerHello(mut alert, &sh, &hello, x) else {
                continue
            }
            any = true
        }
        return if any { "ok" } else { "err" }
    }
    if m.ty == HsEncryptedExtensions {
        exts := ParseEncryptedExtensions(m.body) else {
            return "err"
        }
        _ = CheckEncryptedExtensions(mut alert, exts, &hello) else {
            return "err"
        }
        return "ok"
    }
    return "skip"
}

/// FxRequestUpdate sends a KeyUpdate that asks for one back (RFC 8446
/// 4.6.3) and moves the write key forward, as a peer may at any time.
export FxRequestUpdate(c: &Conn, io: Io): Error!void {
    hv := c.box.Ref()
    var g := try hv.wr.Lock(io)
    var m: [5]u8 = [0; 5]
    var w := Out.New(mut m[..])
    WriteKeyUpdate(mut w, true)
    g.Mut().Send(&c.conn, io, TypeHandshake, m[..]) else e {
        g.Unlock()
        fail e
    }
    g.Mut().Rekey()
    g.Unlock()
}

/// FxWear moves the write key's sequence number to left records before
/// its limit (RFC 8446 5.5), as a long connection would.
export FxWear(c: &Conn, io: Io, left: u64): Error!void {
    hv := c.box.Ref()
    var g := try hv.wr.Lock(io)
    g.Mut().aead.seq = g.Ref().aead.Limit() - left
    g.Unlock()
}

/// FxWearRead moves the read key's sequence number to left records before
/// the write limit, to match a peer that FxWear moved.
export FxWearRead(c: &Conn, io: Io, left: u64): Error!void {
    hv := c.box.Ref()
    var g := try hv.rd.Lock(io)
    g.Mut().aead.seq = g.Ref().aead.Limit() - left
    g.Unlock()
}

/// FxWriteSeq is the write key's next sequence number.
export FxWriteSeq(c: &Conn, io: Io): Error!u64 {
    hv := c.box.Ref()
    var g := try hv.wr.Lock(io)
    n := g.Ref().aead.seq
    g.Unlock()
    return n
}

/// FxSendRaw sends data as one record of type ty under the connection's
/// keys, whatever it holds.
export FxSendRaw(c: &Conn, io: Io, ty: u8, data: []u8): Error!void {
    hv := c.box.Ref()
    var g := try hv.wr.Lock(io)
    g.Mut().Send(&c.conn, io, ty, data) else e {
        g.Unlock()
        fail e
    }
    g.Unlock()
}

/// FxNotify sends close_notify and keeps the connection: a half-close
/// from a tls.Conn, as Secured.Shutdown(Write) makes one.
export FxNotify(c: &Conn, io: Io): Error!void {
    try c.Notify(io)
}

/// FxWriteRawConn writes data on the transport under the connection, as
/// an attacker on the path may: no record of the connection's.
export FxWriteRawConn(c: &Conn, io: Io, data: []u8): Error!void {
    try c.conn.WriteAll(io, data)
}

/// FxClient is Client offering the cipher suites suites, in order.
export FxClient(h: &Heap, io: Io, conn: net.Conn, c: &Config, host: str, suites: []u16): Error!Conn from h, conn {
    return try ClientWith(h, io, conn, c, host, suites)
}

/// FxX25519 is crypto.X25519.
export FxX25519(secret: []u8, point: []u8, out: mut []u8): int {
    var s: [32]u8 = [0; 32]
    var p: [32]u8 = [0; 32]
    (mut s[..]).CopyFrom(secret)
    (mut p[..]).CopyFrom(point)
    r := crypto.X25519(&s, &p)
    (mut out[0..32]).CopyFrom(r[..])
    return 32
}

// The contexts of an extension block.
const FxInClient: int = 1
const FxInServer: int = 2
const FxInRetry: int = 3
const FxInOther: int = 4

// FxRewriteExts parses each extension of exts with its structure's parser
// and writes it back with its writer; an extension std.tls has no
// structure for is copied.
FxRewriteExts(w: mut Out, exts: []u8, ctx: int): Protocol!void {
    var it := ExtIter.New(exts)
    for x := try it.Next() {
        w.U16(x.ty)
        at := w.Open(2)
        if x.ty == ExtServerName && x.data.Len() > 0 {
            host := try ParseServerName(x.data) else {
                fail Protocol
            }
            WriteServerName(w, host)
        } else if x.ty == ExtSupportedGroups {
            WriteU16List(w, 2, try ParseGroups(x.data))
        } else if x.ty == ExtSignatureAlgorithms || x.ty == ExtSignatureAlgorithmsCert {
            WriteU16List(w, 2, try ParseSchemes(x.data))
        } else if x.ty == ExtAlpn {
            WriteAlpnList(w, try ParseAlpn(x.data))
        } else if x.ty == ExtSupportedVersions && ctx == FxInClient {
            WriteU16List(w, 1, try ParseVersionsClient(x.data))
        } else if x.ty == ExtSupportedVersions {
            w.U16(try ParseVersionServer(x.data))
        } else if x.ty == ExtKeyShare && ctx == FxInClient {
            list := try ParseKeySharesClient(x.data)
            inner := w.Open(2)
            var ks := KeyShares(c: Cursor.New(list))
            for e := try ks.Next() {
                WriteKeyShare(w, e.group, e.key)
            }
            w.Close(inner, 2)
        } else if x.ty == ExtKeyShare && ctx == FxInServer {
            e := try ParseKeyShareServer(x.data)
            WriteKeyShare(w, e.group, e.key)
        } else if x.ty == ExtKeyShare && ctx == FxInRetry {
            w.U16(try ParseKeyShareRetry(x.data))
        } else if x.ty == ExtCookie {
            w.Vec16(try ParseCookie(x.data))
        } else if x.ty == ExtPskModes {
            w.Vec8(try ParsePskModes(x.data))
        } else {
            w.Bytes(x.data)
        }
        w.Close(at, 2)
    }
}

/// FxRewrite parses one whole handshake message with its structure's
/// parser and writes it back with its writer, its extensions each through
/// theirs.
export FxRewrite(msg: []u8, out: mut []u8): Error!int {
    r := try ParseHandshake(msg, 0xFFFFFF) else {
        fail Protocol
    }
    if r.n != msg.Len() {
        fail Protocol
    }
    var scratch: [65536]u8 = [0; 65536]
    var e := Out.New(mut scratch[..])
    var w := Out.New(out)
    m := r.m
    if m.ty == HsClientHello {
        var ch := try ParseClientHello(m.body)
        try FxRewriteExts(mut e, ch.exts, FxInClient)
        ch.exts = e.b[0..e.at]
        WriteClientHello(mut w, &ch)
    } else if m.ty == HsServerHello {
        var sh := try ParseServerHello(m.body)
        try FxRewriteExts(mut e, sh.exts, if sh.IsRetry() { FxInRetry } else { FxInServer })
        sh.exts = e.b[0..e.at]
        WriteServerHello(mut w, &sh)
    } else if m.ty == HsEncryptedExtensions {
        exts := try ParseEncryptedExtensions(m.body)
        try FxRewriteExts(mut e, exts, FxInOther)
        WriteEncryptedExtensions(mut w, e.b[0..e.at])
    } else if m.ty == HsCertificateRequest {
        var cr := try ParseCertificateRequest(m.body)
        try FxRewriteExts(mut e, cr.exts, FxInOther)
        cr.exts = e.b[0..e.at]
        WriteCertificateRequest(mut w, &cr)
    } else if m.ty == HsCertificate {
        c := try ParseCertificate(m.body)
        at := WriteCertificateHead(mut w, c.context)
        var it := CertEntries.New(c.list)
        for x := try it.Next() {
            var eo := Out.New(mut scratch[..])
            try FxRewriteExts(mut eo, x.exts, FxInOther)
            WriteCertEntry(mut w, x.cert, eo.b[0..eo.at])
        }
        CloseCertificate(mut w, at)
    } else if m.ty == HsCertificateVerify {
        cv := try ParseCertificateVerify(m.body)
        WriteCertificateVerify(mut w, &cv)
    } else if m.ty == HsFinished {
        WriteFinished(mut w, try ParseFinished(m.body, m.body.Len()))
    } else if m.ty == HsNewTicket {
        var t := try ParseNewTicket(m.body)
        try FxRewriteExts(mut e, t.exts, FxInOther)
        t.exts = e.b[0..e.at]
        WriteNewTicket(mut w, &t)
    } else if m.ty == HsKeyUpdate {
        WriteKeyUpdate(mut w, try ParseKeyUpdate(m.body))
    } else {
        fail Unsupported
    }
    if w.full {
        fail Protocol
    }
    return w.at
}
`

// tlsBuilt holds one driver executable per build mode.
var tlsBuilt struct {
	sync.Mutex
	exe map[string]string
	err map[string]string
}

// tlsExtraFixtures are more fixture files for std/tls, registered by the
// test files of an optional part (std-tls_tls12_test.go), so deleting such a
// file removes its fixture with it.
var tlsExtraFixtures = map[string]string{}

// tlsStdDir builds the std directory the driver's mk uses: the live tree,
// or MK_TLS_BASE with this tree's std/tls; both with the fixture. The
// snapshot exists because mk checks all of std: a module edited elsewhere
// while a run is live, and not checking, would stop every mk command.
func tlsStdDir(t *testing.T, root string, extra map[string]string) string {
	t.Helper()
	fx := map[string]string{"tls/tests_tlsfx.mk": tlsFixture}
	for k, v := range tlsExtraFixtures {
		fx[k] = v
	}
	for k, v := range extra {
		fx[k] = v
	}
	base := os.Getenv("MK_TLS_BASE")
	if base == "" {
		return copyStd(t, root, fx)
	}
	stdDir := filepath.Join(root, "std")
	if err := os.CopyFS(stdDir, os.DirFS(base)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(stdDir, "tls")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stdDir, "tls"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range mkFilesIn(t, "tls") {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stdDir, "tls", filepath.Base(m)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, text := range fx {
		dst := filepath.Join(stdDir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return stdDir
}

// tlsDiskBlocks is the number of test blocks in std/tls's own tests_*.mk
// files, which every mk test --std=std.tls run without --run passes beside
// a fixture's blocks.
func tlsDiskBlocks(t *testing.T) int {
	t.Helper()
	n := 0
	for _, m := range mkFilesIn(t, "tls") {
		if !strings.HasPrefix(filepath.Base(m), "tests_") {
			continue
		}
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		n += len(regexp.MustCompile(`(?m)^test "`).FindAll(b, -1))
	}
	return n
}

// tlsExe builds the driver for mode ("dev" or "release") once per test
// binary and returns its path; a missing toolchain skips the test.
func tlsExe(t *testing.T, mode string) string {
	t.Helper()
	tlsBuilt.Lock()
	defer tlsBuilt.Unlock()
	if tlsBuilt.exe == nil {
		tlsBuilt.exe = map[string]string{}
		tlsBuilt.err = map[string]string{}
	}
	if exe, ok := tlsBuilt.exe[mode]; ok {
		return exe
	}
	if msg, ok := tlsBuilt.err[mode]; ok {
		if strings.HasPrefix(msg, "needs the LLVM toolchain") {
			t.Skip(msg)
		}
		t.Fatal(msg)
	}
	exe, msg := tlsBuild(t, "driver", mode, tlsDriver)
	if msg != "" {
		tlsBuilt.err[mode] = msg
		if strings.HasPrefix(msg, "needs the LLVM toolchain") {
			t.Skip(msg)
		}
		t.Fatal(msg)
	}
	tlsBuilt.exe[mode] = exe
	return exe
}

// tlsBuild builds one driver program (main.mk is src) in mode, with mk's
// extra flags (--target x86_64-linux), in a fresh temporary directory and
// returns its path, or the reason it did not build. Directories of earlier
// runs older than two hours are removed first, and the copy of std a
// build used (with its cache) is removed once the build is done: a
// driver needs nothing of it to run.
func tlsBuild(t *testing.T, kind, mode, src string, flags ...string) (string, string) {
	t.Helper()
	if old, _ := filepath.Glob(filepath.Join(os.TempDir(), "mk-tls-*")); len(old) > 0 {
		for _, d := range old {
			if fi, err := os.Stat(d); err == nil && time.Since(fi.ModTime()) > 2*time.Hour {
				os.RemoveAll(d)
			}
		}
	}
	dir, err := packageTempDir("mk-tls-" + kind + "-" + mode)
	if err != nil {
		t.Fatal(err)
	}
	stdDir := tlsStdDir(t, dir, nil)
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "main")
	if runtime.GOOS == "windows" && !slices.Contains(flags, "x86_64-linux") {
		exe += ".exe"
	}
	args := []string{"build"}
	if mode == "release" {
		args = append(args, "--release")
	}
	args = append(args, flags...)
	args = append(args, "-o", exe, "main.mk")
	out, status := mkCmd(t, dir, stdDir, args...)
	// The std copy (with its cache) is removed whatever the outcome: a
	// failed build's copies once filled the disk (935 of them).
	os.RemoveAll(stdDir)
	if status != 0 {
		if strings.Contains(out, "no usable LLVM") {
			return "", "needs the LLVM toolchain: " + out
		}
		return "", fmt.Sprintf("mk build of the tls %s (%s): exit %d\n%s", kind, mode, status, out)
	}
	return exe, ""
}

// tlsBuildDriver is tlsBuild for a driver whose failure ends the test.
func tlsBuildDriver(t *testing.T, kind, mode, src string, flags ...string) string {
	t.Helper()
	exe, msg := tlsBuild(t, kind, mode, src, flags...)
	if msg != "" {
		if strings.HasPrefix(msg, "needs the LLVM toolchain") {
			t.Skip(msg)
		}
		t.Fatal(msg)
	}
	return exe
}

// tlsModes are the builds every oracle test runs.
var tlsModes = []string{"dev", "release"}

// tlsAsk sends records to the mode's driver and returns its answers, one
// per record.
func tlsAsk(t *testing.T, mode string, records []string) []string {
	t.Helper()
	exe := tlsExe(t, mode)
	var in strings.Builder
	for _, r := range records {
		in.WriteString(r)
		in.WriteString("\n")
	}
	r := runNative(t, exe, in.String())
	if r.code != 0 {
		t.Fatalf("tls driver (%s) exit %d\nstderr: %s\nlast output: %s", mode, r.code, r.stderr, encTail(r.stdout))
	}
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(r.stdout, "\r\n", "\n"), "\n"), "\n")
	if len(lines) != len(records) {
		t.Fatalf("tls driver (%s): %d answers for %d records\nstderr: %s\nlast output: %s", mode, len(lines), len(records), r.stderr, encTail(r.stdout))
	}
	return lines
}

// tlsHex is the driver's hex of b.
func tlsHex(b []byte) string { return hex.EncodeToString(b) }

// tlsCase is one record and the answer it must get.
type tlsCase struct {
	name   string
	record string
	want   string
}

// tlsRun asks every case in every mode and reports each disagreement.
func tlsRun(t *testing.T, cases []tlsCase) {
	t.Helper()
	records := make([]string, len(cases))
	for i, c := range cases {
		records[i] = c.record
	}
	for _, mode := range tlsModes {
		got := tlsAsk(t, mode, records)
		bad := 0
		for i, c := range cases {
			if got[i] != c.want {
				bad++
				if bad <= 20 {
					t.Errorf("%s (%s):\n got %s\nwant %s", c.name, mode, got[i], c.want)
				}
			}
		}
		t.Logf("%s: %d of %d agree", mode, len(cases)-bad, len(cases))
	}
}

// tlsOps packs transcript operations for the "t" record: m with a 24-bit
// length and the message, c, r.
func tlsOps(ops ...any) string {
	var b []byte
	for _, o := range ops {
		switch v := o.(type) {
		case []byte:
			b = append(b, 'm', byte(len(v)>>16), byte(len(v)>>8), byte(len(v)))
			b = append(b, v...)
		case string:
			b = append(b, v[0])
		default:
			panic(fmt.Sprintf("tlsOps: %T", o))
		}
	}
	return tlsHex(b)
}
