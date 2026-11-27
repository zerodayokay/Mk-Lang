// Why: a cached unit must replay the diagnostics it produced, and a cached
// check must record what it read so the cache can tell when it goes stale.
//
// What: Storable (whether a unit's diagnostics may be cached), writeDiags
// and readDiags, writeReads and readReads, EncodeReads and DecodeReads.
//
// How: stored diagnostics are the raw list a unit produced, with no fixes and
// no record of whether they had any; their spans are rebased onto anchors.
// Reads are written sorted and deduplicated (kind, key, name, hash), with the
// same writes ReadSet.Hash makes.

package iface

import (
	"fmt"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// maxFactDepth bounds nested list and object facts on decode.
const maxFactDepth = 64

// Storable reports whether a unit's diagnostics may be cached: never when
// one is E0000 (internal error) or E0001 (toolchain), which describe the
// compiler or the machine and must reappear on the next run.
func Storable(ds []*diag.Diagnostic) bool {
	for _, d := range ds {
		if d.Code == diag.E0000 || d.Code == diag.E0001 {
			return false
		}
	}
	return true
}

// writeDiags writes diagnostics in store form, in the order given: code,
// phase, at, message, cause, facts, alternatives. Fixes are dropped.
func (w *walker) writeDiags(ds []diag.Diagnostic) {
	e := w.e
	e.Uvarint(uint64(len(ds)))
	for i := range ds {
		d := &ds[i]
		if d.Code == diag.E0000 || d.Code == diag.E0001 {
			source.ICEf(d.At, "iface: %v is never stored", d.Code)
		}
		e.Uvarint(uint64(d.Code))
		e.Uvarint(uint64(d.Phase))
		w.span(d.At)
		e.String(d.Message)
		e.Bool(d.Cause != nil)
		if d.Cause != nil {
			e.Uvarint(uint64(d.Cause.Code))
			w.span(d.Cause.At)
		}
		e.Uvarint(uint64(len(d.Facts)))
		for _, f := range d.Facts {
			e.String(f.Key)
			w.writeFact(&f.Value)
		}
		e.Uvarint(uint64(len(d.Alternatives)))
		for _, a := range d.Alternatives {
			e.String(a)
		}
	}
}

func (w *walker) writeFact(f *diag.Fact) {
	e := w.e
	e.Uvarint(uint64(f.Kind))
	switch f.Kind {
	case diag.FactText:
		e.String(f.Str)
	case diag.FactInt:
		e.Varint(f.Int)
	case diag.FactBool:
		e.Bool(f.Bool)
	case diag.FactAt:
		w.span(f.At)
	case diag.FactCode:
		e.Uvarint(uint64(f.Code))
	case diag.FactLoan:
		e.String(f.Str)
		e.Uvarint(uint64(f.Loan))
		w.span(f.At)
	case diag.FactList:
		e.Uvarint(uint64(len(f.List)))
		for i := range f.List {
			w.writeFact(&f.List[i])
		}
	case diag.FactObject:
		e.Uvarint(uint64(len(f.List)))
		for i := range f.List {
			e.String(f.Keys[i])
			w.writeFact(&f.List[i])
		}
	default:
		source.ICEf(f.At, "iface: fact kind %d", f.Kind)
	}
}

// readDiags reads what writeDiags wrote.
func (r *reader) readDiags(d *Decoder) []diag.Diagnostic {
	n := d.Count(6)
	if n == 0 {
		return nil
	}
	out := make([]diag.Diagnostic, 0, n)
	for i := 0; i < n && d.Err == nil; i++ {
		var g diag.Diagnostic
		g.Code = diag.Code(d.Uvarint())
		if d.Err == nil && (!g.Code.IsKnown() || g.Code == diag.E0000 || g.Code == diag.E0001) {
			d.Failf("diagnostic code %d", g.Code)
		}
		g.Phase = diag.Phase(d.U8())
		if d.Err == nil && !g.Phase.IsValid() {
			d.Failf("phase %d", g.Phase)
		}
		g.At = r.span(d)
		g.Message = d.String()
		if d.Bool() {
			c := &diag.Cause{Code: diag.Code(d.Uvarint())}
			if d.Err == nil && !c.Code.IsKnown() {
				d.Failf("cause code %d", c.Code)
			}
			c.At = r.span(d)
			g.Cause = c
		}
		m := d.Count(2)
		for j := 0; j < m && d.Err == nil; j++ {
			k := d.String()
			g.Facts = append(g.Facts, diag.Field{Key: k, Value: r.readFact(d, 0)})
		}
		m = d.Count(1)
		for j := 0; j < m && d.Err == nil; j++ {
			g.Alternatives = append(g.Alternatives, d.String())
		}
		out = append(out, g)
	}
	return out
}

func (r *reader) readFact(d *Decoder, depth int) diag.Fact {
	if depth > maxFactDepth {
		d.Failf("fact nested too deep")
		return diag.Fact{}
	}
	f := diag.Fact{Kind: diag.FactKind(d.U8())}
	switch f.Kind {
	case diag.FactText:
		f.Str = d.String()
	case diag.FactInt:
		f.Int = d.Varint()
	case diag.FactBool:
		f.Bool = d.Bool()
	case diag.FactAt:
		f.At = r.span(d)
	case diag.FactCode:
		f.Code = diag.Code(d.Uvarint())
		if d.Err == nil && !f.Code.IsKnown() {
			d.Failf("fact code %d", f.Code)
		}
	case diag.FactLoan:
		f.Str = d.String()
		f.Loan = diag.LoanKind(d.U8())
		if f.Loan > diag.LoanExclusive {
			d.Failf("loan kind %d", f.Loan)
		}
		f.At = r.span(d)
	case diag.FactList:
		n := d.Count(1)
		for i := 0; i < n && d.Err == nil; i++ {
			f.List = append(f.List, r.readFact(d, depth+1))
		}
	case diag.FactObject:
		n := d.Count(2)
		for i := 0; i < n && d.Err == nil; i++ {
			f.Keys = append(f.Keys, d.String())
			f.List = append(f.List, r.readFact(d, depth+1))
		}
	default:
		d.Failf("fact kind %d", f.Kind)
	}
	return f
}

// writeReads writes reads sorted (types.Read.Compare), deduplicated: count,
// then per read kind, key, name, hash, the writes ReadSet.Hash makes.
func writeReads(e *Encoder, rs []types.Read) {
	// Cache records usually arrive in canonical order from ReadSet.Sorted.
	for i, r := range rs {
		if !r.Kind.IsValid() || (i > 0 && rs[i-1].Compare(r) >= 0) {
			s := readSetOf(rs)
			rs = s.Sorted()
			break
		}
	}
	e.Uvarint(uint64(len(rs)))
	for _, r := range rs {
		e.Uvarint(uint64(r.Kind))
		e.String(r.Key.String())
		e.String(r.Name)
		e.Hash(r.Hash)
	}
}

// readReads reads what writeReads wrote, checking kinds and order.
func readReads(d *Decoder) []types.Read {
	n := d.Count(35)
	if n == 0 {
		return nil
	}
	out := make([]types.Read, 0, n)
	for i := 0; i < n && d.Err == nil; i++ {
		r := types.Read{Kind: types.ReadKind(d.U8())}
		r.Key = types.KeyFromString(d.String())
		r.Name = d.String()
		r.Hash = d.Hash()
		if d.Err == nil && !r.Kind.IsValid() {
			d.Failf("read kind %d", r.Kind)
		}
		if len(out) > 0 && out[len(out)-1].Compare(r) >= 0 {
			d.Failf("reads not sorted")
		}
		out = append(out, r)
	}
	if d.Err != nil {
		return nil // never hand an invalid kind to ReadSet.Add
	}
	return out
}

// readSetOf builds a ReadSet from decoded reads.
func readSetOf(rs []types.Read) types.ReadSet {
	var s types.ReadSet
	for _, r := range rs {
		s.Add(r)
	}
	return s
}

// EncodeReads is the canonical encoding of a read list (sorted, deduplicated).
func EncodeReads(rs []types.Read) []byte {
	var e Encoder
	writeReads(&e, rs)
	return e.Out()
}

// DecodeReads decodes EncodeReads' output; malformed input is ErrCorrupt.
func DecodeReads(b []byte) ([]types.Read, error) {
	d := NewDecoder(b)
	rs := readReads(d)
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("reads: %w", err)
	}
	return rs, nil
}
