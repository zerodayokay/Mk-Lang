// Why: C records, unions and bitfields have layouts Mk structs cannot
// always express, and a binding must never give a field the wrong offset or
// pass a record in other registers than C does.
//
// What: record, which binds a C record as a transparent Mk struct when its
// layout matches (transparent) and as an opaque struct otherwise (opaque,
// opaqueBits); pointerOnly and markSSE, which mark the records that may
// not be used by value; unalias, and signedOf for signed bitfields.
//
// How: a transparent struct's layout is checked against Clang's (natural,
// or packed(n) for #pragma pack). A bitfield becomes a pair of accessors
// over its storage unit, as bindgen generates them. A union, or a layout
// Mk cannot express, is an opaque struct of the record's size and alignment
// with an accessor pair per member, through std.raw.

package cimport

import (
	"fmt"
	"strconv"
	"strings"
)

// mkField is one field of a transparent struct.
type mkField struct {
	name, typ   string
	size, align int64
	off         int64 // Clang's byte offset
	exported    bool
}

// bitAcc is one bitfield's accessor pair over unit field `unit`.
type bitAcc struct {
	name, unit, utyp string
	// styp is the declared signed type, "" for an unsigned one
	styp         string
	shift, width int
	ubits        int
}

// uintOf is the unsigned integer type of n bytes.
func uintOf(n int64) string {
	switch n {
	case 1:
		return "u8"
	case 2:
		return "u16"
	case 4:
		return "u32"
	case 8:
		return "u64"
	}
	return ""
}

// record binds one record.
func (e *emitter) record(d *decl) {
	st := &e.stats[d.imp]
	st.Decls++
	name, ok := e.mkName[d]
	kw := "struct"
	if d.union {
		kw = "union"
	}
	if !ok || name == "" {
		e.skippedDecl(d.imp, kw, "(unnamed)", "unnamed record")
		return
	}
	w := &e.mods[d.imp].items
	cname := d.name
	if cname == "" {
		cname = "(unnamed)"
	}
	if !d.complete {
		fmt.Fprintf(w, "\n// C: %s %s (incomplete: only pointers to it)\nexport struct %s {\n}\n", kw, cname, name)
		st.Direct++
		return
	}
	l := e.lay[d.dumpKey]
	if l == nil || len(l.fields) != len(d.fields) {
		e.skip[d] = "no layout"
		e.skippedDecl(d.imp, kw, cname, "no layout")
		fmt.Fprintf(w, "\n// C: %s %s: Clang gave no layout, so only pointers to it\nexport struct %s {\n}\n", kw, cname, name)
		return
	}
	if !d.union {
		if e.transparent(d, name, l) {
			e.markSSE(d, l, false)
			return
		}
	}
	e.opaque(d, name, l)
	if _, skipped := e.skip[d]; !skipped {
		e.markSSE(d, l, true)
	}
}

// pointerOnly marks, before any item is written, each complete record Mk
// can name only behind a pointer: Clang gave no layout, or its alignment
// is no Mk integer's (over 8: `_Alignas(16)`, `__int128`, SysV `long
// double`). Its binding is an empty struct, so a by-value use of it (a
// field of a transparent struct, a parameter, a result, an array element)
// is not bound, and a pointer to it is still *S. These are the
// conditions record and opaque skip on; a transparent struct never has an
// alignment over 8, since none of its fields has.
func (e *emitter) pointerOnly() {
	for _, d := range e.u.decls {
		if d.kind != dRecord || !d.complete {
			continue
		}
		if n, ok := e.mkName[d]; !ok || n == "" {
			continue
		}
		l := e.lay[d.dumpKey]
		switch {
		case l == nil || len(l.fields) != len(d.fields):
			e.ptrOnly[d] = "no layout"
		case uintOf(l.align) == "" || l.size%l.align != 0:
			e.ptrOnly[d] = "alignment"
		}
	}
}

