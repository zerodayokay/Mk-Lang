// Why: preserve_nonecc tail-dispatch calls must print valid LLVM IR and pass
// clang's verifier on every target before codegen emits them.
//
// What: TestPreserveNoneCC covering FuncSig.CC, CallCC, CallFunc and
// MustTailCallCC, including 8-register-argument indirect musttail, sret,
// C-ABI entry wrapper, external preserve_nonecc declarations, and builder
// invariant panics.
//
// How: builds one module exercising every preserve_nonecc form, checks its
// printed IR text, compiles it with clang's verifier on at -O0 and -O2 across
// smokeTriples, and checks that mismatched calling conventions panic.

package llvm

import (
	"strings"
	"testing"
)

func preserveNoneModule(tg target) *Module {
	m := NewModule("pnone.mk", tg.triple, tg.layout)
	i64 := m.I(64)
	p8 := make([]Type, 8)
	for i := range p8 {
		p8[i] = i64
	}
	pnoneSig := FuncSig{
		Linkage: Internal,
		CC:      PreserveNoneCC,
		Ret:     i64,
		Params:  p8,
		FnAttrs: Attrs{}.Flag("nounwind").Flag("uwtable"),
	}
	op0 := m.AddFunction("op0$pnone", pnoneSig)
	op1 := m.AddFunction("op1$pnone", pnoneSig)
	extSig := FuncSig{
		Linkage:  External,
		DSOLocal: true,
		CC:       PreserveNoneCC,
		Ret:      i64,
		Params:   p8,
		FnAttrs:  Attrs{}.Flag("nounwind"),
	}
	extOp := m.Declare("ext$pnone", extSig)
	slow := m.Declare("slow_helper", FuncSig{
		Linkage: External,
		Ret:     i64,
		Params:  []Type{i64},
		FnAttrs: Attrs{}.Flag("nounwind"),
	})
	tab := m.AddGlobal("ops$pnone", GlobalAttrs{
		Linkage:     Private,
		Constant:    true,
		UnnamedAddr: true,
		Align:       8,
		Type:        m.Array(3, m.Ptr()),
		Init:        m.ConstArray(m.Ptr(), op0.Ref(), op1.Ref(), extOp.Ref()),
	})
	{
		b := op0.NewBlock("entry")
		done := op0.NewBlock("done")
		next := op0.NewBlock("next")
		b.CondBr(b.ICmp(IEQ, op0.Param(0), m.ConstInt(i64, 0)), done, next)
		sv := done.CallFunc(slow, []Value{op0.Param(1)})
		res := done.Bin(Add, sv, op0.Param(7), 0)
		done.Ret(&res)
		idx := next.Bin(And, op0.Param(0), m.ConstInt(i64, 1), 0)
		off := next.Bin(Mul, idx, m.ConstInt(i64, 8), NUW|NSW)
		slot := next.GEP8(m.GlobalRef(tab), off, GEPInbounds|GEPNUW)
		fp := next.Load(m.Ptr(), slot, 8)
		n0 := next.Bin(Sub, op0.Param(0), m.ConstInt(i64, 1), 0)
		v := next.MustTailCallCC(PreserveNoneCC, op0.Type(), fp,
			[]Value{n0, op0.Param(2), op0.Param(3), op0.Param(4), op0.Param(5), op0.Param(6), op0.Param(1), op0.Param(7)},
			nil, Attrs{}, Attrs{}.Flag("nounwind"))
		next.Ret(&v)
	}
	{
		b := op1.NewBlock("entry")
		n0 := b.Bin(Sub, op1.Param(0), m.ConstInt(i64, 1), 0)
		v := b.MustTailCallCC(PreserveNoneCC, op0.Type(), op0.Ref(),
			[]Value{n0, op1.Param(1), op1.Param(2), op1.Param(3), op1.Param(4), op1.Param(5), op1.Param(6), op1.Param(7)},
			nil, Attrs{}, Attrs{}.Flag("nounwind"))
		b.Ret(&v)
	}
	wrapSig := FuncSig{
		Linkage:  External,
		DSOLocal: true,
		Ret:      i64,
		Params:   p8,
		FnAttrs:  Attrs{}.Flag("nounwind").Flag("uwtable"),
	}
	wrap := m.AddFunction("op0", wrapSig)
	{
		b := wrap.NewBlock("entry")
		args := make([]Value, 8)
		for i := range args {
			args[i] = wrap.Param(i)
		}
		v := b.CallFunc(op0, args)
		b.Ret(&v)
	}
	big := m.Array(24, m.I(8))
	sretParams := append([]Type{m.Ptr()}, p8...)
	sretAttrs := make([]Attrs, len(sretParams))
	sretAttrs[0] = Attrs{}.Typed("sret", big).Flag("noalias").Flag("nonnull").Flag("writable").Int("align", 8).Int("dereferenceable", 24)
	sretSig := FuncSig{
		Linkage:    Internal,
		CC:         PreserveNoneCC,
		Ret:        m.Void(),
		Params:     sretParams,
		ParamAttrs: sretAttrs,
		FnAttrs:    Attrs{}.Flag("nounwind").Flag("uwtable"),
	}
	sh := m.AddFunction("sret_op$pnone", sretSig)
	{
		b := sh.NewBlock("entry")
		args := make([]Value, len(sretParams))
		for i := range args {
			args[i] = sh.Param(i)
		}
		b.MustTailCallCC(PreserveNoneCC, sh.Type(), sh.Ref(), args, sretAttrs, Attrs{}, Attrs{}.Flag("nounwind"))
		b.Ret(nil)
	}
	swrapSig := sretSig
	swrapSig.Linkage, swrapSig.DSOLocal, swrapSig.CC = External, true, CCC
	swrap := m.AddFunction("sret_op", swrapSig)
	{
		b := swrap.NewBlock("entry")
		args := make([]Value, len(sretParams))
		for i := range args {
			args[i] = swrap.Param(i)
		}
		b.CallCC(PreserveNoneCC, sh.Type(), sh.Ref(), args, sretAttrs, Attrs{}, Attrs{}.Flag("nounwind"))
		b.Ret(nil)
	}
	return m
}

