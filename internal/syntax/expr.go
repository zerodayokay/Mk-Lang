// Why: expressions are the largest part of the grammar, and their parsing
// carries the precedence ladder, the record-literal and pattern covers and
// the depth limit that keeps deep input from overflowing the Go stack.
//
// What: the expression productions (Expr, Fallback, Or, And, Compare, Add,
// Mul, Unary, Postfix, Suffix, Args, Arg, Primary, RecordLit, RecField,
// ArrayLit, FnLit, quote): parseExpr, parseBin, parseUnary, parseOperand,
// parsePostfix, parseCall, parseParenOrRecord, parseFnLit and parseQuote.
//
// How: binary operators by precedence climbing over binPrec; postfix
// suffixes in a loop. A bare name in a record literal is short for
// `name: name`, and a parenthesized lone name is a record literal. In cover
// mode an argument list may hold pattern-only forms, noted as coverMarks and
// reported unless ":=" follows.

package syntax

import (
	"slices"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
)

// rangeMsg is the E0101 for ".." outside a for head or a slice.
const rangeMsg = "ranges exist only in `for` headers and slices"

// nodeless runs f one counted tree level deeper for a parse path that
// recurses without making a node (a run of unary '+', 'await' or lifetimes
// before an operand), so the depth limit bounds its Go stack like any other
// recursion.
func nodeless[T any](p *parser, f func() T) T {
	p.open++
	if p.open > p.maxDepth {
		p.depthError()
	}
	x := f()
	p.open--
	return x
}

// openerOf is the opener a closer matches.
func openerOf(k ast.TokenKind) ast.TokenKind {
	switch k {
	case ast.TokRBracket:
		return ast.TokLBracket
	case ast.TokRBrace:
		return ast.TokLBrace
	}
	return ast.TokLParen
}

// popTo pops stack down through its innermost open, or leaves it when open
// is not on it (the lexer's rule for a closer).
func popTo(stack []ast.TokenKind, open ast.TokenKind) []ast.TokenKind {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == open {
			return stack[:i]
		}
	}
	return stack
}

// Binding powers, tightest last.
const (
	precOr      = 2
	precAnd     = 3
	precCompare = 4
	precAdd     = 5
	precMul     = 6
)

func binPrec(k ast.TokenKind) int {
	switch k {
	case ast.TokPipePipe:
		return precOr
	case ast.TokAmpAmp:
		return precAnd
	case ast.TokEqEq, ast.TokNotEq, ast.TokLt, ast.TokLtEq, ast.TokGt, ast.TokGtEq:
		return precCompare
	case ast.TokPlus, ast.TokMinus, ast.TokPlusWrap, ast.TokMinusWrap, ast.TokPipe, ast.TokCaret:
		return precAdd
	case ast.TokStar, ast.TokSlash, ast.TokPercent, ast.TokStarWrap, ast.TokShl, ast.TokShr, ast.TokAmp:
		return precMul
	}
	return 0
}

// parseExpr reads Expr = Fallback.
func (p *parser) parseExpr() ast.Expr {
	if p.tooDeep {
		return p.badExprFrom(p.p)
	}
	start := p.curStart()
	s0 := p.p
	x := p.parseBin(precOr, start)
	if p.at(ast.TokQuestion) && p.ternaryAhead() {
		return p.ternaryHabit(s0, start)
	}
	if p.at(ast.TokElse) && !(p.softTerm && p.lineEnd()) {
		return p.parseElse(x, start)
	}
	return x
}

