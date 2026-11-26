// Why: check folds some const values itself (literals, const arguments)
// and must produce them in exactly the form the evaluator and codegen read.
//
// What: scalarConst, strConst, bytesConst, intConst and intArg (interning
// values), strBytes and constInt (reading them back), parseIntLit,
// intFits and floatLitFitsF32.
//
// How: every value is types.ConstValue's canonical little-endian bytes; a
// str is a {ptr, len} pair whose pointer word relocates to a [N]u8 const.

package check

import (
	"math"
	"strconv"
	"strings"

	"mk/internal/types"
)

// scalarConst interns a scalar value of type t (an integer, char, bool or
// float bit pattern) of size bytes.
func (c *Checker) scalarConst(t types.TypeID, size int, bits uint64) types.ConstID {
	b := make([]byte, size)
	types.StoreUint(b, size, bits)
	return c.cfg.Consts.Intern(types.ConstValue{Type: t, Align: uint32(size), Bytes: b})
}

// strConst interns a str value in the evaluator's form: a {ptr, len} pair
// of 8-byte words whose pointer word is a RelocData to a [N]u8 const of the
// bytes. The empty str relocates to the [0]u8 const too: a zero word with
// no reloc is a null pointer, which codegen emits as is, against the
// !nonnull of every str and slice data pointer (out.Str("") dropped the
// rest of Main in a Windows release build), and a 1 word without a reloc
// would make an integer the evaluator refuses as a pointer.
func (c *Checker) strConst(bs []byte) types.ConstID {
	return c.bytesConst(types.Str, bs)
}

// bytesConst is strConst for type t (str or []u8).
func (c *Checker) bytesConst(t types.TypeID, bs []byte) types.ConstID {
	v := types.ConstValue{Type: t, Align: 8, Bytes: make([]byte, 16)}
	types.StoreUint(v.Bytes[8:], 8, uint64(len(bs)))
	arr := c.cfg.Types.Array(c.intArg(int64(len(bs))), types.U8)
	data := c.cfg.Consts.Intern(types.ConstValue{Type: arr, Align: 1, Bytes: append([]byte(nil), bs...)})
	v.Relocs = []types.Reloc{{Offset: 0, Kind: types.RelocData, Data: data}}
	return c.cfg.Consts.Intern(v)
}

// strBytes reads a str const's bytes back; false for any other value.
func (c *Checker) strBytes(id types.ConstID) ([]byte, bool) {
	v := c.cfg.Consts.Get(id)
	if v.Type != types.Str || len(v.Bytes) != 16 {
		return nil, false
	}
	n := types.LoadUint(v.Bytes[8:], 8)
	if n == 0 {
		return []byte{}, true
	}
	if len(v.Relocs) != 1 || v.Relocs[0].Kind != types.RelocData {
		return nil, false
	}
	d := c.cfg.Consts.Get(v.Relocs[0].Data)
	if uint64(len(d.Bytes)) != n {
		return nil, false
	}
	return d.Bytes, true
}

// intConst interns an int value.
func (c *Checker) intConst(v int64) types.ConstID { return c.scalarConst(types.Int, 8, uint64(v)) }

// intArg is the const argument of an int value (an array length).
func (c *Checker) intArg(v int64) types.ConstArg {
	return types.ConstArg{Kind: types.CAValue, Value: c.intConst(v)}
}

// constInt reads an integer const value back, sign-extended when signed.
func (c *Checker) constInt(id types.ConstID) (int64, bool) {
	v := c.cfg.Consts.Get(id)
	ty := c.cfg.Types.Get(v.Type)
	if !ty.IsInteger() || len(v.Bytes) == 0 || len(v.Bytes) > 8 {
		return 0, false
	}
	n := len(v.Bytes)
	x := types.LoadUint(v.Bytes, n)
	if ty.Signed && n < 8 && x&(1<<(uint(n)*8-1)) != 0 {
		x |= ^uint64(0) << (uint(n) * 8)
	}
	return int64(x), true
}

// parseIntLit reads an integer literal's spelling (decimal, or 0x, 0o, 0b,
// with _ separators) as a magnitude; false past 64 bits.
func parseIntLit(raw string) (uint64, bool) {
	s := strings.ReplaceAll(raw, "_", "")
	base := 10
	if len(s) > 2 && s[0] == '0' {
		switch s[1] {
		case 'x', 'X':
			base, s = 16, s[2:]
		case 'o', 'O':
			base, s = 8, s[2:]
		case 'b', 'B':
			base, s = 2, s[2:]
		}
	}
	v, err := strconv.ParseUint(s, base, 64)
	return v, err == nil
}

// intFits reports whether the value (magnitude v, negated when neg) fits an
// integer of bits bits and the given signedness (E0303).
func intFits(v uint64, neg bool, bits uint, signed bool) bool {
	switch {
	case !signed && neg:
		return v == 0
	case !signed:
		return bits == 64 || v < 1<<bits
	case neg:
		return v <= 1<<(bits-1)
	}
	return v < 1<<(bits-1)
}

// floatLitFitsF32 reports whether a float literal's f64 value is exactly an
// f32, so that `f32(x)` changes no value; a literal that rounds or
// overflows in f32 does not fit (E0303).
func floatLitFitsF32(raw string) bool {
	f, err := strconv.ParseFloat(strings.ReplaceAll(raw, "_", ""), 64)
	return err == nil && !math.IsInf(f, 0) && float64(float32(f)) == f
}