func TestPreserveNoneCC(t *testing.T) {
	host := findHostClang(t)
	text := preserveNoneModule(smokeTriples[0]).String()
	for _, want := range []string{
		`define internal preserve_nonecc i64 @op0$pnone(`,
		`declare dso_local preserve_nonecc i64 @ext$pnone(`,
		`= musttail call preserve_nonecc i64 %`,
		`= musttail call preserve_nonecc i64 @op0$pnone(`,
		`= call preserve_nonecc i64 @op0$pnone(`,
		`musttail call preserve_nonecc void @sret_op$pnone(ptr sret([24 x i8])`,
		`call preserve_nonecc void @sret_op$pnone(ptr sret([24 x i8])`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("module lacks %q:\n%s", want, text)
		}
	}
	for _, tg := range smokeTriples {
		t.Run(tg.name, func(t *testing.T) {
			r := resolve(t, host, tg)
			if out, ok := compile(host, r, preserveNoneModule(r)); !ok {
				t.Error(out)
			}
		})
	}
	t.Run("mismatch-musttail-panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("musttail with mismatched calling convention did not panic")
			}
		}()
		m := NewModule("bad.mk", smokeTriples[0].triple, smokeTriples[0].layout)
		f := m.AddFunction("f", FuncSig{CC: PreserveNoneCC, Ret: m.Void()})
		g := m.AddFunction("g", FuncSig{Ret: m.Void()})
		b := f.NewBlock("entry")
		b.MustTailCall(g.Type(), g.Ref(), nil, nil, Attrs{}, Attrs{})
	})
	t.Run("mismatch-declare-panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Declare with mismatched calling convention did not panic")
			}
		}()
		m := NewModule("bad2.mk", smokeTriples[0].triple, smokeTriples[0].layout)
		m.Declare("f", FuncSig{Ret: m.Void()})
		m.Declare("f", FuncSig{CC: PreserveNoneCC, Ret: m.Void()})
	})
}
