// Why: a whole `mut p` or `mut self` call on a local split struct only needs to
// merge fields between which the callee can actually transfer loans at depth 1
// with no `into` clause; collapsing store-disjoint fields (such as &Graph,
// &ScratchBuf, and scalar fields) to whole causes false E0503/E0505 errors.
//
// What: splitMergeMatrix on walker, computing the directed reflexive-transitive
// closure reach[i][j] (whether field i of a concrete non-view struct or record
// can receive a loan from field j during a mut borrow of the whole value).
//
// How: for each carrier field i we collect the assignable depth-1 carrier
// slots (dstSlots: &T, mut T, []T, mut []T, str; any view struct marks dst.any),
// and for each carrier field j we collect the extractable carriers from j
// behind mut self (srcProvides: the field itself by &/mut and every carrier
// reachable through its type; any non-plain view, allocator, fn, or interface
// marks src.any). Field i can receive from j iff fcar[i] && fcar[j] and their
// slot sets intersect (or either side sets .any); Warshall closes transitively.

package borrow

import "mk/internal/types"

type smSlotKind uint8

const (
	smRef smSlotKind = iota
	smMut
	smSlice
	smMutSlice
	smStr
	smPlainView
)

type smSlot struct {
	kind smSlotKind
	elem types.TypeID
}

type smSet struct {
	slots []smSlot
	any   bool
}

func (s *smSet) add(kind smSlotKind, elem types.TypeID) {
	if s.any {
		return
	}
	v := smSlot{kind: kind, elem: elem}
	for _, o := range s.slots {
		if o == v {
			return
		}
	}
	s.slots = append(s.slots, v)
}

func (s *smSet) intersects(o *smSet) bool {
	if s.any || o.any {
		return true
	}
	for _, a := range s.slots {
		for _, b := range o.slots {
			if a == b {
				return true
			}
		}
	}
	return false
}

// splitMergeMatrix returns the reflexive-transitive field transfer matrix
// reach[i][j] for concrete non-view struct/record type t with n fields and
// carrier flags fcar, or nil if every carrier field already reaches every
// other carrier field (so collapsing whole is identical and faster).
func (w *walker) splitMergeMatrix(t types.TypeID, n int, fcar []bool) [][]bool {
	if n <= 1 || len(fcar) != n {
		return nil
	}
	if m, ok := w.splitMerge[t]; ok {
		return m
	}
	u := t
	for {
		k := w.tab.Kind(u)
		if k != types.KOptional && k != types.KErrorUnion {
			break
		}
		u = w.tab.Get(u).Elem
	}
	fts := w.fieldTypesForSplit(u)
	if len(fts) != n {
		w.splitMerge[t] = nil
		return nil
	}
	dsts := make([]smSet, n)
	srcs := make([]smSet, n)
	for i := range n {
		if !fcar[i] {
			continue
		}
		w.collectDstSlots(fts[i], &dsts[i], map[types.TypeID]bool{}, 0)
		w.collectSrcTop(fts[i], &srcs[i])
	}
	reach := make([][]bool, n)
	for i := range n {
		reach[i] = make([]bool, n)
		reach[i][i] = true
		if !fcar[i] {
			continue
		}
		for j := range n {
			if i != j && fcar[j] && dsts[i].intersects(&srcs[j]) {
				reach[i][j] = true
			}
		}
	}
	// Warshall transitive closure: if i can receive from k and k from j,
	// two writes in one mut call can move a loan from j -> k -> i.
	for k := range n {
		if !fcar[k] {
			continue
		}
		for i := range n {
			if !fcar[i] || !reach[i][k] {
				continue
			}
			for j := range n {
				if reach[k][j] {
					reach[i][j] = true
				}
			}
		}
	}
	allConnected := true
	carrierCount := 0
	for i := range n {
		if !fcar[i] {
			continue
		}
		carrierCount++
		for j := range n {
			if fcar[j] && !reach[i][j] {
				allConnected = false
				break
			}
		}
		if !allConnected {
			break
		}
	}
	if allConnected && carrierCount == n {
		w.splitMerge[t] = nil
		return nil
	}
	w.splitMerge[t] = reach
	return reach
}

