// Why: a by-value match takes a linear temporary apart piece by piece: a
// take moves one payload out, a switch edge proves one sub-place holds a
// plain variant. The temporary is consumed only when those pieces together
// leave nothing linear in it; a sibling that a constructor pattern discards
// around `_` (`S(a: some(_), o: none)`) is still there and must be reported.
//
// What: consumePart (the init a take or a narrowing leaves) and consumedAt,
// with below and the payload field lists it walks (coverFields,
// variantFields).
//
// How: entry.cover is the set of sub-paths of a linear temporary that hold
// nothing linear on this path (taken, or narrowed to plain variants); a join
// keeps what every predecessor covers. consumedAt walks the temporary's type
// from the root: a part is covered when it holds nothing linear, when a
// cover path names it, or when each of its fields is covered; a type with
// variants is covered through the variant a cover path's downcast selects
// (lower downcasts only on an edge that tested it), and never with none.

package borrow

import (
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

// consumePart records that path p of temporary r holds nothing linear: a
// take (take) or a narrowing. A temporary that holds nothing linear anywhere
// becomes {T} after a take or a narrowing that follows one, {N} after
// narrowings alone; a take that leaves a linear part becomes {T, I}, so the
// part is still reported; a narrowing that does keeps the init it found.
func (c *checker) consumePart(st *state, r rootID, p pathID, take bool) {
	e := st.get(r)
	in := initOf(e)
	done := !c.roots[r].linear
	if !done {
		e.cover = c.sets.add(e.cover, uint32(p))
		var at []*pathInfo
		for _, x := range c.sets.get(e.cover) {
			at = append(at, c.paths.get(pathID(x)))
		}
		done = c.consumedAt(at, 0, c.paths.get(p).rootType)
	}
	switch {
	case take && done:
		e.init = initT
	case take:
		e.init = initT | initI
	case done && in&initT != 0:
		e.init = initT
	case done:
		e.init = initN
	}
	c.put(st, r, e)
}

// consumedAt reports whether the part of type t that the cover paths at reach
// at step k (every path of at agrees with that part's first k steps) holds
// nothing linear.
func (c *checker) consumedAt(at []*pathInfo, k int, t types.TypeID) bool {
	if !c.w.linear(t) {
		return true
	}
	if len(at) == 0 {
		return false
	}
	for _, pi := range at {
		if len(pi.steps) == k {
			return true
		}
	}
	if fs, ok := c.coverFields(t); ok {
		for j, ft := range fs {
			if !c.consumedAt(below(at, k, step{kind: stField, a: uint32(j)}), k+1, ft) {
				return false
			}
		}
		return true
	}
	// A type with variants: each variant a cover path selects here (one,
	// on any one path) must have all its payload fields covered.
	var done []step
	for _, pi := range at {
		s := pi.steps[k]
		if s.kind != stVariant {
			return false
		}
		if slices.Contains(done, s) {
			continue
		}
		done = append(done, s)
		sub := below(at, k, s)
		if slices.ContainsFunc(sub, func(q *pathInfo) bool { return len(q.steps) == k+1 }) {
			continue // the downcast itself is covered
		}
		fs, ok := c.variantFields(t, s)
		if !ok {
			return false
		}
		for j, ft := range fs {
			if !c.consumedAt(below(sub, k+1, step{kind: stField, a: uint32(j)}), k+2, ft) {
				return false
			}
		}
	}
	return true
}

// below is the paths of at whose step k is s (a field step never matches a
// fields-loop step).
func below(at []*pathInfo, k int, s step) []*pathInfo {
	var out []*pathInfo
	for _, pi := range at {
		if len(pi.steps) <= k {
			continue
		}
		if q := pi.steps[k]; q.kind == s.kind && !q.any && q.a == s.a && q.sel == s.sel {
			out = append(out, pi)
		}
	}
	return out
}

// coverFields is the field types of a type whose value is all its fields at
// once: a struct, a record, an array of known length. False for any other
// type (one with variants, or one with no fields to take).
func (c *checker) coverFields(t types.TypeID) ([]types.TypeID, bool) {
	ty := c.tab.Get(t)
	switch ty.Kind {
	case types.KRecord:
		fs := make([]types.TypeID, len(ty.Fields))
		for i, f := range ty.Fields {
			fs[i] = f.Type
		}
		return fs, true
	case types.KArray:
		if ty.Len.Kind != types.CAValue {
			return nil, false
		}
		v := c.tab.Consts().Get(ty.Len.Value)
		n := types.LoadUint(v.Bytes, len(v.Bytes))
		if n > 1<<16 {
			return nil, false
		}
		fs := make([]types.TypeID, n)
		for i := range fs {
			fs[i] = ty.Elem
		}
		return fs, true
	case types.KNamed:
		if _, ok := c.tab.Items().Get(ty.Item).Decl.(*types.StructDecl); ok {
			return c.w.namedFields(t, ty), true
		}
	}
	return nil, false
}

// variantFields is the payload field types of the variant downcast s of t.
func (c *checker) variantFields(t types.TypeID, s step) ([]types.TypeID, bool) {
	ty := c.tab.Get(t)
	switch s.sel {
	case mir.SelSome, mir.SelOk:
		if ty.Elem == types.Void {
			return nil, true
		}
		return []types.TypeID{ty.Elem}, true
	case mir.SelErr, mir.SelCase:
		return nil, true // error payloads are plain
	case mir.SelEnum:
		if ty.Kind != types.KNamed {
			return nil, false
		}
		d, ok := c.tab.Items().Get(ty.Item).Decl.(*types.EnumDecl)
		if !ok || int(s.a) >= len(d.Variants) {
			return nil, false
		}
		var fs []types.TypeID
		for _, f := range d.Variants[s.a].Fields {
			fs = append(fs, c.w.subst(f.Type, ty.Args))
		}
		return fs, true
	}
	return nil, false
}
