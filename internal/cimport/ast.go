// Why: Clang's JSON AST of a large header set (windows.h has tens of
// thousands of top-level declarations) is too big to decode whole, and the
// binding needs only a small part of each declaration.
//
// What: the decoded model (jnode, decl, field, param, enumConst, unit),
// readUnit, which streams Clang's `-ast-dump=json` output, and readProbes
// for the macro probe's dump.
//
// How: one top-level declaration of the translation unit is decoded at a
// time, and only the fields the mapping reads; a function body stays
// undecoded bytes. Records and enums are linked to their anonymous uses,
// and enumerators with no value are numbered from the one before.

package cimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// jnode is one JSON AST node, with the fields the mapping reads.
type jnode struct {
	ID                  string            `json:"id"`
	Kind                string            `json:"kind"`
	Name                string            `json:"name"`
	IsImplicit          bool              `json:"isImplicit"`
	IsInvalid           bool              `json:"isInvalid"`
	TagUsed             string            `json:"tagUsed"`
	CompleteDefinition  bool              `json:"completeDefinition"`
	Type                *jtype            `json:"type"`
	StorageClass        string            `json:"storageClass"`
	Inline              bool              `json:"inline"`
	Variadic            bool              `json:"variadic"`
	IsBitfield          bool              `json:"isBitfield"`
	Value               json.RawMessage   `json:"value"`
	Init                string            `json:"init"`
	Opcode              string            `json:"opcode"`
	CastKind            string            `json:"castKind"`
	FixedUnderlyingType *jtype            `json:"fixedUnderlyingType"`
	OwnedTagDecl        *jref             `json:"ownedTagDecl"`
	Decl                *jref             `json:"decl"`
	ReferencedDecl      *jref             `json:"referencedDecl"`
	Inner               []json.RawMessage `json:"inner"`
	ArrayFiller         []json.RawMessage `json:"array_filler"`
	Loc                 jloc              `json:"loc"`
	at                  string            // "anon:<file>:<line>:<col>" of a top-level node
	kids                []jnode           // the decoded Inner, once children ran
	kidsOK              bool
}

// jloc is a JSON source location; a macro location holds two.
type jloc struct {
	File         string `json:"file"`
	Line         int    `json:"line"`
	Col          int    `json:"col"`
	ExpansionLoc *jloc  `json:"expansionLoc"`
}

type jtype struct {
	QualType  string `json:"qualType"`
	Desugared string `json:"desugaredQualType"`
}

type jref struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// children decodes n's inner nodes.
func (n *jnode) children() []jnode {
	if n.kidsOK {
		return n.kids
	}
	out := make([]jnode, 0, len(n.Inner))
	for _, raw := range n.Inner {
		var c jnode
		if json.Unmarshal(raw, &c) == nil {
			out = append(out, c)
		}
	}
	n.kids, n.kidsOK = out, true
	return out
}

