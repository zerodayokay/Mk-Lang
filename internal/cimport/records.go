// Why: C records, unions and bitfields have layouts Mk structs cannot
// always express, and a binding must never give a field the wrong offset or
// pass a record in other registers than C does.
//
// What: record, which binds a C record as a transparent Mk struct when its
// layout matches (transparent) and as an opaque struct otherwise (opaque,
// opaqueBits, opaqueSpanBits); pointerOnly and markSSE, which mark the records that may
// not be used by value; unalias, and signedOf for signed bitfields.
//
// How: a transparent struct's layout is checked against Clang's (natural,
// or packed(n) for #pragma pack). A bitfield becomes a pair of accessors
// over its storage unit, as bindgen generates them. A union, or a layout
// Mk cannot express, is an opaque struct of the record's size and alignment
// with an accessor pair per member, through std.raw: a member or a
// bitfield unit the record's alignment does not align is loaded and stored
// at align 1 (memberAccess), and a bitfield no aligned unit inside the
// record holds through its own bytes.

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
	if !ok || name == "" {
		e.skipped(d.imp, "unnamed record")
		return
	}
	w := &e.mods[d.imp].items
	kw := "struct"
	if d.union {
		kw = "union"
	}
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
		e.skipped(d.imp, "no layout")
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
	if glue {
		st.Generated++
	} else {
		st.Direct++
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
		e.skipped(d.imp, "alignment")
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
			e.skipped(d.imp, "member: "+why)
			continue
		}
		sz, al, ok := e.sizeAlign(f.t)
		if f.anon != nil {
			if la := e.lay[f.anon.dumpKey]; la != nil && f.t.kind != tArray && f.t.kind != tPointer {
				sz, al, ok = la.size, la.align, true
			}
		}
		if !ok {
			e.skipped(d.imp, "member size")
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
		// The record is only l.align aligned, so a member is aligned when
		// its offset is a multiple of its alignment and that alignment is
		// at most the record's.
		aligned := al == 0 || off%al == 0 && al <= l.align
		get, set := e.memberAccess(f, typ, name, off, sz, aligned)
		fmt.Fprintf(w, "\n// C: member %s, at byte %d\n", label, off)
		fmt.Fprintf(w, "export %s.%s(&self): %s {\n    unsafe \"the C member %s is at byte %d of %s\" {\n%s    }\n}\n",
			name, g, typ, label, off, name, get)
		s := sc.fresh("Set"+pascal(label), "Field")
		fmt.Fprintf(w, "export %s.%s(mut self, v: %s) {\n    unsafe \"the C member %s is at byte %d of %s\" {\n%s    }\n}\n",
			name, s, typ, label, off, name, set)
	}
	st.Generated++
}

// memberAccess is the body of the getter and of the setter (v) of the
// member f of Mk type typ, sz bytes at byte off of the opaque record name.
// An aligned member is a raw.Read and a raw.Write. An unaligned one is
// loaded and stored at align 1: an integer through raw.ReadUnaligned and
// WriteUnaligned, a float through its bits, and any other type (a pointer,
// a fn, a record, an array) as a copy of its sz bytes, the getter's through
// an aligned local. sz is C's size, which is the Mk type's (sizeAlign).
func (e *emitter) memberAccess(f *field, typ, name string, off, sz int64, aligned bool) (get, set string) {
	at := func(t, of string) string {
		return fmt.Sprintf("raw.PtrCast[u8, %s](raw.Offset(raw.PtrCast[%s, u8](%s(self)), %d))", t, name, of, off)
	}
	if aligned {
		return fmt.Sprintf("        return raw.Read(%s)\n", at(typ, "raw.Of")),
			fmt.Sprintf("        raw.Write(%s, v)\n", at(typ, "raw.OfMut"))
	}
	switch k := e.unalignedKind(f); k {
	case "int":
		return fmt.Sprintf("        return raw.ReadUnaligned(%s)\n", at(typ, "raw.Of")),
			fmt.Sprintf("        raw.WriteUnaligned(%s, v)\n", at(typ, "raw.OfMut"))
	case "f32", "f64":
		bits := map[string]string{"f32": "u32", "f64": "u64"}[k]
		up := strings.ToUpper(k)
		return fmt.Sprintf("        return raw.%sFrom(raw.ReadUnaligned(%s))\n", up, at(bits, "raw.Of")),
			fmt.Sprintf("        raw.WriteUnaligned(%s, raw.%sBits(v))\n", at(bits, "raw.OfMut"), up)
	}
	n := (sz + 7) / 8
	buf := fmt.Sprintf("raw.PtrCast[[%d]u64, %%s](raw.OfMut(mut b))", n)
	return fmt.Sprintf("        var b := [u64(0); %d]\n        raw.Copy[u8](%s, %s, %d)\n        return raw.Read(%s)\n",
			n, fmt.Sprintf(buf, "u8"), at("u8", "raw.Of"), sz, fmt.Sprintf(buf, typ)),
		fmt.Sprintf("        raw.Copy[u8](%s, raw.PtrCast[%s, u8](raw.Of(&v)), %d)\n", at("u8", "raw.OfMut"), typ, sz)
}

