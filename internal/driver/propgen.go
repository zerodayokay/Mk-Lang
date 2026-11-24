// Why: mk test --prop checks a contract on inputs generated from its
// parameter types, and a failing input is useful only when it is small and
// reproducible from a seed.
//
// What: pv, the self-describing generated value (render, size, shrinks);
// propRng, the seeded generator; propShape and propGen, which draw values of
// a type; and the shrink helpers (ddminCuts, towardZero). prop.go runs them.
//
// How: values are drawn from xoshiro256** seeded through SplitMix64, one
// stream per contract (fnv64 of its name). Integer parameters first enumerate
// their edge values, then draw at random. Shrink candidates come simplest
// first: delta-debugging cuts for slices, steps toward zero for numbers.

package driver

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"unicode/utf8"

	"mk/internal/types"
)

// pv is one generated value, self-describing so that it renders as Mk
// source and shrinks without the session that made it (the corpus keeps it
// as JSON). K is the kind: i (signed integer), u (unsigned), f (float), b
// (bool), c (char), s (str), q (slice), a (array), S (struct), r (record),
// o (some), n (none), e (enum variant), & (shared reference).
type pv struct {
	K        string   `json:"k"`
	I        int64    `json:"i,omitempty"`
	U        uint64   `json:"u,omitempty"`
	Bits     uint8    `json:"bits,omitempty"`
	F        float64  `json:"f,omitempty"`
	FBits    uint64   `json:"fbits,omitempty"`
	HasFBits bool     `json:"has_fbits,omitempty"`
	B        bool     `json:"b,omitempty"`
	S        string   `json:"s,omitempty"` // str and char text; struct, enum and variant names
	N        []string `json:"n,omitempty"` // field names of S, r and a payload variant
	E        []pv     `json:"e,omitempty"` // elements, fields, the some or & operand
	// Alt are an enum's earlier variants without payload, simplest first,
	// for shrinking. A payload variant keeps its field names in N and its
	// field values in E, rendered Type.Variant(f: v, ...).
	Alt []string `json:"alt,omitempty"`
}

// pvFloatBits constructs a float pv carrying an exact IEEE-754 bit pattern.
// Non-finite floats leave F as 0 because encoding/json refuses NaN and ±Inf.
func pvFloatBits(bits uint8, raw uint64) pv {
	if bits == 32 {
		b32 := uint32(raw)
		f := float64(math.Float32frombits(b32))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			f = 0
		}
		return pv{K: "f", Bits: 32, F: f, FBits: uint64(b32), HasFBits: true}
	}
	f := math.Float64frombits(raw)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		f = 0
	}
	return pv{K: "f", Bits: 64, F: f, FBits: raw, HasFBits: true}
}

func (v pv) floatBits() uint64 {
	if v.HasFBits {
		if v.Bits == 32 {
			return uint64(uint32(v.FBits))
		}
		return v.FBits
	}
	if v.Bits == 32 {
		return uint64(math.Float32bits(float32(v.F)))
	}
	return math.Float64bits(v.F)
}

func (v pv) floatVal() float64 {
	b := v.floatBits()
	if v.Bits == 32 {
		return float64(math.Float32frombits(uint32(b)))
	}
	return math.Float64frombits(b)
}

// render spells v as a Mk expression.
func (v pv) render() string {
	var b strings.Builder
	v.write(&b)
	return b.String()
}

