// Why: keyword habits from other languages (`func`, `pub`, `async`, `let`,
// `impl` blocks, `throw`, `catch`, macros like `println!`, closures `|x| e`)
// need their own recognition, apart from the punctuation habits of habit.go.
//
// What: the word habits and the foreign forms with no code of their own:
// E1204, E1205, E1207, E1210, E1215 to E1219, E1221 and E0609, through
// fnWordHabit, pubHabit, itemHabit, memberHabit, stmtHabit, letHabit,
// operandHabit, closureHabit, ifLetHabit and MacroHabit.
//
// How: each check looks ahead only far enough to be sure the word cannot be
// a Mk name in that place, records a habitHit with its fix, and parses on.

package syntax

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
)

// pascal is E1205's rename: the first letter upper-cased, and each '_'
// followed by a letter removed with the letter upper-cased.
func pascal(s string) string {
	var b strings.Builder
	up := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' && i+1 < len(s) && isIdentStart(s[i+1]) && s[i+1] != '_' {
			up = true
			continue
		}
		if up && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		up = false
		b.WriteByte(c)
	}
	return b.String()
}

// fnWordAhead is true at fn, func or def followed by a name.
func (p *parser) fnWordAhead() bool {
	if p.peekKind(1) != ast.TokIdent {
		return false
	}
	return p.at(ast.TokFn) || p.atWord("func") || p.atWord("def")
}

// fnWordHabit records E1205 at fn, func or def before a name and consumes
// the word: it is deleted with its whitespace and the name is PascalCased.
func (p *parser) fnWordHabit() {
	w := p.tok()
	name := p.peek(1)
	h := habitHit{code: diag.E1205, at: w.Span(p.file), msg: "Mk declares a function by its signature alone: no `" + p.tokText(w) + "`",
		edits: []diag.Edit{p.edit(w.Start, name.Start, "")}}
	if nt := p.tokText(name); pascal(nt) != nt {
		h.edits = append(h.edits, p.edit(name.Start, name.End(), pascal(nt)))
	}
	p.hit(h)
	p.next()
}

// pubHabit records E1215 at pub, pub(crate) or pub(super); export replaces
// it where export is legal, else it is deleted. Export is not legal before
// import, gen, test, impl or unsafe impl, so there the word goes. It
// returns whether the replacement was export.
func (p *parser) pubHabit(exportOK bool) bool {
	w := p.tok()
	end := w.End()
	p.next()
	if p.at(ast.TokLParen) && p.tok().Start == end {
		for !p.at(ast.TokRParen) && !p.at(ast.TokEOF) && !p.at(ast.TokTerm) {
			p.next()
		}
		end = p.tok().End()
		p.accept(ast.TokRParen)
	}
	switch p.kind() {
	case ast.TokImport, ast.TokGen, ast.TokTest, ast.TokImpl, ast.TokUnsafe:
		exportOK = false
	}
	text := "export"
	editEnd := end
	if !exportOK {
		text = ""
		editEnd = p.curStart()
	}
	p.hit(habitHit{code: diag.E1215, at: p.span(w.Start, end), msg: "Mk exports with `export`, not `pub`",
		edits: []diag.Edit{p.edit(w.Start, editEnd, text)}})
	return exportOK
}

// asyncHabit records E1204 at async: the word and one space are deleted.
func (p *parser) asyncHabit() {
	w := p.tok()
	end := w.End()
	if end < uint32(len(p.text)) && p.text[end] == ' ' {
		end++
	}
	p.hit(habitHit{code: diag.E1204, at: w.Span(p.file), msg: "Mk has no `async`: every function may suspend through `Io`",
		edits: []diag.Edit{p.edit(w.Start, end, "")}})
	p.next()
}