// parseElse reads "else" [ Name ] Block after x.
func (p *parser) parseElse(x ast.Expr, start uint32) ast.Expr {
	n := &ast.ElseExpr{X: x}
	p.beginAt(&n.Base, start)
	p.next()
	if p.at(ast.TokIdent) && p.peekKind(1) == ast.TokLBrace {
		n.Name = p.parseDeclName(declLocal)
	}
	n.Handler = p.parseBlockOrMissing()
	p.finish(n, &n.Base)
	if p.at(ast.TokElse) {
		p.errHere("one `else` per expression")
		s0 := p.p
		p.next()
		if p.at(ast.TokIdent) {
			p.next()
		}
		if p.at(ast.TokLBrace) {
			p.parseBlock()
		}
		_ = s0
	}
	return n
}

// parseBin is the Pratt loop over the binary ladder.
func (p *parser) parseBin(min int, start uint32) ast.Expr {
	return p.parseBinFrom(p.parseUnary(), min, start)
}

// parseBinFrom continues the Pratt loop from an operand already read.
func (p *parser) parseBinFrom(left ast.Expr, min int, start uint32) ast.Expr {
	for !p.tooDeep {
		if p.softTerm && p.lineEnd() {
			break
		}
		op := p.kind()
		prec := binPrec(op)
		if prec == 0 || prec < min {
			break
		}
		if p.adjacentDouble(op) {
			if p.postfixDouble(op) {
				break
			}
		}
		var b ast.Base
		p.beginAt(&b, start)
		p.next()
		right := p.parseBin(prec+1, p.curStart())
		n := &ast.BinaryExpr{Base: b, X: left, Op: op, Y: right}
		p.finish(n, &n.Base)
		left = n
		if prec == precCompare && binPrec(p.kind()) == precCompare {
			p.errHere("comparisons do not chain; use `&&`")
		}
	}
	return left
}

// adjacentDouble is true when the current + - or * is directly followed by
// the same token (x++, x--, a ** b).
func (p *parser) adjacentDouble(op ast.TokenKind) bool {
	if op != ast.TokPlus && op != ast.TokMinus && op != ast.TokStar {
		return false
	}
	t, u := p.tok(), p.peek(1)
	return u.Kind == op && u.Start == t.End()
}

// postfixDouble handles x++ and x-- (E0101, line-based recovery) and a ** b
// (E0101, recovered as *). It returns true when the expression stops.
func (p *parser) postfixDouble(op ast.TokenKind) bool {
	sp := p.span(p.curStart(), p.peek(1).End())
	switch op {
	case ast.TokStar:
		p.syntaxErr(sp, "Mk has no power operator")
		p.next()
		return false
	case ast.TokPlus:
		p.syntaxErr(sp, "Mk has no `++`: write `x += 1`", "x += 1")
	default:
		p.syntaxErr(sp, "Mk has no `--`: write `x -= 1`", "x -= 1")
	}
	p.next()
	p.next()
	if p.lineEnd() {
		p.softTerm = true
	}
	return true
}

