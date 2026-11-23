// Why: a cached record must decode back into the current session or fail
// cleanly as a miss; a panic or a half-filled session would break the build
// that tried the cache.
//
// What: reader and decodeRecord (the store-form decoder), parsePools,
// resolveForeign, the index readers, and value and type decoding.
//
// How: pools are re-interned in index order; a foreign key or anchor that
// changed gives ErrStale, malformed bytes ErrCorrupt. The record body runs
// twice: a dry pass that validates every index and invariant a constructor
// would raise an ICE on, touching no table, then the real pass. A failed
// decode leaves only interned symbols, types and consts, which are harmless.

package iface

import (
	"fmt"
	"strings"

	"mk/internal/source"
	"mk/internal/types"
)

// reader decodes one store-form record.
type reader struct {
	env *Env
	d   *Decoder // the record body
	dry bool

	strs  []string
	syms  []source.Symbol
	keys  []types.Key
	items []poolItem
	// itemIDs[i] is the session item of pool item i (index 0 none).
	itemIDs []types.ItemID
	anchors []Anchor
	uses    []AnchorUse
	vals    [][]byte
	classes []uint8
	tyIDs   []types.TypeID
	esIDs   []types.ErrorSetID
	cIDs    []types.ConstID
}

// ownFunc creates an item the record defines (an interface's own item) and
// returns its ID; it runs once per own pool item, in index order, after
// validation and before the values are interned.
type ownFunc func(key types.Key, kind types.ItemKind) types.ItemID

// decodeRecord parses b as a record with tag, validates it, then runs body
// twice (dry, then real) after interning the pools. body reads the record
// body through r and returns its first error; it must not touch session
// tables in a dry pass (r.dry).
func decodeRecord(env *Env, b []byte, tag uint64, own ownFunc, body func(r *reader) error) error {
	r := &reader{env: env}
	d := NewDecoder(b)
	if v := d.Uvarint(); d.Err == nil && v != FormatVersion {
		return fmt.Errorf("%w: format version %d", ErrCorrupt, v)
	}
	if t := d.Uvarint(); d.Err == nil && t != tag {
		return fmt.Errorf("%w: record tag %d, want %d", ErrCorrupt, t, tag)
	}
	r.parsePools(d)
	if d.Err != nil {
		return d.Err
	}
	r.d = &Decoder{buf: d.buf, off: d.off}
	if err := r.resolveForeign(); err != nil {
		return err
	}
	// Records that define no own items (FuncResult, EvalRecord, GenOutput,
	// Types) mutate no item or module table, only interning harmless symbols,
	// types and consts after per-value validation; decoding them in a single
	// pass halves warm decode cost.
	if own == nil {
		for _, it := range r.items {
			if it.own {
				return fmt.Errorf("%w: own item %s in a record that defines none", ErrCorrupt, r.keys[it.key])
			}
		}
		r.dry = false
		r.tyIDs = make([]types.TypeID, len(r.vals)+1)
		r.esIDs = make([]types.ErrorSetID, len(r.vals)+1)
		r.cIDs = make([]types.ConstID, len(r.vals)+1)
		for i := range r.vals {
			if err := r.value(uint32(i + 1)); err != nil {
				return err
			}
		}
		if err := body(r); err != nil {
			return err
		}
		return r.d.Done()
	}
	// Dry pass: validate every value and the body before creating own items.
	r.dry = true
	r.tyIDs = make([]types.TypeID, len(r.vals)+1)
	r.esIDs = make([]types.ErrorSetID, len(r.vals)+1)
	r.cIDs = make([]types.ConstID, len(r.vals)+1)
	for i := range r.vals {
		if err := r.value(uint32(i + 1)); err != nil {
			return err
		}
	}
	start := r.d.off
	if err := body(r); err != nil {
		return err
	}
	if err := r.d.Done(); err != nil {
		return err
	}
	if err := r.checkOwnAbsent(); err != nil {
		return err
	}
	// Real pass.
	r.dry = false
	for i, it := range r.items {
		if it.own {
			if own == nil {
				return fmt.Errorf("%w: own item %s in a record that defines none", ErrCorrupt, r.keys[it.key])
			}
			r.itemIDs[i+1] = own(r.keys[it.key], it.kind)
		}
	}
	for i := range r.vals {
		if err := r.value(uint32(i + 1)); err != nil {
			return err
		}
	}
	r.d = &Decoder{buf: d.buf, off: start}
	if err := body(r); err != nil {
		return err
	}
	return r.d.Done()
}