// markSSE records a record whose Mk form a by-value call would pass in
// other registers than C. On SysV x86_64 an eightbyte holding only
// float and double members is class SSE, and AAPCS64 passes a record of
// floats in FP registers, but an opaque record is `[n]uA`, all integer,
// and a misaligned (packed) member makes C's class MEMORY. So on every
// target but Windows x64 (which passes an aggregate by its size alone), a
// record of at most 16 bytes that is opaque and holds a float or a
// misaligned member, or that is transparent and holds such a record by
// value, is never passed or returned by value through the binding; a
// pointer to it is.
func (e *emitter) markSSE(d *decl, l *recLayout, opaque bool) {
	if e.win || l.size > 16 {
		return
	}
	if opaque {
		if e.hasFloat(d, 0) {
			e.sseRec[d] = "opaque record with a float member, by value"
			return
		}
		for i := range d.fields {
			f := &d.fields[i]
			if f.bits >= 0 {
				continue
			}
			if _, al, ok := e.sizeAlign(f.t); ok && al > 0 && (l.fields[i].bit/8)%al != 0 {
				e.sseRec[d] = "opaque record with a misaligned member, by value"
				return
			}
		}
		return
	}
	for i := range d.fields {
		if r := e.memberSSE(&d.fields[i]); r != "" {
			e.sseRec[d] = r
			return
		}
	}
}

// memberSSE is the sseRec reason of the record a member holds by value
// (through typedefs and arrays), "" for none.
func (e *emitter) memberSSE(f *field) string {
	if f.anon != nil && (f.t == nil || f.t.kind != tPointer) {
		return e.sseRec[f.anon]
	}
	t := e.resolve(f.t)
	for t != nil && t.kind == tArray {
		t = e.resolve(t.elem)
	}
	if t != nil && t.kind == tRecord {
		if rd := e.tagDecl(t); rd != nil {
			return e.sseRec[rd]
		}
	}
	return ""
}

// hasFloat reports whether record d holds a float or double member by
// value, at any depth.
func (e *emitter) hasFloat(d *decl, depth int) bool {
	if d == nil || depth > 16 {
		return false
	}
	for i := range d.fields {
		f := &d.fields[i]
		if f.bits >= 0 {
			continue
		}
		if f.anon != nil && (f.t == nil || f.t.kind != tPointer) {
			if e.hasFloat(f.anon, depth+1) {
				return true
			}
			continue
		}
		t := e.resolve(f.t)
		for t != nil && t.kind == tArray {
			t = e.resolve(t.elem)
		}
		switch {
		case t == nil:
		case t.kind == tBuiltin && (t.name == "float" || t.name == "double" || t.name == "long double"):
			return true
		case t.kind == tRecord:
			if e.hasFloat(e.tagDecl(t), depth+1) {
				return true
			}
		}
	}
	return false
}