// parseUnary reads Unary = UnaryOp Unary | Postfix.
func (p *parser) parseUnary() ast.Expr {
	if p.tooDeep {
		return p.badExprFrom(p.p)
	}
	cover := p.coverOK
	p.coverOK = false
	start := p.curStart()
	switch op := p.kind(); op {
	case ast.TokMinus, ast.TokBang, ast.TokTilde, ast.TokAmp, ast.TokMut, ast.TokStar, ast.TokTry:
		if op == ast.TokMinus && p.peekKind(1) == ast.TokMinus && p.peek(1).Start == p.tok().End() {
			p.syntaxErr(p.span(start, p.peek(1).End()), "Mk has no `--`; `- -x` with a space negates twice")
			p.next()
		}
		n := &ast.UnaryExpr{Op: op}
		p.begin(&n.Base)
		p.next()
		if op == ast.TokAmp && p.at(ast.TokMut) {
			p.ampMutHabit()
			n.Op = ast.TokMut
			p.next()
		}
		n.X = p.parseUnary()
		p.finish(n, &n.Base)
		return n
	case ast.TokAmpAmp:
		n := &ast.UnaryExpr{Op: ast.TokAmp}
		p.begin(&n.Base)
		inner := &ast.UnaryExpr{Op: ast.TokAmp}
		p.beginAt(&inner.Base, start+1)
		p.next()
		inner.X = p.parseUnary()
		p.finish(inner, &inner.Base)
		n.X = inner
		p.finish(n, &n.Base)
		return n
	case ast.TokPlus:
		// A run of '+' is read by this loop, not by recursion, and the operand
		// after it one counted level deeper: no node is made here, so without
		// the count a long run would grow the Go stack past the depth limit.
		for p.at(ast.TokPlus) {
			if p.peekKind(1) == ast.TokPlus && p.peek(1).Start == p.tok().End() {
				p.syntaxErr(p.span(p.curStart(), p.peek(1).End()), "Mk has no `++`; write `x += 1`")
				p.next()
			} else {
				p.errHere("Mk has no unary plus")
			}
			p.next()
		}
		return nodeless(p, p.parseUnary)
	case ast.TokCaret:
		p.errHere("write `~x` for bitwise not")
		n := &ast.UnaryExpr{Op: ast.TokTilde}
		p.begin(&n.Base)
		p.next()
		n.X = p.parseUnary()
		p.finish(n, &n.Base)
		return n
	}
	x := p.parseOperand(cover)
	return p.parsePostfix(x, start, cover)
}

// parseOperand reads a Primary.
func (p *parser) parseOperand(cover bool) ast.Expr {
	switch k := p.kind(); {
	case k.IsLiteral():
		return p.basicLit()
	case k == ast.TokIdent:
		if x := p.operandHabit(); x != nil {
			return x
		}
		return p.ident()
	case k == ast.TokUnderscore:
		p.errHere("`_` is only a discard or a pattern")
		return p.identUnderscore()
	case k == ast.TokLParen:
		return p.parseParenOrRecord(cover)
	case k == ast.TokLBracket:
		return p.parseArrayLit()
	case k == ast.TokFn && p.peekKind(1) == ast.TokLParen:
		return p.parseFnLit()
	case k == ast.TokQuote && p.peekKind(1) == ast.TokLBrace:
		return p.parseQuote()
	case k == ast.TokPipe || k == ast.TokPipePipe:
		return p.closureHabit()
	case k == ast.TokIf || k == ast.TokMatch:
		return p.operandConstruct()
	case k == ast.TokDot && p.peekKind(1) == ast.TokLBrace:
		return p.anonLitHabit()
	case k == ast.TokDot:
		p.errHere("break a method chain after the dot, not before")
		s0 := p.p
		p.next()
		if p.at(ast.TokIdent) {
			p.next()
		}
		return p.badExprFrom(s0)
	case k == ast.TokLifetime:
		// E1203 in operand position: skip the lifetimes; an operand on the
		// same line is read one counted level deeper, else the operand is a
		// silent BadExpr and a line end after it is the statement's Term.
		// With no operand after them, deleting the lifetimes would leave
		// none, so their hits are alternatives only (`'a'`, a char).
		s0 := p.p
		var lts []ast.Token
		for p.at(ast.TokLifetime) {
			lts = append(lts, p.tok())
			p.next()
		}
		operand := !p.lineEnd() && startsOperand(p.kind())
		for _, t := range lts {
			p.lifetimeHit(t, operand)
		}
		if operand {
			return nodeless(p, p.parseUnary)
		}
		if p.lineEnd() {
			p.softTerm = true
		}
		return p.badExprFrom(s0)
	case k == ast.TokDotDot:
		p.errHere(rangeMsg)
		s0 := p.p
		p.next()
		return p.badExprFrom(s0)
	case k == ast.TokBad:
		s0 := p.p
		p.quiet = true
		p.next()
		return p.badExprFrom(s0)
	case k == ast.TokDollar:
		s0 := p.p
		p.errHere("`$` splices only inside `quote { }`")
		p.next()
		if p.at(ast.TokIdent) {
			p.next()
		}
		return p.badExprFrom(s0)
	case k.IsKeyword() && p.keywordNameAhead():
		p.keywordAsName()
		return p.ident()
	}
	p.expected("an expression")
	return p.badExprFrom(p.p)
}

