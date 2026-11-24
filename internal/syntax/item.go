// Why: a file is a list of items, and each item kind has its own head
// words and body; this file holds the top level of the parser.
//
// What: the item and declaration productions (File, Item, Import, Gen,
// Test, Struct, Packed, Member, Enum, Variant, Fields, ErrorDecl, Interface,
// Impl, Alias, Const, Global, Extern, Func): parseFile, parseItem and one
// parse function per item kind.
//
// How: parseItemInner dispatches on the first token; the contextual
// modifiers open, naked and packed are modifiers only in the token shapes
// they are tested in, so they stay plain names elsewhere. An item nested
// past the depth limit is resynchronized from its first token and becomes
// one BadItem with one E0101.

package syntax

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
)

// parseFile reads { Item Term } until EOF.
func (p *parser) parseFile(file *ast.File) {
	p.nextID = 1
	file.NID = 1
	file.Sp = source.Span{File: p.file, Start: 0, End: uint32(len(p.text))}
	p.region()
	for !p.at(ast.TokEOF) {
		if p.at(ast.TokTerm) {
			p.next()
			continue
		}
		file.Items = append(file.Items, p.parseItem())
		p.endItem()
	}
	p.height = append(p.height, 0)
}

// endItem expects the Term after an item.
func (p *parser) endItem() {
	switch {
	case p.at(ast.TokTerm):
		p.next()
	case p.at(ast.TokEOF):
	case p.softTerm && p.lineEnd():
	default:
		p.errHere("one item per line")
		p.skip(stopItem)
		p.accept(ast.TokTerm)
	}
	p.softTerm = false
	p.region()
}

// docStart is the toks index where the item starting at the current token
// begins: the contiguous /// lines directly above it, else the token itself.
func (p *parser) docStart() uint32 {
	first := p.sig[p.p]
	for i := int(first) - 1; i >= 0; i-- {
		switch p.toks[i].Kind {
		case ast.TokWhitespace, ast.TokNewline:
			continue
		case ast.TokDocComment:
			first = uint32(i)
			continue
		}
		break
	}
	return first
}

// parseItem reads one item; it never returns nil.
func (p *parser) parseItem() ast.Item {
	p.open = 0
	p.tooDeep = false
	p.fn = &fnCtx{}
	s0 := p.p
	docFirst := p.docStart()
	it := p.parseItemInner()
	if p.tooDeep {
		// Resynchronize from the item's first token, so the bracket count
		// starts at the item's depth whatever the deep productions left
		// unconsumed (one E0101 per item). Linear in the item's tokens.
		p.p = s0
		p.softTerm = false
		p.skip(stopItem)
		bad := &ast.BadItem{}
		p.nextID++
		bad.NID = p.nextID
		start := p.toks[p.sig[s0]].Start
		bad.Sp = p.span(start, p.lastEnd)
		it = bad
		p.tooDeep = false
	}
	info := it.Info()
	end := p.lastTok + 1
	if p.p == s0 {
		end = p.sig[s0]
	}
	if end < docFirst {
		end = docFirst
	}
	info.Toks = ast.TokRange{First: docFirst, End: end}
	p.flushKeywordNames(p.sig[s0], end)
	return it
}

// badItem skips to the item's end and wraps the skipped tokens.
func (p *parser) badItem(s0 int) ast.Item {
	p.skip(stopItem)
	if p.p == s0 && !p.at(ast.TokEOF) && !p.at(ast.TokTerm) {
		p.next()
	}
	bad := &ast.BadItem{}
	p.nextID++
	bad.NID = p.nextID
	start := p.toks[p.sig[s0]].Start
	end := start
	if p.p > s0 {
		end = p.lastEnd
	}
	bad.Sp = p.span(start, end)
	return bad
}