// transparent writes d as a Mk struct when the layout Mk gives its fields
// equals Clang's, and reports whether it did.
func (e *emitter) transparent(d *decl, name string, l *recLayout) bool {
	sc := newScope()
	var fields []mkField
	var accs []bitAcc
	var flex *field
	var flexOff int64
	k := 0
	for i := 0; i < len(d.fields); i++ {
		f := &d.fields[i]
		fl := l.fields[i]
		if f.bits >= 0 {
			// A run of bitfields: one unit per run that fits one integer.
			j := i
			lo, hi := fl.bit, fl.bit+int64(fl.width)
			for j+1 < len(d.fields) && d.fields[j+1].bits >= 0 {
				nl := l.fields[j+1]
				if end := nl.bit + int64(nl.width); end > hi {
					if end-lo > 64 {
						break
					}
					hi = end
				}
				j++
			}
			// The unit is the declared type's width when that does not
			// reach the next member (MS layout and most SysV runs), else
			// the smallest integer that holds the run (SysV packs a char
			// after a short run into the same int).
			limit := l.size * 8
			if j+1 < len(d.fields) {
				limit = l.fields[j+1].bit
			}
			size := int64(0)
			var off int64
			declared, _, _ := e.sizeAlign(f.t)
			for _, s := range append([]int64{declared}, 1, 2, 4, 8) {
				if uintOf(s) == "" {
					continue
				}
				o := lo / (8 * s) * s
				if (o+s)*8 >= hi && (o+s)*8 <= limit {
					size, off = s, o
					break
				}
			}
			if size == 0 {
				return false
			}
			unit := sc.fresh("bits"+strconv.FormatInt(off, 10), "_")
			fields = append(fields, mkField{name: unit, typ: uintOf(size), size: size, align: size, off: off, exported: true})
			for q := i; q <= j; q++ {
				bf := &d.fields[q]
				if bf.name == "" || bf.bits == 0 {
					continue
				}
				accs = append(accs, bitAcc{name: bf.name, unit: unit, utyp: uintOf(size), styp: e.signedOf(bf.t),
					shift: int(l.fields[q].bit - off*8), width: l.fields[q].width, ubits: int(size * 8)})
			}
			i = j
			continue
		}
		if r := e.resolve(f.t); r != nil && r.kind == tArray && r.n < 0 {
			if i != len(d.fields)-1 {
				return false
			}
			flex, flexOff = f, fl.bit/8
			continue
		}
		var typ string
		var why string
		if f.name == "" && f.anon != nil {
			typ = e.mkName[f.anon]
			if typ == "" || e.ptrOnly[f.anon] != "" {
				return false
			}
		} else {
			typ, why = e.mkType(e.unalias(f.t, 0), d.imp)
			if why != "" {
				return false
			}
		}
		var sz, al int64
		var ok bool
		if f.anon != nil {
			if la := e.lay[f.anon.dumpKey]; la != nil {
				sz, al, ok = la.size, la.align, true
			}
			if f.t != nil && f.t.kind == tArray {
				sz2, al2, ok2 := e.sizeAlign(f.t)
				sz, al, ok = sz2, al2, ok2
			}
		} else {
			sz, al, ok = e.sizeAlign(f.t)
		}
		if !ok {
			return false
		}
		label := f.name
		if label == "" {
			label = "anon" + strconv.Itoa(k)
			k++
		}
		fields = append(fields, mkField{name: sc.fresh(snake(label), "_"), typ: typ, size: sz, align: al, off: fl.bit / 8,
			exported: true})
	}
	sizes := make([]int64, len(fields))
	aligns := make([]int64, len(fields))
	for i, f := range fields {
		sizes[i], aligns[i] = f.size, f.align
	}
	pack := int64(-1)
	for _, p := range []int64{0, 1, 2, 4, 8} {
		offs, size, align := mkLayout(sizes, aligns, p)
		if flex != nil {
			// The flexible member adds its alignment and no size.
			if _, fa, ok := e.sizeAlign(e.resolve(flex.t).elem); ok && fa > align && p == 0 {
				continue
			}
		}
		match := size == l.size && align == l.align
		for i := range fields {
			if offs[i] != fields[i].off {
				match = false
			}
		}
		if match {
			pack = p
			break
		}
	}
	if pack < 0 {
		return false
	}
	st := &e.stats[d.imp]
	w := &e.mods[d.imp].items
	kw := "struct"
	cname := d.name
	if cname == "" {
		cname = "(unnamed)"
	}
	fmt.Fprintf(w, "\n// C: %s %s, %d bytes, align %d\n", kw, cname, l.size, l.align)
	switch pack {
	case 0:
		w.WriteString("export struct " + name + " {\n")
	case 1:
		w.WriteString("export packed struct " + name + " {\n")
	default:
		fmt.Fprintf(w, "export packed(%d) struct %s {\n", pack, name)
	}
	for _, f := range fields {
		if f.exported {
			fmt.Fprintf(w, "    export %s: %s\n", f.name, f.typ)
		} else {
			fmt.Fprintf(w, "    %s: %s\n", f.name, f.typ)
		}
	}
	w.WriteString("}\n")
	glue := false
	for _, a := range accs {
		g := sc.fresh(pascal(a.name), "Bits")
		s := sc.fresh("Set"+pascal(a.name), "Bits")
		mask := uint64(1)<<uint(a.width) - 1
		if a.width >= 64 {
			mask = ^uint64(0)
		}
		clear := ^(mask << uint(a.shift))
		if a.ubits < 64 {
			clear &= uint64(1)<<uint(a.ubits) - 1
		}
		fmt.Fprintf(w, "\n// C: bitfield %s, %d bits at bit %d of %s\n", a.name, a.width, a.shift, a.unit)
		if a.styp != "" {
			fmt.Fprintf(w, "export %s.%s(&self): %s {\n    return %s\n}\n", name, g, a.styp, signedGet("self."+a.unit, a.ubits, a.shift, a.width, a.styp))
			fmt.Fprintf(w, "export %s.%s(mut self, v: %s) {\n    self.%s = (self.%s & %#x) | ((%s & %#x) << %d)\n}\n",
				name, s, a.styp, a.unit, a.unit, clear, signedSet("v", a.styp, a.utyp), mask, a.shift)
		} else {
			fmt.Fprintf(w, "export %s.%s(&self): %s {\n    return (self.%s >> %d) & %#x\n}\n", name, g, a.utyp, a.unit, a.shift, mask)
			fmt.Fprintf(w, "export %s.%s(mut self, v: %s) {\n    self.%s = (self.%s & %#x) | ((v & %#x) << %d)\n}\n",
				name, s, a.utyp, a.unit, a.unit, clear, mask, a.shift)
		}
		glue = true
	}
	if flex != nil {
		et, why := e.mkType(e.resolve(flex.t).elem, d.imp)
		if why == "" {
			g := sc.fresh(pascal(flex.name), "Data")
			e.mods[d.imp].needRaw = true
			fmt.Fprintf(w, "\n// C: flexible array member %s at byte %d: its address\n", flex.name, flexOff)
			fmt.Fprintf(w, "export %s.%s(&self): *%s {\n    unsafe \"the flexible array member %s starts at byte %d\" {\n        return raw.PtrCast[u8, %s](raw.Offset(raw.PtrCast[%s, u8](raw.Of(self)), %d))\n    }\n}\n",
				name, g, et, flex.name, flexOff, et, name, flexOff)
			glue = true
		}
	}
	if len(accs) == 0 && flex == nil {
		e.structFields[d] = fields
	}
	if e.emitComMethods(d, name, fields, sc) {
		glue = true
	}
	if glue {
		st.Generated++
	} else {
		st.Direct++
	}
	return true
}

