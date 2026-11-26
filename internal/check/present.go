// Why: dl.Present(io, f) answers for one extern function, decided by the
// compiler (true for an eagerly bound one, a run-time resolution for a
// lazily bound one), so its argument must name that function directly.
//
// What: presentItem (std.dl.Present and std.sys.LazyPresent, std's own
// spelling for std.sys, which std.dl imports), presentFnArg and
// presentArgRule (E0705 reason present-argument).
//
// How: the function argument is the call's last; while the call is
// checked it is presentArg, which unsafeValue exempts (the intrinsic never
// calls it, so naming it is not a C call). After the arguments are typed
// its Use must be a bodiless extern function item.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/types"
)

// presentItem reports whether it is std.dl.Present or std.sys.LazyPresent.
func (u *unit) presentItem(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	item := u.c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner != types.NoItem || item.OwnerPrim != types.NoType {
		return false
	}
	mod := u.c.cfg.Items.Module(item.Module)
	if mod == nil || !mod.Std {
		return false
	}
	name := u.c.cfg.Interner.Str(item.Name)
	return mod.Path == "std.dl" && name == "Present" || mod.Path == "std.sys" && name == "LazyPresent"
}

// presentValue is E0705 reason present-argument for Present named as a
// value: only a call names the function it answers for.
func (u *unit) presentValue(at ast.Node) {
	u.err(diag.E0705, at.Span(), "dl.Present is only called, never named as a value: each call answers for the extern function its argument names").
		Text("reason", "present-argument")
}

// presentFnArg is the function argument of a Present call: its last, or
// nil when it has none (argShape reported the count).
func (u *unit) presentFnArg(e *ast.CallExpr) ast.Expr {
	if len(e.Args) == 0 || e.Args[len(e.Args)-1] == nil {
		return nil
	}
	return e.Args[len(e.Args)-1].Value
}

// presentArgRule is E0705 reason present-argument unless the function
// argument names a bodiless extern function directly: by its name, or
// qualified by its module.
func (u *unit) presentArgRule(e *ast.CallExpr) {
	a := u.presentFnArg(e)
	if a == nil {
		return
	}
	switch a.(type) {
	case *ast.Ident, *ast.FieldExpr:
		use := u.useOf(a.ID())
		if use.Kind == UseItem && use.Item != types.NoItem {
			if d, ok := u.c.cfg.Items.Get(use.Item).Decl.(*types.FuncDecl); ok && d.Sig.Extern && d.Bodiless {
				return
			}
		}
	}
	if u.b.TypeOf(a.ID()) == types.Poison {
		return
	}
	u.err(diag.E0705, a.Span(), "dl.Present's argument names a bodiless `extern` function directly, such as `dl.Present(sys.io, GetThing)`: the compiler decides how that function binds, so a local, a field, a call or a function with a body cannot be its argument").
		Text("reason", "present-argument")
}
