// Why: stage 7, the borrow check, runs on every function whose check,
// lower and Validate were clean, std's included, and internal/borrow
// imports neither check nor lower; the driver builds its view of them.
//
// What: borrowEnv (the borrow.Env of one body) and flowCheck (stage 7 of
// one lowered body).
//
// How: the Env is built from check's and lower's own data, with no
// fallback: a type Facts cannot answer is an ICE. The memory rules borrow
// enforces are described in doc/memory.md.

package driver

import (
	"mk/internal/borrow"
	"mk/internal/lower"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// borrowEnv is the Env of one body: the session's tables, the body's
// signature and facts from lower's Input, and the declared signatures of
// callees and interface methods. It is the Env of internal/borrow's probe
// (borrowEnv in its testdata), except that a type Facts cannot answer is an
// ICE, not a fallback: borrow asks Facts only of the body's own types.
func (w *world) borrowEnv(in lower.Input, it *types.Item) *borrow.Env {
	s := w.sem5
	return &borrow.Env{
		Types:  w.tab,
		Known:  &s.known,
		Sig:    in.Sig(),
		Module: w.items.Module(it.Module),
		Callee: func(id types.ItemID) *types.Signature {
			if id == types.NoItem {
				return nil
			}
			if d, ok := w.items.Get(id).Decl.(*types.FuncDecl); ok {
				return &d.Sig
			}
			return nil
		},
		Method: func(iface types.ItemID, m uint32) *types.Signature {
			if d, ok := w.items.Get(iface).Decl.(*types.InterfaceDecl); ok && int(m) < len(d.Methods) {
				return &d.Methods[m]
			}
			return nil
		},
		Facts: func(t types.TypeID) types.Facts {
			f, ok := in.Facts(t)
			if !ok {
				source.ICEf(it.Span, "borrow asked the facts of %s, which check cannot answer", w.tab.TypeString(t, nil))
			}
			return f
		},
		File: func(id source.FileID) *source.File { return w.sm.File(id) },
		// E0501's `from p.f` fix: the clause check recorded at stage 5 for a
		// callee whose body shows it.
		FieldClause: func(id types.ItemID) (borrow.FieldClause, bool) {
			c, at, exported, ok := s.chk.FieldClause(id)
			return borrow.FieldClause{Clause: c, At: at, Exported: exported}, ok
		},
	}
}

// flowCheck is stage 7 of one lowered body, on the caller's goroutine and
// under its source.Guard (an ICE in borrow is the body's E0000). It returns
// the body's diagnostics and, when there are none, the guard decisions eval
// reads (eval.Body.OnerrRuns; nil when the body has no TermOnerrGuard). The
// caller records Result.Reads in the body's read set.
func (w *world) flowCheck(mb *mir.Body, in lower.Input, it *types.Item) (*borrow.Result, []bool) {
	res := borrow.Check(mb, w.borrowEnv(in, it))
	if len(res.Diags) > 0 || len(res.Facts.OnerrRuns) == 0 {
		return res, nil
	}
	return res, res.Facts.GuardDecisions(len(mb.Blocks))
}
