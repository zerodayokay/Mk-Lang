// Why: generators that derive code from functions and their flows, or from
// a data file in the package, need host intrinsics beyond the type queries,
// each recorded as a read so the gen line reruns when its input changes.
//
// What: bindFunction (a non-generic function as a gen argument: a Method with
// Receiver Static), typeFuncs (Type.Methods on a named struct or enum),
// methodFlows and the Flow queries (flowKind, flowParam, flowPath,
// flowField), and genInput (Gen.Input, a data file of the gen line's module).
//
// How: each intrinsic hands out handles into the run's tables and records
// what it read: a signature read per method, an exports read for the member
// list, a ReadFile per declaring file for method order, a ReadData for
// Gen.Input, which goes through the Context's DataFiles.

package gen

import (
	"mk/internal/diag"
	"mk/internal/eval"
	"mk/internal/source"
	"mk/internal/types"
)

// funcWorld is the Context's World as a FuncWorld, nil when it is not one.
func (c *Context) funcWorld() FuncWorld {
	fw, _ := c.World.(FuncWorld)
	return fw
}

// bindFunction binds a function gen argument: the
// generator's parameter must be Method; the function must be a free,
// non-generic function. It returns the Method handle, or a diagnostic, or
// poison: a function of another module with no declaration, never checked
// because its module or one it imports has an error before types, which
// has its own diagnostic.
func (c *Context) bindFunction(r *run, a Arg, pt types.TypeID) (uint32, *diag.Diagnostic, bool) {
	methodT := c.Types.Named(c.Known.Method, nil)
	if c.Known.Method == types.NoItem || pt != methodT {
		return 0, c.mismatch(a.Span, pt, "a function"), false
	}
	it := c.Items.Get(a.Item)
	name := c.Interner.Str(it.Name)
	if it.Owner != types.NoItem || it.OwnerPrim != types.NoType {
		return 0, diag.New(diag.E0301, a.Span, "`"+name+"` is a method; a gen argument names a free function").
			Text("expected", "a non-generic function").Text("found", "a method"), false
	}
	if _, ok := it.Decl.(*types.FuncDecl); !ok && it.Module != r.module {
		return 0, nil, true
	}
	fw := c.funcWorld()
	var v *MethodView
	if fw != nil {
		v, _ = fw.Func(a.Item)
	}
	if v == nil {
		return 0, diag.New(diag.E0301, a.Span, "a gen argument is a type, an interface, a literal or a function").
			Text("expected", "a type, an interface, a literal or a function").Text("found", "`"+name+"`"), false
	}
	if len(v.Sig.Generics) > 0 {
		return 0, diag.New(diag.E0301, a.Span, "`"+name+"` is generic, so it cannot be a gen argument").
			Text("expected", "a non-generic function").Text("found", "the generic `"+name+"`").
			Alt("wrap the instance you want in a non-generic function and name that"), false
	}
	sig := v.Sig
	r.methods = append(r.methods, methodH{iface: a.Item, sig: &sig, flows: v.Flows})
	return uint32(len(r.methods) - 1), nil, false
}

