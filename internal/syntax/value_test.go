// Why: if and match are values wherever an expression stands, and
// statements at the start of a statement; the parser must place each one
// right.
// What: tests of value and operand positions, the destructured loop
// variable and the assert habit with its proven fix.
// How: parses sources and checks which node holds each IfStmt or MatchStmt
// (valueParents), the for-pattern heads, and the fix's reparse.

package syntax

import (
	"fmt"
	"strings"
	"testing"

	"mk/internal/ast"
)

// valueParents lists, for each IfStmt or MatchStmt that stands in an Expr
// field, the type of the node that holds it.
func valueParents(f *ast.File) []string {
	var out []string
	var visit func(ast.Node)
	visit = func(n ast.Node) {
		ast.EachChild(n, func(c ast.Node) {
			switch c.(type) {
			case *ast.IfStmt, *ast.MatchStmt:
				if _, ok := n.(*ast.Block); !ok {
					if _, isIf := n.(*ast.IfStmt); !isIf || c != ast.Node(n.(*ast.IfStmt).Else) {
						out = append(out, fmt.Sprintf("%T<-%T", n, c))
					}
				}
			}
			visit(c)
		})
	}
	visit(f)
	return out
}

func TestValuePositions(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"bind", "x := if c { 1 } else { 2 }", "*ast.BindStmt<-*ast.IfStmt"},
		{"var bind", "var x := match c { .A { 1 } }", "*ast.BindStmt<-*ast.MatchStmt"},
		{"typed bind", "x: i32 = if c { 1 } else { 2 }", "*ast.BindStmt<-*ast.IfStmt"},
		{"discard", "_ = if c { 1 } else { 2 }", "*ast.DiscardStmt<-*ast.IfStmt"},
		{"assign", "p = if c { 1 } else { 2 }", "*ast.AssignStmt<-*ast.IfStmt"},
		{"compound", "p += match c { .A { 1 } }", "*ast.AssignStmt<-*ast.MatchStmt"},
		{"return", "return if c { 1 } else { 2 }", "*ast.ReturnStmt<-*ast.IfStmt"},
		{"argument", "F(1, if c { 1 } else { 2 })", "*ast.Arg<-*ast.IfStmt"},
		{"named argument", "F(a: match c { .A { 1 } })", "*ast.Arg<-*ast.MatchStmt"},
		{"record field", "r := (a: if c { 1 } else { 2 }, b: 3)", "*ast.RecField<-*ast.IfStmt"},
		{"array element", "xs := [1, if c { 1 } else { 2 }]", "*ast.ArrayLit<-*ast.IfStmt"},
		{"multi-line argument", "F(if c {\n        1\n    } else {\n        2\n    })", "*ast.Arg<-*ast.IfStmt"},
		{"if bind cond", "x := if y := o { y } else { 0 }", "*ast.BindStmt<-*ast.IfStmt"},
		{"else if", "x := if a { 1 } else if b { 2 } else { 3 }", "*ast.BindStmt<-*ast.IfStmt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := parse(t, "F() {\n    "+c.body+"\n}\n", Options{})
			if len(r.diags) != 0 {
				t.Fatalf("diagnostics:\n  %s", strings.Join(r.codes(), "\n  "))
			}
			got := strings.Join(valueParents(r.file), " ")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestOperandPositions: an if or match is a primary expression, so it may
// stand wherever an expression does, with suffixes and operators after its
// closing brace.
func TestOperandPositions(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"binary operand", "x := 1 + if c { 2 } else { 3 }", "*ast.BinaryExpr<-*ast.IfStmt"},
		{"operator after value", "x := if c { 1 } else { 2 } + 3", "*ast.BinaryExpr<-*ast.IfStmt"},
		{"call then operand", "x := F(x) + if c { 1 } else { 2 }", "*ast.BinaryExpr<-*ast.IfStmt"},
		{"suffix after value", "x := match x { .A { 1 } }.Len()", "*ast.FieldExpr<-*ast.MatchStmt"},
		{"parenthesized", "x := (if c { 1 } else { 2 }) * 2", "*ast.ParenExpr<-*ast.IfStmt"},
		{"unary", "x := -if c { 1 } else { 2 }", "*ast.UnaryExpr<-*ast.IfStmt"},
		{"index", "x := xs[if c { 0 } else { 1 }]", "*ast.IndexExpr<-*ast.IfStmt"},
		{"condition", "if if c { true } else { false } {\n    }", "*ast.IfStmt<-*ast.IfStmt"},
		{"scrutinee", "match match c { .A { 1 } } {\n    }", "*ast.MatchStmt<-*ast.MatchStmt"},
		{"repeat value", "xs := [if c { 1 } else { 2 }; 3]", "*ast.RepeatLit<-*ast.IfStmt"},
		{"match arm operand", "x := match c {\n        .A { 1 + if d { 2 } else { 3 } }\n    }", "*ast.BindStmt<-*ast.MatchStmt *ast.BinaryExpr<-*ast.IfStmt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := parse(t, "F() {\n    "+c.body+"\n}\n", Options{})
			if len(r.diags) != 0 {
				t.Fatalf("diagnostics:\n  %s", strings.Join(r.codes(), "\n  "))
			}
			got := strings.Join(valueParents(r.file), " ")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	// A const initializer is an expression too.
	r := parse(t, "const N = 1 + if c { 1 } else { 2 }\n", Options{})
	if len(r.diags) != 0 {
		t.Errorf("const: %v", r.codes())
	}
}

// TestStatementIfIsNoOperand: at the start of a statement an if is the
// statement, so nothing may follow its closing brace on the line.
func TestStatementIfIsNoOperand(t *testing.T) {
	for _, body := range []string{"if c { 1 } else { 2 }.F()", "match c { .A { 1 } } + 1"} {
		r := parse(t, "F() {\n    "+body+"\n}\n", Options{})
		if len(r.diags) == 0 || r.diags[0].Code.String() != "E0101" {
			t.Errorf("%q: want E0101, got %v", body, r.codes())
		}
	}
}

// TestNoE1218: if and match used as values or operands get no habit code.
func TestNoE1218(t *testing.T) {
	for _, src := range []string{"F() {\n    x := if c { 1 } else { 2 }\n}\n", "F() {\n    x := 1 + if c { 1 } else { 2 }\n}\n"} {
		if r := parse(t, src, Options{}); len(r.diags) != 0 {
			t.Errorf("%q: %v", src, r.codes())
		}
	}
}

func TestForPatternHeads(t *testing.T) {
	cases := []struct{ name, head, key, value, pat string }{
		{"pair", "(key, value) in m.Iter()", "", "", "key value"},
		{"renamed", "(a: x, b: y) in it", "", "", "a b"},
		{"index and pattern", "i, (a, b) in xs", "i", "", "a b"},
		{"plain", "x in xs", "x", "", ""},
		{"index", "i, x in xs", "i", "x", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := parse(t, "F() {\n    for "+c.head+" {\n    }\n}\n", Options{})
			if len(r.diags) != 0 {
				t.Fatalf("diagnostics: %v", r.codes())
			}
			var h *ast.InHead
			ast.Inspect(r.file, func(n ast.Node) bool {
				if x, ok := n.(*ast.InHead); ok {
					h = x
				}
				return true
			})
			if h == nil {
				t.Fatal("no InHead")
			}
			name := func(id *ast.Ident) string {
				if id == nil {
					return ""
				}
				return r.in.Str(id.Name)
			}
			if name(h.Key) != c.key || name(h.Value) != c.value {
				t.Errorf("key %q value %q", name(h.Key), name(h.Value))
			}
			var fs []string
			if h.Pat != nil {
				if h.Pat.Path != nil || h.Pat.Rest {
					t.Errorf("path or rest set")
				}
				for _, f := range h.Pat.Fields {
					fs = append(fs, r.in.Str(f.Name.Name))
				}
			}
			if got := strings.Join(fs, " "); got != c.pat {
				t.Errorf("pattern %q, want %q", got, c.pat)
			}
		})
	}
}

