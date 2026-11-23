// Why: a capturing fn literal passed for a const parameter has an
// environment the generic body knows nothing about; the instance must carry
// it to every call of the parameter.
//
// What: hiddenParams, envPlan, insertParams and threadEnvs: the hidden
// trailing environment parameters of such an instance and the rewrite of
// its calls.
//
// How: the instance takes the literal's environment as a hidden trailing
// parameter, passes it to every call of the parameter, and on to every
// const parameter it forwards the literal to. The plan is read from the
// generic body before its operands are substituted; the rewrite runs after.

package mono

import (
	"mk/internal/mir"
	"mk/internal/types"
)

// litEnv is the environment type a fn constant needs: when c is a fn value
// whose one relocation names a capturing literal (a KindLiteral item whose
// generic body has a hidden parameter past its signature), the hidden
// parameter's type at the relocation's arguments; NoType otherwise.
func (bl *builder) litEnv(c types.ConstID) types.TypeID {
	if c == types.NoConst {
		return types.NoType
	}
	v := bl.env.Consts().Get(c)
	if bl.tab.Kind(v.Type) != types.KFn || len(v.Relocs) != 1 || v.Relocs[0].Kind != types.RelocFn {
		return types.NoType
	}
	f := v.Relocs[0].Fn
	it := bl.env.Items().Get(f.Item)
	if it.Kind != types.KindLiteral {
		return types.NoType
	}
	fd, ok := it.Decl.(*types.FuncDecl)
	if !ok {
		return types.NoType
	}
	lb := bl.env.Body(f.Item)
	if lb == nil || int(lb.ParamCount) <= len(fd.Sig.Params) {
		return types.NoType
	}
	if int(lb.ParamCount) != len(fd.Sig.Params)+1 {
		ice("literal %s has %d hidden parameters, want one", it.Key, int(lb.ParamCount)-len(fd.Sig.Params))
	}
	t := Subst(bl.tab, lb.Locals[lb.ParamCount].Type, f.Args)
	if !mir.IsEnvType(bl.tab, t) {
		ice("literal %s: hidden parameter of type %s", it.Key, bl.tab.TypeString(t, nil))
	}
	return t
}

// hiddenParams lists, in parameter order, the const value parameters bound
// to a capturing literal and their environment types.
func (bl *builder) hiddenParams() (params []mir.LocalID, ts []types.TypeID) {
	if bl.constParam == nil || bl.sig == nil {
		return nil, nil
	}
	for i, p := range bl.sig.Params {
		if !p.Const {
			continue
		}
		l := mir.LocalID(i + 1)
		if t := bl.litEnv(bl.constParam[l]); t != types.NoType {
			params = append(params, l)
			ts = append(ts, t)
		}
	}
	return params, ts
}

// envPlan is what threadEnvs needs from the generic body, read before its
// operands are substituted: the arguments that are whole copies of a
// const parameter with an environment, by index into Body.Args, and the
// blocks whose CallValue goes through one.
type envPlan struct {
	envOf   map[mir.LocalID]mir.LocalID // const parameter -> its hidden parameter
	fwd     map[uint32]mir.LocalID      // Body.Args index -> const parameter
	valCall map[mir.BlockID]mir.LocalID // CallValue block -> const parameter
}

