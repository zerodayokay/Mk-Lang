// Why: a binding that writes an E!T type keeps the error to be handled
// later; the checker must refuse one overwritten, borrowed mut or ending
// its scope on any path while that error may still be unread.
// What: TestUnhandledUnion.
// How: hand-built bodies over a local `c: Bad!i32` copied from a parameter:
// each case lists the E0802s expected at their span IDs, the accepted
// shapes none.

package borrow

import (
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

func TestUnhandledUnion(t *testing.T) {
	type built struct {
		m    *mb
		want []any
	}
	setup := func(t *testing.T) (*kit, *mb, types.TypeID, mir.LocalID) {
		k := newKit(t)
		u := k.tab.ErrorUnion(k.errorItem("Bad"), types.I32)
		m := k.body(k.sig("F", []types.Param{k.param("a", u), k.param("k", types.Bool)}, types.Void, types.EmptyErrors))
		c := m.user("c", u, true)
		return k, m, u, c
	}
	read := func(m *mb, u types.TypeID, c mir.LocalID) {
		t := m.temp(u)
		m.set(t, use(cp(m.local(c))))
		m.end(t)
	}
	for _, tc := range []struct {
		name  string
		build func(t *testing.T) built
	}{
		{"overwritten", func(t *testing.T) built {
			_, m, u, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			at := m.set(c, use(cp(m.local(1))))
			read(m, u, c)
			m.end(c)
			m.ret(mir.ExitNormal)
			return built{m, []any{diag.E0802, at}}
		}},
		{"read, then assigned", func(t *testing.T) built {
			_, m, u, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			read(m, u, c)
			m.set(c, use(cp(m.local(1))))
			read(m, u, c)
			m.end(c)
			m.ret(mir.ExitNormal)
			return built{m, nil}
		}},
		{"read on one path only", func(t *testing.T) built {
			_, m, u, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			yes, no, join := m.block(), m.block(), m.block()
			m.branch(cp(m.local(2)), yes, no)
			m.at(yes)
			read(m, u, c)
			m.jump(join)
			m.at(no)
			m.jump(join)
			m.at(join)
			m.end(c)
			m.ret(mir.ExitNormal)
			return built{m, []any{diag.E0802, m.b.Locals[c].Span}}
		}},
		{"read on both paths", func(t *testing.T) built {
			_, m, u, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			yes, no, join := m.block(), m.block(), m.block()
			m.branch(cp(m.local(2)), yes, no)
			m.at(yes)
			read(m, u, c)
			m.jump(join)
			m.at(no)
			read(m, u, c)
			m.jump(join)
			m.at(join)
			m.end(c)
			m.ret(mir.ExitNormal)
			return built{m, nil}
		}},
		{"an unused shared borrow leaves it unread", func(t *testing.T) built {
			k, m, u, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			r := m.temp(k.tab.Ref(u))
			m.set(r, ref(m.local(c)))
			m.end(r, c)
			m.ret(mir.ExitNormal)
			return built{m, []any{diag.E0802, m.b.Locals[c].Span}}
		}},
		{"a mut borrow of an unread value", func(t *testing.T) built {
			k, m, u, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			r := m.temp(k.tab.Mut(u))
			at := m.set(r, mutRef(m.local(c)))
			m.end(r)
			read(m, u, c)
			m.end(c)
			m.ret(mir.ExitNormal)
			return built{m, []any{diag.E0802, at}}
		}},
		{"a narrowed copy is its binding", func(t *testing.T) built {
			_, m, u, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			n := m.user("c", u, true)
			m.set(n, mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastNarrow, A: cp(m.local(c)), Type: u})
			read(m, u, n)
			m.end(n)
			m.end(c)
			m.ret(mir.ExitNormal)
			return built{m, nil}
		}},
		{"a narrowed copy never read", func(t *testing.T) built {
			_, m, _, c := setup(t)
			m.set(c, use(cp(m.local(1))))
			n := m.user("c", m.b.Locals[c].Type, true)
			m.set(n, mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastNarrow, A: cp(m.local(c)), Type: m.b.Locals[c].Type})
			m.end(n)
			m.end(c)
			m.ret(mir.ExitNormal)
			return built{m, []any{diag.E0802, m.b.Locals[c].Span}}
		}},
		{"a temporary is not tracked", func(t *testing.T) built {
			_, m, u, _ := setup(t)
			tmp := m.temp(u)
			m.set(tmp, use(cp(m.local(1))))
			m.end(tmp)
			m.ret(mir.ExitNormal)
			return built{m, nil}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.build(t)
			r := b.m.check(t)
			b.m.expect(t, r, b.want...)
		})
	}
}
