// Why: an if with an else, or a match, yields a value wherever an
// expression stands; a for loop may destructure its variable; and
// `assert e` is a common habit. These need parser support.
//
// What: parseValue and operandConstruct for value constructs,
// loopPatAhead, parseLoopPat and loopPatSource for destructured loop
// variables, and assertHabit (E1223).
//
// How: an if or match is a primary expression (operandConstruct), so
// suffixes and operators may follow its closing brace. At the start of a
// statement it stays a statement (parseStmt reads it first). A loop
// pattern goes through the RecordPat cover.

package syntax

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
)

// parseValue reads Value = Expr at a value position: the right side of a
// binding or an assignment, return's operand, a call argument, a record
// field's value and an array element. It is the one place a brace literal
// habit may be read (braceLitAhead); nowhere else does a '{' after an
// operand fail.
func (p *parser) parseValue() ast.Expr {
	saved := p.litOK
	p.litOK = true
	x := p.parseExpr()
	p.litOK = saved
	return x
}

// operandConstruct reads an if or match as a primary expression.
func (p *parser) operandConstruct() ast.Expr {
	if p.at(ast.TokIf) {
		return p.parseIf().(*ast.IfStmt)
	}
	return p.parseMatch().(*ast.MatchStmt)
}

// loopPatAhead is true at a destructured loop variable: "(" … ")" "in", or a
// path head Name "(" … ")" "in" (refused by parseLoopPat).
func (p *parser) loopPatAhead(i int) bool {
	if p.peekKind(i) == ast.TokIdent && p.peekKind(i+1) == ast.TokLParen {
		i++
	}
	if p.peekKind(i) != ast.TokLParen {
		return false
	}
	depth := 0
	for ; ; i++ {
		switch p.peekKind(i) {
		case ast.TokLParen:
			depth++
		case ast.TokRParen:
			depth--
			if depth == 0 {
				return p.peekKind(i+1) == ast.TokIn || p.peekKind(i+1) == ast.TokComma && i > 1
			}
		case ast.TokEOF, ast.TokLBrace, ast.TokTerm:
			return false
		}
	}
}

// parseLoopPat reads a destructured loop variable through the RecordPat
// cover, refusing a type name and a `..`.
func (p *parser) parseLoopPat() *ast.RecordPat {
	start := p.curStart()
	savedMarks := p.coverMarks
	p.coverMarks = nil
	p.coverOK = true
	x := p.parseOperand(true)
	x = p.parsePostfix(x, start, true)
	p.coverOK = false
	marks := p.coverMarks
	p.coverMarks = savedMarks
	rp := p.recordPatOf(x)
	if rp.Path != nil {
		p.syntaxErr(rp.Path.Sp, "a loop variable destructures a record; no type name")
	}
	for _, m := range marks {
		if strings.HasPrefix(m.msg, "`..`") {
			p.syntaxErr(m.span, "a loop pattern names every field it binds")
		}
	}
	return rp
}

// loopPatSource refuses a destructured loop variable over a source that is
// not iterated by copy.
func (p *parser) loopPatSource(n *ast.InHead) {
	if n.Pat == nil {
		return
	}
	switch s := n.Source.(type) {
	case *ast.RangeSource:
		p.syntaxErr(n.Pat.Sp, "a range counts integers; it has no record to destructure")
	case *ast.FieldsSource:
		// The fields loop's own message (a missing Value) is reported first.
		p.syntaxErr(n.Pat.Sp, "a fields loop names the field and its value: `for f, v in fields(x)`")
	case *ast.RefSource:
		if s.Mode != ast.ByCopy {
			p.syntaxErr(n.Pat.Sp, "a destructured loop variable copies each element; write `for x in &xs` and read `x.a`")
		}
	}
}

// assertHabit records E1223 at `assert e`, `assert(e)` or `assert!(e)` as a
// statement: Assert(e) replaces it. A local named assert is not
// matched, since `:=`, `=` or `:` follows it.
func (p *parser) assertHabit() ast.Stmt {
	k1 := p.peekKind(1)
	// assert!(e) is the macro form only when `!` touches the word and `(`
	// touches the `!` (L7R-6): `assert !(c)` asserts not c, `Assert(!(c))`.
	w0, b1, l2 := p.tok(), p.peek(1), p.peek(2)
	bang := k1 == ast.TokBang && l2.Kind == ast.TokLParen && b1.Start == w0.End() && l2.Start == b1.End()
	paren := k1 == ast.TokLParen
	if !bang && !paren && (!startsOperand(k1) || binPrec(k1) != 0 || k1 == ast.TokLBracket || p.lineEndAt(1)) {
		return nil
	}
	if paren && p.parenThenAssign(1) {
		return nil
	}
	w := p.tok()
	at := w.Span(p.file)
	start := p.curStart()
	p.next()
	if bang {
		p.next() // the '!'
	}
	open := p.curStart()
	x := p.parseExpr()
	// The proven fix keeps the meaning: `assert(e)` and `assert!(e)`
	// whose parsed condition is exactly the parenthesized e become
	// Assert(e); anything else, `assert e`, `assert (a) && b` or
	// `assert!(a) && b`, is wrapped whole: Assert(e).
	sp := x.Span()
	var edits []diag.Edit
	if _, ok := x.(*ast.ParenExpr); ok && (bang || paren) && sp.Start == open {
		edits = []diag.Edit{p.edit(w.Start, open, "Assert")}
	} else {
		edits = []diag.Edit{p.edit(w.Start, sp.Start, "Assert("), p.edit(sp.End, sp.End, ")")}
	}
	call := &ast.CallExpr{}
	p.beginAt(&call.Base, start)
	fn := &ast.Ident{Name: p.in.Intern("Assert")}
	p.adopt(fn, &fn.Base, at)
	call.Fun = fn
	arg := &ast.Arg{Value: x}
	p.adopt(arg, &arg.Base, x.Span())
	call.Args = []*ast.Arg{arg}
	p.finish(call, &call.Base)
	n := &ast.ExprStmt{X: call}
	p.beginAt(&n.Base, start)
	p.finish(n, &n.Base)
	p.hit(habitHit{code: diag.E1223, at: at, msg: "Mk asserts with the prelude's `Assert(c)`", edits: edits})
	return n
}