func (w *walker) fieldTypesForSplit(t types.TypeID) []types.TypeID {
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KNamed:
		d, ok := w.items.Get(ty.Item).Decl.(*types.StructDecl)
		if !ok || d.View || len(d.Fields) == 0 {
			return nil
		}
		// Ensure ReadShape/ReadFields records are registered for cache verification.
		_ = w.factsOf(t)
		_ = w.inlineFields(t)
		out := make([]types.TypeID, len(d.Fields))
		for i, f := range d.Fields {
			out[i] = w.subst(f.Type, ty.Args)
		}
		return out
	case types.KRecord:
		if len(ty.Fields) == 0 {
			return nil
		}
		out := make([]types.TypeID, len(ty.Fields))
		for i, f := range ty.Fields {
			out[i] = f.Type
		}
		return out
	}
	return nil
}

func (w *walker) isInterfaceType(t types.TypeID) bool {
	if w.items == nil || t == types.NoType {
		return false
	}
	ty := w.tab.Get(t)
	if ty.Kind != types.KNamed || ty.Item == types.NoItem {
		return false
	}
	_, ok := w.items.Get(ty.Item).Decl.(*types.InterfaceDecl)
	return ok
}

// collectDstSlots records every carrier slot assignable inside t (either at
// depth 1, or at depth >= 2 through mut T / mut []T / interior-writable or
// store-taking referents under a `*self into self` clause).
func (w *walker) collectDstSlots(t types.TypeID, out *smSet, seen map[types.TypeID]bool, depth int) {
	if out.any || t == types.NoType || t == types.Void || seen[t] {
		return
	}
	if depth > 64 || len(seen) > 256 {
		out.any = true
		return
	}
	seen[t] = true
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KRef:
		if w.isInterfaceType(ty.Elem) {
			out.any = true
			return
		}
		out.add(smRef, ty.Elem)
		if w.writableByType(ty.Elem) || w.takesStores(ty.Elem) {
			w.collectDstSlots(ty.Elem, out, seen, depth+1)
		}
		return
	case types.KMut:
		if w.isInterfaceType(ty.Elem) {
			out.any = true
			return
		}
		out.add(smMut, ty.Elem)
		w.collectDstSlots(ty.Elem, out, seen, depth+1)
		return
	case types.KSlice:
		if w.isInterfaceType(ty.Elem) {
			out.any = true
			return
		}
		out.add(smSlice, ty.Elem)
		if w.writableByType(ty.Elem) || w.takesStores(ty.Elem) {
			w.collectDstSlots(ty.Elem, out, seen, depth+1)
		}
		return
	case types.KMutSlice:
		if w.isInterfaceType(ty.Elem) {
			out.any = true
			return
		}
		out.add(smMutSlice, ty.Elem)
		w.collectDstSlots(ty.Elem, out, seen, depth+1)
		return
	case types.KStr:
		out.add(smStr, types.NoType)
		return
	case types.KFn, types.KParam, types.KSelf:
		out.any = true
		return
	case types.KNamed:
		if w.isInterfaceType(t) {
			out.any = true
			return
		}
		if w.factsOf(t).View {
			// Any view (plain, read-only, or mut) can be constructed from any
			// compatible borrow of another field (e.g. Sym from &Interner).
			out.any = true
			return
		}
	}
	for _, f := range w.inlineFields(t) {
		w.collectDstSlots(f, out, seen, depth+1)
		if out.any {
			return
		}
	}
}

type srcVisitKey struct {
	t     types.TypeID
	inRef bool
	inMut bool
}

