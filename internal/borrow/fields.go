// Why: with one loan set per value, borrowing one field of a struct
// borrows all of it; field-level loan sets let a signature say which
// arguments each field of the result comes from, and let a caller use the
// other fields freely.
//
// What: fvID and the field vectors of setStore (internFV, joinFV, meetFV),
// the split shape of a type (shapeFields, shapeOf, splitFieldCount), field
// keys of a path (fieldKey, keyOf), and the field-wise reads, writes,
// aggregates and call results (splitRead, operandVector, setSplit,
// aggregateFV, resultFV).
//
// How: a frame root's entry holds fv, an interned vector of one held set per
// field of its split shape (fvID 0 is whole). held is always a superset of
// every field's set, so every reader that names no field reads the union;
// put keeps the vector sound against every other write (access.go). A view's
// shape has one more set, its rest, for what it keeps outside its fields.

package borrow

import (
	"encoding/binary"
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

// fvID names an interned vector of per-field held sets; 0 is whole.
type fvID uint32

// internFV returns the ID of fs (one set per field), 0 for no fields.
func (s *setStore) internFV(fs []setID) fvID {
	if len(fs) == 0 {
		return 0
	}
	if s.fvIndex == nil {
		s.fvIndex = make(map[string]fvID)
		s.fvs = [][]setID{nil}
	}
	s.fvKey = s.fvKey[:0]
	for _, f := range fs {
		s.fvKey = binary.LittleEndian.AppendUint32(s.fvKey, uint32(f))
	}
	if id, ok := s.fvIndex[string(s.fvKey)]; ok {
		return id
	}
	id := fvID(len(s.fvs))
	s.fvs = append(s.fvs, slices.Clone(fs))
	s.fvIndex[string(s.fvKey)] = id
	return id
}

// fieldSets is the vector of fv; nil when whole.
func (s *setStore) fieldSets(id fvID) []setID {
	if id == 0 {
		return nil
	}
	return s.fvs[id]
}

// joinFV is the field-wise union of two split vectors of one shape; whole
// wins (a root whole on any predecessor is whole after the join).
func (s *setStore) joinFV(a, b fvID) fvID {
	if a == b {
		return a
	}
	if a == 0 || b == 0 {
		return 0
	}
	fa, fb := s.fieldSets(a), s.fieldSets(b)
	if len(fa) != len(fb) {
		return 0
	}
	out := make([]setID, len(fa))
	for i := range fa {
		out[i] = s.unite(fa[i], fb[i])
	}
	return s.internFV(out)
}

// meetFV narrows every field of a to held: the loans a removal (a kill, a
// purge, a move, ends) took from the whole value left every field too.
func (s *setStore) meetFV(a fvID, held setID) fvID {
	fa := s.fieldSets(a)
	out := make([]setID, len(fa))
	for i, f := range fa {
		out[i] = s.meet(f, held)
	}
	return s.internFV(out)
}

// shapeFields is the field types of t's split shape: t through any ?T and
// E!T, a struct or a record, field types under t's arguments. ok is false
// for any other type. A view's shape has one more set after its fields,
// its rest: what the view may keep outside its carrier fields, in
// raw storage or a field that holds no loans as typed. Its type is the view
// itself, a carrier, so every builder that fills a field left out of a
// clause (resultFV's union, splitEntry, a store through a whole loan) fills
// the rest too. No field key ever names it, so a field write never empties
// it; only a whole write of the root replaces it.
func (c *checker) shapeFields(t types.TypeID) ([]types.TypeID, bool) {
	fs, _, ok := c.shapeOf(t)
	return fs, ok
}

// shapeOf is shapeFields with rest true when t's shape is a view's, whose
// last set is its rest.
func (c *checker) shapeOf(t types.TypeID) (fs []types.TypeID, rest, ok bool) {
	for {
		k := c.tab.Kind(t)
		if k != types.KOptional && k != types.KErrorUnion {
			break
		}
		t = c.tab.Get(t).Elem
	}
	ty := c.tab.Get(t)
	switch ty.Kind {
	case types.KNamed:
		d, ok := c.tab.Items().Get(ty.Item).Decl.(*types.StructDecl)
		if !ok || len(d.Fields) == 0 {
			return nil, false, false
		}
		out := make([]types.TypeID, len(d.Fields), len(d.Fields)+1)
		for i, f := range d.Fields {
			out[i] = c.w.subst(f.Type, ty.Args)
		}
		if d.View {
			out = append(out, t) // the rest
		}
		return out, d.View, true
	case types.KRecord:
		if len(ty.Fields) == 0 {
			return nil, false, false
		}
		out := make([]types.TypeID, len(ty.Fields))
		for i, f := range ty.Fields {
			out[i] = f.Type
		}
		return out, false, true
	}
	return nil, false, false
}

// shapeFieldName is field i of t's split shape (t through ?T and E!T), for
// a diagnostic.
func (c *checker) shapeFieldName(t types.TypeID, i int) string {
	for {
		k := c.tab.Kind(t)
		if k != types.KOptional && k != types.KErrorUnion {
			break
		}
		t = c.tab.Get(t).Elem
	}
	return c.fieldName(t, uint32(i))
}

// splitFieldCount is how many fields root ri keeps sets for: a frame root (a
// compiler temporary, local 0, a user local, or a by-value parameter) of a
// carrier type with a split shape; 0 for every other root (a reference
// parameter, a scope, a loop holder, a slot, an abstract or static node:
// nothing behind a reference ever has more than one set).
func (c *checker) splitFieldCount(ri *rootInfo) int {
	if c.whole {
		return 0 // the body is checked whole (call.go remnantReach)
	}
	switch ri.kind {
	case rkTemp, rkReturn, rkUser:
	case rkParam:
		if !ri.byValueParam {
			return 0
		}
	default:
		return 0
	}
	t := ri.typ
	if !c.w.carrier(t) {
		return 0
	}
	fs, rest, ok := c.shapeOf(t)
	if !ok {
		return 0
	}
	ri.rest = rest
	return len(fs)
}

// keyKind is what a read of a split root's path is.
type keyKind uint8

const (
	// keyWhole: the root itself, or a step that is no payload pair and no
	// field (a deref, an index, a user enum's variant, an error's side, a
	// fields-loop step): a whole read.
	keyWhole keyKind = iota
	// keyPayload: only payload pairs, each a downcast to some or ok then
	// field 0; the value is the split shape's, field-wise.
	keyPayload
	// keyField: payload pairs, then field f; the value holds field f's set.
	keyField
)

// fieldKey reads path p's key: leading payload pairs (ProjDowncast SelSome
// or SelOk, then ProjField 0) are skipped, never taken as the field, and
// the first field step after them names the field. rest is the
// index of the first step after the key.
func fieldKey(pi *pathInfo) (k keyKind, f uint32, rest int) {
	i := 0
	for i+1 < len(pi.steps) {
		v, z := pi.steps[i], pi.steps[i+1]
		if v.kind != stVariant || v.sel != mir.SelSome && v.sel != mir.SelOk || z.kind != stField || z.any || z.a != 0 {
			break
		}
		i += 2
	}
	switch {
	case i == 0 && len(pi.steps) == 0:
		return keyWhole, 0, 0
	case i == len(pi.steps):
		return keyPayload, 0, i
	case pi.steps[i].kind == stField && !pi.steps[i].any:
		return keyField, pi.steps[i].a, i + 1
	}
	return keyWhole, 0, 0
}

// keyOf is fieldKey on root pi.root: on a view's split shape a key
// on a field that holds no loans as typed, a raw pointer or an integer, is
// a whole key, since what such a field reaches is in the rest and in no
// field's set. So a read, borrow, write or use of one reads or writes the
// whole value.
func (c *checker) keyOf(pi *pathInfo) (k keyKind, f uint32, rest int) {
	k, f, rest = fieldKey(pi)
	if k != keyField || int(pi.root) >= len(c.roots) {
		return k, f, rest
	}
	if ri := &c.roots[pi.root]; ri.rest && (int(f) >= len(ri.fcar) || !ri.fcar[f]) {
		return keyWhole, 0, 0
	}
	return k, f, rest
}

// splitRead is the field-precise value of reading path p of a split root
// for keyField the field's set, read through B once when a deref follows
// the field (one B step whatever the depth); for keyPayload the whole set
// with the vector. ok is false when the root is whole or the path keys no
// field: the caller then reads H(root) whole.
func (c *checker) splitRead(st *state, p pathID) (s setID, fv fvID, ok bool) {
	pi := c.paths.get(p)
	if int(pi.root) >= len(c.roots) || c.roots[pi.root].nfields == 0 {
		return 0, 0, false
	}
	e := st.get(pi.root)
	fs := c.sets.fieldSets(e.fv)
	if fs == nil {
		return 0, 0, false
	}
	k, f, rest := c.keyOf(pi)
	switch k {
	case keyPayload:
		return e.held, e.fv, true
	case keyField:
		if int(f) >= len(fs) {
			return 0, 0, false
		}
		h := fs[f]
		if !c.w.carrier(pi.typ()) {
			return emptySet, 0, true
		}
		for _, sp := range pi.steps[rest:] {
			if sp.kind == stDeref {
				return c.tag(c.behind(st, h), p), 0, true
			}
		}
		return h, 0, true
	}
	return 0, 0, false
}

// operandVector is the per-field vector a copy or move of o carries: the
// root's own for the whole root or a path of payload pairs only; 0 for
// anything else, or a root that is whole.
func (c *checker) operandVector(st *state, o mir.Operand) fvID {
	if o.Kind != mir.OpCopy && o.Kind != mir.OpMove {
		return 0
	}
	p := c.paths.ofPlace(c.body, c.tab, o.Place)
	pi := c.paths.get(p)
	if c.roots[pi.root].nfields == 0 {
		return 0
	}
	fv := st.get(pi.root).fv
	if len(pi.steps) == 0 {
		return fv
	}
	if k, _, _ := fieldKey(pi); k == keyPayload {
		return fv
	}
	return 0
}

// setSplit gives root r the value of a split write: held S (the union, a
// superset of every field) and the vector fv, through put; a root that
// keeps no field sets, or a vector of another length, takes S whole.
func (c *checker) setSplit(st *state, r rootID, S setID, fv fvID) {
	ri := &c.roots[r]
	if fv == 0 || ri.nfields == 0 || len(c.sets.fieldSets(fv)) != ri.nfields {
		c.setHeld(st, r, S)
		return
	}
	// held is exactly the union of the fields (locals.go writeSplit):
	// a loan of S that no field holds is in no byte of the value. A view's
	// vector ends with its rest, which its builders fill with all of S
	// (aggregateFV, resultFV, splitEntry), so none is dropped there.
	c.writeSplit(st, r, c.sets.fieldSets(fv), S) // a whole write: deep
}

// aggregateFV is the vector of a struct or record literal into a root that
// may be split: field i holds operand i's set (a nested
// struct operand gives its union: depth one); and `some` or `ok` of one
// split operand keeps its vector. A view literal's rest is the union of
// its operands. 0 for any other aggregate.
func (c *checker) aggregateFV(rv *mir.Rvalue, sets []setID, fvs []fvID, dst types.TypeID) fvID {
	switch rv.Agg {
	case mir.AggStruct, mir.AggRecord:
		fs, rest, ok := c.shapeOf(dst)
		if rest {
			// A view literal: its rest holds every operand's loans,
			// the whole set a whole literal holds, since a raw pointer operand
			// carries none of what it reaches and the rest must cover it as
			// the whole value does.
			u := emptySet
			for _, s := range sets {
				u = c.sets.unite(u, s)
			}
			sets = append(slices.Clone(sets), u)
		}
		if !ok || len(fs) != len(sets) || c.tab.Kind(dst) == types.KOptional || c.tab.Kind(dst) == types.KErrorUnion {
			return 0
		}
		return c.sets.internFV(sets)
	case mir.AggSome, mir.AggOk:
		if len(fvs) == 1 {
			return fvs[0]
		}
	}
	return 0
}

// resultFV is step 5 of the call rule per field: each field of Dest's shape
// gets the union over its sources (a field the clause leaves out,
// Flows.Sources), read from argH after the rounds of steps 3 and 4, never from
// the entry sets; a field of a type that is no carrier holds nothing. A view's
// rest, which no clause names, holds the union, Flows.Sources. 0 when the
// callee has no per-field clause or Dest keeps no field sets.
func (c *checker) resultFV(st *state, fv *flowView, argH []setID, dest types.TypeID, destRoot rootID) fvID {
	if fv == nil || len(fv.fields) == 0 || c.roots[destRoot].nfields == 0 {
		return 0
	}
	fts, ok := c.shapeFields(dest)
	if !ok {
		return 0
	}
	flat := types.Flows{Sources: fv.sources, Fields: fv.fields}
	out := make([]setID, len(fts))
	for i, ft := range fts {
		if !c.w.carrier(ft) {
			continue
		}
		srcs, _ := c.fieldSources(flat, uint32(i))
		// The callee's clause mapped through this call's arguments: source p
		// reads argument p's set.
		for _, s := range srcs {
			if int(s.Param) > len(argH) || s.Param == types.NoParam {
				continue
			}
			h := argH[s.Param-1]
			if s.Behind {
				h = c.behind(st, h)
			}
			out[i] = c.sets.unite(out[i], h)
		}
	}
	return c.sets.internFV(out)
}

// fieldSources is field f's sources under fl: its own when the
// clause names it, else every source of the clause (named false).
func (c *checker) fieldSources(fl types.Flows, f uint32) ([]types.Source, bool) {
	for _, x := range fl.Fields {
		if x.Field == f {
			return x.Sources, true
		}
	}
	return fl.Sources, false
}

// sourcesSet is step 5's union over sources: argH[p] for H(p), B(argH[p])
// for B(p).
func (c *checker) sourcesSet(st *state, srcs []types.Source, argH []setID) setID {
	res := emptySet
	for _, s := range srcs {
		if int(s.Param) > len(argH) || s.Param == types.NoParam {
			continue
		}
		if s.Behind {
			res = c.sets.unite(res, c.behind(st, argH[s.Param-1]))
		} else {
			res = c.sets.unite(res, argH[s.Param-1])
		}
	}
	return res
}
