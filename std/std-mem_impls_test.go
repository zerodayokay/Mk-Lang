// Why: the primitive impls of core/core_impls.mk (Format, Eq, Ord, Hash) back
// every formatted or hashed value in std.
// What: Format tests against Go's strconv and the float layout, and
// TestHashImpls; &T formats, compares, orders and hashes as T.
// How: native programs call each impl as a method; Hash runs at compile time
// in std.core, since Hasher has no public constructor.

package std

import (
	"strings"
	"testing"
)

const implsProgram = `EqVia[T: Eq](a: &T, b: &T): bool {
    return a.Eq(b)
}

LessVia[T: Ord](a: &T, b: &T): bool {
    return a.Less(b)
}

Line[T: Format](out: mut Writer, x: &T): WriteError!void {
    try x.Format(out)
    try out.Str("\n")
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    a: i8 = -128
    b: i16 = 32767
    c: i32 = -2147483648
    d: i64 = -9223372036854775807 - 1
    e: u8 = 255
    f: u16 = 65535
    g: u32 = 4294967295
    h: u64 = 18446744073709551615
    i: int = 0
    j: uint = 1234567890
    k: f64 = -0.0
    l: f32 = 16777216.0
    m: f64 = 0.00001
    n := true
    o := false
    p := 'é'
    q := '😀'
    r := "naïve"
    try Line(mut out, &a)
    try Line(mut out, &b)
    try Line(mut out, &c)
    try Line(mut out, &d)
    try Line(mut out, &e)
    try Line(mut out, &f)
    try Line(mut out, &g)
    try Line(mut out, &h)
    try Line(mut out, &i)
    try Line(mut out, &j)
    try Line(mut out, &k)
    try Line(mut out, &l)
    try Line(mut out, &m)
    try Line(mut out, &n)
    try Line(mut out, &o)
    try Line(mut out, &p)
    try Line(mut out, &q)
    try Line(mut out, &r)
    rr := &r
    try Line(mut out, &rr)
    x: i32 = 7
    y: i32 = 9
    rx := &x
    ry := &y
    if !EqVia(&rx, &rx) || EqVia(&rx, &ry) || !LessVia(&rx, &ry) || LessVia(&ry, &rx) {
        try out.Str("ref order wrong\n")
    }
}
`

// TestPrimitiveImpls: implsProgram's lines, natively.
func TestPrimitiveImpls(t *testing.T) {
	t.Parallel()
	want := strings.Join([]string{
		"-128", "32767", "-2147483648", "-9223372036854775808", "255", "65535",
		"4294967295", "18446744073709551615", "0", "1234567890",
		"-0", "16777216", "1e-05", "true", "false", "é", "😀", "naïve", "naïve",
	}, "\n") + "\n"
	r := runNative(t, buildNative(t, nil, implsProgram), "")
	wantRun(t, r, 0, want)
}

// TestHashImpls, at compile time in std.core (Hasher has no public
// constructor): every signed width hashes as U64 of the sign-extended value
// and every unsigned width as U64 of its value, so -1 hashes alike as i8 and
// i64 and 255 alike as u8 and u64; bool is U64 of 0 or 1, char of its
// scalar; a str is its bytes then one 0xFF, so ("ab", "c") and ("a", "bc")
// differ; &T hashes as T.
func TestHashImpls(t *testing.T) {
	t.Parallel()
	expectClean(t, "mem_hash", `HashU64(x: u64): u64 {
    var h := NewHasher()
    h.U64(x)
    return HashFinish(mut h)
}

MemHashOf[T: Hash](x: &T): u64 {
    var h := NewHasher()
    x.Hash(mut h)
    return HashFinish(mut h)
}

HashPairFixture(a: str, b: str): u64 {
    var h := NewHasher()
    a.Hash(mut h)
    b.Hash(mut h)
    return HashFinish(mut h)
}

HashChecks(): int {
    var bad := 0
    m1: i8 = -1
    m2: i16 = -1
    m3: i32 = -1
    m4: i64 = -1
    m5: int = -1
    all := HashU64(18446744073709551615)
    if MemHashOf(&m1) != all || MemHashOf(&m2) != all || MemHashOf(&m3) != all || MemHashOf(&m4) != all || MemHashOf(&m5) != all {
        bad += 1
    }
    u1: u8 = 255
    u2: u16 = 255
    u3: u32 = 255
    u4: u64 = 255
    u5: uint = 255
    f := HashU64(255)
    if MemHashOf(&u1) != f || MemHashOf(&u2) != f || MemHashOf(&u3) != f || MemHashOf(&u4) != f || MemHashOf(&u5) != f {
        bad += 1
    }
    tr := true
    fa := false
    if MemHashOf(&tr) != HashU64(1) || MemHashOf(&fa) != HashU64(0) {
        bad += 1
    }
    c := 'é'
    if MemHashOf(&c) != HashU64(233) {
        bad += 1
    }
    if HashPairFixture("ab", "c") == HashPairFixture("a", "bc") {
        bad += 1
    }
    s := "key"
    rs := &s
    if MemHashOf(&rs) != MemHashOf(&s) {
        bad += 1
    }
    return bad
}

const MemHash = [0; 1][HashChecks()]
`)
}
