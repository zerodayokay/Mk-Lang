// Why: generators inspect types (fields, variants, methods, parameters), and
// every answer must be recorded as a read so the incremental cache knows
// which edits rerun which gen line.
//
// What: the host side of the Type, Field, Variant, Method and Param queries
// (typeKind, typeFields, fieldDoc, methodSignature and the rest), subst for
// an instance's field types, and nameToks, typeToks and fnToks, which spell
// a spliced Type as a resolved reference.
//
// How: each query reads the stage-4 declaration view and records the
// narrowest read that covers its answer: identity, structure, signature, or
// a whole-file ReadFile for doc comments. Spelled types pin every name to
// its item, so the output needs no imports.

package gen

import (
	"slices"
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/eval"
	"mk/internal/source"
	"mk/internal/syntax"
	"mk/internal/types"
)

// TypeKind and Receiver discriminants: the declaration order of std's enums
// (std/core/core_gen.mk).
const (
	tkStruct uint64 = iota
	tkEnum
	tkError
	tkInterface
	tkRecord
	tkScalar
	tkRef
	tkSlice
	tkArray
	tkOptional
	tkFallible
	tkFn
	tkRawPtr
	tkOpaque
)

// setEnum stores a payload-free enum result (TypeKind, Receiver) as its tag.
func setEnum(m *eval.Machine, dst eval.Value, x uint64) *types.EvalError {
	m.SetUint(dst, x)
	return nil
}

func (r *run) sym(s source.Symbol) string { return r.c.Interner.Str(s) }

// itemOf is the declared item a Type handle names, NoItem for structural
// and Opaque types.
func (r *run) itemOf(h hostType) types.ItemID {
	if h.iface != types.NoItem {
		return h.iface
	}
	if h.id != types.NoType {
		switch t := r.c.Types.Get(h.id); t.Kind {
		case types.KNamed:
			return t.Item
		case types.KErrorSet:
			// The stage-4 view spells an error item as the set of that one
			// error (check's stage-4 translation), so a set of one error
			// names its declaration (an identity read).
			if ms := r.c.Types.Members(t.Errors); len(ms) == 1 && !ms[0].IsParam {
				return ms[0].Item
			}
		}
	}
	return types.NoItem
}

// identity records the identity read of a Type handle: the
// declaration header of a named type, "no such item" for an Opaque name,
// nothing for a structural type.
func (r *run) identity(m *eval.Machine, h hostType) {
	if it := r.itemOf(h); it != types.NoItem {
		r.sigRead(m, it)
		return
	}
	if h.opaque != source.NoSymbol {
		mod := r.c.Items.Module(r.module)
		m.Reads().Add(types.Read{Kind: types.ReadNoItem, Key: types.ModuleKey(mod.Path), Name: r.sym(h.opaque)})
	}
}

func (r *run) sigRead(m *eval.Machine, it types.ItemID) {
	m.Reads().Add(types.Read{Kind: types.ReadSignature, Key: r.c.Items.Get(it).Key,
		Hash: r.c.World.Hash(it, types.ReadSignature, "")})
}

// view is the stage-4 declaration of item, seen from the line's module.
func (r *run) view(it types.ItemID) (*DeclView, *types.EvalError) {
	v, ok := r.c.World.Decl(it, r.module)
	if !ok || v == nil {
		return nil, fail("gen: no declaration view of %s", r.c.Items.Get(it).Key)
	}
	return v, nil
}

// typeArg reads argument i as a Type handle and records its identity.
func (r *run) typeArg(m *eval.Machine, c *eval.Call, i int) (hostType, *types.EvalError) {
	h, err := r.arg(m, c, i, len(r.types), "Type")
	if err != nil {
		return hostType{}, err
	}
	r.identity(m, r.types[h])
	return r.types[h], nil
}

// opaqueFail ends the run with a tentative E0906 for a structure query on a
// type only generated code may declare.
func (r *run) opaqueFail(h hostType, at source.Span, what string) *types.EvalError {
	r.opaque, r.opaqueAt = h.opaque, at
	return fail("gen: %s of %s, a type the stage-4 view cannot see", what, r.sym(h.opaque))
}

func (r *run) typeName(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	ts := r.nameToks(h)
	if err := r.spellFailed(); err != nil {
		return err
	}
	if err := r.chargeToks(m, len(ts)); err != nil {
		return err
	}
	return r.setCode(m, c, ts)
}