// itemHabit recognizes a habit at item start; it returns the item it read,
// or nil to let ordinary parsing go on (after any word it consumed).
func (p *parser) itemHabit(s0 int) ast.Item {
	switch {
	case p.fnWordAhead():
		p.fnWordHabit()
		return p.parseFunc(p.curStart(), sigFunc)
	case p.atWord("pub") && (p.peekKind(1) == ast.TokLParen || p.peekKind(1) == ast.TokIdent || itemStarter(p.peekKind(1)) || p.peekKind(1) == ast.TokFn || p.peekKind(1) == ast.TokMut && p.peekKind(2) == ast.TokView):
		exported := p.pubHabit(true)
		it := p.parseItemInner()
		if exported {
			it.Info().Export = true
		}
		return it
	case p.atWord("async") && (p.peekKind(1) == ast.TokFn || p.peekKind(1) == ast.TokIdent):
		p.asyncHabit()
		return p.parseItemInner()
	case p.atWord("use") && p.peekKind(1) == ast.TokIdent:
		p.errHere("write `import a.b`")
		return p.badItem(s0)
	}
	return nil
}

// memberHabit handles pub at a struct member's start.
func (p *parser) memberHabit() ast.Member {
	if p.atWord("pub") && (p.peekKind(1) == ast.TokIdent || p.peekKind(1) == ast.TokLParen) {
		p.pubHabit(true)
	}
	return nil
}

// memberHabitAhead is true at a habit word before an interface member.
func (p *parser) memberHabitAhead() bool {
	return p.fnWordAhead() || p.atWord("pub") || p.atWord("async")
}

// memberWordHabit consumes fn, func, def, pub and async before a member.
func (p *parser) memberWordHabit() {
	for {
		switch {
		case p.atWord("pub") && (p.peekKind(1) == ast.TokIdent || p.peekKind(1) == ast.TokFn || p.peekKind(1) == ast.TokLParen):
			p.pubHabit(false)
		case p.atWord("async") && (p.peekKind(1) == ast.TokFn || p.peekKind(1) == ast.TokIdent):
			p.asyncHabit()
		case p.fnWordAhead():
			p.fnWordHabit()
		default:
			return
		}
	}
}

// implBlockHabit reads "impl" Type "{" … "}" with no "for" (E1219): the
// header and its closing brace are deleted and "T." goes before each
// member's name. The item becomes a BadItem.
func (p *parser) implBlockHabit(n *ast.ImplDecl) ast.Item {
	header := p.span(n.Sp.Start, p.curStart()) // through the space before '{'
	typeText := p.f.Slice(n.Interface.Span())
	h := habitHit{code: diag.E1219, at: p.span(n.Sp.Start, n.Interface.Span().End),
		msg: "Mk has no `impl` blocks: write each method as `" + typeText + ".Name(…)`"}
	open := p.tok()
	p.next()
	// Delete the header line through '{' and its newline.
	delEnd := open.End()
	if delEnd < uint32(len(p.text)) && p.text[delEnd] == '\n' {
		delEnd++
	}
	h.edits = append(h.edits, p.edit(header.Start, delEnd, ""))
	for !p.at(ast.TokRBrace) && !p.at(ast.TokEOF) {
		if p.at(ast.TokTerm) {
			p.next()
			continue
		}
		p.memberWordHabit()
		if !p.at(ast.TokIdent) {
			p.expected("a method")
			p.skip(stopStmt)
			p.endElem("one member per line")
			continue
		}
		at := p.curStart()
		h.edits = append(h.edits, p.edit(at, at, typeText+"."))
		p.parseFunc(at, sigImpl)
		p.endElem("one member per line")
	}
	if p.at(ast.TokRBrace) {
		c := p.tok()
		ls := p.wsBefore(c.Start)
		end := c.End()
		if ls > 0 && p.text[ls-1] == '\n' {
			h.edits = append(h.edits, p.edit(ls-1, end, ""))
		} else {
			h.edits = append(h.edits, p.edit(c.Start, end, ""))
		}
		p.next()
	}
	first := p.f.LineCol(n.Sp.Start).Line
	last := p.f.LineCol(p.lastEnd).Line
	h.lines = [2]uint32{first, last}
	p.hit(h)
	bad := &ast.BadItem{}
	p.nextID++
	bad.NID = p.nextID
	bad.Sp = p.span(n.Sp.Start, p.lastEnd)
	p.open--
	return bad
}