// fillerNodes decodes n's array_filler nodes (for partially initialized
// arrays: element 0 is the filler, elements 1.. are the explicit ones).
func (n *jnode) fillerNodes() []jnode {
	if len(n.ArrayFiller) == 0 {
		return nil
	}
	out := make([]jnode, 0, len(n.ArrayFiller))
	for _, raw := range n.ArrayFiller {
		var c jnode
		if json.Unmarshal(raw, &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// valueStr returns n's JSON "value" field as a string (unquoting JSON strings
// and keeping JSON numbers or booleans as text), or "" when absent.
func (n *jnode) valueStr() string {
	if len(n.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(n.Value, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(n.Value))
}

// declKind is the kind of a top-level C declaration.
type declKind uint8

const (
	dRecord declKind = iota + 1
	dEnum
	dTypedef
	dFunc
	dVar
)

// field is one member of a record.
type field struct {
	name  string // "" for an anonymous member
	t     *ctype
	bits  int    // bit width of a bitfield; -1 for a plain field
	anon  *decl  // the record of an anonymous member, or of a field whose type is an unnamed record
	cname string // the C spelling of the type, for the comment
}

// enumConst is one enumerator.
type enumConst struct {
	name  string
	value int64
}

// param is one function parameter.
type param struct {
	name string
	t    *ctype
}

// decl is one C declaration of the translation unit.
type decl struct {
	kind declKind
	id   string // Clang's node id
	name string // C name; "" for an unnamed record or enum
	imp  int    // the index of the import it belongs to
	// The spelling key the record layout dump names it by: "struct S",
	// "union U", or "anon:<F:L:C>" for an unnamed one (known once a type
	// spelling refers to it).
	dumpKey string

	// dRecord
	union, complete bool
	fields          []field
	nested          bool // declared inside another record

	// dEnum
	consts  []enumConst
	fixed   *ctype // the fixed underlying type, or nil
	enumMin int64
	enumMax int64

	// dTypedef
	under *ctype
	owned string // the id of the tag the typedef declares, or ""

	// dFunc
	ret       *ctype
	params    []param
	variadic  bool
	static    bool
	inline    bool
	hasBody   bool
	ctypeText string // the function type's spelling, for the comment

	// dVar
	vt       *ctype
	extern   bool
	constVar bool
	initExpr *jnode

	// probeIdx is a probe variable's macro index (__mk_m_<k>), -1 else.
	probeIdx int
}

// unit is the read translation unit.
type unit struct {
	decls    []*decl
	byID     map[string]*decl
	tags     map[string]*decl // "struct S", "union U", "enum E" to the complete (or last) decl
	typedefs map[string]*decl
	anon     map[string]*decl // anon loc to its record or enum
	probes   map[int]*decl    // macro index to its probe variable
	funcs    map[string]*decl // C function name to its (defining, else last) decl
	enumVals map[string]int64 // EnumConstantDecl ID to its integer value
	total    int              // top-level nodes read

	file string // the location state: the last file and line Clang wrote
	line int
}

// locate is the anon key of a node at loc, from the location state.
func (u *unit) locate(l *jloc) string {
	if l.ExpansionLoc != nil {
		l = l.ExpansionLoc
	}
	f, ln := u.file, u.line
	if l.File != "" {
		f = l.File
	}
	if l.Line != 0 {
		ln = l.Line
	}
	return "anon:" + normPath(f) + ":" + strconv.Itoa(ln) + ":" + strconv.Itoa(l.Col)
}

// normPath is a path spelling compared between Clang's outputs: one
// separator, and no case on Windows-style paths.
func normPath(p string) string {
	p = strings.ReplaceAll(p, `\\`, "/")
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) > 1 && p[1] == ':' {
		p = strings.ToLower(p)
	}
	return p
}

// lastKey is the index just after the last occurrence of key in raw (for
// "file", one that is not inside an includedFrom object), or -1.
func lastKey(raw []byte, key string, skipIncluded bool) int {
	end := len(raw)
	for {
		i := bytes.LastIndex(raw[:end], []byte(key))
		if i < 0 {
			return -1
		}
		if skipIncluded {
			// Before it: whitespace, "{", whitespace, "includedFrom":
			j := i - 1
			for j >= 0 && isSpaceB(raw[j]) {
				j--
			}
			if j >= 0 && raw[j] == '{' {
				j--
				for j >= 0 && isSpaceB(raw[j]) {
					j--
				}
				if bytes.HasSuffix(raw[:j+1], []byte(`"includedFrom":`)) {
					end = i
					continue
				}
			}
		}
		return i + len(key)
	}
}

// markerPrefix names the typedef that opens import i's part of the TU.
const markerPrefix = "__mk_import_"

// probePrefix names the probe variable of macro k.
const probePrefix = "__mk_m_"

// readUnit streams the JSON AST of the translation unit from r, its
// top-level elements decoded in parallel (split.go) and applied in order.
func readUnit(r io.Reader) (*unit, error) {
	u := &unit{byID: map[string]*decl{}, tags: map[string]*decl{}, typedefs: map[string]*decl{},
		anon: map[string]*decl{}, probes: map[int]*decl{}, funcs: map[string]*decl{}, enumVals: map[string]int64{}}
	imp := -1
	err := decodeElements(r, func(e *element) error {
		n := &e.n
		// The node's own presumed location, then the state after every
		// location it holds (Clang writes a location's file and line only
		// when they change).
		n.at = u.locate(&n.Loc)
		if e.line != 0 {
			u.line = e.line
		}
		if e.hasFile {
			u.file = e.file
		}
		u.total++
		if e.implicit {
			return nil
		}
		if n.Kind == "TypedefDecl" && strings.HasPrefix(n.Name, markerPrefix) {
			if v, err := strconv.Atoi(n.Name[len(markerPrefix):]); err == nil {
				imp = v
			}
			return nil
		}
		if imp < 0 {
			return nil // the predefined part before the first marker
		}
		u.top(n, imp)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if u.total == 0 {
		return nil, fmt.Errorf("cimport: the AST holds no translation unit")
	}
	return u, nil
}

// top reads one top-level node.
func (u *unit) top(n *jnode, imp int) {
	switch n.Kind {
	case "RecordDecl":
		u.record(n, imp, false)
	case "EnumDecl":
		u.enum(n, imp)
	case "TypedefDecl":
		d := &decl{kind: dTypedef, id: n.ID, name: n.Name, imp: imp, probeIdx: -1}
		if n.Type != nil {
			d.under = parseType(n.Type.QualType)
		}
		for _, c := range n.children() {
			if c.OwnedTagDecl != nil {
				d.owned = c.OwnedTagDecl.ID
			}
		}
		if t := u.byID[d.owned]; t != nil && t.name == "" && d.under != nil {
			u.linkAnon(d.under, t)
			// Clang spells an unnamed tag a typedef declares by the
			// typedef's name: "struct max_align_t".
			if r := d.under; r.kind == tRecord || r.kind == tEnum {
				if r.name != "" {
					kw := "struct "
					if r.kind == tEnum {
						kw = "enum "
					} else if r.union {
						kw = "union "
					}
					if u.tags[kw+r.name] == nil {
						u.tags[kw+r.name] = t
					}
				}
			}
		}
		u.add(d)
		if prev := u.typedefs[d.name]; prev == nil {
			u.typedefs[d.name] = d
		}
	case "FunctionDecl":
		d := &decl{kind: dFunc, id: n.ID, name: n.Name, imp: imp, probeIdx: -1, variadic: n.Variadic,
			static: n.StorageClass == "static", inline: n.Inline}
		if n.Type != nil {
			d.ctypeText = n.Type.QualType
			if ft := parseType(n.Type.QualType); ft.kind == tFunc {
				d.ret = ft.ret
			} else {
				d.ret = &ctype{kind: tBad, name: n.Type.QualType}
			}
		}
		i := 0
		for _, c := range n.children() {
			switch c.Kind {
			case "ParmVarDecl":
				p := param{name: c.Name}
				if c.Type != nil {
					p.t = parseType(c.Type.QualType)
				}
				d.params = append(d.params, p)
				i++
			case "CompoundStmt":
				d.hasBody = true
			}
		}
		if n.IsInvalid {
			return
		}
		u.add(d)
		if prev := u.funcs[d.name]; prev == nil || d.hasBody {
			u.funcs[d.name] = d
		}
	case "VarDecl":
		if strings.HasPrefix(n.Name, probePrefix) {
			if k, err := strconv.Atoi(n.Name[len(probePrefix):]); err == nil && !n.IsInvalid && n.Type != nil {
				qt := n.Type.QualType
				if n.Type.Desugared != "" {
					qt = n.Type.Desugared
				}
				u.probes[k] = &decl{kind: dVar, name: n.Name, vt: parseType(qt), probeIdx: k}
			}
			return
		}
		d := &decl{kind: dVar, id: n.ID, name: n.Name, imp: imp, probeIdx: -1, extern: n.StorageClass == "extern", constVar: isTopConst(n.Type)}
		if n.Type != nil {
			d.vt = parseType(n.Type.QualType)
		}
		if d.constVar && n.Init != "" && !n.IsInvalid {
			for i := range n.children() {
				c := &n.kids[i]
				if !strings.HasSuffix(c.Kind, "Attr") {
					d.initExpr = c
					break
				}
			}
		}
		u.add(d)
	}
}

// isTopConst reports whether t is top-level const (not merely a pointer
// to const): "const int", "const struct _GUID", "char *const", not "const char *".
func isTopConst(t *jtype) bool {
	if t == nil {
		return false
	}
	qt := t.Desugared
	if qt == "" {
		qt = t.QualType
	}
	depth, lastStar := 0, -1
	for i := 0; i < len(qt); i++ {
		switch qt[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '*':
			if depth == 0 {
				lastStar = i
			}
		}
	}
	tail := qt[lastStar+1:]
	for _, tok := range lexType(tail) {
		if tok.word && tok.s == "const" {
			return true
		}
	}
	return false
}

func (u *unit) add(d *decl) {
	u.decls = append(u.decls, d)
	if d.id != "" {
		u.byID[d.id] = d
	}
}

// record reads a RecordDecl and its fields; nested records join the unit
// as their own declarations (C gives a nested tag file scope).
func (u *unit) record(n *jnode, imp int, nested bool) *decl {
	d := &decl{kind: dRecord, id: n.ID, name: n.Name, imp: imp, probeIdx: -1, union: n.TagUsed == "union",
		complete: n.CompleteDefinition, nested: nested}
	kw := "struct "
	if d.union {
		kw = "union "
	}
	if d.name != "" {
		d.dumpKey = kw + d.name
	} else if n.at != "" {
		d.dumpKey = n.at
	}
	var lastAnon *decl
	for _, c := range n.children() {
		switch c.Kind {
		case "RecordDecl":
			r := u.record(&c, imp, true)
			if r.name == "" {
				lastAnon = r
			}
		case "FieldDecl":
			f := field{name: c.Name, bits: -1}
			if c.Type != nil {
				f.t = parseType(c.Type.QualType)
				f.cname = c.Type.QualType
			}
			if c.IsBitfield {
				for _, e := range c.children() {
					if e.Kind == "ConstantExpr" {
						if v, err := strconv.Atoi(e.valueStr()); err == nil {
							f.bits = v
						}
					}
				}
			}
			if lastAnon != nil && f.t != nil && anonOf(f.t) != "" {
				u.linkAnon(f.t, lastAnon)
				f.anon = lastAnon
				lastAnon = nil
			} else if f.t != nil {
				if a := anonOf(f.t); a != "" {
					f.anon = u.anon[a]
				}
			}
			d.fields = append(d.fields, f)
		}
	}
	if n.IsInvalid {
		d.complete = false
	}
	u.add(d)
	if d.name != "" {
		key := kw + d.name
		if prev := u.tags[key]; prev == nil || d.complete {
			u.tags[key] = d
		}
	}
	return d
}

// anonOf is the anon loc of the unnamed record or enum t names, through
// pointers and arrays; "" when it names none.
func anonOf(t *ctype) string {
	for t != nil && (t.kind == tPointer || t.kind == tArray) {
		t = t.elem
	}
	if t != nil && (t.kind == tRecord || t.kind == tEnum) && t.name == "" {
		return t.anon
	}
	return ""
}

// linkAnon records that the unnamed tag t spells is d.
func (u *unit) linkAnon(t *ctype, d *decl) {
	if a := anonOf(t); a != "" {
		u.anon[a] = d
		if d.dumpKey == "" {
			d.dumpKey = "anon:" + normPath(a)
		}
	}
}

// enum reads an EnumDecl and its enumerators, numbering those with no
// value from the one before (C11 6.7.2.2).
func (u *unit) enum(n *jnode, imp int) {
	d := &decl{kind: dEnum, id: n.ID, name: n.Name, imp: imp, probeIdx: -1, complete: true}
	if n.FixedUnderlyingType != nil {
		d.fixed = parseType(n.FixedUnderlyingType.QualType)
	}
	next := int64(0)
	first := true
	for _, c := range n.children() {
		if c.Kind != "EnumConstantDecl" {
			continue
		}
		v := next
		if s, ok := enumValue(c, 0); ok {
			if x, err := strconv.ParseInt(s, 10, 64); err == nil {
				v = x
			} else if y, err := strconv.ParseUint(s, 10, 64); err == nil {
				v = int64(y)
			}
		}
		d.consts = append(d.consts, enumConst{name: c.Name, value: v})
		if c.ID != "" {
			u.enumVals[c.ID] = v
		}
		if first || v < d.enumMin {
			d.enumMin = v
		}
		if first || v > d.enumMax {
			d.enumMax = v
		}
		first = false
		next = v + 1
	}
	u.add(d)
	if d.name != "" {
		u.tags["enum "+d.name] = d
	}
}

// enumValue is the value of an enumerator's initializer: the first
// ConstantExpr below n. In C, Clang wraps it in an ImplicitCastExpr to int
// when the initializer's type is not int (`1u << 4`, `sizeof(T)`, a
// cast), so it need not be a direct child; the search never enters a
// ConstantExpr, whose value is the whole initializer's.
func enumValue(n jnode, depth int) (string, bool) {
	if depth > 8 {
		return "", false
	}
	for _, e := range n.children() {
		if e.Kind == "ConstantExpr" {
			v := e.valueStr()
			return v, v != ""
		}
		if v, ok := enumValue(e, depth+1); ok {
			return v, true
		}
	}
	return "", false
}

func isSpaceB(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' }

// readProbes streams the probe variables' AST (`-ast-dump-filter`, a
// sequence of top-level JSON objects, one per probe variable) from r: each
// macro index to its variable, with the variable's type. The initializers
// are read past, never held.
func readProbes(r io.Reader) (map[int]*decl, error) {
	out := map[int]*decl{}
	dec := json.NewDecoder(bufio.NewReaderSize(r, 1<<20))
	for {
		var n struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			IsInvalid bool   `json:"isInvalid"`
			Type      *jtype `json:"type"`
		}
		err := dec.Decode(&n)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if n.Kind != "VarDecl" || n.IsInvalid || n.Type == nil || !strings.HasPrefix(n.Name, probePrefix) {
			continue
		}
		k, err := strconv.Atoi(n.Name[len(probePrefix):])
		if err != nil {
			continue
		}
		qt := n.Type.QualType
		if n.Type.Desugared != "" {
			qt = n.Type.Desugared
		}
		out[k] = &decl{kind: dVar, name: n.Name, vt: parseType(qt), probeIdx: k}
	}
}