// text is Type.Text: the bare declared name of a named type, else the
// type's source spelling.
func (r *run) text(h hostType) string {
	if h.opaque != source.NoSymbol {
		return r.sym(h.opaque)
	}
	if it := r.itemOf(h); it != types.NoItem {
		return r.sym(r.c.Items.Get(it).Name)
	}
	return r.c.Types.TypeString(h.id, nil)
}

func (r *run) typeText(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	m.SetStr(c.Result, r.text(h))
	return nil
}

// kind is Type.Kind.
func (r *run) kind(h hostType) uint64 {
	if h.opaque != source.NoSymbol {
		return tkOpaque
	}
	if h.iface != types.NoItem {
		// An interface argument whose item is not an interface (dyn.Of(S)
		// over a struct) answers with the item's own kind.
		switch r.c.Items.Get(h.iface).Kind {
		case types.KindInterface:
			return tkInterface
		case types.KindEnum:
			return tkEnum
		case types.KindError:
			return tkError
		}
		return tkStruct
	}
	t := r.c.Types.Get(h.id)
	switch t.Kind {
	case types.KNamed:
		switch r.c.Items.Get(t.Item).Kind {
		case types.KindEnum:
			return tkEnum
		case types.KindError:
			return tkError
		case types.KindInterface:
			return tkInterface
		}
		return tkStruct
	case types.KErrorSet:
		return tkError
	case types.KRecord:
		return tkRecord
	case types.KRef, types.KMut:
		return tkRef
	case types.KSlice, types.KMutSlice:
		return tkSlice
	case types.KArray:
		return tkArray
	case types.KOptional:
		return tkOptional
	case types.KErrorUnion:
		return tkFallible
	case types.KFn:
		return tkFn
	case types.KRawPtr:
		return tkRawPtr
	case types.KParam:
		source.ICEf(r.line.Span, "gen: a type parameter reached the host")
	}
	return tkScalar
}

func (r *run) typeKind(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	return setEnum(m, c.Result, r.kind(h))
}

func (r *run) typeExported(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	exp := true
	if it := r.itemOf(h); it != types.NoItem {
		exp = r.c.Items.Get(it).Exported
	}
	m.SetBool(c.Result, exp)
	return nil
}

// same is Type.Same: TypeID or interface identity for resolved
// types, name equality for two Opaque types, never across the two.
func same(a, b hostType) bool {
	if (a.opaque != source.NoSymbol) != (b.opaque != source.NoSymbol) {
		return false
	}
	if a.opaque != source.NoSymbol {
		return a.opaque == b.opaque
	}
	return a.id == b.id && a.iface == b.iface
}

func (r *run) typeSame(m *eval.Machine, c *eval.Call) *types.EvalError {
	a, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	b, err := r.typeArg(m, c, 1)
	if err != nil {
		return err
	}
	m.SetBool(c.Result, same(a, b))
	return nil
}

func elemType(m *eval.Machine, r *run, c *eval.Call) types.TypeID {
	return r.c.Types.Get(c.Result.Type).Elem
}

func (r *run) typeArgs(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	if h.opaque != source.NoSymbol {
		return r.opaqueFail(h, c.Span, "Type.Args")
	}
	var hs []uint32
	if h.id != types.NoType {
		if t := r.c.Types.Get(h.id); t.Kind == types.KNamed {
			for _, a := range t.Args {
				if !a.IsConst {
					hs = append(hs, r.newType(hostType{id: a.Type}))
				}
			}
		}
	}
	m.SetHandles(c.Result, elemType(m, r, c), hs)
	return nil
}

func (r *run) typeElem(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	if h.opaque != source.NoSymbol {
		return r.opaqueFail(h, c.Span, "Type.Elem")
	}
	if h.id != types.NoType {
		switch t := r.c.Types.Get(h.id); t.Kind {
		case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KArray, types.KOptional, types.KRawPtr,
			types.KErrorUnion:
			m.SetHandle(c.Result, r.newType(hostType{id: t.Elem}))
			return nil
		}
	}
	return fail("gen: Type.Elem of %s, which has no element", r.text(h))
}

// argsOf is the generic arguments of a named type handle, none otherwise.
func (r *run) argsOf(h hostType) []types.GenericArg {
	if h.id == types.NoType {
		return nil
	}
	return r.c.Types.Get(h.id).Args
}

// fieldHandles hands out a view's fields of an instance with args
// substituted; an unresolved field type is Opaque.
func (r *run) fieldHandles(fs []FieldView, args []types.GenericArg) []uint32 {
	hs := make([]uint32, 0, len(fs))
	for _, f := range fs {
		ht := hostType{opaque: f.Unresolved}
		if f.Type != types.NoType {
			ht = hostType{id: r.subst(f.Type, args)}
		}
		r.fields = append(r.fields, fieldH{name: f.Name, typ: r.newType(ht), exported: f.Exported, span: f.Span})
		hs = append(hs, uint32(len(r.fields)-1))
	}
	return hs
}