// stmtHabit recognizes a habit at statement start; it returns the
// statement it read, or nil.
func (p *parser) stmtHabit() ast.Stmt {
	switch p.kind() {
	case ast.TokIdent:
	case ast.TokFn:
		if p.peekKind(1) == ast.TokIdent {
			return p.localFnHabit()
		}
		return nil
	case ast.TokMut:
		if p.peekKind(1) == ast.TokIdent && (p.peekKind(2) == ast.TokColonEq || p.peekKind(2) == ast.TokColon) {
			p.errHere("write `var " + p.tokText(p.peek(1)) + " := e`")
			return p.parseBind(true)
		}
		return nil
	case ast.TokTry:
		if p.peekKind(1) == ast.TokLBrace {
			s0 := p.p
			p.hit(habitHit{code: diag.E1210, at: p.curSpan(), msg: "Mk has no `try` blocks: handle each error with `else`",
				alts: []string{"else", "match"}})
			p.next()
			p.parseBlock()
			p.catchHabit()
			return p.badStmtFrom(s0)
		}
		return nil
	case ast.TokGo:
		p.errHere("write `s.go F()` inside `scope s := io.Scope() { }`")
		s0 := p.p
		p.next()
		p.parseExpr()
		return p.badStmtFrom(s0)
	default:
		return nil
	}
	word := p.curText()
	k1 := p.peekKind(1)
	switch word {
	case "let":
		// let( is a call on a name `let` unless the parentheses are followed
		// by '=' (E1207).
		if k1 == ast.TokIdent || k1 == ast.TokMut || k1 == ast.TokLParen && p.parenThenAssign(1) {
			return p.letHabit()
		}
	case "while":
		if p.peekWord(1, "let") {
			return p.whileLetHabit()
		}
		if p.headBeforeBrace() {
			p.hit(habitHit{code: diag.E1216, at: p.curSpan(), msg: "Mk loops with `for`",
				edits: []diag.Edit{p.edit(p.curStart(), p.tok().End(), "for")}})
			return p.parseFor(nil)
		}
	case "loop":
		if k1 == ast.TokLBrace {
			p.hit(habitHit{code: diag.E1216, at: p.curSpan(), msg: "Mk loops with `for`",
				edits: []diag.Edit{p.edit(p.curStart(), p.tok().End(), "for")}})
			return p.parseFor(nil)
		}
	case "assert":
		if h := p.assertHabit(); h != nil {
			return h
		}
	case "throw":
		// Only where `throw` cannot be a name: an operand follows on the
		// line, and it is not '(' or '[' (a call or index on a local named
		// throw), nor a binary operator.
		if startsOperand(k1) && k1 != ast.TokLParen && k1 != ast.TokLBracket && binPrec(k1) == 0 && !p.lineEndAt(1) {
			at := p.curSpan()
			n := &ast.FailStmt{}
			p.begin(&n.Base)
			p.next()
			n.Value = p.parseExpr()
			p.finish(n, &n.Base)
			// The hit is recorded after the value, from its span, so the
			// alternative holds neither a comment nor an enclosing '}'.
			p.hit(habitHit{code: diag.E1210, at: at, msg: "Mk returns errors with `fail`, not `throw`",
				alts: []string{"fail " + p.f.Slice(n.Value.Span())}})
			return n
		}
	case "switch":
		if k1 != ast.TokColonEq && k1 != ast.TokAssign && k1 != ast.TokDot && k1 != ast.TokLParen && k1 != ast.TokColon && p.headBeforeBrace() {
			s0 := p.p
			p.errHere("use `match`")
			p.skipToBrace()
			if p.at(ast.TokLBrace) {
				p.skipBalanced()
			}
			return p.badStmtFrom(s0)
		}
	case "func", "def":
		if k1 == ast.TokIdent {
			return p.localFnHabit()
		}
	case "async":
		if k1 == ast.TokLBrace || k1 == ast.TokFn {
			p.asyncHabit()
			return p.parseStmtInner()
		}
	}
	if k1 == ast.TokComma && p.multiAssignAhead() {
		s0 := p.p
		msg, alts := p.multiAssignForm()
		p.errHere(msg, alts...)
		p.skip(stopStmt)
		return p.badStmtFrom(s0)
	}
	return nil
}