func TestForPatternRefused(t *testing.T) {
	cases := []struct{ name, head, msg string }{
		{"path", "Point(a, b) in ps", "no type name"},
		{"rest", "(a, ..) in ps", "names every field"},
		{"range", "(a, b) in 0..n", "range counts integers"},
		{"fields", "(a, b) in fields(x)", "fields loop"},
		{"ref", "(a, b) in &xs", "copies each element"},
		{"mut", "(a, b) in mut xs", "copies each element"},
		{"not last", "(a, b), x in xs", "last name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := parse(t, "F() {\n    for "+c.head+" {\n    }\n}\n", Options{})
			if len(r.diags) == 0 || r.diags[0].Code.String() != "E0101" || !strings.Contains(r.diags[0].Message, c.msg) {
				t.Errorf("got %v, want E0101 with %q", r.codes(), c.msg)
			}
		})
	}
}

func TestAssertHabit(t *testing.T) {
	runHabits(t, []hcase{
		{name: "assert e", src: "F() {\n    assert x == 1\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert(x == 1)"},
		{name: "assert(e)", src: "F() {\n    assert(x == 1)\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert(x == 1)"},
		{name: "assert!(e)", src: "F() {\n    assert!(x == 1)\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert(x == 1)"},
		{name: "local named assert", src: "F() {\n    assert := 1\n    assert = 2\n}\n", count: -1},
		// The fix keeps the meaning. `assert !(c)` is not the macro
		// form; a condition that is more than the parenthesized one is
		// wrapped whole.
		{name: "assert !(c)", src: "F(c: bool) {\n    assert !(c)\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert(!(c))"},
		{name: "assert !c", src: "F(c: bool) {\n    assert !c\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert(!c)"},
		{name: "assert (a) && b", src: "F(a: bool, b: bool) {\n    assert (a) && b\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert((a) && b)"},
		{name: "assert!(a) && b", src: "F(a: bool, b: bool) {\n    assert!(a) && b\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert((a) && b)"},
		{name: "assert (e)", src: "F() {\n    assert (x == 1)\n}\n", code: "E1223", habits: "[E1223]", target: "    Assert(x == 1)"},
	})
}