// parsePools reads the strings, keys, items, anchors and values sections.
func (r *reader) parsePools(d *Decoder) {
	n := d.Count(1)
	r.strs = make([]string, n+1)
	r.syms = make([]source.Symbol, n+1)
	for i := 1; i <= n; i++ {
		r.strs[i] = d.String()
		if d.Err == nil {
			r.syms[i] = r.env.Interner.Intern(r.strs[i])
		}
	}
	n = d.Count(1)
	r.keys = make([]types.Key, n+1)
	for i := 1; i <= n; i++ {
		s := d.String()
		if s == "" {
			d.Failf("empty key")
		}
		r.keys[i] = types.KeyFromString(s)
	}
	n = d.Count(3)
	r.items = make([]poolItem, n)
	r.itemIDs = make([]types.ItemID, n+1)
	for i := range r.items {
		k := d.U32()
		kind := types.ItemKind(d.U8())
		own := d.Bool()
		if k == 0 || int(k) >= len(r.keys) {
			d.Failf("item key index %d", k)
		}
		r.items[i] = poolItem{key: k, kind: kind, own: own}
	}
	n = d.Count(33)
	r.anchors = make([]Anchor, n+1)
	r.uses = make([]AnchorUse, n)
	for i := 0; i < n; i++ {
		r.uses[i] = AnchorUse{Key: AnchorKey(d.String()), TokenHash: d.Hash()}
	}
	n = d.Count(1)
	r.vals = make([][]byte, n)
	r.classes = make([]uint8, n+1)
	for i := range r.vals {
		r.vals[i] = d.Bytes()
		if len(r.vals[i]) > 0 {
			r.classes[i+1] = r.vals[i][0]
		}
	}
}

// resolveForeign looks up every foreign item by key and every anchor by key,
// checking item kinds and anchor token hashes: a mismatch is ErrStale.
func (r *reader) resolveForeign() error {
	for i, it := range r.items {
		if it.own {
			continue
		}
		id, ok := r.env.Items.ByKey(r.keys[it.key])
		if !ok {
			return fmt.Errorf("%w: item %s", ErrStale, r.keys[it.key])
		}
		if r.env.Items.Get(id).Kind != it.kind {
			return fmt.Errorf("%w: item %s changed kind", ErrStale, r.keys[it.key])
		}
		r.itemIDs[i+1] = id
	}
	for i, u := range r.uses {
		if r.env.Anchors == nil {
			return fmt.Errorf("%w: anchor %s with no Env.Anchors", ErrStale, u.Key)
		}
		a, ok := r.env.Anchors.Anchor(u.Key)
		if !ok {
			return fmt.Errorf("%w: anchor %s", ErrStale, u.Key)
		}
		if a.TokenHash != u.TokenHash {
			return fmt.Errorf("%w: anchor %s tokens changed", ErrStale, u.Key)
		}
		if a.File == source.NoFile && r.env.Sources != nil {
			if f, ok := r.env.Sources.ByPath(a.Path); ok {
				a.File = f.ID
			}
		}
		if len(a.Starts) != len(a.Ends) {
			return fmt.Errorf("%w: anchor %s malformed", ErrStale, u.Key)
		}
		r.anchors[i+1] = a
	}
	return nil
}

