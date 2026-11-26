// Why: a generator runs in the compile-time evaluator, but its API (Type,
// Code, Block, Emit, Fail, Fresh) needs the compiler's own data; the host
// is the bridge between the two.
//
// What: run, one generator run's host with its handle tables (types, fields,
// variants, methods, flows, codes, blocks), hostKeys mapping every std key of
// the generator API to its intrinsic, quote expansion and splice.
//
// How: the evaluator sees only u32 handles; each table maps a handle to the
// compiler value it stands for. Every token the host copies or keeps is
// charged to the run's fuel, so fuel bounds both time and memory.

package gen

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/eval"
	"mk/internal/source"
	"mk/internal/types"
)

// hostType is one Type handle: a resolved type (id), an interface (iface),
// or an Opaque name the stage-4 view cannot resolve (opaque).
type hostType struct {
	id     types.TypeID
	iface  types.ItemID
	opaque source.Symbol
}

// fieldH, variantH, methodH, paramH are the views a reflection call hands
// out; their reads were recorded by the call that made them.
type fieldH struct {
	name     source.Symbol
	typ      uint32 // a Type handle
	exported bool
	span     source.Span
}

type variantH struct {
	name   source.Symbol
	fields []uint32
}

type methodH struct {
	// iface is the item whose signature every query reads: the interface
	// of an interface method, the function itself for a function.
	iface types.ItemID
	sig   *types.Signature
	flows []FlowView
}

// flowH is one Flow handle.
type flowH struct {
	flow FlowView
}

type paramH struct {
	name    source.Symbol
	typ     uint32
	isConst bool
}

// emit is one g.Emit: the Code's tokens and the call's span.
type emit struct {
	toks []tok
	span source.Span
}

// run is one generator run's host: nothing is shared between runs.
type run struct {
	c         *Context
	line      *Line
	module    types.ModuleID // the line's module
	genModule types.ModuleID // the generator's module
	types     []hostType     // index 0 reserved
	fields    []fieldH
	variants  []variantH
	methods   []methodH
	params    []paramH
	flows     []flowH
	codes     [][]tok
	blocks    [][]tok
	emits     []emit
	fails     []*diag.Diagnostic
	fresh     int
	// docHash memoizes, per file, the content hash of Field.Doc's ReadFile
	// read, so the file is copied and hashed once per run, not once
	// per field; docHashes counts the hashes taken, for tests.
	docHash   map[source.FileID]source.Hash
	docHashes int
	// argTypes is how many Type handles, from 1, bind issued for the
	// line's type and interface arguments.
	argTypes int
	// opaque is set when a structure query on an Opaque type failed the run:
	// the run's E0901 becomes a tentative E0906 for that name.
	opaque source.Symbol
	// opaqueAt is the Type.Fields (or other) call's span.
	opaqueAt source.Span
	// poisoned is set when a reflection call met a type that failed to
	// check (NoType or Poison in a stage-4 signature): the checker's own
	// diagnostic stands and the run yields poison, not E0901.
	poisoned bool
	// unspelled is set when a spliced type held a fn type whose result is
	// an error union held as a value (valueResult): the entry point that
	// spelled it fails the run with it.
	unspelled *types.EvalError
}

func newRun(c *Context, l *Line) *run {
	r := &run{c: c, line: l, module: c.Items.Get(l.Item).Module, types: make([]hostType, 1),
		fields: make([]fieldH, 1), variants: make([]variantH, 1), methods: make([]methodH, 1),
		params: make([]paramH, 1), flows: make([]flowH, 1), codes: make([][]tok, 1), blocks: make([][]tok, 1)}
	if l.Generator != types.NoItem {
		r.genModule = c.Items.Get(l.Generator).Module
	}
	return r
}

