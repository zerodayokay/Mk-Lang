// Why: every hash and every stored record of the incremental cache must be
// written the same way, byte for byte, on every machine and run; one shared
// set of primitives keeps them in agreement.
//
// What: Encoder and Decoder (uvarints, zigzag varints, bools, length-prefixed
// strings and bytes, raw 32-byte hashes), FormatVersion, the hash domain
// constants and their table Domains, and the decode errors.
//
// How: Encoder writes exactly the forms source.Hasher writes. Decoder checks
// every read against the bytes left, keeps the first error (wrapping
// ErrCorrupt) and returns zero values, so malformed input never panics.

// Package iface is the canonical binary encoding and decoding of everything
// the incremental cache stores, and the stable hashes over it. It never writes
// a session ID (TypeID, ItemID, ModuleID, ConstID, FileID, Symbol, NodeID) or
// a Span into a hash; store forms replace IDs by pool indices assigned in walk
// order.
package iface

import (
	"encoding/binary"
	"errors"
	"fmt"

	"mk/internal/source"
)

// FormatVersion is written into every store-form header; every domain string
// ends in "v3" for it (DomPGO, DomData and DomDataFail, which mirror other
// packages' domains, excepted). A change to any encoding bumps it (and the
// build ID changes anyway, because the encoder is in the binary).
const FormatVersion = 3

// Domains, frozen per FormatVersion.
const (
	DomType     = "mk.ty.v3"
	DomErrSet   = "mk.errset.v3"
	DomConst    = "mk.const.v3"
	DomFn       = "mk.fn.v3"
	DomArgs     = "mk.args.v3"
	DomSig      = "mk.sig.v3"
	DomDecl     = "mk.decl.v3"
	DomFields   = "mk.fields.v3"
	DomShape    = "mk.shape.v3"
	DomLayout   = "mk.layout.v3"
	DomBody     = "mk.body.v3"
	DomInstance = "mk.instance.v3"
	DomInstName = "mk.instname.v3"
	// DomInstNameCPU is InstanceNameCPU's domain for a non-empty feature
	// set; an empty set is InstanceName itself.
	DomInstNameCPU = "mk.instname.cpu.v3"
	DomAPI         = "mk.api.v3"
	DomShapes      = "mk.shapes.v3"
	DomImpl        = "mk.impl.v3"
	DomNames       = "mk.names.v3"
	DomTokens      = "mk.tokens.v3"
	DomImports     = "mk.imports.v3"
	DomKey         = "mk.cachekey.v3" // every cache key, with the entry family as first write
	DomBuild       = "mk.build.v3"
	DomTarget      = "mk.target.v3" // ReadTarget values
	// DomPGO is pgo.HashDomain, the domain of a profile's hash in a pack
	// key; registered here so no iface domain reuses it. pgo declares its
	// own copy (iface is not among its imports); driver's
	// TestPGODomainRegistered keeps the two equal.
	DomPGO = "mk.pgo.v1"
	// DomData is types.DataDomain, the domain of a ReadData read's hash: the
	// SHA-256 of a data file's bytes; registered here so no iface domain
	// reuses it. TestDataDomainRegistered keeps the two equal.
	DomData = "mk.data.v1"
	// DomDataFail is types.DataFailDomain, the domain of a failed ReadData
	// read's hash (its reason and path).
	DomDataFail = "mk.datafail.v1"
)

// Domains is the domain table: every hash domain iface writes or
// registers, for the uniqueness test.
var Domains = []string{
	DomType, DomErrSet, DomConst, DomFn, DomArgs, DomSig, DomDecl, DomFields,
	DomShape, DomLayout, DomBody, DomInstance, DomInstName, DomInstNameCPU, DomAPI, DomShapes,
	DomImpl, DomNames, DomTokens, DomImports, DomKey, DomBuild, DomTarget, DomPGO, DomData,
	DomDataFail,
}

// Errors a decoder returns. Both are cache misses, never a
// diagnostic and never a crash.
var (
	ErrStale   = errors.New("iface: entry refers to something that changed")
	ErrCorrupt = errors.New("iface: malformed entry")
)

// Encoder appends a canonical encoding: uvarints, zigzag varints,
// length-prefixed strings and bytes, and fixed 32-byte hashes, in exactly the
// forms source.Hasher writes. Every iface hash is source.HashBytes(domain,
// enc.Out()) over a hash-form encoding. The zero Encoder is ready.
type Encoder struct {
	buf []byte
}

// Uvarint writes u in unsigned LEB128.
func (e *Encoder) Uvarint(u uint64) {
	if u < 0x80 {
		e.buf = append(e.buf, byte(u))
		return
	}
	e.buf = binary.AppendUvarint(e.buf, u)
}

// Varint writes i zigzag-encoded as a uvarint.
func (e *Encoder) Varint(i int64) { e.buf = binary.AppendVarint(e.buf, i) }

// Bool writes 1 or 0 as a uvarint.
func (e *Encoder) Bool(b bool) {
	if b {
		e.buf = append(e.buf, 1)
	} else {
		e.buf = append(e.buf, 0)
	}
}