// structureRead records ReadFields of item: "all" from the item's own
// module, "" from any other.
func (r *run) structureRead(m *eval.Machine, it types.ItemID) {
	name := ""
	if r.c.Items.Get(it).Module == r.module {
		name = "all"
	}
	m.Reads().Add(types.Read{Kind: types.ReadFields, Key: r.c.Items.Get(it).Key, Name: name,
		Hash: r.c.World.Hash(it, types.ReadFields, name)})
}

func (r *run) typeFields(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	if h.opaque != source.NoSymbol {
		return r.opaqueFail(h, c.Span, "Type.Fields")
	}
	var hs []uint32
	switch k := r.kind(h); k {
	case tkStruct, tkError:
		it := r.itemOf(h)
		if it == types.NoItem {
			return fail("gen: Type.Fields of %s, which has no declaration", r.text(h))
		}
		v, err := r.view(it)
		if err != nil {
			return err
		}
		r.structureRead(m, it)
		hs = r.fieldHandles(v.Fields, r.argsOf(h))
	case tkRecord:
		for _, f := range r.c.Types.Get(h.id).Fields {
			r.fields = append(r.fields, fieldH{name: f.Name, typ: r.newType(hostType{id: f.Type}), exported: true})
			hs = append(hs, uint32(len(r.fields)-1))
		}
	default:
		return fail("gen: Type.Fields of %s, which is not a struct, error or record", r.text(h))
	}
	m.SetHandles(c.Result, elemType(m, r, c), hs)
	return nil
}

func (r *run) typeVariants(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	if h.opaque != source.NoSymbol {
		return r.opaqueFail(h, c.Span, "Type.Variants")
	}
	if r.kind(h) != tkEnum {
		return fail("gen: Type.Variants of %s, which is not an enum", r.text(h))
	}
	it := r.itemOf(h)
	v, err := r.view(it)
	if err != nil {
		return err
	}
	r.structureRead(m, it)
	args := r.argsOf(h)
	hs := make([]uint32, 0, len(v.Variants))
	for _, vv := range v.Variants {
		r.variants = append(r.variants, variantH{name: vv.Name, fields: r.fieldHandles(vv.Fields, args)})
		hs = append(hs, uint32(len(r.variants)-1))
	}
	m.SetHandles(c.Result, elemType(m, r, c), hs)
	return nil
}

func (r *run) typeMethods(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.typeArg(m, c, 0)
	if err != nil {
		return err
	}
	if h.opaque != source.NoSymbol {
		return r.opaqueFail(h, c.Span, "Type.Methods")
	}
	if k := r.kind(h); k == tkStruct || k == tkEnum {
		return r.typeFuncs(m, c, h) // inherent functions (methods.go)
	}
	if r.kind(h) != tkInterface {
		return fail("gen: Type.Methods of %s, which is not an interface, struct or enum", r.text(h))
	}
	it := r.itemOf(h)
	v, err := r.view(it)
	if err != nil {
		return err
	}
	hs := make([]uint32, 0, len(v.Methods))
	for i := range v.Methods {
		r.methods = append(r.methods, methodH{iface: it, sig: &v.Methods[i].Sig, flows: v.Methods[i].Flows})
		hs = append(hs, uint32(len(r.methods)-1))
	}
	m.SetHandles(c.Result, elemType(m, r, c), hs)
	return nil
}

// Field, Variant, Param.

func (r *run) fieldName(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.fields), "Field")
	if err != nil {
		return err
	}
	m.SetStr(c.Result, r.sym(r.fields[h].name))
	return nil
}

func (r *run) fieldType(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.fields), "Field")
	if err != nil {
		return err
	}
	m.SetHandle(c.Result, r.fields[h].typ)
	return nil
}

func (r *run) fieldExported(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.fields), "Field")
	if err != nil {
		return err
	}
	m.SetBool(c.Result, r.fields[h].exported)
	return nil
}

// fileReadDomain is the hash domain of a ReadFile read's Hash: the SHA-256
// of the file's text as the SourceMap holds it. A World answering ReadFile
// (iface.SessionWorld.File) must hash the same way.
const fileReadDomain = "mk.file.v1"