// hostKeys maps each API intrinsic's std key to its implementation.
var hostKeys = map[string]func(*run, *eval.Machine, *eval.Call) *types.EvalError{
	"#quote":                    (*run).quote,
	"std.core.Gen:Emit":         (*run).genEmit,
	"std.core.Gen:Block":        (*run).genBlock,
	"std.core.Gen:Arena":        (*run).genArena,
	"std.core.Gen:Fail":         (*run).genFail,
	"std.core.Gen:FailAt":       (*run).genFailAt,
	"std.core.Gen:FailOn":       (*run).genFailOn,
	"std.core.Gen:Fresh":        (*run).genFresh,
	"std.core.Gen:Ident":        (*run).genIdent,
	"std.core.Gen:Str":          (*run).genStr,
	"std.core.Block:Add":        (*run).blockAdd,
	"std.core.Code:Then":        (*run).codeThen,
	"std.core.Type:Name":        (*run).typeName,
	"std.core.Type:Text":        (*run).typeText,
	"std.core.Type:Kind":        (*run).typeKind,
	"std.core.Type:Exported":    (*run).typeExported,
	"std.core.Type:Same":        (*run).typeSame,
	"std.core.Type:Args":        (*run).typeArgs,
	"std.core.Type:Elem":        (*run).typeElem,
	"std.core.Type:Fields":      (*run).typeFields,
	"std.core.Type:Variants":    (*run).typeVariants,
	"std.core.Type:Methods":     (*run).typeMethods,
	"std.core.Field:Name":       (*run).fieldName,
	"std.core.Field:Type":       (*run).fieldType,
	"std.core.Field:Exported":   (*run).fieldExported,
	"std.core.Field:Doc":        (*run).fieldDoc,
	"std.core.Variant:Name":     (*run).variantName,
	"std.core.Variant:Fields":   (*run).variantFields,
	"std.core.Method:Name":      (*run).methodName,
	"std.core.Method:Receiver":  (*run).methodReceiver,
	"std.core.Method:Params":    (*run).methodParams,
	"std.core.Method:Result":    (*run).methodResult,
	"std.core.Method:Generic":   (*run).methodGeneric,
	"std.core.Method:Flows":     (*run).methodFlows,
	"std.core.Method:HasFlows":  (*run).methodHasFlows,
	"std.core.Flow:Kind":        (*run).flowKind,
	"std.core.Flow:Param":       (*run).flowParam,
	"std.core.Flow:Path":        (*run).flowPath,
	"std.core.Flow:Field":       (*run).flowField,
	"std.core.Gen:Input":        (*run).genInput,
	"std.core.Method:Signature": (*run).methodSignature,
	"std.core.Param:Name":       (*run).paramName,
	"std.core.Param:Type":       (*run).paramType,
	"std.core.Param:Const":      (*run).paramConst,
}

// Intrinsic implements eval.Host.
func (r *run) Intrinsic(key types.Key) (eval.HostFunc, bool) {
	f, ok := hostKeys[key.String()]
	if !ok {
		return nil, false
	}
	return func(m *eval.Machine, c *eval.Call) *types.EvalError { return f(r, m, c) }, true
}

// fail is a run-ending bug in the generator: E0901 at the gen line, fact
// panic the message.
func fail(format string, args ...any) *types.EvalError {
	return &types.EvalError{Kind: types.EvalPanic, Panic: types.PanicExplicit, Message: fmt.Sprintf(format, args...)}
}

// checked fails the run as poison when id is a type that failed to check:
// the stage-4 view gives a method parameter or result it cannot translate
// as NoType, and the checker has reported why.
func (r *run) checked(id types.TypeID, what string) *types.EvalError {
	if id != types.NoType && id != types.Poison {
		return nil
	}
	r.poisoned = true
	return fail("gen: %s of a type that failed to check", what)
}

// handleOf reads the handle a handle value holds, through &T or mut T.
func (r *run) handleOf(m *eval.Machine, v eval.Value) (uint32, *types.EvalError) {
	if t := r.c.Types.Get(v.Type); t.Kind == types.KRef || t.Kind == types.KMut {
		d, err := m.Deref(v)
		if err != nil {
			return 0, err
		}
		v = d
	}
	return m.Handle(v), nil
}

// arg reads argument i's handle and checks it against a table of n entries.
func (r *run) arg(m *eval.Machine, c *eval.Call, i int, n int, what string) (uint32, *types.EvalError) {
	if i >= len(c.Args) {
		source.ICEf(c.Span, "gen: %s call with %d arguments", what, len(c.Args))
	}
	h, err := r.handleOf(m, c.Args[i])
	if err != nil {
		return 0, err
	}
	if h == 0 || int(h) >= n {
		return 0, fail("gen: invalid %s handle %d", what, h)
	}
	return h, nil
}