// collectSrcTop records every carrier value extractable from field type t
// when the enclosing struct is behind `mut self` (so &self.f and mut self.f
// are both borrowable, as is anything reachable from t's own bytes).
func (w *walker) collectSrcTop(t types.TypeID, out *smSet) {
	out.add(smRef, t)
	out.add(smMut, t)
	if ty := w.tab.Get(t); ty.Kind == types.KArray {
		out.add(smSlice, ty.Elem)
		out.add(smMutSlice, ty.Elem)
		out.add(smRef, ty.Elem)
		out.add(smMut, ty.Elem)
		if ty.Elem == types.U8 {
			out.add(smStr, types.NoType)
		}
	}
	seen := make(map[srcVisitKey]bool)
	w.collectSrc(t, true, true, out, seen)
}

func (w *walker) collectSrc(t types.TypeID, inRef, inMut bool, out *smSet, seen map[srcVisitKey]bool) {
	if out.any || t == types.NoType || t == types.Void {
		return
	}
	key := srcVisitKey{t: t, inRef: inRef, inMut: inMut}
	if seen[key] {
		return
	}
	if len(seen) > 256 {
		out.any = true
		return
	}
	seen[key] = true
	if inRef {
		out.add(smRef, t)
		if inMut {
			out.add(smMut, t)
		}
	}
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KRef:
		out.add(smRef, ty.Elem)
		w.addArrayCoercions(ty.Elem, false, out)
		w.collectSrc(ty.Elem, true, false, out, seen)
		return
	case types.KMut:
		out.add(smRef, ty.Elem)
		if inMut {
			out.add(smMut, ty.Elem)
		}
		w.addArrayCoercions(ty.Elem, inMut, out)
		w.collectSrc(ty.Elem, true, inMut, out, seen)
		return
	case types.KSlice:
		out.add(smSlice, ty.Elem)
		out.add(smRef, ty.Elem)
		if ty.Elem == types.U8 {
			out.add(smStr, types.NoType)
		}
		w.collectSrc(ty.Elem, true, false, out, seen)
		return
	case types.KMutSlice:
		out.add(smSlice, ty.Elem)
		out.add(smRef, ty.Elem)
		if ty.Elem == types.U8 {
			out.add(smStr, types.NoType)
		}
		if inMut {
			out.add(smMutSlice, ty.Elem)
			out.add(smMut, ty.Elem)
		}
		w.collectSrc(ty.Elem, true, inMut, out, seen)
		return
	case types.KStr:
		out.add(smStr, types.NoType)
		out.add(smSlice, types.U8)
		out.add(smRef, types.U8)
		return
	case types.KFn, types.KParam, types.KSelf:
		out.any = true
		return
	case types.KArray:
		w.addArrayCoercions(t, inRef && inMut, out)
	case types.KNamed:
		if w.isInterfaceType(t) {
			out.any = true
			return
		}
		f := w.factsOf(t)
		if f.Allocator || (f.View && !f.PlainView) {
			out.any = true
			return
		}
		if f.PlainView {
			out.add(smPlainView, t)
		}
		if w.isKnown(t, func(k *types.KnownItems) types.ItemID { return k.Mutex }) && len(ty.Args) > 0 && !ty.Args[0].IsConst {
			elem := ty.Args[0].Type
			out.add(smRef, elem)
			out.add(smMut, elem)
			w.collectSrc(elem, true, true, out, seen)
			return
		}
	}
	for _, fd := range w.inlineFields(t) {
		w.collectSrc(fd, inRef, inMut, out, seen)
		if out.any {
			return
		}
	}
}

func (w *walker) addArrayCoercions(elem types.TypeID, allowMut bool, out *smSet) {
	ty := w.tab.Get(elem)
	if ty.Kind != types.KArray {
		return
	}
	out.add(smSlice, ty.Elem)
	out.add(smRef, ty.Elem)
	if allowMut {
		out.add(smMutSlice, ty.Elem)
		out.add(smMut, ty.Elem)
	}
	if ty.Elem == types.U8 {
		out.add(smStr, types.NoType)
	}
}