func (v pv) write(b *strings.Builder) {
	switch v.K {
	case "i":
		b.WriteString(strconv.FormatInt(v.I, 10))
	case "u":
		b.WriteString(strconv.FormatUint(v.U, 10))
	case "f":
		b.WriteString(floatRender(v))
	case "b":
		b.WriteString(strconv.FormatBool(v.B))
	case "c":
		r, _ := utf8.DecodeRuneInString(v.S)
		b.WriteString("'" + runeEscape(r, '\'') + "'")
	case "s":
		b.WriteByte('"')
		for _, r := range v.S {
			b.WriteString(runeEscape(r, '"'))
		}
		b.WriteByte('"')
	case "q", "a":
		b.WriteByte('[')
		for i, e := range v.E {
			if i > 0 {
				b.WriteString(", ")
			}
			e.write(b)
		}
		b.WriteByte(']')
	case "S", "r":
		if v.K == "S" {
			b.WriteString(v.S)
		}
		b.WriteByte('(')
		for i, e := range v.E {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(v.N[i] + ": ")
			e.write(b)
		}
		b.WriteByte(')')
	case "o":
		b.WriteString("some(")
		v.E[0].write(b)
		b.WriteByte(')')
	case "n":
		b.WriteString("none")
	case "e":
		b.WriteString(v.S)
		if len(v.N) > 0 {
			b.WriteByte('(')
			for i, e := range v.E {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(v.N[i] + ": ")
				e.write(b)
			}
			b.WriteByte(')')
		}
	case "&":
		b.WriteByte('&')
		v.E[0].write(b)
	}
}

// floatText is a float literal Mk reads back: digits with a point, never an
// exponent.
func floatText(f float64, bitSize int) string {
	t := strconv.FormatFloat(f, 'f', -1, bitSize)
	if !strings.Contains(t, ".") {
		t += ".0"
	}
	return t
}

// floatRender spells a generated float pv: NaN, ±Inf, -0.0, and subnormals
// render via f64.FromBits / f32.FromBits to preserve exact IEEE-754 bit patterns.
func floatRender(v pv) string {
	raw := v.floatBits()
	if v.Bits == 32 {
		b32 := uint32(raw)
		exp := (b32 >> 23) & 0xff
		if exp == 0xff || b32 == 0x80000000 || (exp == 0 && (b32&0x7fffff) != 0) {
			return fmt.Sprintf("f32.FromBits(0x%08X)", b32)
		}
		return floatText(float64(math.Float32frombits(b32)), 32)
	}
	exp := (raw >> 52) & 0x7ff
	if exp == 0x7ff || raw == 0x8000000000000000 || (exp == 0 && (raw&0xfffffffffffff) != 0) {
		return fmt.Sprintf("f64.FromBits(0x%016X)", raw)
	}
	return floatText(math.Float64frombits(raw), 64)
}