// parenThenAssign is true when peek(k) is a '(' whose matching ')' is
// directly followed by '=' on the line: `let (a, b) = e`. The scan is bounded
// like rustArrayType's, so statement starts stay linear; a destructure head
// is short.
func (p *parser) parenThenAssign(k int) bool {
	depth := 0
	for i := k; i < k+1024; i++ {
		switch p.peekKind(i) {
		case ast.TokLParen, ast.TokLBracket, ast.TokLBrace:
			depth++
		case ast.TokRParen, ast.TokRBracket, ast.TokRBrace:
			depth--
			if depth == 0 {
				return p.peekKind(i+1) == ast.TokAssign
			}
			if depth < 0 {
				return false
			}
		case ast.TokTerm, ast.TokEOF:
			return false
		}
	}
	return false
}

// headBeforeBrace is true when a '{' follows on the current line at bracket
// depth zero, so the statement is a loop or switch head.
func (p *parser) headBeforeBrace() bool {
	depth := 0
	for i := 1; ; i++ {
		switch p.peekKind(i) {
		case ast.TokLParen, ast.TokLBracket:
			depth++
		case ast.TokRParen, ast.TokRBracket:
			depth--
		case ast.TokLBrace:
			return depth == 0 && i > 1
		case ast.TokTerm, ast.TokEOF, ast.TokColonEq, ast.TokAssign:
			return false
		}
	}
}

// multiAssignAhead is true at a, b := or a, b = at statement start.
func (p *parser) multiAssignAhead() bool {
	for i := 0; ; i += 2 {
		if p.peekKind(i) != ast.TokIdent {
			return false
		}
		switch p.peekKind(i + 1) {
		case ast.TokComma:
			continue
		case ast.TokColonEq, ast.TokAssign:
			return i > 0
		}
		return false
	}
}

// localFnHabit records E1205 at a function declared inside a body and skips
// the declaration (there are no local functions).
func (p *parser) localFnHabit() ast.Stmt {
	s0 := p.p
	p.fnWordHabit()
	saved := p.fn
	p.parseSignature(sigFunc)
	if p.at(ast.TokLBrace) {
		p.fn = &fnCtx{}
		p.parseBlock()
	}
	p.fn = saved
	return p.badStmtFrom(s0)
}

// letHabit reads let [ mut ] Name [ ":" Type ] "=" Expr, or let ( … ) = e,
// as the Mk form (E1207).
func (p *parser) letHabit() ast.Stmt {
	let := p.tok()
	h := habitHit{code: diag.E1207, at: let.Span(p.file), msg: "Mk binds with `x := e`; `var x := e` is mutable"}
	if p.peekKind(1) == ast.TokLParen {
		h.edits = append(h.edits, p.edit(let.Start, p.peek(1).Start, ""))
		p.next()
		start := p.curStart()
		savedMarks := p.coverMarks
		p.coverMarks = nil
		p.coverOK = true
		x := p.parseExpr()
		p.coverOK = false
		p.coverMarks = savedMarks
		if p.at(ast.TokAssign) {
			h.edits = append(h.edits, p.edit(p.curStart(), p.tok().End(), ":="))
			p.hit(h)
			n := &ast.DestructureStmt{Reinterpreted: true}
			p.beginAt(&n.Base, start)
			n.Pat = p.recordPatOf(x)
			p.next()
			n.Value = p.parseExpr()
			p.finish(n, &n.Base)
			return n
		}
		p.hit(h)
		st := &ast.ExprStmt{X: x}
		p.adopt(st, &st.Base, x.Span())
		return st
	}
	p.next()
	isVar := false
	if p.at(ast.TokMut) {
		isVar = true
		h.edits = append(h.edits, p.edit(let.Start, p.tok().End(), "var"))
		p.next()
	} else {
		h.edits = append(h.edits, p.edit(let.Start, p.curStart(), ""))
	}
	n := &ast.BindStmt{Var: isVar}
	p.begin(&n.Base)
	n.Name = p.parseDeclName(declLocal)
	if p.accept(ast.TokColon) {
		n.Type = p.parseType()
		if p.accept(ast.TokAssign) {
			n.Value = p.parseExpr()
		}
	} else if p.at(ast.TokAssign) || p.at(ast.TokColonEq) {
		if p.at(ast.TokAssign) {
			h.edits = append(h.edits, p.edit(p.curStart(), p.tok().End(), ":="))
		}
		p.next()
		n.Value = p.parseExpr()
	} else {
		p.expected("`=`")
		n.Value = p.badExprFrom(p.p)
	}
	p.finish(n, &n.Base)
	p.hit(h)
	return n
}