// keywordNameAhead is true when the keyword at the current token is followed
// by a token that makes it a name.
func (p *parser) keywordNameAhead() bool {
	switch p.peekKind(1) {
	case ast.TokColonEq, ast.TokColon, ast.TokAssign, ast.TokComma, ast.TokRParen, ast.TokRBracket,
		ast.TokDot, ast.TokLParen, ast.TokTerm:
		return true
	}
	return binPrec(p.peekKind(1)) != 0
}

// parsePostfix reads the suffixes after an operand. cover says the
// operand opened a RecordPat cover, so its first call's arguments are read in
// cover mode.
func (p *parser) parsePostfix(x ast.Expr, start uint32, cover bool) (out ast.Expr) {
	coverCall := cover && isPathExpr(x)
	var qs []qmark
	defer func() {
		if len(qs) > 0 {
			qs[len(qs)-1].last = qs[len(qs)-1].span.End >= out.Span().End
			p.questionChain(start, qs)
		}
	}()
	for !p.tooDeep {
		if p.softTerm && p.lineEnd() {
			return x
		}
		switch p.kind() {
		case ast.TokDot, ast.TokColonColon:
			if p.at(ast.TokColonColon) {
				p.colonColonHabit()
			}
			x = p.parseDotSuffix(x, start)
			if _, ok := x.(*ast.GoExpr); ok {
				return x
			}
		case ast.TokLBracket:
			x = p.parseIndex(x, start)
			coverCall = false
		case ast.TokLParen:
			x = p.parseCall(x, start, coverCall)
			coverCall = false
		case ast.TokQuestion:
			if p.ternaryAhead() {
				return x // parseExpr reads `c ? a : b` whole
			}
			_, onCall := x.(*ast.CallExpr)
			qs = append(qs, qmark{span: p.curSpan(), onCall: onCall})
			p.next()
			if p.lineEnd() {
				p.softTerm = true
				return x
			}
		case ast.TokBang:
			if p.inBracket || p.tok().Start != p.lastEnd {
				return x
			}
			p.errHere("Mk has no `!` suffix")
			p.next()
		case ast.TokArrow:
			// E1211 outside a result position: C's a->b, read as a
			// field step, alternatives only.
			if p.lineEnd() || p.peekKind(1) != ast.TokIdent {
				return x
			}
			arrow := p.tok()
			alt := p.slice(start, x.Span().End) + "." + p.tokText(p.peek(1))
			p.hit(habitHit{code: diag.E1211, at: arrow.Span(p.file), msg: "Mk reaches a field with `.`, not `->`",
				alts: []string{alt}})
			x = p.parseDotSuffix(x, start)
		case ast.TokLBrace:
			if !p.braceLitAhead(x) {
				return x
			}
			x = p.braceLitHabit(x, start)
		case ast.TokLt:
			if !isPathExpr(x) || !p.angleCallAhead() {
				return x
			}
			x = p.angleCall(x, start)
		case ast.TokIdent:
			if p.atWord("as") && !p.lineEnd() {
				x = p.asHabit(x, start)
				continue
			}
			return x
		default:
			return x
		}
	}
	return x
}

// isPathExpr is true for a name or a chain of field steps over a name.
func isPathExpr(x ast.Expr) bool {
	switch y := x.(type) {
	case *ast.Ident:
		return true
	case *ast.FieldExpr:
		_, ok := fieldChain(y)
		return ok
	}
	return false
}