func (r *run) str(m *eval.Machine, c *eval.Call, i int) (string, *types.EvalError) {
	if i >= len(c.Args) {
		source.ICEf(c.Span, "gen: call with %d arguments", len(c.Args))
	}
	return m.Str(c.Args[i])
}

// tokBytes is the host memory one tok takes on 64-bit Go: kind, a string
// header, origin, module and item, padded (TestTokBytes pins it).
const tokBytes = 40

// chargeToks charges n tokens the host is about to copy or keep as an
// allocation of n·tokBytes bytes, c(b) = ceil(b/16) steps (copies are charged
// so that fuel bounds time). Without it a generator doubling a Code reaches
// 2^k tokens in O(k) steps and fuel no longer bounds the compiler's memory.
func (r *run) chargeToks(m *eval.Machine, n int) *types.EvalError {
	if n <= 0 {
		return nil
	}
	b := uint64(n) * tokBytes
	return m.Charge((b + 15) / 16)
}

func (r *run) newCode(ts []tok) uint32 {
	r.codes = append(r.codes, ts)
	return uint32(len(r.codes) - 1)
}

func (r *run) setCode(m *eval.Machine, c *eval.Call, ts []tok) *types.EvalError {
	m.SetHandle(c.Result, r.newCode(ts))
	return nil
}

func (r *run) newType(h hostType) uint32 {
	r.types = append(r.types, h)
	return uint32(len(r.types) - 1)
}

// Gen.

func (r *run) genEmit(m *eval.Machine, c *eval.Call) *types.EvalError {
	if _, err := r.arg(m, c, 0, 2, "Gen"); err != nil {
		return err
	}
	h, err := r.arg(m, c, 1, len(r.codes), "Code")
	if err != nil {
		return err
	}
	// The list is kept, not copied, but merge prints every token of it.
	if err := r.chargeToks(m, len(r.codes[h])); err != nil {
		return err
	}
	r.emits = append(r.emits, emit{toks: r.codes[h], span: c.Span})
	return nil
}

func (r *run) genBlock(m *eval.Machine, c *eval.Call) *types.EvalError {
	if _, err := r.arg(m, c, 0, 2, "Gen"); err != nil {
		return err
	}
	r.blocks = append(r.blocks, nil)
	m.SetHandle(c.Result, uint32(len(r.blocks)-1))
	return nil
}

// genArena hands out the run's arena, the one EvalArena() gives.
func (r *run) genArena(m *eval.Machine, c *eval.Call) *types.EvalError {
	v, err := m.Arena()
	if err != nil {
		return err
	}
	return m.Copy(c.Result, v)
}

// failAt records E0903 at at.
func (r *run) failAt(m *eval.Machine, c *eval.Call, i int, at source.Span) *types.EvalError {
	msg, err := r.str(m, c, i)
	if err != nil {
		return err
	}
	d := diag.New(diag.E0903, at, msg).Text("generator", r.line.Path).Loc("gen", r.line.Span)
	r.fails = append(r.fails, d)
	return nil
}

func (r *run) genFail(m *eval.Machine, c *eval.Call) *types.EvalError {
	return r.failAt(m, c, 1, r.line.Span)
}

func (r *run) genFailAt(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 1, len(r.fields), "Field")
	if err != nil {
		return err
	}
	at := r.fields[h].span
	if at == (source.Span{}) {
		at = r.line.Span
	}
	return r.failAt(m, c, 2, at)
}

func (r *run) genFailOn(m *eval.Machine, c *eval.Call) *types.EvalError {
	h, err := r.arg(m, c, 1, len(r.types), "Type")
	if err != nil {
		return err
	}
	at := r.line.Span
	if it := r.itemOf(r.types[h]); it != types.NoItem {
		at = r.c.Items.Get(it).Span
	}
	r.identity(m, r.types[h])
	return r.failAt(m, c, 2, at)
}