// whileLetHabit reads while let P = e { … } (one E1216): for x := e when P
// is Some(x), else alternatives.
func (p *parser) whileLetHabit() ast.Stmt {
	w := p.tok()
	h := habitHit{code: diag.E1216, at: w.Span(p.file), msg: "Mk loops with `for x := e` while an optional has a value"}
	someX := p.peekWord(2, "Some") && p.peekKind(3) == ast.TokLParen && p.peekKind(4) == ast.TokIdent &&
		p.peekKind(5) == ast.TokRParen && p.peekKind(6) == ast.TokAssign
	n := &ast.ForStmt{}
	p.begin(&n.Base)
	p.next() // while
	p.next() // let
	if someX {
		name := p.peek(2)
		eq := p.peek(4)
		h.edits = []diag.Edit{p.edit(w.Start, eq.End(), "for "+p.tokText(name)+" :=")}
		p.next()
		p.next()
		bc := &ast.BindCond{}
		p.begin(&bc.Base)
		bc.Name = p.parseDeclName(declLocal)
		p.next()
		p.next()
		bc.Value = p.parseExpr()
		p.finish(bc, &bc.Base)
		n.Head = bc
	} else {
		h.alts = []string{"for x := e { }", "for { match e { … } }"}
		s0 := p.p
		p.skipToBrace()
		n.Head = p.badExprFrom(s0)
	}
	p.hit(h)
	p.fn.loops++
	n.Body = p.parseBlockOrMissing()
	p.fn.loops--
	p.finish(n, &n.Base)
	return n
}

// catchHabit records E1210 for catch directly after a block's '}'.
func (p *parser) catchHabit() {
	if !p.atWord("catch") || p.lineEnd() {
		return
	}
	p.hit(habitHit{code: diag.E1210, at: p.curSpan(), msg: "Mk has no `catch`: handle the error with `else`",
		alts: []string{"else", "match"}})
	p.next()
	for !p.at(ast.TokLBrace) && !p.at(ast.TokTerm) && !p.at(ast.TokEOF) {
		p.next()
	}
	if p.at(ast.TokLBrace) {
		p.parseBlock()
	}
}

// macroNames are E1221's macro forms and the Mk name each one suggests.
var macroNames = map[string]string{
	"println": "Print(mut out, …)", "print": "Print(mut out, …)", "eprintln": "Print(mut out, …)",
	"format": "arena.Fmt(\"{x}\", (x))", "vec": "List", "write": "Print(mut out, …)", "writeln": "Print(mut out, …)",
}

// MacroHabit reports whether name! before '(' or '[' is a habit the parser
// reports in a bracket argument too (E1210, E1221), where '!' is otherwise
// the error-union operator: the formatter keeps `name !` spaced there.
func MacroHabit(name string) bool {
	_, named := macroNames[name]
	return named || name == "panic"
}

// operandHabit recognizes a habit at an Ident in operand position (await,
// panic!, macro forms); it returns the operand it read, or nil.
func (p *parser) operandHabit() ast.Expr {
	text := p.curText()
	next := p.peek(1)
	adjacentBang := next.Kind == ast.TokBang && next.Start == p.tok().End()
	// Inside a bracket argument '!' is also the error-union operator, so
	// List[E![]u8] is a type, as the formatter writes it; there only the
	// named macro forms and panic! are habits.
	if p.inBracket && !MacroHabit(text) {
		adjacentBang = false
	}
	switch {
	case text == "await" && !p.lineEndAt(1) && startsOperand(next.Kind):
		// A run of await is a loop, and the operand after it one counted
		// level deeper (no node is made here).
		for p.atWord("await") && !p.lineEndAt(1) && startsOperand(p.peekKind(1)) {
			p.asyncAwait()
		}
		return nodeless(p, p.parseUnary)
	case adjacentBang && text == "panic":
		p.hit(habitHit{code: diag.E1210, at: p.span(p.curStart(), next.End()), msg: "Mk panics with `Panic(…)`, not `panic!`",
			alts: []string{"Panic(…)"}})
		id := p.ident()
		p.next()
		return id
	case adjacentBang && (p.peekKind(2) == ast.TokLParen || p.peekKind(2) == ast.TokLBracket):
		if mk, ok := macroNames[text]; ok {
			h := habitHit{code: diag.E1221, at: p.span(p.curStart(), next.End()), msg: "Mk has no macros: `" + text + "!` is " + mk,
				alts: []string{mk}, facts: []diag.Field{{Key: "mk_name", Value: diag.TextFact(mk)}}}
			p.hit(h)
		} else {
			p.syntaxErr(p.span(p.curStart(), next.End()), "Mk has no macros")
		}
		id := p.ident()
		p.next()
		return id
	}
	return nil
}

