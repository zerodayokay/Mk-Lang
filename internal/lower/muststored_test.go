// Why: check accepts try, else, match and Must on any E!T value, a stored
// one as well as a call's, so lower must take each of them without an
// internal error, and Must must panic with the error.
// What: TestStoredUnionForms, TestMustStoredPanics,
// TestMustDroppedLinearPayload and checkLowerAll.
// How: a table of operation x operand form, each case one module: the
// handling operations must check and lower clean, and the operations check
// refuses must get a diagnostic, never E0000 or a lower ICE. Must's MIR is
// read for one switch on the value's discriminant, a panic with its error,
// and a payload that is not plain moved into a T temporary.

package lower

import (
	"fmt"
	"strings"
	"testing"

	"mk/internal/check"
	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// TestStoredUnionForms: every E!T operand form (a typed local, a
// parameter, a field, an array, slice or record element, a deref, an if
// or match value, a global, a const, the E!T a `?(E!T).Must()` gives, a
// value a fn literal captures) under every error operation: Must, try,
// else (value and named), match, and Must and else in a defer lower;
// return and `_ =` of the value are check's E0802.
func TestStoredUnionForms(t *testing.T) {
	const pre = `error Bad

Get(n: int): Bad!int {
	if n < 0 {
		fail Bad
	}
	return n
}

struct H {
	r: Bad!int
}

var G: Bad!int = Get(11)

const C: Bad!int = Get(12)

Apply(const f: fn(): Bad!int): Bad!int {
	return try f()
}

F(c: bool, r: Bad!int, h: H, a: [2]H, sl: []H, t: (a: Bad!int, b: int), o: ?(Bad!int), p: &(Bad!int)): Bad!int {
	var v: Bad!int = Get(4)
	var w: Bad!int = Get(5)
	_ = (c, r, h, a, sl, t, o, p, v, w)
`
	forms := []struct{ name, expr string }{
		{"local", "v"},
		{"param", "r"},
		{"field", "h.r"},
		{"array", "a[1].r"},
		{"slice", "sl[0].r"},
		{"record", "t.a"},
		{"deref", "(*p)"},
		{"if", "(if c { v } else { w })"},
		{"match", "(match c {\n\t\ttrue { v }\n\t\t_ { w }\n\t})"},
		{"global", "G"},
		{"const", "C"},
		{"optmust", "o.Must()"},
		{"capture", "v"},
	}
	ops := []struct {
		name  string
		body  string
		lower bool
	}{
		{"must", "x = {E}.Must()", true},
		{"try", "x = try {E}", true},
		{"else", "x = {E} else { 0 }", true},
		{"elsenamed", "x = {E} else e {\n\t\t_ = e\n\t\treturn 0\n\t}", true},
		{"match", "match {E} {\n\tok(n) {\n\t\tx = n\n\t}\n\terr(_) {\n\t}\n\t}", true},
		{"matchnamed", "match {E} {\n\tok(n) {\n\t\tx = n\n\t}\n\terr(e) {\n\t\t_ = e\n\t}\n\t}", true},
		{"defermust", "defer {\n\t\t_ = {E}.Must()\n\t}", true},
		{"deferelse", "defer {\n\t\t_ = {E} else { 0 }\n\t}", true},
		{"return", "return {E}", false},
		{"drop", "_ = {E}", false},
	}
	for _, f := range forms {
		for _, op := range ops {
			t.Run(op.name+"/"+f.name, func(t *testing.T) {
				body := "\tvar x := 0\n\t" + strings.ReplaceAll(op.body, "{E}", f.expr) + "\n\treturn x\n"
				src := pre
				if f.name == "capture" {
					src += "\treturn try Apply(fn(): Bad!int {\n" + body + "\t})\n}\n"
				} else {
					src += body + "}\n"
				}
				codes, ice := checkLowerAll(t, src)
				if ice != "" {
					t.Fatalf("ICE: %s\n%s", ice, src)
				}
				if op.lower {
					if len(codes) > 0 {
						t.Fatalf("want clean, got %v\n%s", codes, src)
					}
					return
				}
				if len(codes) == 0 {
					t.Fatalf("want check's diagnostic, got none\n%s", src)
				}
				for _, c := range codes {
					if c == diag.E0000 {
						t.Fatalf("got E0000\n%s", src)
					}
				}
			})
		}
	}
}

// TestMustStoredPanics: Must on a stored E!T switches on the value's
// discriminant and panics with its error, as Must on a fallible call does.
func TestMustStoredPanics(t *testing.T) {
	l := lowerSrc(t, `error Bad

struct H {
	r: Bad!int
}

linear struct Ticket {
	seat: int
}

Ticket.Redeem(self): int {
	Ticket(seat) := self
	return seat
}

Field(h: H): int {
	return h.r.Must()
}

Ref(p: &(Bad!int)): int {
	return p.Must()
}

Linear(t: Bad!Ticket): int {
	return t.Must().Redeem()
}
`)
	for _, fn := range []string{"Field", "Ref", "Linear"} {
		got := l.print(fn)
		wantIn(t, got, "Discriminant(", "panic "+types.PanicMust.String()+" ")
		if strings.Contains(got, " err must ") || strings.Count(got, "Discriminant(") != 1 {
			t.Errorf("%s: want one switch on the value's discriminant and no call edge:\n%s", fn, got)
		}
	}
}

// TestMustDroppedLinearPayload: Must on a stored E!T moves a payload that
// is not plain out of the E!T temporary into a T temporary, as else's
// statement form and try do, so a dropped `r.Must()` is a Res for borrow
// (E0402 with Res's release facts), as a call's destination is, not an
// E!Res with none.
func TestMustDroppedLinearPayload(t *testing.T) {
	l := lowerSrc(t, resSrc+`Stmt(r: E!Res) {
	r.Must()
}

Drop(r: E!Res) {
	_ = r.Must()
}
`)
	for _, k := range []string{"Stmt", "Drop"} {
		s := l.print(k)
		d := mir.NoLocal
		for i, lc := range l.body(k).Locals {
			if lc.Kind == mir.LocalTemp && l.w.tab.TypeString(lc.Type, nil) == "Res" {
				d = mir.LocalID(i)
				break
			}
		}
		if d == mir.NoLocal || !strings.Contains(s, "_"+itoa(int(d))+" = move (_2 as ok).0\n") {
			t.Errorf("%s: the ok payload is not moved into a Res temporary:\n%s", k, s)
		}
	}
}

// checkLowerAll checks and lowers every unit of src; it returns the
// diagnostics' codes and the first ICE's text.
func checkLowerAll(t *testing.T, src string) ([]diag.Code, string) {
	t.Helper()
	w := newWorld(t, true)
	b := w.add("m", opt{}, src)
	var codes []diag.Code
	for _, d := range b.diags() {
		codes = append(codes, d.Code)
	}
	if len(codes) > 0 {
		return codes, ""
	}
	cfg := w.lowerCfg()
	for _, id := range b.m.Units() {
		it := w.items.Get(id)
		var body *check.Body
		if ie := source.Guard(it.Key.String(), it.Span, func() { body = w.c.Check(b.m, id) }); ie != nil {
			return codes, fmt.Sprint(ie)
		}
		for _, d := range body.Diags {
			codes = append(codes, d.Code)
		}
		if len(body.Diags) > 0 {
			continue
		}
		if ie := source.Guard(it.Key.String(), it.Span, func() { Lower(cfg, FromCheck(cfg, w.c, body)) }); ie != nil {
			return codes, fmt.Sprint(ie)
		}
	}
	return codes, ""
}