// emitComMethods emits one Mk method per vtable entry when d's first field
// points to a transparent vtable struct of function pointers that each take a
// pointer to d as their first parameter. It reports whether any method was
// emitted.
func (e *emitter) emitComMethods(d *decl, name string, fields []mkField, sc *nameScope) bool {
	if len(d.fields) == 0 || len(fields) == 0 || d.fields[0].bits >= 0 {
		return false
	}
	f0t := e.resolve(d.fields[0].t)
	if f0t == nil || f0t.kind != tPointer {
		return false
	}
	vt := e.resolve(f0t.elem)
	if vt == nil || vt.kind != tRecord || vt.union {
		return false
	}
	vtd := e.tagDecl(vt)
	if vtd == nil || !vtd.complete || vtd.union || len(vtd.fields) == 0 || e.ptrOnly[vtd] != "" {
		return false
	}
	vl := e.lay[vtd.dumpKey]
	if vl == nil || len(vl.fields) != len(vtd.fields) {
		return false
	}
	type comSlot struct {
		cName string
		fn    *ctype
	}
	slots := make([]comSlot, len(vtd.fields))
	for i := range vtd.fields {
		vf := &vtd.fields[i]
		if vf.bits >= 0 || vf.name == "" {
			return false
		}
		ft := e.resolve(vf.t)
		if ft == nil || ft.kind != tPointer {
			return false
		}
		fn := e.resolve(ft.elem)
		if fn == nil || fn.kind != tFunc || fn.variadic || len(fn.params) == 0 {
			return false
		}
		p0 := e.resolve(fn.params[0])
		if p0 == nil || p0.kind != tPointer {
			return false
		}
		rec := e.resolve(p0.elem)
		if rec == nil || rec.kind != tRecord || rec.union || e.tagDecl(rec) != d {
			return false
		}
		if _, why := e.fnType(fn, vtd.imp); why != "" {
			return false
		}
		if d.imp != vtd.imp {
			if _, why := e.fnType(fn, d.imp); why != "" {
				return false
			}
		}
		slots[i] = comSlot{cName: vf.name, fn: fn}
	}
	vtSc := newScope()
	vtField := fields[0].name
	w := &e.mods[d.imp].items
	e.mods[d.imp].needRaw = true
	cname := d.name
	if cname == "" {
		cname = "(unnamed)"
	}
	for _, sl := range slots {
		vfMkName := vtSc.fresh(snake(sl.cName), "_")
		mname := sc.fresh(pascal(sl.cName), "Method")
		var ps, args []string
		args = append(args, "raw.Of(self)")
		for k, pt := range sl.fn.params[1:] {
			mkP, _ := e.paramType(pt, d.imp)
			pn := "p" + strconv.Itoa(k)
			ps = append(ps, pn+": "+mkP)
			args = append(args, pn)
		}
		rt, _ := e.mkType(sl.fn.ret, d.imp)
		sig := "&self"
		if len(ps) > 0 {
			sig += ", " + strings.Join(ps, ", ")
		}
		retSuffix := ""
		callPrefix := ""
		if rt != "void" {
			retSuffix = ": " + rt
			callPrefix = "return "
		}
		fmt.Fprintf(w, "\n// C: %s::%s\n", cname, sl.cName)
		fmt.Fprintf(w, "export %s.%s(%s)%s {\n    unsafe \"the C vtable pointer and slot match %s\" {\n        %s(*self.%s).%s.Must()(%s)\n    }\n}\n",
			name, mname, sig, retSuffix, name, callPrefix, vtField, vfMkName, strings.Join(args, ", "))
	}
	return true
}