// lineEndAt is true when a newline lies between peek(k-1) and peek(k).
func (p *parser) lineEndAt(k int) bool {
	i := p.p + k
	if i >= len(p.sig) {
		return true
	}
	for j := p.sig[i-1] + 1; j < p.sig[i]; j++ {
		if p.toks[j].Kind == ast.TokNewline {
			return true
		}
	}
	return p.toks[p.sig[i-1]].Kind == ast.TokTerm
}

// asyncAwait records E1204 at a prefix await and consumes it.
func (p *parser) asyncAwait() {
	w := p.tok()
	end := w.End()
	if end < uint32(len(p.text)) && p.text[end] == ' ' {
		end++
	}
	p.hit(habitHit{code: diag.E1204, at: w.Span(p.file), msg: "Mk has no `await`: a call through `Io` suspends by itself",
		edits: []diag.Edit{p.edit(w.Start, end, "")}})
	p.next()
}

// startsOperand is true for a token that can begin an operand.
func startsOperand(k ast.TokenKind) bool {
	switch k {
	case ast.TokIdent, ast.TokLParen, ast.TokLBracket, ast.TokMinus, ast.TokBang, ast.TokTilde,
		ast.TokAmp, ast.TokStar, ast.TokMut, ast.TokTry, ast.TokFn:
		return true
	}
	return k.IsLiteral()
}

// closureHabit reads |x| e, |x, y| e or || e (E0609 in phase parse).
func (p *parser) closureHabit() ast.Expr {
	s0 := p.p
	p.hit(habitHit{code: diag.E0609, at: p.curSpan(), msg: "Mk has no closures",
		alts: []string{"a `fn` literal", "a struct plus a `fn`"}})
	if p.at(ast.TokPipe) {
		p.next()
		for !p.at(ast.TokPipe) && !p.at(ast.TokEOF) && !p.at(ast.TokTerm) {
			p.next()
		}
		p.accept(ast.TokPipe)
	} else {
		p.next()
	}
	if p.at(ast.TokLBrace) {
		p.parseBlock()
	} else if !p.at(ast.TokTerm) {
		p.parseExpr()
	}
	return p.badExprFrom(s0)
}

// ifLetHabit reads if let P = e (E1217): if x := e when P is Some(x), else
// alternatives; the condition becomes a BindCond or a BadExpr.
func (p *parser) ifLetHabit() ast.Node {
	let := p.tok()
	h := habitHit{code: diag.E1217, at: let.Span(p.file), msg: "Mk tests an optional with `if x := e`"}
	someX := p.peekWord(1, "Some") && p.peekKind(2) == ast.TokLParen && p.peekKind(3) == ast.TokIdent &&
		p.peekKind(4) == ast.TokRParen && p.peekKind(5) == ast.TokAssign
	if !someX {
		h.alts = []string{"if x := e { }", "match e { … }"}
		p.hit(h)
		s0 := p.p
		p.skipToBrace()
		return p.badExprFrom(s0)
	}
	name, eq := p.peek(3), p.peek(5)
	h.edits = []diag.Edit{p.edit(let.Start, eq.End(), p.tokText(name)+" :=")}
	p.hit(h)
	p.next()
	p.next()
	p.next()
	n := &ast.BindCond{}
	p.begin(&n.Base)
	n.Name = p.parseDeclName(declLocal)
	p.next()
	p.next()
	n.Value = p.parseExpr()
	p.finish(n, &n.Base)
	return n
}