// Index readers used by value entries and record bodies. Each validates the
// index (and, for values, the class and that it is below limit, the index of
// the entry being read; 0 for a record body) and returns the session ID, the
// zero ID in a dry pass.

func (r *reader) refVal(d *Decoder, class uint8, limit uint32, what string) uint32 {
	i := d.U32()
	if i == 0 {
		return 0
	}
	if int(i) >= len(r.classes) || (limit != 0 && i >= limit) || r.classes[i] != class {
		d.Failf("%s index %d", what, i)
		return 0
	}
	return i
}

func (r *reader) typ(d *Decoder, limit uint32) types.TypeID {
	return r.tyIDs[r.refVal(d, valType, limit, "type")]
}

// needTyp is typ for a field that may not be NoType.
func (r *reader) needTyp(d *Decoder, limit uint32) types.TypeID {
	i := r.refVal(d, valType, limit, "type")
	if i == 0 {
		d.Failf("missing type")
	}
	return r.tyIDs[i]
}

func (r *reader) errset(d *Decoder, limit uint32) types.ErrorSetID {
	return r.esIDs[r.refVal(d, valErrSet, limit, "error set")]
}

func (r *reader) cnst(d *Decoder, limit uint32) types.ConstID {
	return r.cIDs[r.refVal(d, valConst, limit, "const")]
}

func (r *reader) item(d *Decoder) types.ItemID {
	i := d.U32()
	if int(i) >= len(r.itemIDs) {
		d.Failf("item index %d", i)
		return types.NoItem
	}
	return r.itemIDs[i]
}

// itemKey is the key of a pool item index read from d (zero for 0).
func (r *reader) itemKey(d *Decoder) (types.ItemID, types.Key) {
	i := d.U32()
	if int(i) >= len(r.itemIDs) {
		d.Failf("item index %d", i)
		return types.NoItem, types.Key{}
	}
	if i == 0 {
		return types.NoItem, types.Key{}
	}
	return r.itemIDs[i], r.keys[r.items[i-1].key]
}

func (r *reader) key(d *Decoder) types.Key {
	i := d.U32()
	if int(i) >= len(r.keys) {
		d.Failf("key index %d", i)
		return types.Key{}
	}
	return r.keys[i]
}

func (r *reader) sym(d *Decoder) source.Symbol {
	i := d.U32()
	if int(i) >= len(r.syms) {
		d.Failf("string index %d", i)
		return source.NoSymbol
	}
	return r.syms[i]
}

// symStr is the text of a string index (for order checks).
func (r *reader) symStr(d *Decoder) (source.Symbol, string, bool) {
	i := d.U32()
	if i == 0 || int(i) >= len(r.syms) {
		d.Failf("string index %d", i)
		return source.NoSymbol, "", false
	}
	return r.syms[i], r.strs[i], true
}

// span reads a rebased span (pools.span) against the current anchors.
func (r *reader) span(d *Decoder) source.Span {
	ai := d.U32()
	if ai == 0 {
		return source.Span{}
	}
	if int(ai) >= len(r.anchors) {
		d.Failf("anchor index %d", ai)
		return source.Span{}
	}
	a := &r.anchors[ai]
	so, sd, eo, ed := d.U32(), d.U32(), d.U32(), d.U32()
	if d.Err != nil {
		return source.Span{}
	}
	start, ok1 := a.offset(so, sd)
	end, ok2 := a.offset(eo, ed)
	if !ok1 || !ok2 || start > end || a.File == source.NoFile {
		// The anchor's tokens hashed equal, so this is not staleness.
		d.Failf("span outside anchor %s", r.uses[ai-1].Key)
		return source.Span{}
	}
	return source.Span{File: a.File, Start: start, End: end}
}