// unalias spells a field's type without the typedefs whose expansion reaches a
// record or a function type: a struct field naming such an alias, whose own
// expansion names the struct back (EXPR_EVAL, a function pointer taking
// *MIDL_STUB_MESSAGE, in a field of that struct), is E0907 in check. Integer
// typedefs stay, so the fields read as the C does.
func (e *emitter) unalias(t *ctype, depth int) *ctype {
	if t == nil || depth > 64 {
		return t
	}
	switch t.kind {
	case tTypedef:
		if _, ok := specialTypedefs[t.name]; ok {
			return t
		}
		d := e.u.typedefs[t.name]
		if d == nil || d.under == nil || !e.reachesRecord(d.under, 0) {
			return t
		}
		return e.unalias(d.under, depth+1)
	case tPointer, tArray:
		c := *t
		c.elem = e.unalias(t.elem, depth+1)
		return &c
	case tFunc:
		c := *t
		c.ret = e.unalias(t.ret, depth+1)
		c.params = make([]*ctype, len(t.params))
		for i, q := range t.params {
			c.params[i] = e.unalias(q, depth+1)
		}
		return &c
	}
	return t
}

// reachesRecord reports whether t, its typedefs expanded, mentions a
// struct, a union or a function type.
func (e *emitter) reachesRecord(t *ctype, depth int) bool {
	if t == nil || depth > 64 {
		return false
	}
	switch t.kind {
	case tRecord, tFunc:
		return true
	case tTypedef:
		d := e.u.typedefs[t.name]
		return d != nil && e.reachesRecord(d.under, depth+1)
	case tPointer, tArray:
		return e.reachesRecord(t.elem, depth+1)
	}
	return false
}