// fieldDoc is Field.Doc (cli.Args uses it for usage text): the "///" lines
// directly above the field's declaration, each with "///" and one following
// space removed, joined by "\n", which is syntax.DocComment's form for items;
// "" when there are none, and for a record field or any field without a
// declaration span. A doc comment is in no token hash (comments and
// whitespace are not hashed), so the read is recorded as ReadFile of the
// declaring file with its content hash: an edit of that file, a comment
// included, invalidates what the gen line produced. The hash is taken once
// per file per run (docHash), so a generator calling Doc on every field of a
// struct hashes its file once, not once per field.
func (r *run) fieldDoc(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.fields), "Field")
	if err != nil {
		return err
	}
	doc := ""
	if sp := r.fields[h].span; sp.File != source.NoFile && r.c.Sources != nil {
		if f := r.c.Sources.File(sp.File); f != nil {
			doc = docAbove(f.Text, sp.Start)
			// Added on every call: the read belongs to the running
			// segment's set, which keeps one entry per read.
			m.Reads().Add(types.Read{Kind: types.ReadFile, Name: f.Path, Hash: r.docFileHash(sp.File, f.Text)})
		}
	}
	m.SetStr(c.Result, doc)
	return nil
}

// docFileHash is the ReadFile hash of file id, whose text is text: taken
// once per run and memoized, since every Field.Doc of a struct reads
// the same file.
func (r *run) docFileHash(id source.FileID, text string) source.Hash {
	if h, ok := r.docHash[id]; ok {
		return h
	}
	if r.docHash == nil {
		r.docHash = make(map[source.FileID]source.Hash)
	}
	h := source.HashBytes(fileReadDomain, []byte(text))
	r.docHash[id] = h
	r.docHashes++
	return h
}

// docAbove is the doc comment of the declaration that starts at off: the run
// of lines directly above off's line whose text, after indentation, is a doc
// comment by the lexer's rule (exactly three slashes not followed by a
// fourth), in source order. A declaration that does not start its line has
// none. The line may hold `export` before off: a field's span starts at its
// name (check's declaration view), so an exported field's `export ` precedes
// it. Linear in the doc lines and the declaration's line prefix.
func docAbove(text string, off uint32) string {
	if int(off) > len(text) {
		return ""
	}
	ls := strings.LastIndexByte(text[:off], '\n') + 1
	if !declStartsLine(text[ls:off]) {
		return ""
	}
	var lines []string
	for end := ls; end > 0; {
		prev := strings.LastIndexByte(text[:end-1], '\n') + 1
		line := strings.TrimLeft(strings.TrimSuffix(text[prev:end-1], "\r"), " \t")
		if !strings.HasPrefix(line, "///") || strings.HasPrefix(line, "////") {
			break
		}
		lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "///"), " "))
		end = prev
	}
	slices.Reverse(lines)
	return strings.Join(lines, "\n")
}

// declStartsLine reports whether prefix, a line's text before a
// declaration's span, leaves the declaration starting its line: nothing but
// indentation, or indentation, the keyword export and at least one blank.
func declStartsLine(prefix string) bool {
	p := strings.TrimLeft(prefix, " \t")
	if p == "" {
		return true
	}
	rest, ok := strings.CutPrefix(p, "export")
	return ok && rest != "" && strings.TrimLeft(rest, " \t") == ""
}

func (r *run) variantName(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.variants), "Variant")
	if err != nil {
		return err
	}
	m.SetStr(c.Result, r.sym(r.variants[h].name))
	return nil
}

func (r *run) variantFields(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.variants), "Variant")
	if err != nil {
		return err
	}
	m.SetHandles(c.Result, elemType(m, r, c), r.variants[h].fields)
	return nil
}

func (r *run) paramName(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.params), "Param")
	if err != nil {
		return err
	}
	m.SetStr(c.Result, r.sym(r.params[h].name))
	return nil
}

func (r *run) paramType(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.params), "Param")
	if err != nil {
		return err
	}
	m.SetHandle(c.Result, r.params[h].typ)
	return nil
}

func (r *run) paramConst(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 0, len(r.params), "Param")
	if err != nil {
		return err
	}
	m.SetBool(c.Result, r.params[h].isConst)
	return nil
}

// Method: every query records ReadSignature of the interface.

func (r *run) method(m *eval.Machine, c *eval.Call) (*methodH, *types.EvalError) {
	h, err := r.arg(m, c, 0, len(r.methods), "Method")
	if err != nil {
		return nil, err
	}
	mh := &r.methods[h]
	r.sigRead(m, mh.iface)
	return mh, nil
}

func (r *run) methodName(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	m.SetStr(c.Result, r.sym(mh.sig.Name))
	return nil
}