// runeEscape spells one rune inside a quoted literal: printable ASCII as
// itself, the quote and backslash escaped, everything else as \u{X}.
func runeEscape(r rune, quote rune) string {
	switch {
	case r == quote || r == '\\':
		return `\` + string(r)
	case r >= 0x20 && r < 0x7f:
		return string(r)
	}
	return `\u{` + strconv.FormatInt(int64(r), 16) + `}`
}

// size is a rough measure of v, for "no larger than" in tests and the
// shrinker's progress.
func (v pv) size() uint64 {
	var n uint64
	switch v.K {
	case "i":
		if v.I < 0 {
			n = uint64(-(v.I + 1)) + 1
		} else {
			n = uint64(v.I)
		}
	case "u":
		n = v.U
	case "f":
		f := v.floatVal()
		raw := v.floatBits()
		switch {
		case math.IsNaN(f):
			n = (1 << 62) + 3
		case math.IsInf(f, 0):
			n = (1 << 62) + 2
		case raw == 0x8000000000000000 || (v.Bits == 32 && raw == 0x80000000):
			n = 1
		case f != 0:
			n = uint64(math.Min(math.Abs(f), 1<<62))
			if n == 0 {
				n = 1
			}
		}
	case "b":
		if v.B {
			n = 1
		}
	case "c":
		r, _ := utf8.DecodeRuneInString(v.S)
		n = uint64(r)
	case "s":
		n = uint64(len(v.S))
	}
	for _, e := range v.E {
		n += e.size() + 1
	}
	return n
}

// propRng is xoshiro256** (Blackman and Vigna 2018) seeded through
// SplitMix64, the same generator as std's math.Rng.
type propRng struct{ s [4]uint64 }

func newPropRng(seed uint64) *propRng {
	r := &propRng{}
	for i := range r.s {
		seed += 0x9e3779b97f4a7c15
		z := seed
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		r.s[i] = z ^ (z >> 31)
	}
	return r
}

func (r *propRng) next() uint64 {
	s := &r.s
	out := bits.RotateLeft64(s[1]*5, 7) * 9
	t := s[1] << 17
	s[2] ^= s[0]
	s[3] ^= s[1]
	s[1] ^= s[2]
	s[0] ^= s[3]
	s[2] ^= t
	s[3] = bits.RotateLeft64(s[3], 45)
	return out
}

// below is uniform in [0, n), n > 0 (Lemire's multiply-shift, with the
// rejection step).
func (r *propRng) below(n uint64) uint64 {
	hi, lo := bits.Mul64(r.next(), n)
	if lo < n {
		thresh := -n % n
		for lo < thresh {
			hi, lo = bits.Mul64(r.next(), n)
		}
	}
	return hi
}

// fnv64 is FNV-1a of s, to give each contract its own stream.
func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// propShape is a generatable type, resolved once per contract instance from
// the session's tables: everything the generator needs, nothing else.
type propShape struct {
	k      string // as pv.K: i u f b c s q a S r o e &
	bits   uint8
	n      int // array length
	min1   bool
	elem   *propShape
	name   string       // struct name, enum type name
	fields []string     // struct and record field names
	fs     []*propShape // field shapes
	vars   []string     // enum variants, spelled Type.Variant
	// vfields and vfs are each enum variant's payload field names and
	// shapes (empty for a variant without payload), as vars.
	vfields [][]string
	vfs     [][]*propShape
	text    string // the type as Mk source
}

// propGen draws values of shapes from one stream.
type propGen struct {
	r *propRng
	// i is the index of the case being drawn: the first cases take the
	// edges and the shortest lengths.
	i int
	// radix is the current integer parameter's place value and top whether
	// the value is the parameter itself (not inside one): the first
	// propEdgeCases cases enumerate the edge values of the integer
	// parameters, in mixed radix over those parameters only (a str, slice
	// or struct parameter, the receiver included, takes no digit), so MIN /
	// -1 and MAX + 1 are met at once by the first two integer parameters
	// wherever they stand. radix is the product of the earlier integer
	// parameters' edge counts (propEdgeCount); draw sets it. edgeLimit
	// bounds edge enumeration; top marks a top-level parameter.
	radix     int
	edgeLimit int
	top       bool
}

const propFloatEdges = 17

// propEdgeCount is how many edge values a top-level parameter of sh takes
// in the enumeration, 0 for a parameter that is no integer or float.
func propEdgeCount(sh *propShape) int {
	switch sh.k {
	case "i":
		return 7
	case "u":
		return 5
	case "f":
		return propFloatEdges
	}
	return 0
}

const propEdgeCases = 256

// edge is the case's edge index for the current integer parameter of n
// edges, or -1 past the enumeration.
func (g *propGen) edge(n int) int {
	if !g.top || g.i >= g.edgeLimit {
		return -1
	}
	return g.i / max(g.radix, 1) % n
}

var propRunes = []rune{'a', 'b', 'z', 'A', '0', ' ', 0, '"', '\\', '\n', 0xe9, 0x4e2d, 0x1f600}
var propChars = []rune{'a', 'Z', '0', 0, '\'', 0x7f, 0xe9, 0x4e2d, 0x1f600, 0x10ffff}

// draw makes one value of sh.
func (g *propGen) draw(sh *propShape, depth int) pv {
	g.top = depth == 0
	switch sh.k {
	case "i":
		return pv{K: "i", Bits: sh.bits, I: g.signed(sh.bits)}
	case "u":
		return pv{K: "u", Bits: sh.bits, U: g.unsigned(sh.bits)}
	case "f":
		return g.float(sh.bits)
	case "b":
		return pv{K: "b", B: g.r.next()&1 == 1}
	case "c":
		return pv{K: "c", S: string(propChars[g.r.below(uint64(len(propChars)))])}
	case "s":
		n := g.length(16)
		var b strings.Builder
		for range n {
			b.WriteRune(propRunes[g.r.below(uint64(len(propRunes)))])
		}
		return pv{K: "s", S: b.String()}
	case "q", "a":
		n := sh.n
		if sh.k == "q" {
			n = g.length(8)
			if sh.min1 && n == 0 {
				n = 1
			}
		}
		v := pv{K: sh.k, E: []pv{}}
		for range n {
			v.E = append(v.E, g.draw(sh.elem, depth+1))
		}
		return v
	case "S", "r":
		v := pv{K: sh.k, S: sh.name, N: sh.fields, E: []pv{}}
		for _, f := range sh.fs {
			v.E = append(v.E, g.draw(f, depth+1))
		}
		return v
	case "o":
		if g.r.below(4) == 0 || depth > 3 {
			return pv{K: "n"}
		}
		return pv{K: "o", E: []pv{g.draw(sh.elem, depth+1)}}
	case "e":
		j := g.r.below(uint64(len(sh.vars)))
		v := pv{K: "e", S: sh.vars[j]}
		for k := range j {
			if len(sh.vfs[k]) == 0 {
				v.Alt = append(v.Alt, sh.vars[k])
			}
		}
		if len(sh.vfs[j]) > 0 {
			v.N, v.E = sh.vfields[j], []pv{}
			for _, f := range sh.vfs[j] {
				v.E = append(v.E, g.draw(f, depth+1))
			}
		}
		return v
	case "&":
		return pv{K: "&", E: []pv{g.draw(sh.elem, depth+1)}}
	}
	return pv{K: "b"}
}

// length is a slice or string length: 0, 1, 2 for the first cases, then
// mostly short with a tail up to max.
func (g *propGen) length(max uint64) int {
	if g.i < 3 {
		return g.i
	}
	if g.r.below(2) == 0 {
		return int(g.r.below(5))
	}
	return int(g.r.below(max + 1))
}

// signed draws an integer of the width: the edges, small values, powers of
// two and their neighbours, or uniform over the range.
func (g *propGen) signed(w uint8) int64 {
	lo, hi := int64(-1)<<(w-1), int64(uint64(1)<<(w-1)-1)
	edges := []int64{0, 1, -1, hi, lo, hi - 1, lo + 1}
	if e := g.edge(len(edges)); e >= 0 {
		return edges[e]
	}
	pick := g.r.below(10)
	if g.i < len(edges) && g.r.below(2) == 0 {
		pick = 0
	}
	switch {
	case pick < 3:
		return edges[g.r.below(uint64(len(edges)))]
	case pick < 6:
		return max(lo, min(hi, int64(g.r.below(33))-16))
	case pick < 8:
		k := g.r.below(uint64(w - 1))
		x := int64(1) << k
		x += int64(g.r.below(3)) - 1
		if g.r.below(2) == 0 {
			x = -x
		}
		return max(lo, min(hi, x))
	}
	x := g.r.next()
	if w < 64 {
		x &= uint64(1)<<w - 1
		return int64(x<<(64-w)) >> (64 - w)
	}
	return int64(x)
}

func (g *propGen) unsigned(w uint8) uint64 {
	var top uint64 = math.MaxUint64
	if w < 64 {
		top = uint64(1)<<w - 1
	}
	edges := []uint64{0, 1, top, 2, top - 1}
	if e := g.edge(len(edges)); e >= 0 {
		return edges[e]
	}
	pick := g.r.below(10)
	if g.i < len(edges) && g.r.below(2) == 0 {
		pick = 0
	}
	switch {
	case pick < 3:
		return edges[g.r.below(uint64(len(edges)))]
	case pick < 6:
		return min(top, g.r.below(17))
	case pick < 8:
		x := uint64(1) << g.r.below(uint64(w))
		if g.r.below(2) == 0 && x > 0 {
			x--
		}
		return min(top, x)
	}
	return g.r.next() & top
}

var propFloat64Edges = []uint64{
	0x0000000000000000, // +0.0
	0x8000000000000000, // -0.0
	0x3ff0000000000000, // 1.0
	0xbff0000000000000, // -1.0
	0x7ff0000000000000, // +Inf
	0xfff0000000000000, // -Inf
	0x7ff8000000000000, // canonical qNaN
	0x7ff8000000001234, // non-canonical payload qNaN
	0xfff8000000000001, // negative payload qNaN
	0x0000000000000001, // +SmallestNonzeroFloat64
	0x8000000000000001, // -SmallestNonzeroFloat64
	0x0010000000000000, // +MinNormal f64
	0x8010000000000000, // -MinNormal f64
	0x7fefffffffffffff, // +MaxFloat64
	0xffefffffffffffff, // -MaxFloat64
	0x3fe0000000000000, // 0.5
	0xc004000000000000, // -2.5
}

var propFloat32Edges = []uint64{
	0x00000000, // +0.0
	0x80000000, // -0.0
	0x3f800000, // 1.0
	0xbf800000, // -1.0
	0x7f800000, // +Inf
	0xff800000, // -Inf
	0x7fc00000, // canonical qNaN
	0x7fc01234, // non-canonical payload qNaN
	0xffc00001, // negative payload qNaN
	0x00000001, // +SmallestNonzeroFloat32
	0x80000001, // -SmallestNonzeroFloat32
	0x00800000, // +MinNormal f32
	0x80800000, // -MinNormal f32
	0x7f7fffff, // +MaxFloat32
	0xff7fffff, // -MaxFloat32
	0x3f000000, // 0.5
	0xc0200000, // -2.5
}

func (g *propGen) float(w uint8) pv {
	edges := propFloat64Edges
	if w == 32 {
		edges = propFloat32Edges
	}
	if e := g.edge(len(edges)); e >= 0 {
		return pvFloatBits(w, edges[e])
	}
	pick := g.r.below(10)
	if g.i < len(edges) && g.r.below(2) == 0 {
		pick = 0
	}
	switch {
	case pick < 3:
		return pvFloatBits(w, edges[g.r.below(uint64(len(edges)))])
	case pick < 7:
		f := float64(int64(g.r.below(2000001))-1000000) / 1000
		if w == 32 {
			return pvFloatBits(32, uint64(math.Float32bits(float32(f))))
		}
		return pvFloatBits(64, math.Float64bits(f))
	}
	return pvFloatBits(w, g.r.next())
}

// floatRank orders IEEE-754 floats by simplicity so shrinks always terminate.
func floatRank(w uint8, raw uint64) (class int, mag uint64, sign uint64) {
	if w == 32 {
		b32 := uint32(raw)
		sign = uint64(b32 >> 31)
		mag = uint64(b32 & 0x7fffffff)
		f := float64(math.Float32frombits(b32))
		switch {
		case math.IsNaN(f):
			if b32 == 0x7fc00000 {
				return 5, mag, sign
			}
			return 6, mag, sign
		case math.IsInf(f, 1):
			return 3, mag, sign
		case math.IsInf(f, -1):
			return 4, mag, sign
		case b32 == 0:
			return 0, 0, 0
		case mag == 0:
			return 1, 0, 1
		}
		return 2, mag, sign
	}
	sign = raw >> 63
	mag = raw & 0x7fffffffffffffff
	f := math.Float64frombits(raw)
	switch {
	case math.IsNaN(f):
		if raw == 0x7ff8000000000000 {
			return 5, mag, sign
		}
		return 6, mag, sign
	case math.IsInf(f, 1):
		return 3, mag, sign
	case math.IsInf(f, -1):
		return 4, mag, sign
	case raw == 0:
		return 0, 0, 0
	case mag == 0:
		return 1, 0, 1
	}
	return 2, mag, sign
}

func floatLess(w uint8, a, b uint64) bool {
	ac, am, as := floatRank(w, a)
	bc, bm, bs := floatRank(w, b)
	if ac != bc {
		return ac < bc
	}
	if am != bm {
		return am < bm
	}
	return as < bs
}

func floatShrinks(w uint8, raw uint64) []pv {
	if w != 32 {
		w = 64
	} else {
		raw = uint64(uint32(raw))
	}
	curClass, curMag, curSign := floatRank(w, raw)
	if curClass == 0 {
		return nil
	}
	var out []pv
	seen := map[uint64]bool{raw: true}
	addBits := func(b uint64) {
		if w == 32 {
			b = uint64(uint32(b))
		}
		if seen[b] || !floatLess(w, b, raw) {
			return
		}
		seen[b] = true
		out = append(out, pvFloatBits(w, b))
	}
	addFloat := func(f float64) {
		if w == 32 {
			addBits(uint64(math.Float32bits(float32(f))))
		} else {
			addBits(math.Float64bits(f))
		}
	}
	addBits(0) // +0.0 is simplest for every non-(+0.0) float
	switch curClass {
	case 1: // -0.0
		return out
	case 6: // non-canonical / negative NaN -> +0.0, canonical qNaN
		if w == 32 {
			addBits(0x7fc00000)
		} else {
			addBits(0x7ff8000000000000)
		}
		return out
	case 5: // canonical qNaN -> +0.0
		return out
	case 3: // +Inf -> +0.0, 1.0, MaxFloat
		addFloat(1.0)
		if w == 32 {
			addBits(0x7f7fffff)
		} else {
			addBits(0x7fefffffffffffff)
		}
		return out
	case 4: // -Inf -> +0.0, +Inf, -1.0, -MaxFloat
		if w == 32 {
			addBits(0x7f800000)
			addFloat(-1.0)
			addBits(0xff7fffff)
		} else {
			addBits(0x7ff0000000000000)
			addFloat(-1.0)
			addBits(0xffefffffffffffff)
		}
		return out
	}
	// Finite non-zero.
	signMask := uint64(1) << 63
	expMask := uint64(0x7ff0000000000000)
	if w == 32 {
		signMask = 1 << 31
		expMask = 0x7f800000
	}
	if curSign == 1 {
		addBits(curMag)
	}
	f := pvFloatBits(w, raw).floatVal()
	addFloat(1.0)
	if curSign == 1 {
		addFloat(-1.0)
	}
	if (curMag&expMask) == 0 && curMag > 1 {
		addBits(1)
		if curSign == 1 {
			addBits(signMask | 1)
		}
	}
	addFloat(math.Trunc(f))
	addFloat(f / 2.0)
	return out
}

// shrinks are the candidates simpler than v, simplest first.
func (v pv) shrinks() []pv {
	var out []pv
	switch v.K {
	case "i":
		for _, x := range towardZero(v.I) {
			out = append(out, pv{K: "i", Bits: v.Bits, I: x})
		}
	case "u":
		for _, x := range towardZeroU(v.U) {
			out = append(out, pv{K: "u", Bits: v.Bits, U: x})
		}
	case "f":
		return floatShrinks(v.Bits, v.floatBits())
	case "b":
		if v.B {
			out = append(out, pv{K: "b"})
		}
	case "c":
		if v.S != "a" {
			out = append(out, pv{K: "c", S: "a"})
		}
	case "s":
		rs := []rune(v.S)
		for _, cut := range ddminCuts(len(rs)) {
			out = append(out, pv{K: "s", S: string(append(append([]rune(nil), rs[:cut[0]]...), rs[cut[1]:]...))})
		}
		for i, r := range rs {
			if r != 'a' {
				c := append([]rune(nil), rs...)
				c[i] = 'a'
				out = append(out, pv{K: "s", S: string(c)})
			}
		}
	case "q":
		for _, cut := range ddminCuts(len(v.E)) {
			c := v
			c.E = append(append([]pv{}, v.E[:cut[0]]...), v.E[cut[1]:]...)
			out = append(out, c)
		}
		out = append(out, v.elemShrinks()...)
	case "a", "S", "r", "&":
		out = append(out, v.elemShrinks()...)
	case "o":
		out = append(out, pv{K: "n"})
		for _, e := range v.E[0].shrinks() {
			out = append(out, pv{K: "o", E: []pv{e}})
		}
	case "e":
		// Alt are the earlier variants without payload, simplest first;
		// then a payload variant's fields shrink one at a time.
		for i, a := range v.Alt {
			out = append(out, pv{K: "e", S: a, Alt: v.Alt[:i]})
		}
		out = append(out, v.elemShrinks()...)
	}
	return out
}

// elemShrinks change one element or field of v at a time, interleaved so
// every position gets its first candidate before any gets its second.
func (v pv) elemShrinks() []pv {
	per := make([][]pv, len(v.E))
	for i, e := range v.E {
		per[i] = e.shrinks()
	}
	var out []pv
	for j := 0; ; j++ {
		any := false
		for i := range per {
			if j < len(per[i]) {
				any = true
				c := v
				c.E = append([]pv(nil), v.E...)
				c.E[i] = per[i][j]
				out = append(out, c)
			}
		}
		if !any {
			return out
		}
	}
}

// ddminCuts are the [from, to) ranges to delete from a sequence of n, the
// largest first: halves, quarters, ... single elements (Zeller and
// Hildebrandt 2002, ddmin's complement step).
func ddminCuts(n int) [][2]int {
	var out [][2]int
	if n > 0 {
		out = append(out, [2]int{0, n})
	}
	for chunk := n / 2; chunk >= 1; chunk /= 2 {
		for from := 0; from < n; from += chunk {
			out = append(out, [2]int{from, min(n, from+chunk)})
		}
		if chunk == 1 {
			break
		}
	}
	return out
}

// towardZero are integers between 0 and x, the closest to 0 first: 0,
// the unit of x's sign, then x minus x/2, x/4, ..., 1 (a binary search
// toward 0 in one round of candidates).
func towardZero(x int64) []int64 {
	if x == 0 {
		return nil
	}
	out := []int64{0}
	sign := int64(1)
	if x < 0 {
		sign = -1
	}
	if x != sign {
		out = append(out, sign)
	}
	if x < 0 && x != math.MinInt64 && -x != x {
		out = append(out, -x)
	}
	for d := x / 2; d != 0; d /= 2 {
		c := x - d
		if c != 0 && c != sign && c != x {
			out = append(out, c)
		}
	}
	return dedupInts(out)
}

func towardZeroU(x uint64) []uint64 {
	if x == 0 {
		return nil
	}
	out := []uint64{0}
	if x != 1 {
		out = append(out, 1)
	}
	for d := x / 2; d != 0; d /= 2 {
		if c := x - d; c > 1 && c != x {
			out = append(out, c)
		}
	}
	seen := map[uint64]bool{}
	var u []uint64
	for _, c := range out {
		if !seen[c] {
			seen[c] = true
			u = append(u, c)
		}
	}
	return u
}

func dedupInts(xs []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// shapeOf resolves t (the contract's parameter type, with sub giving the
// instance type of each generic index) into a propShape, or says why the
// tester cannot generate it. mod is the contract's module: a struct or enum
// of another module is not generated (its constructor may be private).
func (s *Session) shapeOf(t types.TypeID, sub map[uint32]types.TypeID, mod types.ModuleID, depth int) (*propShape, string) {
	if depth > 6 {
		return nil, "a type nested too deep"
	}
	tab := s.w.tab
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KInt, types.KIntPtr:
		w := ty.Bits
		if w == 0 {
			w = 64
		}
		k := "u"
		if ty.Signed {
			k = "i"
		}
		return &propShape{k: k, bits: w, text: tab.TypeString(t, nil)}, ""
	case types.KFloat:
		return &propShape{k: "f", bits: ty.Bits, text: tab.TypeString(t, nil)}, ""
	case types.KBool:
		return &propShape{k: "b", text: "bool"}, ""
	case types.KChar:
		return &propShape{k: "c", text: "char"}, ""
	case types.KStr:
		return &propShape{k: "s", text: "str"}, ""
	case types.KParam:
		if c, ok := sub[ty.Index]; ok {
			return s.shapeOf(c, sub, mod, depth)
		}
		return nil, "a type parameter with no instance"
	case types.KSlice, types.KMutSlice, types.KArray, types.KOptional, types.KRef, types.KMut:
		e, why := s.shapeOf(ty.Elem, sub, mod, depth+1)
		if e == nil {
			return nil, why
		}
		switch ty.Kind {
		case types.KSlice:
			return &propShape{k: "q", elem: e, text: "[]" + e.text}, ""
		case types.KMutSlice:
			return &propShape{k: "q", elem: e, min1: true, text: "mut []" + e.text}, ""
		case types.KOptional:
			return &propShape{k: "o", elem: e, text: "?" + e.text}, ""
		case types.KRef:
			return &propShape{k: "&", elem: e, text: "&" + e.text}, ""
		case types.KMut:
			return e, "" // the case's helper holds it in a var (prop.go)
		}
		n, err := strconv.Atoi(tab.ConstArgString(ty.Len, nil))
		if err != nil || n < 0 || n > 64 {
			return nil, "an array whose length is not a small constant"
		}
		return &propShape{k: "a", n: n, elem: e, text: "[" + strconv.Itoa(n) + "]" + e.text}, ""
	case types.KRecord:
		if len(ty.Fields) == 0 {
			return nil, "the empty record"
		}
		sh := &propShape{k: "r"}
		var parts []string
		for _, f := range ty.Fields {
			fs, why := s.shapeOf(f.Type, sub, mod, depth+1)
			if fs == nil {
				return nil, why
			}
			name := s.w.in.Str(f.Name)
			sh.fields, sh.fs = append(sh.fields, name), append(sh.fs, fs)
			parts = append(parts, name+": "+fs.text)
		}
		sh.text = "(" + strings.Join(parts, ", ") + ")"
		return sh, ""
	case types.KNamed:
		it := s.w.items.Get(ty.Item)
		name := s.w.in.Str(it.Name)
		if it.Module != mod || it.Owner != types.NoItem || len(ty.Args) > 0 {
			return nil, "`" + tab.TypeString(t, nil) + "`, a type of another module or a generic type"
		}
		switch d := it.Decl.(type) {
		case *types.StructDecl:
			if d.Linear || d.View || len(d.Generics) > 0 {
				return nil, "`" + name + "`, a linear, view or generic struct"
			}
			sh := &propShape{k: "S", name: name, text: name}
			for _, f := range d.Fields {
				fs, why := s.shapeOf(f.Type, sub, mod, depth+1)
				if fs == nil {
					return nil, why
				}
				sh.fields, sh.fs = append(sh.fields, s.w.in.Str(f.Name)), append(sh.fs, fs)
			}
			return sh, ""
		case *types.EnumDecl:
			if len(d.Generics) > 0 || len(d.Variants) == 0 {
				return nil, "`" + name + "`, a generic or empty enum"
			}
			// Every variant, payload ones included: a hole that sits in a
			// payload variant must be reachable, and a payload field the
			// tester cannot generate makes the parameter a skip, never a
			// pass over the payload-free variants alone.
			sh := &propShape{k: "e", name: name, text: name}
			for _, v := range d.Variants {
				vn := name + "." + s.w.in.Str(v.Name)
				var names []string
				var shapes []*propShape
				for _, f := range v.Fields {
					fs, why := s.shapeOf(f.Type, sub, mod, depth+1)
					if fs == nil {
						return nil, "variant `" + vn + "`: " + why
					}
					names, shapes = append(names, s.w.in.Str(f.Name)), append(shapes, fs)
				}
				sh.vars = append(sh.vars, vn)
				sh.vfields, sh.vfs = append(sh.vfields, names), append(sh.vfs, shapes)
			}
			return sh, ""
		}
		return nil, "`" + name + "`"
	}
	return nil, "`" + tab.TypeString(t, nil) + "`"
}