// opaque writes d as a struct of its size and alignment with accessors.
func (e *emitter) opaque(d *decl, name string, l *recLayout) {
	st := &e.stats[d.imp]
	w := &e.mods[d.imp].items
	kw := "struct"
	if d.union {
		kw = "union"
	}
	cname := d.name
	if cname == "" {
		cname = "(unnamed)"
	}
	ut := uintOf(l.align)
	if ut == "" || l.size%l.align != 0 {
		e.skip[d] = "alignment"
		e.skippedDecl(d.imp, kw, cname, "alignment")
		fmt.Fprintf(w, "\n// C: %s %s: alignment %d is not expressible, so only pointers to it\nexport struct %s {\n}\n", kw, cname, l.align, name)
		return
	}
	n := l.size / l.align
	fmt.Fprintf(w, "\n// C: %s %s, %d bytes, align %d: opaque, members through accessors\n", kw, cname, l.size, l.align)
	fmt.Fprintf(w, "export struct %s {\n    export bytes: [%d]%s\n}\n", name, n, ut)
	sc := newScope()
	sc.claim("bytes")
	k := 0
	for i := range d.fields {
		f := &d.fields[i]
		if f.bits >= 0 {
			if f.name != "" && f.bits > 0 {
				e.opaqueBits(d, name, l, i, sc)
			}
			continue
		}
		var typ string
		var why string
		if f.anon != nil && (f.t == nil || f.t.kind != tArray && f.t.kind != tPointer) {
			typ = e.mkName[f.anon]
			if typ == "" {
				why = "unnamed member"
			} else if r := e.ptrOnly[f.anon]; r != "" {
				why = r
			}
		} else {
			typ, why = e.mkType(f.t, d.imp)
		}
		if why != "" {
			e.skippedDecl(d.imp, "member", cname+"."+f.name, "member: "+why)
			continue
		}
		_, al, ok := e.sizeAlign(f.t)
		if f.anon != nil {
			if la := e.lay[f.anon.dumpKey]; la != nil && f.t.kind != tArray && f.t.kind != tPointer {
				al, ok = la.align, true
			}
		}
		if !ok {
			e.skippedDecl(d.imp, "member", cname+"."+f.name, "member size")
			continue
		}
		off := l.fields[i].bit / 8
		label := f.name
		if label == "" {
			label = "anon" + strconv.Itoa(k)
			k++
		}
		g := sc.fresh(pascal(label), "Field")
		e.mods[d.imp].needRaw = true
		ptr := fmt.Sprintf("raw.PtrCast[u8, %s](raw.Offset(raw.PtrCast[%s, u8](raw.Of(self)), %d))", typ, name, off)
		read := "raw.Read"
		aligned := al == 0 || off%al == 0
		if !aligned {
			read = "raw.ReadUnaligned"
		}
		fmt.Fprintf(w, "\n// C: member %s, at byte %d\n", label, off)
		fmt.Fprintf(w, "export %s.%s(&self): %s {\n    unsafe \"the C member %s is at byte %d of %s\" {\n        return %s(%s)\n    }\n}\n",
			name, g, typ, label, off, name, read, ptr)
		if aligned {
			s := sc.fresh("Set"+pascal(label), "Field")
			mptr := strings.Replace(ptr, "raw.Of(self)", "raw.OfMut(self)", 1)
			fmt.Fprintf(w, "export %s.%s(mut self, v: %s) {\n    unsafe \"the C member %s is at byte %d of %s\" {\n        raw.Write(%s, v)\n    }\n}\n",
				name, s, typ, label, off, name, mptr)
		}
	}
	st.Generated++
}