// unalignedKind is how an unaligned member of declared type f.t is
// accessed: "int" (an integer or an enum), "f32" or "f64", or "" (bytes).
func (e *emitter) unalignedKind(f *field) string {
	if f.anon != nil && (f.t == nil || f.t.kind != tArray && f.t.kind != tPointer) {
		return ""
	}
	r := e.resolve(f.t)
	if r == nil {
		return ""
	}
	switch r.kind {
	case tEnum:
		return "int"
	case tTypedef:
		if specialTypedefs[r.name] != "" {
			return "int"
		}
	case tBuiltin:
		switch m, _, _ := e.builtin(r.name); m {
		case "u8", "i8", "u16", "i16", "u32", "i32", "u64", "i64", "uint", "int":
			return "int"
		case "f32", "f64":
			return m
		}
	}
	return ""
}

// opaqueBits writes the accessor pair of bitfield i of an opaque record:
// the smallest integer unit at an aligned offset that holds its bits, else
// the smallest that starts at its first byte, read through std.raw
// (unaligned when the unit is not aligned in the record), and set by a
// load, a mask and a store of the unit (both align 1 in that case). The
// unit lies inside the record and holds no byte of another member but the
// bitfields of the field's own run (otherBytes), the bytes C's own store
// may write: a setter that rewrote a neighbour's byte would lose a store
// to that member made at the same time. When no such unit fits (a 20-bit
// field at byte 3 of a 6-byte packed record, or one after an int at byte
// 1 of a 5-byte one), opaqueSpanBits.
func (e *emitter) opaqueBits(d *decl, name string, l *recLayout, i int, sc *nameScope) {
	f := &d.fields[i]
	fl := l.fields[i]
	lo, hi := fl.bit, fl.bit+int64(fl.width)
	taken := e.otherBytes(d, l, i)
	free := func(o, s int64) bool {
		for b := o; b < o+s; b++ {
			if taken[b] {
				return false
			}
		}
		return true
	}
	var size, off int64
	for _, start := range []func(s int64) int64{func(s int64) int64 { return lo / (8 * s) * s }, func(int64) int64 { return lo / 8 }} {
		for _, s := range []int64{1, 2, 4, 8} {
			o := start(s)
			if (o+s)*8 >= hi && o+s <= l.size && free(o, s) {
				size, off = s, o
				break
			}
		}
		if size != 0 {
			break
		}
	}
	if size == 0 {
		e.opaqueSpanBits(d, name, l, i, sc)
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
	// A unit at a multiple of its size is aligned when that size is at
	// most the record's alignment; else it is loaded and stored at align 1.
	load, store := "raw.Read", "raw.Write"
	if size > l.align || off%size != 0 {
		load, store = "raw.ReadUnaligned", "raw.WriteUnaligned"
	}
	s := sc.fresh("Set"+pascal(f.name), "Bits")
	mptr := strings.Replace(ptr, "raw.Of(self)", "raw.OfMut(self)", 1)
	fmt.Fprintf(w, "export %s.%s(mut self, v: %s) {\n    unsafe \"the C bitfield %s is in the %s at byte %d of %s\" {\n        p := %s\n        %s(p, (%s(p) & %#x) | ((%s & %#x) << %d))\n    }\n}\n",
		name, s, vt, f.name, ut, off, name, mptr, store, load, clear, val, mask, shift)
}

// otherBytes are the bytes of record d that bitfield i's unit may not
// hold: those of every other member but the bitfields of i's run (C11
// 6.7.2.1: adjacent bitfields of nonzero width are one memory location; a
// zero-width bitfield or another member ends the run). A member of unknown
// size takes every byte up to the next member's offset or the record's
// end. In a union every member holds the same bytes, so none is taken.
func (e *emitter) otherBytes(d *decl, l *recLayout, i int) map[int64]bool {
	taken := map[int64]bool{}
	if d.union {
		return taken
	}
	runLo, runHi := i, i
	for runLo > 0 && d.fields[runLo-1].bits > 0 {
		runLo--
	}
	for runHi+1 < len(d.fields) && d.fields[runHi+1].bits > 0 {
		runHi++
	}
	for j := range d.fields {
		if j >= runLo && j <= runHi {
			continue
		}
		f, fl := &d.fields[j], l.fields[j]
		from, to := fl.bit/8, int64(-1)
		switch {
		case f.bits == 0:
			continue
		case f.bits > 0:
			to = (fl.bit + int64(fl.width) + 7) / 8
		default:
			sz, _, ok := e.sizeAlign(f.t)
			if f.anon != nil && (f.t == nil || f.t.kind != tArray && f.t.kind != tPointer) {
				if la := e.lay[f.anon.dumpKey]; la != nil {
					sz, ok = la.size, true
				}
			}
			if ok {
				to = from + sz
			} else {
				to = l.size
				for _, g := range l.fields {
					if g.bit/8 > from && g.bit/8 < to {
						to = g.bit / 8
					}
				}
			}
		}
		for b := from; b < to; b++ {
			taken[b] = true
		}
	}
	return taken
}

// opaqueSpanBits writes the accessor pair of bitfield i of an opaque
// record that no aligned unit inside the record holds: its bytes alone
// (from its first bit's byte to its last's) are copied into a u64 and
// back at align 1, so neither accessor touches a byte of another member
// (a wider unit at an unaligned start would load and store the
// neighbour's bytes, which C's own code does not). The getter's type is
// the smallest unsigned integer that holds those bytes, or the field's
// signed type. A field spanning more than 8 bytes (a 64-bit field off a
// byte boundary) gets no accessor.
func (e *emitter) opaqueSpanBits(d *decl, name string, l *recLayout, i int, sc *nameScope) {
	f := &d.fields[i]
	fl := l.fields[i]
	lo, hi := fl.bit, fl.bit+int64(fl.width)
	off, end := lo/8, (hi+7)/8
	n := end - off
	if n > 8 || end > l.size {
		e.skipped(d.imp, "bitfield of an opaque record")
		return
	}
	size := int64(1)
	for size < n {
		size *= 2
	}
	ut := uintOf(size)
	shift := lo - off*8
	mask := uint64(1)<<uint(fl.width) - 1
	if fl.width >= 64 {
		mask = ^uint64(0)
	}
	clear := ^(mask << uint(shift))
	e.mods[d.imp].needRaw = true
	w := &e.mods[d.imp].items
	at := func(of string) string {
		return fmt.Sprintf("raw.Offset(raw.PtrCast[%s, u8](%s(self)), %d)", name, of, off)
	}
	load := fmt.Sprintf("        var b := u64(0)\n        raw.Copy[u8](raw.PtrCast[u64, u8](raw.OfMut(mut b)), %s, %d)\n", at("raw.Of"), n)
	g := sc.fresh(pascal(f.name), "Bits")
	fmt.Fprintf(w, "\n// C: bitfield %s, %d bits at bit %d of its %d bytes at byte %d\n", f.name, fl.width, shift, n, off)
	vt, read, val := ut, fmt.Sprintf("(b >> %d) & %#x", shift, mask), "u64(v)"
	if size < 8 {
		read = "Trunc[" + ut + "](" + read + ")"
	}
	if st := e.signedOf(f.t); st != "" {
		vt, read, val = st, signedGet("b", 64, int(shift), fl.width, st), signedSet("v", st, "u64")
	}
	fmt.Fprintf(w, "export %s.%s(&self): %s {\n    unsafe \"the C bitfield %s is in the %d bytes at byte %d of %s\" {\n%s        return %s\n    }\n}\n",
		name, g, vt, f.name, n, off, name, load, read)
	set := strings.Replace(load, at("raw.Of"), "p", 1)
	s := sc.fresh("Set"+pascal(f.name), "Bits")
	fmt.Fprintf(w, "export %s.%s(mut self, v: %s) {\n    unsafe \"the C bitfield %s is in the %d bytes at byte %d of %s\" {\n        p := %s\n%s        b = (b & %#x) | ((%s & %#x) << %d)\n        raw.Copy[u8](p, raw.PtrCast[u64, u8](raw.Of(&b)), %d)\n    }\n}\n",
		name, s, vt, f.name, n, off, name, at("raw.OfMut"), set, clear, val, mask, shift, n)
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
			if d.fixed != nil {
				// Its fixed type's, through any typedef (enumInt spells
				// an alias by its Mk name).
				return e.signedOf(d.fixed)
			}
			if !e.win && d.enumMin >= 0 {
				// GNU C's type for an enum with no negative constant is
				// unsigned (unsigned int, or wider), and so its
				// bitfield; on the MSVC target it is int.
				return ""
			}
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