func (p *parser) parseItemInner() ast.Item {
	s0 := p.p
	export := false
	if p.at(ast.TokExport) {
		p.next()
		export = true
		switch p.kind() {
		case ast.TokImport, ast.TokGen, ast.TokTest, ast.TokImpl, ast.TokUnsafe:
			p.syntaxErr(p.toks[p.sig[s0]].Span(p.file), "only declarations are exported; an impl is visible wherever its type and interface are")
		}
	}
	start := p.toks[p.sig[s0]].Start
	var it ast.Item
	switch k := p.kind(); {
	case k == ast.TokImport:
		it = p.parseImport(start)
	case k == ast.TokGen:
		it = p.parseGen(start)
	case k == ast.TokTest:
		it = p.parseTest(start)
	case k == ast.TokLinear || k == ast.TokView || k == ast.TokMut || k == ast.TokStruct || (k == ast.TokIdent && (p.packedModifier() || p.alignModifier() || p.plainModifier())):
		it = p.parseStruct(start)
	case k == ast.TokEnum:
		it = p.parseEnum(start)
	case k == ast.TokError:
		it = p.parseErrorDecl(start)
	case k == ast.TokInterface:
		it = p.parseInterface(start)
	case k == ast.TokImpl || (k == ast.TokUnsafe && p.peekKind(1) == ast.TokImpl):
		it = p.parseImpl(start)
	case k == ast.TokUnsafe:
		p.errHere("an unsafe block is a statement")
		return p.badItem(s0)
	case k == ast.TokType:
		it = p.parseAlias(start)
	case k == ast.TokConst:
		it = p.parseConst(start)
	case k == ast.TokVar:
		it = p.parseGlobal(start)
	case k == ast.TokExtern:
		it = p.parseExtern(start)
	case k == ast.TokIdent && p.atWord("open") && (p.peekKind(1) == ast.TokEnum || p.peekKind(1) == ast.TokType):
		// open before enum or type is the modifier, so open stays
		// a plain name everywhere else.
		p.next()
		if p.at(ast.TokEnum) {
			it = p.parseEnum(start)
			if n, ok := it.(*ast.EnumDecl); ok {
				n.Open = true
			}
		} else {
			it = p.parseAlias(start)
			if n, ok := it.(*ast.AliasDecl); ok {
				n.Open = true
			}
		}
	case k == ast.TokIdent && p.nakedModifier():
		p.next()
		it = p.parseFunc(start, sigFunc)
		if fd, ok := it.(*ast.FuncDecl); ok {
			fd.Naked = true
		}
	case k == ast.TokIdent:
		if h := p.itemHabit(s0); h != nil {
			return h
		}
		it = p.parseFunc(start, sigFunc)
	case k == ast.TokFn && p.peekKind(1) == ast.TokIdent:
		if h := p.itemHabit(s0); h != nil {
			return h
		}
		it = p.parseFunc(start, sigFunc)
	case k == ast.TokFn:
		p.errHere("a fn literal is not an item")
		return p.badItem(s0)
	case k == ast.TokBad:
		p.quiet = true
		return p.badItem(s0)
	default:
		if h := p.itemHabit(s0); h != nil {
			return h
		}
		p.expected("an item")
		return p.badItem(s0)
	}
	it.Info().Export = export
	return it
}

// packedModifier decides whether an Ident packed at item position is the
// modifier when struct, linear, view, mut view, plain or packed follows, or "(" Int.
func (p *parser) packedModifier() bool {
	if !p.atWord("packed") {
		return false
	}
	switch k := p.peekKind(1); {
	case k == ast.TokStruct || k == ast.TokLinear || k == ast.TokView:
		return true
	case k == ast.TokMut && (p.peekKind(2) == ast.TokView || p.peekKind(2) == ast.TokStruct || p.peekWord(2, "plain")):
		return true
	case p.peekWord(1, "plain"):
		return true
	case p.peekWord(1, "packed"):
		return true
	case p.peekWord(1, "align") && p.peekKind(2) == ast.TokLParen:
		return true // packed align(n) struct
	case k == ast.TokLParen && p.peekKind(2) == ast.TokInt:
		return true
	}
	return false
}

// plainModifier decides whether an Ident plain at item or struct-modifier
// position is the contextual `plain` modifier rather than an ordinary
// identifier (`plain(x: int)`, `plain: int`, `plain := 1`).
func (p *parser) plainModifier() bool {
	if !p.atWord("plain") {
		return false
	}
	switch k := p.peekKind(1); {
	case k == ast.TokStruct || k == ast.TokLinear || k == ast.TokView || k == ast.TokMut:
		return true
	case p.peekWord(1, "plain") || p.peekWord(1, "packed") || p.peekWord(1, "align"):
		return true
	}
	return false
}