// opaqueBits writes the accessor pair of bitfield i of an opaque record:
// the smallest integer unit at an aligned offset that holds its bits,
// read through std.raw (unaligned when the unit is wider than the
// record's alignment, and then with no setter).
func (e *emitter) opaqueBits(d *decl, name string, l *recLayout, i int, sc *nameScope) {
	f := &d.fields[i]
	fl := l.fields[i]
	lo, hi := fl.bit, fl.bit+int64(fl.width)
	var size, off int64
	for _, s := range []int64{1, 2, 4, 8} {
		o := lo / (8 * s) * s
		if (o+s)*8 >= hi && o+s <= l.size {
			size, off = s, o
			break
		}
	}
	if size == 0 {
		e.skippedDecl(d.imp, "member", d.name+"."+f.name, "bitfield of an opaque record")
		return
	}
	ut := uintOf(size)
	shift := lo - off*8
	mask := uint64(1)<<uint(fl.width) - 1
	if fl.width >= 64 {
		mask = ^uint64(0)
	}
	clear := ^(mask << uint(shift))
	if size < 8 {
		clear &= uint64(1)<<uint(size*8) - 1
	}
	e.mods[d.imp].needRaw = true
	w := &e.mods[d.imp].items
	ptr := fmt.Sprintf("raw.PtrCast[u8, %s](raw.Offset(raw.PtrCast[%s, u8](raw.Of(self)), %d))", ut, name, off)
	g := sc.fresh(pascal(f.name), "Bits")
	fmt.Fprintf(w, "\n// C: bitfield %s, %d bits at bit %d of the %s at byte %d\n", f.name, fl.width, shift, ut, off)
	vt, read, val := ut, fmt.Sprintf("(raw.ReadUnaligned(%s) >> %d) & %#x", ptr, shift, mask), "v"
	if st := e.signedOf(f.t); st != "" {
		vt, read, val = st, signedGet("raw.ReadUnaligned("+ptr+")", int(size*8), int(shift), fl.width, st), signedSet("v", st, ut)
	}
	fmt.Fprintf(w, "export %s.%s(&self): %s {\n    unsafe \"the C bitfield %s is in the %s at byte %d of %s\" {\n        return %s\n    }\n}\n",
		name, g, vt, f.name, ut, off, name, read)
	if size <= l.align {
		s := sc.fresh("Set"+pascal(f.name), "Bits")
		mptr := strings.Replace(ptr, "raw.Of(self)", "raw.OfMut(self)", 1)
		fmt.Fprintf(w, "export %s.%s(mut self, v: %s) {\n    unsafe \"the C bitfield %s is in the %s at byte %d of %s\" {\n        p := %s\n        raw.Write(p, (raw.Read(p) & %#x) | ((%s & %#x) << %d))\n    }\n}\n",
			name, s, vt, f.name, ut, off, name, mptr, clear, val, mask, shift)
	}
}

// signedOf is the Mk signed integer type a bitfield of declared type t
// reads as, "" when t is unsigned: C reads a signed bitfield sign-extended
// (C11 6.7.2.1). Plain char is u8 in a binding, so unsigned here.
func (e *emitter) signedOf(t *ctype) string {
	r := e.resolve(t)
	if r == nil {
		return ""
	}
	var m string
	switch r.kind {
	case tBuiltin:
		m, _, _ = e.builtin(r.name)
	case tEnum:
		if d := e.tagDecl(r); d != nil {
			m = e.enumInt(d)
		}
	}
	switch m {
	case "i8", "i16", "i32", "i64":
		return m
	}
	return ""
}

// signedGet reads the w-bit signed bitfield at bit s of the n-bit unsigned
// unit u as typ: the unit shifted left until the field's top bit is the
// unit's, reinterpreted as signed (Trunc keeps the bits), then shifted
// right arithmetically, and converted to typ.
func signedGet(u string, n, s, w int, typ string) string {
	in := "i" + strconv.Itoa(n)
	x := fmt.Sprintf("Trunc[%s](%s << %d) >> %d", in, u, n-s-w, n-w)
	if typ == in {
		return x
	}
	tw, _ := strconv.Atoi(typ[1:])
	if tw > n {
		return typ + "(" + x + ")"
	}
	return "Trunc[" + typ + "](" + x + ")"
}

// signedSet is the setter's value v, of signed type typ, as the unit's
// unsigned type utyp, before the mask: its low bits.
func signedSet(v, typ, utyp string) string {
	if typ != "i64" {
		v = "i64(" + v + ")"
	}
	return "Trunc[" + utyp + "](" + v + ")"
}