// typeFuncs is Type.Methods of a named struct or enum: its inherent
// functions in declaration order, each recorded as a signature read when a
// query reads it, and the type's member list as an exports read, so adding
// or removing one reruns the line.
func (r *run) typeFuncs(m *eval.Machine, c *eval.Call, h hostType) *types.EvalError {
	it := r.itemOf(h)
	fw := r.c.funcWorld()
	if it == types.NoItem || fw == nil {
		return fail("gen: Type.Methods of %s, which has no declaration", r.text(h))
	}
	if len(r.argsOf(h)) > 0 {
		return fail("gen: Type.Methods of %s, a generic type's instance; name a non-generic type", r.text(h))
	}
	if v, err := r.view(it); err != nil {
		return err
	} else if len(v.Generics) > 0 {
		return fail("gen: Type.Methods of the generic %s", r.text(h))
	}
	ids, ok := fw.Funcs(it, r.module)
	if !ok {
		return fail("gen: Type.Methods of %s, which is not a struct or enum", r.text(h))
	}
	name := ""
	if r.c.Items.Get(it).Module == r.module {
		name = "all"
	}
	m.Reads().Add(types.Read{Kind: types.ReadExports, Key: r.c.Items.Get(it).Key, Name: name,
		Hash: r.c.World.Hash(it, types.ReadExports, name)})
	// The ReadExports read covers which methods there are, not their
	// order, which is the declaring files' paths and the methods'
	// positions in them (stage4_funcs.go's Funcs), and which no API hash
	// keeps: a ReadFile of each declaring file of another module, as
	// Field.Doc records, so that reordering two methods there reruns the
	// line. The reader's own module is its tokens.
	if r.c.Items.Get(it).Module != r.module && r.c.Sources != nil {
		seen := map[source.FileID]bool{}
		for _, id := range ids {
			sp := r.c.Items.Get(id).Span
			if sp.File == source.NoFile || seen[sp.File] {
				continue
			}
			seen[sp.File] = true
			if f := r.c.Sources.File(sp.File); f != nil {
				m.Reads().Add(types.Read{Kind: types.ReadFile, Name: f.Path, Hash: r.docFileHash(sp.File, f.Text)})
			}
		}
	}
	hs := make([]uint32, 0, len(ids))
	for _, id := range ids {
		v, ok := fw.Func(id)
		if !ok {
			continue
		}
		sig := v.Sig
		r.methods = append(r.methods, methodH{iface: id, sig: &sig, flows: v.Flows})
		hs = append(hs, uint32(len(r.methods)-1))
	}
	m.SetHandles(c.Result, elemType(m, r, c), hs)
	return nil
}

// methodFlows is Method.Flows: the declared clauses as Flow handles.
func (r *run) methodFlows(m *eval.Machine, c *eval.Call) *types.EvalError {
	mh, err := r.method(m, c)
	if err != nil {
		return err
	}
	hs := make([]uint32, 0, len(mh.flows))
	for _, f := range mh.flows {
		r.flows = append(r.flows, flowH{flow: f})
		hs = append(hs, uint32(len(r.flows)-1))
	}
	m.SetHandles(c.Result, elemType(m, r, c), hs)
	return nil
}

func (r *run) flow(m *eval.Machine, c *eval.Call) (*FlowView, *types.EvalError) {
	h, err := r.arg(m, c, 0, len(r.flows), "Flow")
	if err != nil {
		return nil, err
	}
	return &r.flows[h].flow, nil
}

func (r *run) flowKind(m *eval.Machine, c *eval.Call) *types.EvalError {
	f, err := r.flow(m, c)
	if err != nil {
		return err
	}
	return setEnum(m, c.Result, uint64(f.Kind))
}

func (r *run) flowParam(m *eval.Machine, c *eval.Call) *types.EvalError {
	f, err := r.flow(m, c)
	if err != nil {
		return err
	}
	m.SetStr(c.Result, f.Param)
	return nil
}

func (r *run) flowPath(m *eval.Machine, c *eval.Call) *types.EvalError {
	f, err := r.flow(m, c)
	if err != nil {
		return err
	}
	m.SetStr(c.Result, f.Path)
	return nil
}

func (r *run) flowField(m *eval.Machine, c *eval.Call) *types.EvalError {
	f, err := r.flow(m, c)
	if err != nil {
		return err
	}
	m.SetStr(c.Result, f.Field)
	return nil
}

// genInput is Gen.Input(path): []u8: the file under the package
// root of the gen line's module, read by the Context's DataFiles,
// recorded as a ReadData of the line. A failed read is E0910 at the gen
// line. A Context without DataFiles refuses it as I/O.
func (r *run) genInput(m *eval.Machine, c *eval.Call) *types.EvalError {
	path, err := r.str(m, c, 1)
	if err != nil {
		return err
	}
	if r.c.DataFiles == nil {
		return &types.EvalError{Kind: types.EvalRefused, Refusal: types.RefuseIo, Message: "Gen.Input without a data file reader"}
	}
	return m.GenDataFile(c.Result, r.c.DataFiles.ReadData(r.module, path), path)
}