// planEnvs reads gb for envPlan, given the const parameters with an
// environment; nil when there are none.
func planEnvs(gb *mir.Body, params []mir.LocalID) *envPlan {
	if len(params) == 0 {
		return nil
	}
	has := map[mir.LocalID]bool{}
	for _, p := range params {
		has[p] = true
	}
	// A once-assigned temporary holding a whole copy of such a parameter is
	// that parameter: lower slots an operand that a later one could change,
	// and a later fn literal or call argument is not inert, so
	// F(xs, f, fn(..) { .. }) passes `copy _t` after `_t = copy f`.
	// constArgs folds the temporary into the literal's constant
	// as it folds the parameter itself.
	temps := map[mir.LocalID]mir.LocalID{}
	bad := map[mir.LocalID]bool{}
	for bi := range gb.Blocks {
		for si := range gb.Blocks[bi].Stmts {
			st := &gb.Blocks[bi].Stmts[si]
			if st.Kind != mir.StAssign || gb.Locals[st.Place.Local].Kind != mir.LocalTemp {
				continue
			}
			l, rv := st.Place.Local, &st.Rvalue
			if _, seen := temps[l]; seen || st.Place.Proj.Len != 0 || rv.Kind != mir.RvUse ||
				(rv.A.Kind != mir.OpCopy && rv.A.Kind != mir.OpMove) || rv.A.Place.Proj.Len != 0 || !has[rv.A.Place.Local] {
				bad[l] = true
				continue
			}
			temps[l] = rv.A.Place.Local
		}
	}
	whole := func(o mir.Operand) (mir.LocalID, bool) {
		if (o.Kind != mir.OpCopy && o.Kind != mir.OpMove) || o.Place.Proj.Len != 0 {
			return 0, false
		}
		if has[o.Place.Local] {
			return o.Place.Local, true
		}
		if p, ok := temps[o.Place.Local]; ok && !bad[o.Place.Local] {
			return p, true
		}
		return 0, false
	}
	pl := &envPlan{envOf: map[mir.LocalID]mir.LocalID{}, fwd: map[uint32]mir.LocalID{}, valCall: map[mir.BlockID]mir.LocalID{}}
	for bi := range gb.Blocks {
		t := &gb.Blocks[bi].Term
		if !isCall(t) {
			continue
		}
		if p, ok := whole(t.Callee.Value); ok && t.Callee.Kind == mir.CallValue {
			noTail(t, "a call through a const parameter")
			if t.Spawn != mir.NoLocal {
				ice("s.go through a const parameter bound to a capturing literal (E0705 is check's)")
			}
			pl.valCall[mir.BlockID(bi)] = p
		}
		for j := t.Args.Start; j < t.Args.End(); j++ {
			if p, ok := whole(gb.Args[j].Operand); ok {
				pl.fwd[j] = p
			}
		}
	}
	return pl
}

// insertParams adds one hidden LocalParam per type after the body's
// parameters: the new locals take ParamCount+1 .. ParamCount+k, every
// local above the parameters moves up by k, and every LocalID of the body is
// rewritten. It returns the new locals.
func insertParams(b *mir.Body, ts []types.TypeID) []mir.LocalID {
	k := mir.LocalID(len(ts))
	if k == 0 {
		return nil
	}
	at := mir.LocalID(b.ParamCount) + 1
	shift := func(l mir.LocalID) mir.LocalID {
		if l != mir.NoLocal && l >= at {
			return l + k
		}
		return l
	}
	locals := make([]mir.Local, 0, len(b.Locals)+len(ts))
	locals = append(locals, b.Locals[:at]...)
	ids := make([]mir.LocalID, len(ts))
	for i, t := range ts {
		ids[i] = at + mir.LocalID(i)
		locals = append(locals, mir.Local{Type: t, Kind: mir.LocalParam, Param: types.ParamIndex(at) + types.ParamIndex(i), Loop: mir.NoLoop})
	}
	locals = append(locals, b.Locals[at:]...)
	b.Locals = locals
	b.ParamCount += uint32(k)

	place := func(p *mir.Place) { p.Local = shift(p.Local) }
	operand := func(o *mir.Operand) {
		if o.Kind == mir.OpCopy || o.Kind == mir.OpMove {
			place(&o.Place)
		}
	}
	for i := range b.Projections {
		if pr := &b.Projections[i]; pr.Kind == mir.ProjIndex {
			pr.A = uint32(shift(mir.LocalID(pr.A)))
		}
	}
	for i := range b.Operands {
		operand(&b.Operands[i])
	}
	for i := range b.Args {
		operand(&b.Args[i].Operand)
	}
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			st := &blk.Stmts[si]
			place(&st.Place)
			st.Local = shift(st.Local)
			rv := &st.Rvalue
			place(&rv.Place)
			operand(&rv.A)
			operand(&rv.B)
		}
		t := &blk.Term
		operand(&t.On)
		operand(&t.Msg)
		operand(&t.Callee.Value)
		place(&t.Dest)
		t.Spawn = shift(t.Spawn)
		t.Err.Error = shift(t.Err.Error)
		t.Value = shift(t.Value)
	}
	for i := range b.Loops {
		b.Loops[i].Holder = shift(b.Loops[i].Holder)
	}
	if len(b.Asms) > 0 {
		asms := make([]mir.Asm, len(b.Asms))
		for i, a := range b.Asms {
			a.Operands = append([]mir.AsmOperand(nil), a.Operands...)
			for j := range a.Operands {
				operand(&a.Operands[j].In)
				place(&a.Operands[j].Out)
			}
			asms[i] = a
		}
		b.Asms = asms
	}
	return ids
}