// freshName is g.Fresh's spelling: base + G<line>N<n> for an
// upper-case base, base_g<line>_<n> otherwise.
func freshName(base string, line, n int) string {
	if r, _ := utf8.DecodeRuneInString(base); unicode.IsUpper(r) {
		return fmt.Sprintf("%sG%dN%d", base, line, n)
	}
	return fmt.Sprintf("%s_g%d_%d", base, line, n)
}

func (r *run) genFresh(m *eval.Machine, c *eval.Call) *types.EvalError {
	base, err := r.str(m, c, 1)
	if err != nil {
		return err
	}
	if !isIdent(base) {
		return fail("%s", identError("Gen.Fresh", base))
	}
	r.fresh++
	return r.setCode(m, c, []tok{identTok(freshName(base, r.line.Index, r.fresh))})
}

func (r *run) genIdent(m *eval.Machine, c *eval.Call) *types.EvalError {
	s, err := r.str(m, c, 1)
	if err != nil {
		return err
	}
	if !isIdent(s) {
		return fail("%s", identError("Gen.Ident", s))
	}
	return r.setCode(m, c, []tok{identTok(s)})
}

func (r *run) genStr(m *eval.Machine, c *eval.Call) *types.EvalError {
	s, err := r.str(m, c, 1)
	if err != nil {
		return err
	}
	return r.setCode(m, c, []tok{{kind: ast.TokStr, text: strLit(s)}})
}

// Block and Code.

func (r *run) blockAdd(m *eval.Machine, c *eval.Call) *types.EvalError {
	b, err := r.arg(m, c, 0, len(r.blocks), "Block")
	if err != nil {
		return err
	}
	h, err := r.arg(m, c, 1, len(r.codes), "Code")
	if err != nil {
		return err
	}
	// c's leading and trailing newlines are dropped where a newline already
	// stands: at the block's start or after its last line, and before the
	// newline Add writes.
	cs := r.codes[h]
	for len(cs) > 0 && cs[0].kind == ast.TokNewline {
		cs = cs[1:]
	}
	for len(cs) > 0 && cs[len(cs)-1].kind == ast.TokNewline {
		cs = cs[:len(cs)-1]
	}
	if err := r.chargeToks(m, len(cs)+1); err != nil {
		return err
	}
	// In place: every reader of a block (splice) copies it, so appending
	// keeps n Adds linear, where a copy per Add would be quadratic.
	r.blocks[b] = append(append(r.blocks[b], cs...), tok{kind: ast.TokNewline})
	return nil
}

func (r *run) codeThen(m *eval.Machine, c *eval.Call) *types.EvalError {
	a, err := r.arg(m, c, 0, len(r.codes), "Code")
	if err != nil {
		return err
	}
	b, err := r.arg(m, c, 1, len(r.codes), "Code")
	if err != nil {
		return err
	}
	if err := r.chargeToks(m, len(r.codes[a])+len(r.codes[b])); err != nil {
		return err
	}
	return r.setCode(m, c, append(append([]tok(nil), r.codes[a]...), r.codes[b]...))
}

// quote expands one BuiltinQuote: the template's tokens, literal
// names tagged OriginLiteral with the generator's module, each hole
// replaced by its operand per the splice table.
func (r *run) quote(m *eval.Machine, c *eval.Call) *types.EvalError {
	if len(c.Args) == 0 {
		source.ICEf(c.Span, "gen: quote with no template")
	}
	raw, err := m.Str(c.Args[0])
	if err != nil {
		return err
	}
	tmpl, derr := decodeTemplate([]byte(raw))
	if derr != nil {
		source.ICEf(c.Span, "gen: %v", derr)
	}
	if err := r.chargeToks(m, len(tmpl)); err != nil {
		return err
	}
	var out []tok
	for i, t := range tmpl {
		if t.hole < 0 {
			nt := tok{kind: t.kind, text: t.text}
			if t.kind == ast.TokIdent {
				nt.origin, nt.module = OriginLiteral, r.genModule
			}
			out = append(out, nt)
			continue
		}
		if t.hole+1 >= len(c.Args) {
			source.ICEf(c.Span, "gen: quote hole %d of %d splices", t.hole, len(c.Args)-1)
		}
		ts, err := r.splice(m, c.Args[t.hole+1], c.Span)
		if err != nil {
			return err
		}
		// Leading and trailing newlines of a spliced Code or Block are
		// dropped where a newline already stands.
		for len(ts) > 0 && ts[0].kind == ast.TokNewline && (len(out) == 0 || out[len(out)-1].kind == ast.TokNewline) {
			ts = ts[1:]
		}
		nextNL := i+1 >= len(tmpl) || tmpl[i+1].kind == ast.TokNewline
		for len(ts) > 0 && ts[len(ts)-1].kind == ast.TokNewline && nextNL {
			ts = ts[:len(ts)-1]
		}
		out = append(out, ts...)
	}
	return r.setCode(m, c, out)
}