func (r *run) methodReceiver(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	// Static Value Ref Mut, the order of types.Receiver.
	return setEnum(m, c.Result, uint64(mh.sig.Receiver))
}

// ownParams are a method's parameters after the receiver.
func ownParams(s *types.Signature) []types.Param {
	if s.Receiver != types.NoReceiver && len(s.Params) > 0 {
		return s.Params[1:]
	}
	return s.Params
}

func (r *run) methodParams(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	if mh.sig.OwnGenerics > 0 {
		return fail("gen: Method.Params of generic method %s; test Method.Generic first", r.sym(mh.sig.Name))
	}
	ps := ownParams(mh.sig)
	hs := make([]uint32, 0, len(ps))
	for _, p := range ps {
		if err := r.checked(p.Type, "Method.Params"); err != nil {
			return err
		}
		r.params = append(r.params, paramH{name: p.Name, typ: r.newType(hostType{id: p.Type}), isConst: p.Const})
		hs = append(hs, uint32(len(r.params)-1))
	}
	m.SetHandles(c.Result, elemType(m, r, c), hs)
	return nil
}

func (r *run) methodResult(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	if mh.sig.OwnGenerics > 0 {
		return fail("gen: Method.Result of generic method %s; test Method.Generic first", r.sym(mh.sig.Name))
	}
	if err := r.checked(mh.sig.Result, "Method.Result"); err != nil {
		return err
	}
	m.SetHandle(c.Result, r.newType(hostType{id: r.c.Types.ReturnType(mh.sig.Result, mh.sig.Errors)}))
	return nil
}

func (r *run) methodGeneric(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	m.SetBool(c.Result, mh.sig.OwnGenerics > 0)
	return nil
}

// flows reports whether a signature declares from, into, *q into p or ends.
func flows(s *types.Signature) bool {
	if s.Flows.Declared || len(s.Flows.Stores) > 0 || len(s.Flows.Ends) > 0 {
		return true
	}
	for _, p := range s.Params {
		if p.Into != types.NoParam {
			return true
		}
	}
	return false
}

func (r *run) methodHasFlows(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	m.SetBool(c.Result, flows(mh.sig))
	return nil
}

// methodSignature prints (<receiver>, <name>: <type>, ...): <result> from
// the signature, Self kept, flows not printed.
func (r *run) methodSignature(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	s := mh.sig
	if s.OwnGenerics > 0 {
		return fail("gen: Method.Signature of generic method %s; test Method.Generic first", r.sym(s.Name))
	}
	for _, p := range ownParams(s) {
		if err := r.checked(p.Type, "Method.Signature"); err != nil {
			return err
		}
	}
	if err := r.checked(s.Result, "Method.Signature"); err != nil {
		return err
	}
	ts := []tok{{kind: ast.TokLParen}}
	n := 0
	switch s.Receiver {
	case types.ByValue:
		ts = append(ts, identTok("self"))
		n++
	case types.ByShared:
		ts = append(ts, tok{kind: ast.TokAmp}, identTok("self"))
		n++
	case types.ByMut:
		ts = append(ts, tok{kind: ast.TokMut}, identTok("self"))
		n++
	}
	for _, p := range ownParams(s) {
		if n > 0 {
			ts = append(ts, tok{kind: ast.TokComma})
		}
		n++
		if p.Const {
			ts = append(ts, tok{kind: ast.TokConst})
		}
		ts = append(ts, identTok(r.sym(p.Name)), tok{kind: ast.TokColon})
		ts = append(ts, r.typeToks(p.Type)...)
	}
	ts = append(ts, tok{kind: ast.TokRParen})
	if s.Result != types.Void || s.Errors != types.EmptyErrors {
		r.valueResult(s.Result, s.Errors, "the method `"+r.sym(s.Name)+"`")
		ts = append(ts, tok{kind: ast.TokColon})
		ts = append(ts, r.typeToks(r.c.Types.ReturnType(s.Result, s.Errors))...)
	}
	if err := r.spellFailed(); err != nil {
		return err
	}
	if err := r.chargeToks(m, len(ts)); err != nil {
		return err
	}
	return r.setCode(m, c, ts)
}

// Spelling a spliced Type as tokens.