// value reads value entry i into the session (or only validates it, dry).
func (r *reader) value(i uint32) error {
	b := r.vals[i-1]
	d := NewDecoder(b)
	switch class := d.U8(); class {
	case valType:
		ty := r.readType(d, i)
		if err := d.Done(); err != nil {
			return err
		}
		if !r.dry {
			r.tyIDs[i] = r.env.Types.Intern(ty)
		}
	case valErrSet:
		ms := r.readErrSet(d, i)
		if err := d.Done(); err != nil {
			return err
		}
		if len(ms) == 0 {
			return fmt.Errorf("%w: empty error set entry", ErrCorrupt)
		}
		if !r.dry {
			r.esIDs[i] = r.env.Types.ErrorSet(ms)
		}
	case valConst:
		v := r.readConst(d, i)
		if err := d.Done(); err != nil {
			return err
		}
		if !r.dry {
			r.cIDs[i] = r.env.Consts.Intern(v)
		}
	default:
		if d.Err != nil {
			return d.Err
		}
		return fmt.Errorf("%w: value class %d", ErrCorrupt, class)
	}
	return nil
}

// readConstArg reads 0 none, 1 param, 2 const, 3 sizeof.
func (r *reader) readConstArg(d *Decoder, limit uint32) types.ConstArg {
	switch d.Uvarint() {
	case 0:
		return types.ConstArg{}
	case 1:
		return types.ConstArg{Kind: types.CAParam, Param: d.U32()}
	case 2:
		i := r.refVal(d, valConst, limit, "const")
		if i == 0 {
			d.Failf("const arg with no value")
		}
		return types.ConstArg{Kind: types.CAValue, Value: r.cIDs[i]}
	case 3:
		return types.ConstArg{Kind: types.CASizeOf, Type: r.needTyp(d, limit)}
	}
	d.Failf("const arg tag")
	return types.ConstArg{}
}

func (r *reader) readGenericArgs(d *Decoder, limit uint32) []types.GenericArg {
	n := d.Count(2)
	if n == 0 {
		return nil
	}
	out := make([]types.GenericArg, n)
	for j := range out {
		switch d.Uvarint() {
		case 1:
			out[j] = types.TypeArg(r.needTyp(d, limit))
		case 2:
			c := r.readConstArg(d, limit)
			if c.Kind == types.CANone {
				d.Failf("generic const arg with no kind")
			}
			out[j] = types.ConstGenericArg(c)
		default:
			d.Failf("generic arg tag")
		}
	}
	return out
}

// readType reads a type node, checking every invariant Table.Intern raises
// an ICE on.
func (r *reader) readType(d *Decoder, own uint32) types.Type {
	tag := d.Uvarint()
	kind, ok := tagKind[tag]
	if !ok {
		d.Failf("type tag %d", tag)
		return types.Type{}
	}
	ty := types.Type{Kind: kind}
	switch kind {
	case types.KInt:
		ty.Bits = d.U8()
		ty.Signed = d.Bool()
		if ty.Bits != 8 && ty.Bits != 16 && ty.Bits != 32 && ty.Bits != 64 {
			d.Failf("int bits %d", ty.Bits)
		}
	case types.KIntPtr:
		ty.Signed = d.Bool()
	case types.KFloat:
		ty.Bits = d.U8()
		if ty.Bits != 32 && ty.Bits != 64 {
			d.Failf("float bits %d", ty.Bits)
		}
	case types.KBool, types.KChar, types.KVoid, types.KNever, types.KStr, types.KSelf:
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional, types.KRawPtr:
		ty.Elem = r.needTyp(d, own)
	case types.KArray:
		ty.Len = r.readConstArg(d, own)
		if ty.Len.Kind == types.CANone {
			d.Failf("array with no length")
		}
		ty.Elem = r.needTyp(d, own)
	case types.KErrorUnion:
		i := r.refVal(d, valErrSet, own, "error set")
		if i == 0 {
			d.Failf("error union with no errors")
		}
		ty.Errors = r.esIDs[i]
		ty.Elem = r.needTyp(d, own)
	case types.KErrorSet:
		i := r.refVal(d, valErrSet, own, "error set")
		if i == 0 {
			d.Failf("empty error set type")
		}
		ty.Errors = r.esIDs[i]
	case types.KRecord:
		n := d.Count(2)
		prev := ""
		for j := 0; j < n; j++ {
			sym, s, ok := r.symStr(d)
			if ok && j > 0 && strings.Compare(prev, s) >= 0 {
				d.Failf("record fields not sorted at %q", s)
			}
			prev = s
			ty.Fields = append(ty.Fields, types.RecordField{Name: sym, Type: r.needTyp(d, own)})
		}
	case types.KFn:
		ty.Fn = r.readFnType(d, own)
	case types.KNamed:
		i := d.U32()
		if i == 0 || int(i) >= len(r.itemIDs) {
			d.Failf("named type item %d", i)
		} else {
			ty.Item = r.itemIDs[i]
		}
		ty.Args = r.readGenericArgs(d, own)
		if r.dry {
			// Table.Intern needs a real item; the dry pass only validates.
			ty.Item = types.NoItem
		}
	case types.KParam:
		ty.Index = d.U32()
	}
	return ty
}