// parseDotSuffix reads "." Name or "." "go" Postfix.
func (p *parser) parseDotSuffix(x ast.Expr, start uint32) ast.Expr {
	if p.peekKind(1) == ast.TokGo {
		n := &ast.GoExpr{Scope: x}
		p.beginAt(&n.Base, start)
		p.next()
		p.next()
		cs := p.curStart()
		call := p.parsePostfix(p.parseOperand(false), cs, false)
		if _, ok := call.(*ast.CallExpr); !ok {
			p.syntaxErr(call.Span(), "`.go` spawns a call")
		}
		n.Call = call
		p.finish(n, &n.Base)
		p.goSpans = append(p.goSpans, n.Sp)
		return n
	}
	n := &ast.FieldExpr{X: x}
	p.beginAt(&n.Base, start)
	p.next()
	switch k := p.kind(); {
	case k == ast.TokIdent:
		n.Name = p.ident()
	case k == ast.TokInt || k == ast.TokFloat:
		p.errHere("records have names: there is no `.0`")
		n.Name = p.missingIdent()
		p.next()
	case k.IsKeyword():
		p.keywordAsName()
		n.Name = p.ident()
	default:
		p.expected("a field or method name")
		n.Name = p.missingIdent()
	}
	p.finish(n, &n.Base)
	return n
}