// valueResult records, for what's result, that an error union held as a
// value (result a KErrorUnion, errors empty: an alias's or a type
// argument's E!T) has no spelling: a written E!T result is one that fails,
// so the text would read back as another type. The first one fails the
// run (spellFailed).
func (r *run) valueResult(result types.TypeID, errs types.ErrorSetID, what string) {
	if r.unspelled != nil || errs != types.EmptyErrors || r.c.Types.Get(result).Kind != types.KErrorUnion {
		return
	}
	r.unspelled = fail("gen: %s has no spelling in generated code: its result `%s` is an error union held as a value, which cannot fail, and a written E!T result is one that fails", what, r.c.Types.TypeString(result, nil))
}

// spellFailed is the run's failure for a type spelled since the last call
// that has no spelling (valueResult), nil when there is none.
func (r *run) spellFailed() *types.EvalError {
	err := r.unspelled
	r.unspelled = nil
	return err
}

// nameToks spells a Type handle as a resolved reference.
func (r *run) nameToks(h hostType) []tok {
	switch {
	case h.opaque != source.NoSymbol:
		return []tok{identTok(r.sym(h.opaque))}
	case h.iface != types.NoItem:
		return r.itemToks(h.iface)
	}
	return r.typeToks(h.id)
}

// itemToks is a pinned reference to item: its module's last path segment
// as qualifier for an item of another module, bare for the line's module.
func (r *run) itemToks(it types.ItemID) []tok {
	item := r.c.Items.Get(it)
	name := tok{kind: ast.TokIdent, text: r.sym(item.Name), origin: OriginPinnedItem, item: it}
	if item.Module == r.module {
		return []tok{name}
	}
	path := r.c.Items.Module(item.Module).Path
	q := path[strings.LastIndexByte(path, '.')+1:]
	return []tok{{kind: ast.TokIdent, text: q, origin: OriginPinnedModule, module: item.Module}, {kind: ast.TokDot}, name}
}

// typeToks spells a concrete type as tokens, named types pinned.
func (r *run) typeToks(id types.TypeID) []tok {
	t := r.c.Types.Get(id)
	// pre writes a prefix operator and its operand, parenthesized as
	// types.TypeString does so the text parses back to the same type: an
	// error union, or a union of several errors, under any prefix ("!" and
	// "|" bind looser than every prefix), and a slice after mut
	// (mut ([]T) is KMut of a KSlice, mut []T is KMutSlice).
	pre := func(ts ...tok) []tok {
		e := r.c.Types.Get(t.Elem)
		paren := e.Kind == types.KErrorUnion || (t.Kind == types.KMut && e.Kind == types.KSlice) ||
			(e.Kind == types.KErrorSet && len(r.c.Types.Members(e.Errors)) > 1)
		if !paren {
			return append(ts, r.typeToks(t.Elem)...)
		}
		ts = append(append(ts, tok{kind: ast.TokLParen}), r.typeToks(t.Elem)...)
		return append(ts, tok{kind: ast.TokRParen})
	}
	switch t.Kind {
	case types.KNamed:
		ts := r.itemToks(t.Item)
		if len(t.Args) > 0 {
			ts = append(ts, tok{kind: ast.TokLBracket})
			for i, a := range t.Args {
				if i > 0 {
					ts = append(ts, tok{kind: ast.TokComma})
				}
				if a.IsConst {
					ts = append(ts, r.constArgToks(a.Const)...)
				} else {
					ts = append(ts, r.typeToks(a.Type)...)
				}
			}
			ts = append(ts, tok{kind: ast.TokRBracket})
		}
		return ts
	case types.KRef:
		return pre(tok{kind: ast.TokAmp})
	case types.KMut:
		return pre(tok{kind: ast.TokMut})
	case types.KSlice:
		return pre(tok{kind: ast.TokLBracket}, tok{kind: ast.TokRBracket})
	case types.KMutSlice:
		return pre(tok{kind: ast.TokMut}, tok{kind: ast.TokLBracket}, tok{kind: ast.TokRBracket})
	case types.KRawPtr:
		return pre(tok{kind: ast.TokStar})
	case types.KOptional:
		return pre(tok{kind: ast.TokQuestion})
	case types.KArray:
		ts := append([]tok{{kind: ast.TokLBracket}}, r.constArgToks(t.Len)...)
		return pre(append(ts, tok{kind: ast.TokRBracket})...)
	case types.KRecord:
		ts := []tok{{kind: ast.TokLParen}}
		for i, f := range t.Fields {
			if i > 0 {
				ts = append(ts, tok{kind: ast.TokComma})
			}
			ts = append(ts, identTok(r.sym(f.Name)), tok{kind: ast.TokColon})
			ts = append(ts, r.typeToks(f.Type)...)
		}
		return append(ts, tok{kind: ast.TokRParen})
	case types.KErrorUnion, types.KErrorSet:
		var ts []tok
		for i, mem := range r.c.Types.Members(t.Errors) {
			if i > 0 {
				ts = append(ts, tok{kind: ast.TokPipe, tight: true})
			}
			if mem.IsParam {
				source.ICEf(r.line.Span, "gen: an error-set parameter reached the host")
			}
			ts = append(ts, r.itemToks(mem.Item)...)
		}
		if t.Kind == types.KErrorSet {
			return ts
		}
		return append(append(ts, tok{kind: ast.TokBang}), r.typeToks(t.Elem)...)
	case types.KFn:
		return r.fnToks(t.Fn)
	case types.KSelf:
		return []tok{identTok("Self")}
	case types.KParam, types.KInfer:
		source.ICEf(r.line.Span, "gen: a type parameter reached the host")
	}
	return r.lexed(r.c.Types.TypeString(id, nil))
}