// envArg is the call argument threading hidden environment parameter e:
// ArgCopy for a shared &R environment, ArgReborrow for an exclusive mut R
// environment so the callee may call or forward the literal more than once.
func (bl *builder) envArg(b *mir.Body, e mir.LocalID) mir.Arg {
	mode := mir.ArgCopy
	if bl.tab.Kind(b.Locals[e].Type) == types.KMut {
		mode = mir.ArgReborrow
	}
	return mir.Arg{Operand: mir.Copy(mir.LocalPlace(e)), Mode: mode}
}

// threadEnvs threads the environments through the instance body b, after
// its operands are substituted and every const argument is a constant: a call
// through a const parameter with an environment, now direct, gets the
// hidden parameter appended; every other direct call gets its trailing
// environment arguments rebuilt in the callee's const-parameter order,
// a forwarded parameter's from its hidden parameter and a literal written at
// the call's from the arguments lower wrote. A count that does not match is
// an ICE naming the call.
func (bl *builder) threadEnvs(b *mir.Body, pl *envPlan) {
	for bi := range b.Blocks {
		t := &b.Blocks[bi].Term
		if !isCall(t) {
			continue // a tail call's environment arguments are rebuilt as a call's
		}
		args := b.Args[t.Args.Start:t.Args.End()]
		if pl != nil {
			if p, ok := pl.valCall[mir.BlockID(bi)]; ok {
				if t.Callee.Kind != mir.CallDirect {
					ice("instance %s: a call through const parameter %d was not made direct", nameOf(bl.env, bl.item, bl.args), p)
				}
				all := append(append([]mir.Arg(nil), args...), bl.envArg(b, pl.envOf[p]))
				t.Args = b.AddArgs(all...)
				continue
			}
		}
		if t.Callee.Kind != mir.CallDirect {
			continue
		}
		fd, ok := bl.env.Items().Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl)
		if !ok || fd.Sig.Variadic {
			continue
		}
		n := len(fd.Sig.Params)
		if len(args) < n {
			continue // the validator names it
		}
		written := args[n:]
		var envs []mir.Arg
		var mutUsed map[mir.LocalID]bool
		wi, changed := 0, false
		for i, p := range fd.Sig.Params {
			if !p.Const {
				continue
			}
			if pl != nil {
				if q, ok := pl.fwd[t.Args.Start+uint32(i)]; ok {
					if e, ok := pl.envOf[q]; ok {
						if bl.tab.Kind(b.Locals[e].Type) == types.KMut {
							if mutUsed[e] {
								ice("instance %s: call of %s passes mut environment %d more than once (E0705 is check's)",
									nameOf(bl.env, bl.item, bl.args), bl.env.Items().Get(t.Callee.Fn.Item).Key, e)
							}
							if mutUsed == nil {
								mutUsed = map[mir.LocalID]bool{}
							}
							mutUsed[e] = true
						}
						envs = append(envs, bl.envArg(b, e))
						changed = true
						continue
					}
				}
			}
			o := args[i].Operand
			if o.Kind != mir.OpConst || bl.litEnv(o.Const) == types.NoType {
				continue
			}
			if wi >= len(written) {
				ice("instance %s: call of %s passes a capturing literal to const parameter %d with no environment argument",
					nameOf(bl.env, bl.item, bl.args), bl.env.Items().Get(t.Callee.Fn.Item).Key, i+1)
			}
			envs = append(envs, written[wi])
			wi++
		}
		if wi != len(written) {
			ice("instance %s: call of %s has %d environment arguments for %d capturing literals",
				nameOf(bl.env, bl.item, bl.args), bl.env.Items().Get(t.Callee.Fn.Item).Key, len(written), wi)
		}
		if changed {
			if t.Spawn != mir.NoLocal {
				ice("instance %s: an environment on a spawned call", nameOf(bl.env, bl.item, bl.args))
			}
			all := append(append([]mir.Arg(nil), args[:n]...), envs...)
			t.Args = b.AddArgs(all...)
		}
	}
}