// String writes len(s) as a uvarint, then the bytes of s.
func (e *Encoder) String(s string) {
	e.Uvarint(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

// Bytes writes len(b) as a uvarint, then b.
func (e *Encoder) Bytes(b []byte) {
	e.Uvarint(uint64(len(b)))
	e.buf = append(e.buf, b...)
}

// Hash writes a nested hash as its 32 raw bytes.
func (e *Encoder) Hash(h source.Hash) { e.buf = append(e.buf, h[:]...) }

// raw appends bytes with no length prefix (store-form section assembly).
func (e *Encoder) raw(b []byte) { e.buf = append(e.buf, b...) }

// Out is everything written so far; the slice is the encoder's own.
func (e *Encoder) Out() []byte { return e.buf }

// Len is the number of bytes written.
func (e *Encoder) Len() int { return len(e.buf) }

// Reset empties the encoder, keeping its buffer.
func (e *Encoder) Reset() { e.buf = e.buf[:0] }

// sum is the hash of what was written, in domain.
func (e *Encoder) sum(domain string) source.Hash { return source.HashBytes(domain, e.buf) }

// Decoder reads what Encoder wrote. Every read is bounds-checked; malformed
// input sets Err (sticky, wrapping ErrCorrupt) and returns zero values, never
// panics.
type Decoder struct {
	buf []byte
	off int
	Err error
}

// NewDecoder reads b.
func NewDecoder(b []byte) *Decoder { return &Decoder{buf: b} }

func (d *Decoder) fail(format string, args ...any) {
	if d.Err == nil {
		d.Err = fmt.Errorf("%w: at byte %d: %s", ErrCorrupt, d.off, fmt.Sprintf(format, args...))
	}
}

// Failf records a structural error found by a caller (an index out of
// range, an invariant broken); the first error sticks.
func (d *Decoder) Failf(format string, args ...any) { d.fail(format, args...) }

// Uvarint reads an unsigned LEB128 value.
func (d *Decoder) Uvarint() uint64 {
	if d.Err != nil {
		return 0
	}
	// One byte is most values of a record (indices, kinds, small counts).
	if d.off < len(d.buf) && d.buf[d.off] < 0x80 {
		u := uint64(d.buf[d.off])
		d.off++
		return u
	}
	u, n := binary.Uvarint(d.buf[d.off:])
	if n <= 0 {
		d.fail("bad uvarint")
		return 0
	}
	d.off += n
	return u
}

// Varint reads a zigzag varint.
func (d *Decoder) Varint() int64 {
	if d.Err != nil {
		return 0
	}
	i, n := binary.Varint(d.buf[d.off:])
	if n <= 0 {
		d.fail("bad varint")
		return 0
	}
	d.off += n
	return i
}

// Bool reads a 0 or 1; any other value is corrupt.
func (d *Decoder) Bool() bool {
	switch d.Uvarint() {
	case 0:
		return false
	case 1:
		return true
	}
	d.fail("bad bool")
	return false
}

// U32 reads a uvarint that must fit 32 bits.
func (d *Decoder) U32() uint32 {
	u := d.Uvarint()
	if u > 0xffffffff {
		d.fail("value %d exceeds 32 bits", u)
		return 0
	}
	return uint32(u)
}

// U8 reads a uvarint that must fit 8 bits.
func (d *Decoder) U8() uint8 {
	u := d.Uvarint()
	if u > 0xff {
		d.fail("value %d exceeds 8 bits", u)
		return 0
	}
	return uint8(u)
}

// Count reads a list length and checks it against the bytes left, assuming
// each element takes at least min bytes, so a forged length cannot make a
// caller allocate without bound.
func (d *Decoder) Count(min int) int {
	u := d.Uvarint()
	if min < 1 {
		min = 1
	}
	if u > uint64((len(d.buf)-d.off)/min) {
		d.fail("count %d exceeds remaining bytes", u)
		return 0
	}
	return int(u)
}

// String reads a length-prefixed string.
func (d *Decoder) String() string {
	n := d.Uvarint()
	if d.Err != nil {
		return ""
	}
	if n > uint64(len(d.buf)-d.off) {
		d.fail("length %d exceeds remaining bytes", n)
		return ""
	}
	s := string(d.buf[d.off : d.off+int(n)])
	d.off += int(n)
	return s
}

// Bytes reads a length-prefixed byte string; the result is a copy.
func (d *Decoder) Bytes() []byte {
	n := d.Uvarint()
	if d.Err != nil {
		return nil
	}
	if n > uint64(len(d.buf)-d.off) {
		d.fail("length %d exceeds remaining bytes", n)
		return nil
	}
	out := make([]byte, n)
	copy(out, d.buf[d.off:])
	d.off += int(n)
	return out
}

// Hash reads 32 raw bytes.
func (d *Decoder) Hash() source.Hash {
	var h source.Hash
	if d.Err != nil {
		return h
	}
	if len(d.buf)-d.off < len(h) {
		d.fail("short hash")
		return h
	}
	copy(h[:], d.buf[d.off:])
	d.off += len(h)
	return h
}

// Done returns Err, or an error if bytes remain unread.
func (d *Decoder) Done() error {
	if d.Err != nil {
		return d.Err
	}
	if d.off != len(d.buf) {
		return fmt.Errorf("%w: %d trailing bytes", ErrCorrupt, len(d.buf)-d.off)
	}
	return nil
}