// fnToks spells a fn type with its parameter, result and error types pinned
// like any other spliced type (as Type.Name pins it). Flows are positional in
// the type but written with names (ParamFlow, Flow), so a fn type with any
// flow names its parameters p1, p2, ... and is parenthesized, which keeps its
// flows from taking the rest of a comma list. The stage-4 view builds fn
// types without flows (check's stage-4 fnType), so a generator normally meets
// none; a checked fn type's flows are spelled the same way.
func (r *run) fnToks(fn *types.FnType) []tok {
	named := len(fn.Sources) > 0 || len(fn.Stores) > 0 || len(fn.Ends) > 0
	for _, p := range fn.Params {
		named = named || p.Into != types.NoParam
	}
	pname := func(i types.ParamIndex) tok { return identTok("p" + itoa(int(i))) }
	word := func(s string) tok { return tok{kind: ast.TokIdent, text: s} } // a flow word, never looked up
	var ts []tok
	if named {
		ts = append(ts, tok{kind: ast.TokLParen})
	}
	ts = append(ts, tok{kind: ast.TokFn}, tok{kind: ast.TokLParen})
	for i, p := range fn.Params {
		if i > 0 {
			ts = append(ts, tok{kind: ast.TokComma})
		}
		if named {
			ts = append(ts, pname(types.ParamIndex(i+1)), tok{kind: ast.TokColon})
		}
		ts = append(ts, r.typeToks(p.Type)...)
		if p.Into != types.NoParam {
			ts = append(ts, word("into"), pname(p.Into))
		}
	}
	ts = append(ts, tok{kind: ast.TokRParen})
	if fn.Result != types.Void || fn.Errors != types.EmptyErrors {
		r.valueResult(fn.Result, fn.Errors, "the fn type `"+r.c.Types.TypeString(r.c.Types.Fn(*fn), nil)+"`")
		ts = append(ts, tok{kind: ast.TokColon})
		ts = append(ts, r.typeToks(r.c.Types.ReturnType(fn.Result, fn.Errors))...)
	}
	for i, s := range fn.Sources {
		if i == 0 {
			ts = append(ts, word("from"))
		} else {
			ts = append(ts, tok{kind: ast.TokComma})
		}
		ts = append(ts, pname(s.Param))
		owner := types.NoType
		if int(s.Param) >= 1 && int(s.Param) <= len(fn.Params) {
			owner = fn.Params[s.Param-1].Type
		}
		for _, st := range s.Path {
			ts = append(ts, tok{kind: ast.TokDot}, identTok(r.stepName(owner, st)))
			owner = st.Type
		}
	}
	for _, st := range fn.Stores {
		ts = append(ts, tok{kind: ast.TokStar}, pname(st.From), word("into"), pname(st.Into))
	}
	for _, e := range fn.Ends {
		ts = append(ts, word("ends"), pname(e))
	}
	if named {
		ts = append(ts, tok{kind: ast.TokRParen})
	}
	return ts
}

// stepName is the name of the field a from-path step takes out of owner
// (through references): FieldStep.Field indexes the declaring struct's
// Fields.
func (r *run) stepName(owner types.TypeID, st types.FieldStep) string {
	for owner != types.NoType {
		t := r.c.Types.Get(owner)
		if t.Kind != types.KRef && t.Kind != types.KMut {
			if t.Kind == types.KNamed {
				if d, ok := r.c.Items.Get(t.Item).Decl.(*types.StructDecl); ok && int(st.Field) < len(d.Fields) {
					return r.sym(d.Fields[st.Field].Name)
				}
			}
			break
		}
		owner = t.Elem
	}
	source.ICEf(r.line.Span, "gen: a fn type's from path names field %d of %s, which has none", st.Field,
		r.c.Types.TypeString(owner, nil))
	return ""
}