func (r *reader) readFnType(d *Decoder, own uint32) *types.FnType {
	fn := &types.FnType{}
	n := d.Count(2)
	for j := 0; j < n; j++ {
		fn.Params = append(fn.Params, types.FnParam{Type: r.needTyp(d, own), Into: types.ParamIndex(d.U32())})
	}
	fn.Result = r.needTyp(d, own)
	fn.Errors = r.errset(d, own)
	fn.Sources = r.readSources(d, own)
	fn.Fields = r.readFieldSources(d, own)
	fn.Stores = r.readStores(d)
	fn.Ends = r.readEnds(d)
	return fn
}

func (r *reader) readSources(d *Decoder, limit uint32) []types.Source {
	n := d.Count(3)
	var out []types.Source
	for j := 0; j < n; j++ {
		s := types.Source{Param: types.ParamIndex(d.U32()), Behind: d.Bool()}
		m := d.Count(2)
		for k := 0; k < m; k++ {
			s.Path = append(s.Path, types.FieldStep{Field: d.U32(), Type: r.typ(d, limit)})
		}
		out = append(out, s)
	}
	return out
}

// readFieldSources reads writeFieldSources' encoding.
func (r *reader) readFieldSources(d *Decoder, limit uint32) []types.FieldSources {
	n := d.Count(2)
	var out []types.FieldSources
	for j := 0; j < n; j++ {
		f := types.FieldSources{Field: d.U32()}
		f.Sources = r.readSources(d, limit)
		out = append(out, f)
	}
	return out
}

func (r *reader) readStores(d *Decoder) []types.Store {
	n := d.Count(2)
	var out []types.Store
	for j := 0; j < n; j++ {
		out = append(out, types.Store{From: types.ParamIndex(d.U32()), Into: types.ParamIndex(d.U32())})
	}
	return out
}

func (r *reader) readEnds(d *Decoder) []types.ParamIndex {
	n := d.Count(1)
	var out []types.ParamIndex
	for j := 0; j < n; j++ {
		out = append(out, types.ParamIndex(d.U32()))
	}
	return out
}

func (r *reader) readErrSet(d *Decoder, own uint32) []types.ErrorMember {
	n := d.Count(2)
	out := make([]types.ErrorMember, 0, n)
	for j := 0; j < n; j++ {
		switch d.Uvarint() {
		case 1:
			out = append(out, types.ErrorMember{IsParam: true, Param: d.U32()})
		case 2:
			i := d.U32()
			if i == 0 || int(i) >= len(r.itemIDs) {
				d.Failf("error member item %d", i)
				continue
			}
			out = append(out, types.ErrorMember{Item: r.itemIDs[i]})
		default:
			d.Failf("error member tag")
		}
	}
	return out
}
