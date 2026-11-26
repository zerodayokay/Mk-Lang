// Why: besides module interfaces, the cache stores three kinds of result: a
// function's check, a compile-time evaluation and a generator's output. Each
// needs a stored form whose spans survive edits elsewhere in the file.
//
// What: FuncResult, EvalRecord and GenOutput (with GenItem), their
// Encode/Decode pairs, and EncodeTypes and DecodeTypes for the types a
// layout read names.
//
// How: each record lists every anchor its stored spans use with that
// anchor's token hash; decoding returns ErrStale on a mismatch, because the
// record's reads (body hashes) do not see token edits that keep MIR equal.

package iface

import (
	"slices"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// FuncResult is one function's (or test's, or const initializer's) check
// result.
type FuncResult struct {
	Item   types.Key
	Accept bool
	Diags  []diag.Diagnostic
	Body   *mir.Body // nil when rejected before MIR
	Borrow []byte    // borrow.Facts, encoded by borrow
	Reads  types.ReadSet
	// Anchors are every anchor a stored span uses, filled by decoding.
	Anchors []AnchorUse
}

// EncodeFuncResult writes the store form. Spans anchor to the item first.
func EncodeFuncResult(env *Env, h *Hasher, r *FuncResult) []byte {
	p := newPools(h, env, types.NoModule)
	p.prefer = []AnchorKey{AnchorKey(r.Item.String())}
	var e Encoder
	w := walker{e: &e, h: h, p: p}
	w.key(r.Item)
	e.Bool(r.Accept)
	w.writeDiags(r.Diags)
	e.Bool(r.Body != nil)
	if r.Body != nil {
		w.writeBody(r.Body)
	}
	e.Bytes(r.Borrow)
	writeReads(&e, r.Reads.Sorted())
	return p.finish(recFunc, e.Out())
}

// DecodeFuncResult re-interns a function result into env (ErrStale,
// ErrCorrupt as DecodeInterface).
func DecodeFuncResult(env *Env, b []byte) (*FuncResult, error) {
	var out *FuncResult
	err := decodeRecord(env, b, recFunc, nil, func(r *reader) error {
		d := r.d
		out = &FuncResult{Item: r.key(d), Accept: d.Bool()}
		out.Diags = r.readDiags(d)
		if d.Bool() {
			out.Body = r.readBody(d)
		}
		out.Borrow = d.Bytes()
		if len(out.Borrow) == 0 {
			out.Borrow = nil
		}
		out.Reads = readSetOf(readReads(d))
		out.Anchors = slices.Clone(r.uses)
		sortAnchorUses(out.Anchors)
		return d.Err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EvalRecord is one evaluation's cached outcome. Err.At and Err.Trace are
// stored as rebased spans; every anchor they use is stored with its token
// hash, and decoding returns ErrStale on a mismatch, because the reads (body
// hashes) do not see token edits that keep MIR equal.
type EvalRecord struct {
	Value   types.ConstID    // NoConst when Err is set (or poison)
	Err     *types.EvalError // E0901, E0902, E0908, E0909 replay
	Reads   types.ReadSet
	Anchors []AnchorUse // filled by decoding
	Steps   uint64
	// Site is the requesting item's anchor, tried first for the spans.
	Site AnchorKey
}

// EncodeEvalRecord writes the store form.
func EncodeEvalRecord(env *Env, h *Hasher, r *EvalRecord) []byte {
	p := newPools(h, env, types.NoModule)
	if r.Site != "" {
		p.prefer = []AnchorKey{r.Site}
	}
	var e Encoder
	w := walker{e: &e, h: h, p: p}
	w.cnst(r.Value)
	e.Bool(r.Err != nil)
	if x := r.Err; x != nil {
		if len(x.Trace) != len(x.Items) {
			source.ICEf(x.At, "iface: eval trace has %d spans and %d items", len(x.Trace), len(x.Items))
		}
		e.Uvarint(uint64(x.Kind))
		e.Uvarint(x.Steps)
		e.Uvarint(uint64(x.Panic))
		e.String(x.Message)
		e.Uvarint(uint64(x.Refusal))
		w.span(x.At)
		e.Uvarint(uint64(len(x.Trace)))
		for i := range x.Trace {
			w.span(x.Trace[i])
			w.item(x.Items[i])
		}
		// The fuel that ran out, or the failed read's reason and path,
		// after the trace, so records of every other kind keep their bytes.
		switch x.Kind {
		case types.EvalFuel:
			e.Uvarint(x.Fuel)
		case types.EvalDataFile:
			e.Uvarint(uint64(x.File))
			e.String(x.Path)
		}
	}
	writeReads(&e, r.Reads.Sorted())
	e.Uvarint(r.Steps)
	e.String(string(r.Site))
	return p.finish(recEval, e.Out())
}

// DecodeEvalRecord re-interns an evaluation record into env.
func DecodeEvalRecord(env *Env, b []byte) (*EvalRecord, error) {
	var out *EvalRecord
	err := decodeRecord(env, b, recEval, nil, func(r *reader) error {
		d := r.d
		out = &EvalRecord{Value: r.cnst(d, 0)}
		if d.Bool() {
			x := &types.EvalError{Kind: types.EvalErrorKind(d.U8()), Steps: d.Uvarint(), Panic: types.PanicCode(d.U32()),
				Message: d.String(), Refusal: types.Refusal(d.U8())}
			if d.Err == nil && (x.Kind < types.EvalFuel || x.Kind > types.EvalDataFile) {
				d.Failf("eval error kind %d", x.Kind)
			}
			x.At = r.span(d)
			n := d.Count(2)
			for i := 0; i < n && d.Err == nil; i++ {
				x.Trace = append(x.Trace, r.span(d))
				x.Items = append(x.Items, r.item(d))
			}
			switch x.Kind {
			case types.EvalFuel:
				x.Fuel = d.Uvarint()
			case types.EvalDataFile:
				x.File = types.DataFileError(d.Uvarint())
				x.Path = d.String()
				if d.Err == nil && (x.File == types.DataFileOK || x.File > types.DataFileTooLarge) {
					d.Failf("data file error %d", x.File)
				}
			}
			out.Err = x
		}
		out.Reads = readSetOf(readReads(d))
		out.Steps = d.Uvarint()
		out.Site = AnchorKey(d.String())
		out.Anchors = slices.Clone(r.uses)
		sortAnchorUses(out.Anchors)
		return d.Err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GenOutput is one gen line's output. Core writes no gen/ view and has no
// E0904: the text lives at the virtual path <module dir>/gen/<target>/gen.mk.
type GenOutput struct {
	Gen     types.Key
	Items   []GenItem // in emission order
	Diags   []diag.Diagnostic
	Reads   types.ReadSet // bodies entered plus reflection reads
	Anchors []AnchorUse   // filled by decoding
	Steps   uint64
}

// GenItem is one generated item: its name, its text, and the mk.tokens
// hash of its text (the per-item cutoff; the trace is not in it).
type GenItem struct {
	Name string
	Text string
	Hash source.Hash
}

// EncodeGenOutput writes the store form. Spans anchor to the gen line first.
func EncodeGenOutput(env *Env, h *Hasher, g *GenOutput) []byte {
	p := newPools(h, env, types.NoModule)
	p.prefer = []AnchorKey{AnchorKey(g.Gen.String())}
	var e Encoder
	w := walker{e: &e, h: h, p: p}
	w.key(g.Gen)
	e.Uvarint(uint64(len(g.Items)))
	for _, it := range g.Items {
		e.String(it.Name)
		e.String(it.Text)
		e.Hash(it.Hash)
	}
	w.writeDiags(g.Diags)
	writeReads(&e, g.Reads.Sorted())
	e.Uvarint(g.Steps)
	return p.finish(recGen, e.Out())
}

// DecodeGenOutput re-interns a generator output into env.
func DecodeGenOutput(env *Env, b []byte) (*GenOutput, error) {
	var out *GenOutput
	err := decodeRecord(env, b, recGen, nil, func(r *reader) error {
		d := r.d
		out = &GenOutput{Gen: r.key(d)}
		n := d.Count(34)
		for i := 0; i < n && d.Err == nil; i++ {
			out.Items = append(out.Items, GenItem{Name: d.String(), Text: d.String(), Hash: d.Hash()})
		}
		out.Diags = r.readDiags(d)
		out.Reads = readSetOf(readReads(d))
		out.Steps = d.Uvarint()
		out.Anchors = slices.Clone(r.uses)
		sortAnchorUses(out.Anchors)
		return d.Err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EncodeTypes writes a store-form record of types alone, with no own
// module: every named type's item is foreign, by key. The driver stores
// the types a ReadLayout read with generic arguments names, so a later
// session can intern them before it validates the read.
func EncodeTypes(env *Env, h *Hasher, tys []types.TypeID) []byte {
	p := newPools(h, env, types.NoModule)
	var e Encoder
	w := walker{e: &e, h: h, p: p}
	e.Uvarint(uint64(len(tys)))
	for _, t := range tys {
		w.typ(t)
	}
	return p.finish(recValues, e.Out())
}

// DecodeTypes interns the types of an EncodeTypes record into env. A
// missing foreign item is ErrStale; malformed bytes are ErrCorrupt.
func DecodeTypes(env *Env, b []byte) ([]types.TypeID, error) {
	var out []types.TypeID
	err := decodeRecord(env, b, recValues, nil, func(r *reader) error {
		out = out[:0]
		d := r.d
		n := d.Count(1)
		for i := 0; i < n && d.Err == nil; i++ {
			out = append(out, r.typ(d, 0))
		}
		return d.Err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