// parseIndex reads "[" … "]" after an operand: a SliceExpr, or an IndexExpr
// of bracket arguments.
func (p *parser) parseIndex(x ast.Expr, start uint32) ast.Expr {
	var b ast.Base
	p.beginAt(&b, start)
	p.next()
	savedList, savedCover := p.listCtx, p.coverOK
	p.listCtx, p.coverOK = true, false
	defer func() { p.listCtx, p.coverOK = savedList, savedCover }()
	if p.at(ast.TokDotDot) {
		return p.parseSliceRest(b, x, nil)
	}
	if p.at(ast.TokRBracket) {
		p.errHere("an index needs a value")
		p.next()
		n := &ast.IndexExpr{Base: b, X: x}
		p.finish(n, &n.Base)
		return n
	}
	first := p.parseBArg()
	if p.at(ast.TokDotDot) {
		return p.parseSliceRest(b, x, p.toExpr(first))
	}
	n := &ast.IndexExpr{Base: b, X: x, Args: []ast.TypeOrExpr{first}}
	for p.accept(ast.TokComma) {
		if p.at(ast.TokRBracket) {
			break
		}
		n.Args = append(n.Args, p.parseBArg())
	}
	if !p.expect(ast.TokRBracket) {
		p.skip(stopList)
		p.accept(ast.TokRBracket)
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseSliceRest(b ast.Base, x, lo ast.Expr) ast.Expr {
	n := &ast.SliceExpr{Base: b, X: x, Lo: lo}
	p.next()
	eq := p.inclusiveEq()
	p.listCtx = false
	if !p.at(ast.TokRBracket) {
		n.Hi = p.parseExpr()
	}
	if eq.File != 0 {
		p.inclusiveRangeHabit(eq, n.Hi)
	}
	if !p.expect(ast.TokRBracket) {
		p.skip(stopList)
		p.accept(ast.TokRBracket)
	}
	p.finish(n, &n.Base)
	return n
}

// parseCall reads "(" [ Args ] ")"; cover reads the arguments in
// RecordPat cover mode.
func (p *parser) parseCall(x ast.Expr, start uint32, cover bool) ast.Expr {
	n := &ast.CallExpr{Fun: x}
	p.beginAt(&n.Base, start)
	p.next()
	savedList, savedBracket := p.listCtx, p.inBracket
	p.listCtx, p.inBracket = false, false
	defer func() { p.inBracket = savedBracket }()
	for !p.at(ast.TokRParen) && !p.at(ast.TokEOF) {
		if p.at(ast.TokSemi) {
			p.semiInBrackets()
			continue
		}
		if cover && p.at(ast.TokDotDot) {
			p.coverRest(n.NID)
			break
		}
		n.Args = append(n.Args, p.parseArg(cover))
		if p.at(ast.TokSemi) {
			p.semiInBrackets()
			continue
		}
		if !p.at(ast.TokComma) {
			if !p.at(ast.TokRParen) {
				p.expected("`,`", "`)`")
				p.skip(stopList)
				if p.accept(ast.TokComma) {
					p.region()
					continue
				}
			}
			break
		}
		p.next()
		p.region()
	}
	p.listCtx = savedList
	p.expect(ast.TokRParen)
	p.finish(n, &n.Base)
	return n
}

// coverRest reads ".." [ "," ] as the last element of a cover-mode list.
func (p *parser) coverRest(owner ast.NodeID) {
	p.coverMarks = append(p.coverMarks, coverMark{p.curSpan(), "`..` is only for a destructure: `Point(x, ..) := p`"})
	if p.coverRests == nil {
		p.coverRests = map[ast.NodeID]bool{}
	}
	p.coverRests[owner] = true
	p.next()
	p.accept(ast.TokComma)
}

// parseArg reads [ Name ":" ] Expr.
func (p *parser) parseArg(cover bool) *ast.Arg {
	n := &ast.Arg{}
	p.begin(&n.Base)
	if (p.at(ast.TokIdent) || p.kind().IsKeyword()) && p.peekKind(1) == ast.TokColon {
		n.Name = p.parseName("an argument name")
		p.next()
	}
	n.Value = p.parseCoverValue(cover, n.NID)
	p.finish(n, &n.Base)
	return n
}

// parseCoverValue reads an argument or field value; in cover mode a "_", a
// leading-'.' pattern and a range pattern are read as placeholders.
// A range, `(x: 1..5) := e`, is a pattern of the destructure's PatField,
// which check refuses as refutable (E0306); in an expression
// it is E0101.
func (p *parser) parseCoverValue(cover bool, owner ast.NodeID) ast.Expr {
	if cover {
		switch {
		case p.coverRange():
			s0 := p.p
			sp := p.curSpan()
			pat := p.parseAlt()
			sp.End = p.lastEnd
			p.coverMarks = append(p.coverMarks, coverMark{sp, rangeMsg})
			bad := p.badExprFrom(s0)
			if p.coverPats == nil {
				p.coverPats = map[ast.NodeID]ast.Pattern{}
			}
			p.coverPats[owner] = pat
			return bad
		case p.at(ast.TokUnderscore) && (p.peekKind(1) == ast.TokComma || p.peekKind(1) == ast.TokRParen):
			p.coverMarks = append(p.coverMarks, coverMark{p.curSpan(), "`_` is only a discard or a pattern"})
			return p.identUnderscore()
		case p.at(ast.TokDot) && p.peekKind(1) == ast.TokIdent:
			s0 := p.p
			sp := p.curSpan()
			pat := p.parsePattern()
			sp.End = p.lastEnd
			p.coverMarks = append(p.coverMarks, coverMark{sp, "a leading `.` names a variant only in a pattern"})
			bad := p.badExprFrom(s0)
			if p.coverPats == nil {
				p.coverPats = map[ast.NodeID]ast.Pattern{}
			}
			p.coverPats[owner] = pat
			return bad
		}
		p.coverOK = true
	}
	x := p.parseValue()
	p.coverOK = false
	return x
}

// coverRange reports whether a whole range pattern is here, the value of a
// field or argument: an Int or Char end (a '-' before an Int), "..", and
// an optional end, or ".." and an end, followed by ',' or ')'. Anything
// longer, `G(0..n)`, stays an expression (rangeMsg).
func (p *parser) coverRange() bool {
	end := func(i int) int { // the tokens of an end at i, 0 for none
		switch k := p.peekKind(i); {
		case k == ast.TokInt || k == ast.TokChar:
			return 1
		case k == ast.TokMinus && p.peekKind(i+1) == ast.TokInt:
			return 2
		}
		return 0
	}
	i := end(0)
	if p.peekKind(i) != ast.TokDotDot {
		return false
	}
	j := end(i + 1)
	if i == 0 && j == 0 {
		return false
	}
	k := p.peekKind(i + 1 + j)
	return k == ast.TokComma || k == ast.TokRParen
}

// parseParenOrRecord reads "(" … ")" in expression mode; a bare name in a
// record literal is short for `name: name`.
func (p *parser) parseParenOrRecord(cover bool) ast.Expr {
	k1, k2 := p.peekKind(1), p.peekKind(2)
	// A keyword field name is E0102 and read as a name.
	nameNext := k1 == ast.TokIdent || k1.IsKeyword()
	if k1 == ast.TokRParen || (nameNext && (k2 == ast.TokRParen || k2 == ast.TokColon || k2 == ast.TokComma)) ||
		(cover && k1 == ast.TokDotDot) {
		return p.parseRecordLit(cover)
	}
	n := &ast.ParenExpr{}
	p.begin(&n.Base)
	p.next()
	savedList := p.listCtx
	p.listCtx = false
	n.X = p.parseExpr()
	p.listCtx = savedList
	if p.at(ast.TokComma) {
		p.errHere("record fields need names: `(a: 1, b: 2)`", "return a record: `(a: x, b: y)`")
		for p.accept(ast.TokComma) {
			if p.at(ast.TokRParen) {
				break
			}
			p.parseExpr()
		}
	}
	if !p.expect(ast.TokRParen) {
		p.skip(stopList)
		p.accept(ast.TokRParen)
	}
	p.finish(n, &n.Base)
	return n
}

// parseRecordLit reads "(" [ RecField { "," RecField } [ "," ] ] ")".
func (p *parser) parseRecordLit(cover bool) ast.Expr {
	n := &ast.RecordLit{}
	p.begin(&n.Base)
	p.next()
	savedList := p.listCtx
	p.listCtx = false
	for !p.at(ast.TokRParen) && !p.at(ast.TokEOF) {
		if cover && p.at(ast.TokDotDot) {
			p.coverRest(n.NID)
			break
		}
		f := &ast.RecField{}
		p.begin(&f.Base)
		if p.at(ast.TokIdent) || p.kind().IsKeyword() {
			f.Name = p.parseName("a field name")
		} else {
			p.expected("a field name")
			f.Name = p.missingIdent()
			p.skip(stopList)
		}
		if p.accept(ast.TokColon) {
			f.Value = p.parseCoverValue(cover, f.NID)
		}
		p.finish(f, &f.Base)
		n.Fields = append(n.Fields, f)
		if !p.at(ast.TokComma) {
			if !p.at(ast.TokRParen) {
				p.expected("`,`", "`)`")
				p.skip(stopList)
				if p.accept(ast.TokComma) {
					continue
				}
			}
			break
		}
		p.next()
		p.region()
	}
	p.listCtx = savedList
	p.expect(ast.TokRParen)
	p.finish(n, &n.Base)
	return n
}

// parseArrayLit reads "[" [ Expr { "," Expr } [ "," ] ] "]" or
// "[" Expr ";" Expr "]".
func (p *parser) parseArrayLit() ast.Expr {
	start := p.curStart()
	var b ast.Base
	p.beginAt(&b, start)
	p.next()
	savedList := p.listCtx
	p.listCtx = false
	defer func() { p.listCtx = savedList }()
	var elems []ast.Expr
	for !p.at(ast.TokRBracket) && !p.at(ast.TokEOF) {
		// A value construct as a repeat literal's value is E0101 at the
		// ';' (the repeat form's expressions are not value positions).
		elems = append(elems, p.parseValue())
		if len(elems) == 1 && p.at(ast.TokSemi) {
			p.next()
			n := &ast.RepeatLit{Base: b, Value: elems[0], Count: p.parseExpr()}
			p.expect(ast.TokRBracket)
			p.finish(n, &n.Base)
			return n
		}
		if !p.accept(ast.TokComma) {
			break
		}
		p.region()
	}
	if !p.expect(ast.TokRBracket) {
		p.skip(stopList)
		p.accept(ast.TokRBracket)
	}
	n := &ast.ArrayLit{Base: b, Elems: elems}
	p.finish(n, &n.Base)
	return n
}

// parseFnLit reads "fn" "(" [ Params ] ")" [ ":" Type ] Block with a fresh
// function context.
func (p *parser) parseFnLit() ast.Expr {
	n := &ast.FnLit{}
	p.begin(&n.Base)
	p.next()
	p.next()
	var sig ast.Signature
	p.parseParams(&sig, false, true)
	n.Self, n.Params = sig.Self, sig.Params
	p.expect(ast.TokRParen)
	if p.at(ast.TokColon) || p.at(ast.TokArrow) {
		if p.at(ast.TokArrow) {
			p.arrowHabit()
		}
		p.next()
		savedList := p.listCtx
		p.listCtx = false
		n.Result = p.parseType()
		p.listCtx = savedList
	}
	p.fn = &fnCtx{outer: p.fn}
	n.Body = p.parseBlockOrMissing()
	p.fn = p.fn.outer
	p.finish(n, &n.Base)
	return n
}

// parseQuote reads "quote" "{" Tokens "}": raw balanced tokens, with
// $( Expr ) and $Name splices parsed as expressions.
func (p *parser) parseQuote() ast.Expr {
	n := &ast.QuoteExpr{}
	p.begin(&n.Base)
	if p.inQuote > 0 {
		p.errHere("quotes do not nest")
	}
	p.inQuote++
	defer func() { p.inQuote-- }()
	p.next()
	openTok := p.sig[p.p]
	p.next()
	// Balance is counted with the lexer's rule: a closer pops
	// down to its matching opener when one is open and is ignored otherwise,
	// so a stray ')' the lexer reported does not swallow the file; a '}' with
	// no '{' open is the quote's own closer.
	var stack []ast.TokenKind
	for !p.at(ast.TokEOF) {
		switch k := p.kind(); k {
		case ast.TokLParen, ast.TokLBracket, ast.TokLBrace:
			stack = append(stack, k)
		case ast.TokRParen, ast.TokRBracket:
			stack = popTo(stack, openerOf(k))
		case ast.TokRBrace:
			if !slices.Contains(stack, ast.TokLBrace) {
				n.Toks = ast.TokRange{First: openTok + 1, End: p.sig[p.p]}
				p.next()
				p.finish(n, &n.Base)
				return n
			}
			stack = popTo(stack, ast.TokLBrace)
		case ast.TokDollar:
			n.Splices = append(n.Splices, p.parseSplice())
			continue
		case ast.TokQuote:
			if p.peekKind(1) == ast.TokLBrace {
				p.errHere("quotes do not nest")
			}
		}
		p.next()
	}
	n.Toks = ast.TokRange{First: openTok + 1, End: p.sig[p.p]}
	p.expect(ast.TokRBrace)
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseSplice() *ast.Splice {
	n := &ast.Splice{}
	p.begin(&n.Base)
	p.next()
	switch {
	case p.at(ast.TokLParen):
		p.next()
		savedList := p.listCtx
		p.listCtx = false
		n.X = p.parseExpr()
		p.listCtx = savedList
		p.expect(ast.TokRParen)
	case p.at(ast.TokIdent):
		n.X = p.ident()
	default:
		p.errHere("a splice is `$name` or `$(expr)`")
		n.X = p.badExprFrom(p.p)
	}
	p.finish(n, &n.Base)
	return n
}

// coverMark is a cover-only form noted while reading a statement head: an
// E0101 at span unless ":=" follows.
type coverMark struct {
	span source.Span
	msg  string
}