// TestAssertFixKeepsMeaning: after the proven fix, the condition of
// `assert !(c)` is still a negation, and `assert (a) && b` still asserts the
// whole conjunction.
func TestAssertFixKeepsMeaning(t *testing.T) {
	for _, c := range []struct {
		src string
		op  ast.TokenKind
	}{
		{"F(c: bool) {\n    assert !(c)\n}\n", ast.TokBang},
		{"F(c: bool) {\n    assert !c\n}\n", ast.TokBang},
		{"F(a: bool, b: bool) {\n    assert (a) && b\n}\n", ast.TokAmpAmp},
		{"F(a: bool, b: bool) {\n    assert!(a) && b\n}\n", ast.TokAmpAmp},
	} {
		r := parse(t, c.src, Options{})
		if len(r.diags) != 1 {
			t.Fatalf("%q: %d diagnostics", c.src, len(r.diags))
		}
		fixed := applyFix(r.src.Text, r.diags[0])
		again := parse(t, fixed, Options{})
		if len(again.diags) != 0 {
			t.Fatalf("%q: fixed text %q does not parse clean", c.src, fixed)
		}
		var arg ast.Expr
		ast.Inspect(again.file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && len(call.Args) == 1 {
				arg = call.Args[0].Value
			}
			return true
		})
		for {
			p, ok := arg.(*ast.ParenExpr)
			if !ok {
				break
			}
			arg = p.X
		}
		switch x := arg.(type) {
		case *ast.UnaryExpr:
			if x.Op != c.op {
				t.Errorf("%q fixed to %q: argument op %v, want %v", c.src, fixed, x.Op, c.op)
			}
		case *ast.BinaryExpr:
			if x.Op != c.op {
				t.Errorf("%q fixed to %q: argument op %v, want %v", c.src, fixed, x.Op, c.op)
			}
		default:
			t.Errorf("%q fixed to %q: argument %T", c.src, fixed, arg)
		}
	}
}