// itemStart begins an item node at the item's first token (export included).
func (p *parser) itemStart(b *ast.Base, start uint32) { p.beginAt(b, start) }

func (p *parser) parseImport(start uint32) ast.Item {
	n := &ast.Import{}
	p.itemStart(&n.Base, start)
	p.next()
	if p.at(ast.TokIdent) && p.peekKind(1) == ast.TokAssign {
		n.Alias = p.parseDeclName(declImport)
		p.next()
	}
	n.Path = p.parsePath()
	if p.atWord("as") && p.peekKind(1) == ast.TokIdent {
		p.errHere("write `import " + p.tokText(p.peek(1)) + " = " + p.f.Slice(n.Path.Sp) + "`")
		p.next()
		p.next()
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseGen(start uint32) ast.Item {
	n := &ast.GenItem{}
	p.itemStart(&n.Base, start)
	p.next()
	n.Generator = p.parsePath()
	if p.expect(ast.TokLParen) {
		n.Args = p.parseBracketList(ast.TokRParen)
		p.expect(ast.TokRParen)
	}
	if p.atWord("fuel") {
		p.next()
		n.Fuel = p.parseExpr()
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseTest(start uint32) ast.Item {
	n := &ast.TestItem{}
	p.itemStart(&n.Base, start)
	p.next()
	switch p.kind() {
	case ast.TokStr, ast.TokRawStr, ast.TokMultiStr:
		n.Name = p.basicLit()
	case ast.TokByteStr:
		p.errHere("a test name is a string, not bytes")
		n.Name = p.basicLit()
	default:
		p.expected("a test name")
		lit := &ast.BasicLit{Kind: ast.TokStr}
		p.begin(&lit.Base)
		p.finishAt(lit, &lit.Base, lit.Sp.Start)
		n.Name = lit
	}
	if p.at(ast.TokLParen) {
		p.next()
		if p.at(ast.TokConst) || p.at(ast.TokVar) {
			p.errHere("a test parameter takes no `const` or `var`")
			p.next()
		}
		if !p.at(ast.TokRParen) {
			n.Param = p.parseParam(false)
		}
		p.accept(ast.TokComma)
		p.expect(ast.TokRParen)
	}
	n.Body = p.parseBlockOrMissing()
	p.finish(n, &n.Base)
	return n
}

// parseStruct reads { "linear" | [ "mut" | "plain" ] "view" | Packed | Aligned }
// "struct" Name [ TypeParams ] "{" { Member Term } "}".
func (p *parser) parseStruct(start uint32) ast.Item {
	n := &ast.StructDecl{}
	p.itemStart(&n.Base, start)
	var plainSpan source.Span
	var hasPlainSpan bool
	var plainNotAdj bool
	for {
		switch {
		case p.at(ast.TokLinear):
			if n.Linear {
				p.errHere("`linear` written twice")
			}
			n.Linear = true
			p.next()
			continue
		case p.at(ast.TokMut):
			mutSpan := p.curSpan()
			p.next()
			if p.plainModifier() {
				if n.MutView {
					p.syntaxErr(mutSpan, "`mut` written twice")
				}
				n.MutView = true
				continue
			}
			if !p.at(ast.TokView) {
				p.syntaxErr(mutSpan, "`mut` on a struct applies only to `view`: write `mut view struct`")
				continue
			}
			if n.View {
				p.errHere("`view` written twice")
			}
			n.View = true
			n.MutView = true
			p.next()
			continue
		case p.plainModifier():
			sp := p.curSpan()
			if n.PlainView {
				p.errHere("`plain` written twice")
			} else {
				plainSpan = sp
				hasPlainSpan = true
			}
			if n.View && !n.PlainView {
				p.syntaxErr(sp, "`plain` must appear before `view`: write `plain view struct`")
			}
			n.PlainView = true
			p.next()
			if !n.View && (p.packedModifier() || p.alignModifier()) {
				plainNotAdj = true
			}
			continue
		case p.at(ast.TokView):
			if n.View {
				p.errHere("`view` written twice")
			}
			n.View = true
			p.next()
			continue
		case p.packedModifier():
			if n.Packed != nil {
				p.errHere("`packed` written twice")
			}
			n.Packed = p.parsePacked()
			continue
		case p.alignModifier():
			if n.Align != nil {
				p.errHere("`align` written twice")
			}
			n.Align = p.parseAligned()
			continue
		}
		break
	}
	if n.PlainView {
		switch {
		case n.Linear:
			p.syntaxErr(plainSpan, "`plain` and `linear` cannot both be written on a struct")
			n.PlainView = false
		case n.MutView:
			p.syntaxErr(plainSpan, "`plain` and `mut` cannot both be written on a `view struct`")
			n.PlainView = false
		case !n.View:
			if !hasPlainSpan {
				plainSpan = p.curSpan()
			}
			p.syntaxErr(plainSpan, "`plain` only modifies a `view struct`: write `plain view struct` for a copyable view, or remove `plain` (a struct without `view` is already plain when its fields are)")
			n.PlainView = false
		case plainNotAdj:
			p.syntaxErr(plainSpan, "`plain` must immediately precede `view`: write `plain view struct`")
			n.PlainView = false
		}
	}
	p.expect(ast.TokStruct)
	n.Name = p.parseDeclName(declItem)
	if p.at(ast.TokLBracket) {
		n.TypeParams = p.parseTypeParams()
	}
	if p.expect(ast.TokLBrace) {
		for !p.at(ast.TokRBrace) && !p.at(ast.TokEOF) {
			if p.at(ast.TokTerm) {
				p.next()
				continue
			}
			n.Members = append(n.Members, p.parseMember())
			p.endElem("one field per line")
		}
		p.expect(ast.TokRBrace)
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parsePacked() *ast.Packed {
	n := &ast.Packed{}
	p.begin(&n.Base)
	p.next()
	if p.at(ast.TokLParen) && p.peekKind(1) == ast.TokInt {
		p.next()
		n.Align = p.basicLit()
		if v, ok := IntValue(n.Align.Raw); ok && v != 1 && v != 2 && v != 4 && v != 8 {
			p.syntaxErr(n.Align.Sp, "`packed(n)` takes 1, 2, 4 or 8")
		}
		p.expect(ast.TokRParen)
		switch {
		case p.at(ast.TokStruct), p.at(ast.TokLinear), p.at(ast.TokView), p.at(ast.TokMut), p.atWord("packed"), p.atWord("align"), p.atWord("plain"):
		default:
			p.expected("`struct`")
		}
	}
	p.finish(n, &n.Base)
	return n
}

// endElem ends a member, variant, statement or arm: a Term, the closing
// '}', or a line end that stands for the Term.
func (p *parser) endElem(oneMsg string) {
	switch {
	case p.at(ast.TokTerm):
		p.next()
	case p.at(ast.TokRBrace) || p.at(ast.TokEOF):
	case p.softTerm && p.lineEnd():
	default:
		if p.at(ast.TokSemi) && p.semiHabit() {
			break
		}
		if p.at(ast.TokDotDot) {
			oneMsg = rangeMsg
		}
		p.errHere(oneMsg)
		p.skip(stopStmt)
		p.accept(ast.TokTerm)
	}
	p.softTerm = false
	p.region()
}

func (p *parser) parseMember() ast.Member {
	if p.at(ast.TokInvariant) {
		n := &ast.InvariantMember{}
		p.begin(&n.Base)
		p.next()
		n.X = p.parseExpr()
		p.finish(n, &n.Base)
		return n
	}
	if h := p.memberHabit(); h != nil {
		return h
	}
	n := &ast.FieldMember{}
	p.begin(&n.Base)
	if p.accept(ast.TokExport) {
		n.Export = true
	}
	n.Name = p.parseDeclName(declField)
	if p.expect(ast.TokColon) {
		n.Type = p.parseType()
	} else {
		n.Type = p.badTypeSkipList()
	}
	if p.at(ast.TokComma) {
		p.errHere("one field per line")
		p.next()
		p.softTerm = p.lineEnd()
	}
	p.finish(n, &n.Base)
	return n
}

// badTypeSkipList is a BadType at the current position after an error.
func (p *parser) badTypeSkipList() ast.Type {
	s0 := p.p
	if !p.at(ast.TokTerm) && !p.at(ast.TokRBrace) && !p.at(ast.TokEOF) {
		p.skip(stopStmt)
	}
	return p.badTypeFrom(s0)
}

// parseEnum reads "enum" Name [ TypeParams ] [ ":" Type ] "{" { Variant Term } "}".
func (p *parser) parseEnum(start uint32) ast.Item {
	n := &ast.EnumDecl{}
	p.itemStart(&n.Base, start)
	p.next()
	n.Name = p.parseDeclName(declItem)
	if p.at(ast.TokLBracket) {
		n.TypeParams = p.parseTypeParams()
	}
	if p.accept(ast.TokColon) {
		n.Tag = p.parseType()
	}
	payload := false
	if p.expect(ast.TokLBrace) {
		for !p.at(ast.TokRBrace) && !p.at(ast.TokEOF) {
			if p.at(ast.TokTerm) {
				p.next()
				continue
			}
			v := p.parseVariant()
			payload = payload || v.HasFields
			n.Variants = append(n.Variants, v)
			if p.at(ast.TokComma) {
				p.errHere("one variant per line")
				p.next()
				p.softTerm = p.lineEnd()
			}
			p.endElem("one variant per line")
		}
		p.expect(ast.TokRBrace)
	}
	if payload && n.Tag != nil {
		p.syntaxErr(n.Tag.Span(), "an enum with payloads has no representation type")
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseVariant() *ast.Variant {
	n := &ast.Variant{}
	p.begin(&n.Base)
	n.Name = p.parseName("a variant name")
	if p.at(ast.TokLParen) {
		n.HasFields = true
		if p.peekKind(1) == ast.TokRParen {
			p.errHere("a variant with no fields has no parentheses")
		}
		n.Fields = p.parseFieldsParen()
	}
	if p.at(ast.TokAssign) {
		if n.HasFields {
			p.errHere("a variant has a payload or a discriminant, not both")
		}
		p.next()
		n.Value = p.parseExpr()
	}
	p.finish(n, &n.Base)
	return n
}

// parseFieldsParen reads "(" Fields ")" (trailing comma allowed).
func (p *parser) parseFieldsParen() []*ast.Field {
	p.expect(ast.TokLParen)
	saved := p.listCtx
	p.listCtx = true
	var out []*ast.Field
	for !p.at(ast.TokRParen) && !p.at(ast.TokEOF) {
		if p.at(ast.TokIdent) || p.kind().IsKeyword() {
			out = append(out, p.parseField())
		} else {
			p.expected("a field")
			p.skip(stopList)
		}
		if !p.at(ast.TokComma) {
			break
		}
		p.next()
		p.region()
	}
	p.listCtx = saved
	p.expect(ast.TokRParen)
	return out
}

func (p *parser) parseField() *ast.Field {
	n := &ast.Field{}
	p.begin(&n.Base)
	n.Name = p.parseDeclName(declField)
	if p.expect(ast.TokColon) {
		n.Type = p.parseType()
	} else {
		s0 := p.p
		p.skip(stopList)
		n.Type = p.badTypeFrom(s0)
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseErrorDecl(start uint32) ast.Item {
	n := &ast.ErrorDecl{}
	p.itemStart(&n.Base, start)
	p.next()
	n.Name = p.parseDeclName(declItem)
	if p.at(ast.TokLParen) {
		n.HasFields = true
		if p.peekKind(1) == ast.TokRParen {
			p.errHere("write `error " + p.in.Str(n.Name.Name) + "`")
		}
		n.Fields = p.parseFieldsParen()
	}
	p.finish(n, &n.Base)
	return n
}

// parseInterface reads "interface" Name [ TypeParams ] "{" { Signature Term } "}".
func (p *parser) parseInterface(start uint32) ast.Item {
	n := &ast.InterfaceDecl{}
	p.itemStart(&n.Base, start)
	p.next()
	n.Name = p.parseDeclName(declItem)
	if p.at(ast.TokLBracket) {
		n.TypeParams = p.parseTypeParams()
	}
	if p.expect(ast.TokLBrace) {
		for !p.at(ast.TokRBrace) && !p.at(ast.TokEOF) {
			if p.at(ast.TokTerm) {
				p.next()
				continue
			}
			if p.at(ast.TokExport) {
				p.errHere("interface members are not exported on their own")
				p.next()
			}
			s0 := p.p
			if !p.at(ast.TokIdent) && !p.memberHabitAhead() {
				p.expected("a method signature")
				p.skip(stopStmt)
				p.endElem("one member per line")
				continue
			}
			p.memberWordHabit()
			sig, _ := p.parseSignature(sigInterface)
			if sig.Name.Owner != nil {
				p.syntaxErr(sig.Name.Sp, "interface members have plain names")
			}
			for _, c := range sig.Contracts {
				p.syntaxErr(c.Sp, "contracts belong on the implementing functions")
				break
			}
			if p.at(ast.TokLBrace) || (p.isLineTerm() && p.peekKind(1) == ast.TokLBrace) {
				if p.at(ast.TokTerm) {
					p.next()
				}
				p.errHere("interface members have no body")
				p.parseBlock()
			}
			_ = s0
			n.Methods = append(n.Methods, sig)
			p.endElem("one member per line")
		}
		p.expect(ast.TokRBrace)
	}
	p.finish(n, &n.Base)
	return n
}

// parseImpl reads [ "unsafe" ] "impl" [ TypeParams ] Type "for" Type "{" {
// Func Term } "}".
func (p *parser) parseImpl(start uint32) ast.Item {
	n := &ast.ImplDecl{}
	p.itemStart(&n.Base, start)
	if p.accept(ast.TokUnsafe) {
		n.Unsafe = true
	}
	p.next()
	if p.at(ast.TokLBracket) {
		n.TypeParams = p.parseTypeParams()
	}
	n.Interface = p.parseType()
	if p.at(ast.TokLBrace) {
		return p.implBlockHabit(n)
	}
	if p.expect(ast.TokFor) {
		n.For = p.parseType()
	} else {
		n.For = p.badTypeFrom(p.p)
	}
	if p.expect(ast.TokLBrace) {
		for !p.at(ast.TokRBrace) && !p.at(ast.TokEOF) {
			if p.at(ast.TokTerm) {
				p.next()
				continue
			}
			if p.at(ast.TokExport) {
				p.errHere("impl methods are not exported on their own")
				p.next()
			}
			p.memberWordHabit()
			if !p.at(ast.TokIdent) {
				p.expected("a method")
				p.skip(stopStmt)
				p.endElem("one member per line")
				continue
			}
			fd := p.parseFunc(p.curStart(), sigImpl).(*ast.FuncDecl)
			if fd.Sig.Name.Owner != nil {
				p.syntaxErr(fd.Sig.Name.Sp, "impl methods have plain names")
			}
			n.Methods = append(n.Methods, fd)
			p.endElem("one member per line")
		}
		p.expect(ast.TokRBrace)
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseAlias(start uint32) ast.Item {
	n := &ast.AliasDecl{}
	p.itemStart(&n.Base, start)
	p.next()
	n.Name = p.parseDeclName(declItem)
	if p.at(ast.TokLBracket) {
		n.TypeParams = p.parseTypeParams()
	}
	if p.expect(ast.TokAssign) {
		n.Type = p.parseType()
	} else {
		n.Type = p.badTypeSkipList()
	}
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseConst(start uint32) ast.Item {
	n := &ast.ConstDecl{}
	p.itemStart(&n.Base, start)
	p.next()
	n.Name = p.parseDeclName(declItem)
	if p.accept(ast.TokColon) {
		n.Type = p.parseType()
	}
	if p.expect(ast.TokAssign) {
		n.Value = p.parseExpr()
	} else {
		n.Value = p.badExprSkip(stopStmt)
	}
	if p.atWord("fuel") {
		p.next()
		n.Fuel = p.parseExpr()
	}
	n.Section = p.sectionClause()
	p.finish(n, &n.Base)
	return n
}

func (p *parser) parseGlobal(start uint32) ast.Item {
	n := &ast.GlobalDecl{}
	p.itemStart(&n.Base, start)
	p.next()
	n.Name = p.parseDeclName(declItem)
	if p.accept(ast.TokColon) {
		n.Type = p.parseType()
	}
	if p.accept(ast.TokAssign) {
		n.Value = p.parseExpr()
		n.Section = p.sectionClause()
	}
	if n.Type == nil || n.Value == nil {
		p.syntaxErr(p.span(n.Sp.Start, p.lastEnd), "a top-level var needs a type and a value")
		if n.Type == nil {
			n.Type = p.badTypeFrom(p.p)
		}
		if n.Value == nil {
			n.Value = p.badExprFrom(p.p)
		}
	}
	p.finish(n, &n.Base)
	return n
}

// parseExtern reads "extern" Signature [ Block ], or an extern var.
func (p *parser) parseExtern(start uint32) ast.Item {
	if p.peekKind(1) == ast.TokVar {
		p.next()
		return p.parseExternVar(start)
	}
	n := &ast.ExternDecl{}
	p.itemStart(&n.Base, start)
	p.next()
	if p.nakedModifier() {
		p.next()
		n.Naked = true
	}
	sig, cont := p.parseSignature(sigExtern)
	n.Sig = sig
	n.Body = p.parseFuncBody(cont)
	if n.Body == nil && sig.Name.Owner != nil {
		p.syntaxErr(sig.Name.Sp, "a bodiless extern has a plain name")
	}
	if sig.TypeParams != nil {
		p.syntaxErr(sig.TypeParams.Sp, "an extern is not generic")
	}
	if !sig.Ellipsis.IsEmpty() && n.Body != nil {
		p.syntaxErr(sig.Ellipsis, "`...` is only for declaring a C variadic")
	}
	p.finish(n, &n.Base)
	return n
}

// parseFunc reads Signature Block | Signature.
func (p *parser) parseFunc(start uint32, ctx sigCtx) ast.Item {
	n := &ast.FuncDecl{}
	p.itemStart(&n.Base, start)
	sig, cont := p.parseSignature(ctx)
	n.Sig = sig
	if !sig.Ellipsis.IsEmpty() {
		p.syntaxErr(sig.Ellipsis, "`...` is only for declaring a C variadic")
	}
	allman := p.isLineTerm() && p.peekKind(1) == ast.TokLBrace && !cont
	n.Body = p.parseFuncBody(cont)
	if n.Body == nil && !allman && !p.tooDeep {
		switch {
		case ctx == sigImpl:
			p.syntaxErr(sig.Sp, "an impl method needs a body")
		case !p.opts.AllowBodiless:
			p.syntaxErr(sig.Sp, "function needs a body")
		}
	}
	p.finish(n, &n.Base)
	return n
}

// parseFuncBody reads the body after a signature: on its line, or on the
// next line after a continued signature; an Allman '{' after a
// one-line signature is one E0101 and is still read as the body.
func (p *parser) parseFuncBody(continued bool) *ast.Block {
	if p.at(ast.TokLBrace) {
		return p.parseBlock()
	}
	if p.isLineTerm() && p.peekKind(1) == ast.TokLBrace {
		p.next()
		if !continued {
			d := p.errHere("the body's `{` goes on the line of the signature")
			if d != nil {
				d.Alt("move `{` to the end of the signature's line")
			}
		}
		return p.parseBlock()
	}
	return nil
}

// parseBlockOrMissing reads a Block, or reports one missing and returns an
// empty Block at the current position.
func (p *parser) parseBlockOrMissing() *ast.Block {
	if p.at(ast.TokLBrace) {
		return p.parseBlock()
	}
	p.expected("`{`")
	b := &ast.Block{}
	p.begin(&b.Base)
	p.finishAt(b, &b.Base, b.Sp.Start)
	return b
}

// basicLit builds a BasicLit from the current literal token.
func (p *parser) basicLit() *ast.BasicLit {
	n := &ast.BasicLit{Kind: p.kind(), Raw: p.curText()}
	p.begin(&n.Base)
	p.next()
	p.finish(n, &n.Base)
	return n
}

var _ = diag.E0101