// splice is one hole's tokens, by the operand's static type.
func (r *run) splice(m *eval.Machine, v eval.Value, at source.Span) ([]tok, *types.EvalError) {
	t := r.c.Types.Get(v.Type)
	switch {
	case t.Kind == types.KStr:
		s, err := m.Str(v)
		if err != nil {
			return nil, err
		}
		if !isIdent(s) {
			return nil, fail("%s", identError("splice", s))
		}
		return []tok{identTok(s)}, nil
	case t.IsInteger():
		return intToks(m.Int(v), t.Signed, m.Uint(v)), nil
	case t.Kind == types.KBool:
		if m.Bool(v) {
			return []tok{identTok("true")}, nil
		}
		return []tok{identTok("false")}, nil
	case t.Kind == types.KChar:
		return []tok{{kind: ast.TokChar, text: charLit(m.Char(v))}}, nil
	case t.Kind == types.KRef || t.Kind == types.KMut || t.Kind == types.KNamed:
		item := t.Item
		if t.Kind != types.KNamed {
			item = r.c.Types.Get(t.Elem).Item
		}
		k := r.c.Known
		switch item {
		case k.TypeInfo:
			h, err := r.handleOf(m, v)
			if err != nil {
				return nil, err
			}
			if h == 0 || int(h) >= len(r.types) {
				return nil, fail("gen: invalid Type handle %d", h)
			}
			r.identity(m, r.types[h])
			ts := r.nameToks(r.types[h])
			if err := r.spellFailed(); err != nil {
				return nil, err
			}
			return ts, r.chargeToks(m, len(ts))
		case k.Code:
			h, err := r.handleOf(m, v)
			if err != nil {
				return nil, err
			}
			if h == 0 || int(h) >= len(r.codes) {
				return nil, fail("gen: invalid Code handle %d", h)
			}
			if err := r.chargeToks(m, len(r.codes[h])); err != nil {
				return nil, err
			}
			return append([]tok(nil), r.codes[h]...), nil
		case k.Block:
			h, err := r.handleOf(m, v)
			if err != nil {
				return nil, err
			}
			if h == 0 || int(h) >= len(r.blocks) {
				return nil, fail("gen: invalid Block handle %d", h)
			}
			if err := r.chargeToks(m, len(r.blocks[h])); err != nil {
				return nil, err
			}
			return append([]tok(nil), r.blocks[h]...), nil
		}
	}
	if ts, ok, err := r.spliceValue(m, v, t); ok { // floats and fn values
		return ts, err
	}
	// check refuses every other operand type with E0301 (genonly.go,
	// spliceOperand), except one that names a type parameter, which a
	// generic helper's instance resolves: E0301 at the splice then, never an
	// internal error (internal/driver TestGen_SpliceOtherOperandE0301
	// and TestGen_GenericSplice). The run's output is discarded, as for a
	// Fail.
	ts := r.c.Types.TypeString(v.Type, nil)
	r.fails = append(r.fails, diag.New(diag.E0301, at, "a splice is a str, an integer, a bool, a char, a float, a fn value, a Type, a Code or a Block; this instance's is `"+ts+"`").
		Text("expected", "str, an integer, bool, char, a float, a fn value, Type, Code or Block").Text("found", ts).Loc("gen", r.line.Span))
	return nil, nil
}