// constArgToks spells a const argument: a value as its literal, SizeOf[T]()
// as written.
func (r *run) constArgToks(a types.ConstArg) []tok {
	switch a.Kind {
	case types.CAValue:
		return r.lexed(r.c.Types.ConstString(a.Value))
	case types.CASizeOf:
		ts := []tok{identTok("SizeOf"), {kind: ast.TokLBracket}}
		ts = append(ts, r.typeToks(a.Type)...)
		return append(ts, tok{kind: ast.TokRBracket}, tok{kind: ast.TokLParen}, tok{kind: ast.TokRParen})
	}
	source.ICEf(r.line.Span, "gen: a const parameter reached the host")
	return nil
}

// lexed tokenizes a spelling the type printer made (scalars, fn types,
// values), its names spliced.
func (r *run) lexed(s string) []tok {
	lt, _ := syntax.Lex(0, s)
	var ts []tok
	for _, t := range lt {
		switch {
		case t.Kind.IsTrivia() || t.Kind == ast.TokTerm || t.Kind == ast.TokEOF:
		case t.Kind == ast.TokIdent:
			ts = append(ts, identTok(t.Text(s)))
		case hasText(t.Kind):
			ts = append(ts, tok{kind: t.Kind, text: t.Text(s)})
		default:
			ts = append(ts, tok{kind: t.Kind})
		}
	}
	return ts
}

// subst rewrites a declaration's field type for an instance:
// KParam index i becomes the instance's i-th argument, bottom-up, each
// rebuilt type interned through Context.Types.
func (r *run) subst(id types.TypeID, args []types.GenericArg) types.TypeID {
	t := r.c.Types.Get(id)
	tab := r.c.Types
	switch t.Kind {
	case types.KParam:
		if int(t.Index) >= len(args) || args[t.Index].IsConst {
			source.ICEf(r.line.Span, "gen: type parameter %d left after substitution", t.Index)
		}
		return args[t.Index].Type
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional:
		e := r.subst(t.Elem, args)
		if e == t.Elem {
			return id
		}
		return tab.Intern(types.Type{Kind: t.Kind, Elem: e})
	case types.KArray:
		e, n := r.subst(t.Elem, args), r.substConst(t.Len, args)
		if e == t.Elem && n == t.Len {
			return id
		}
		return tab.Array(n, e)
	case types.KErrorUnion:
		e := r.subst(t.Elem, args)
		if e == t.Elem {
			return id
		}
		return tab.ErrorUnion(t.Errors, e)
	case types.KNamed:
		if len(t.Args) == 0 {
			return id
		}
		as := make([]types.GenericArg, len(t.Args))
		changed := false
		for i, a := range t.Args {
			if a.IsConst {
				as[i] = types.ConstGenericArg(r.substConst(a.Const, args))
			} else {
				as[i] = types.TypeArg(r.subst(a.Type, args))
			}
			changed = changed || as[i] != a
		}
		if !changed {
			return id
		}
		return tab.Named(t.Item, as)
	case types.KRecord:
		fs := make([]types.RecordField, len(t.Fields))
		changed := false
		for i, f := range t.Fields {
			fs[i] = types.RecordField{Name: f.Name, Type: r.subst(f.Type, args)}
			changed = changed || fs[i].Type != f.Type
		}
		if !changed {
			return id
		}
		return tab.Record(fs)
	case types.KFn:
		fn := *t.Fn
		fn.Params = append([]types.FnParam(nil), fn.Params...)
		changed := false
		for i := range fn.Params {
			p := r.subst(fn.Params[i].Type, args)
			changed = changed || p != fn.Params[i].Type
			fn.Params[i].Type = p
		}
		res := r.subst(fn.Result, args)
		changed = changed || res != fn.Result
		fn.Result = res
		if !changed {
			return id
		}
		return tab.Fn(fn)
	}
	return id
}

func (r *run) substConst(c types.ConstArg, args []types.GenericArg) types.ConstArg {
	switch c.Kind {
	case types.CAParam:
		if int(c.Param) >= len(args) || !args[c.Param].IsConst {
			source.ICEf(r.line.Span, "gen: const parameter %d left after substitution", c.Param)
		}
		return args[c.Param].Const
	case types.CASizeOf:
		c.Type = r.subst(c.Type, args)
	}
	return c
}

// itoa is strconv.Itoa, for the printer.
func itoa(n int) string { return strconv.Itoa(n) }
