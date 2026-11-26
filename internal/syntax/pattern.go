// Why: match arms and destructures need patterns, and a destructure's left
// side is first read as an expression (the parser cannot know it is a
// pattern until ":=" follows), so it must be converted afterwards.
//
// What: the pattern productions (Pattern, Alt, PatFields, PatField):
// parsePattern, parseAlt, literal patterns with negative integers, range
// patterns lo..hi (half-open, an end may be open) with the "..=" habit
// E1220, and the destructure conversions recordPatOf and patternOf.
//
// How: an alternative may not bind, so checkAltBinds rejects a bare field
// name anywhere inside one. A destructure's cover value is turned into a
// pattern node by node, keeping its NodeIDs.

package syntax

import (
	"fmt"
	"strconv"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
)

// parsePattern reads Alt { "|" Alt }.
func (p *parser) parsePattern() ast.Pattern {
	if p.tooDeep {
		return p.badPatFrom(p.p)
	}
	start := p.curStart()
	x := p.parseAlt()
	if !p.at(ast.TokPipe) {
		return x
	}
	n := &ast.AltPattern{Alts: []ast.Pattern{x}}
	p.beginAt(&n.Base, start)
	for p.accept(ast.TokPipe) {
		n.Alts = append(n.Alts, p.parseAlt())
	}
	p.finish(n, &n.Base)
	for _, a := range n.Alts {
		if p.checkAltBinds(a) {
			break
		}
	}
	return n
}

// checkAltBinds reports a field written as a bare name anywhere inside an
// alternative: it would bind. The search goes through field
// sub-patterns, some/ok/err and nested alternatives, so `.A(x: .C(y)) | .B`
// is caught as well as `.A(x) | .B(x)`. It reports once and returns true
// when it did.
func (p *parser) checkAltBinds(a ast.Pattern) bool {
	var fields []*ast.PatField
	switch x := a.(type) {
	case *ast.VariantPattern:
		fields = x.Fields
	case *ast.PathPattern:
		fields = x.Fields
	case *ast.SomePattern:
		return x.Pat != nil && p.checkAltBinds(x.Pat)
	case *ast.OkPattern:
		return x.Pat != nil && p.checkAltBinds(x.Pat)
	case *ast.ErrPattern:
		return x.Pat != nil && p.checkAltBinds(x.Pat)
	case *ast.AltPattern:
		for _, b := range x.Alts {
			if p.checkAltBinds(b) {
				return true
			}
		}
	}
	for _, f := range fields {
		if f.Pat == nil {
			p.syntaxErr(f.Sp, "alternatives bind nothing")
			return true
		}
		if p.checkAltBinds(f.Pat) {
			return true
		}
	}
	return false
}

// parseAlt reads one Alt.
func (p *parser) parseAlt() ast.Pattern {
	if p.tooDeep {
		return p.badPatFrom(p.p)
	}
	switch k := p.kind(); {
	case k == ast.TokDot:
		n := &ast.VariantPattern{}
		p.begin(&n.Base)
		p.next()
		n.Name = p.parseName("a variant name")
		if p.at(ast.TokLParen) {
			n.HasFields = true
			n.Fields = p.parsePatFields()
		}
		p.finish(n, &n.Base)
		return n
	case k == ast.TokUnderscore:
		n := &ast.WildcardPattern{}
		p.begin(&n.Base)
		p.next()
		p.finish(n, &n.Base)
		return n
	case k.IsLiteral():
		return p.literalPattern(false)
	case k == ast.TokDotDot:
		return p.rangePattern(nil)
	case k == ast.TokMinus:
		switch p.peekKind(1) {
		case ast.TokInt:
			return p.literalPattern(true)
		case ast.TokFloat:
			p.errHere("a float literal is not a pattern")
			return p.literalPattern(true)
		}
	case k == ast.TokIdent:
		return p.identPattern()
	case k == ast.TokBad:
		s0 := p.p
		p.quiet = true
		p.next()
		return p.badPatFrom(s0)
	}
	p.expected("a pattern")
	s0 := p.p
	return p.badPatFrom(s0)
}

func (p *parser) literalPattern(neg bool) ast.Pattern {
	n := &ast.LiteralPattern{Neg: neg}
	p.begin(&n.Base)
	if neg {
		p.next()
	}
	n.Lit = p.basicLit()
	p.finish(n, &n.Base)
	if p.at(ast.TokDotDot) {
		return p.rangePattern(n)
	}
	return n
}

// rangePattern reads the rest of a range Alt at "..", lo its low end or
// nil: ( [ "-" ] Int | Char ) ".." [ [ "-" ] Int | Char ] or
// ".." ( [ "-" ] Int | Char ). An end of another kind is E0101, and "..=" is
// the habit E1220, whose fix writes the next value as the high end.
func (p *parser) rangePattern(lo *ast.LiteralPattern) ast.Pattern {
	n := &ast.RangePattern{Lo: lo}
	if lo != nil {
		p.beginAt(&n.Base, lo.Sp.Start)
		if !rangeEnd(lo) {
			p.syntaxErr(lo.Sp, "a range pattern's ends are integer or `char` literals")
		}
	} else {
		p.begin(&n.Base)
	}
	p.next()
	eq := p.inclusiveEq()
	if p.kind().IsLiteral() || p.at(ast.TokMinus) && p.peekKind(1) == ast.TokInt {
		neg := p.at(ast.TokMinus)
		hi := &ast.LiteralPattern{Neg: neg}
		p.begin(&hi.Base)
		if neg {
			p.next()
		}
		hi.Lit = p.basicLit()
		p.finish(hi, &hi.Base)
		n.Hi = hi
		if !rangeEnd(hi) {
			p.syntaxErr(hi.Sp, "a range pattern's ends are integer or `char` literals")
		}
	} else if lo == nil {
		p.expected("a range's high end, an integer or `char` literal")
	}
	p.finish(n, &n.Base)
	if eq != (source.Span{}) {
		p.inclusivePatternHabit(eq, n.Hi)
	}
	return n
}

// typeMax reports whether a range pattern's high end is the maximum of
// some integer type (127, 255, 32767, 65535, 2^31-1, 2^32-1, 2^63-1).
func typeMax(hi *ast.LiteralPattern) bool {
	if hi.Neg || hi.Lit.Kind != ast.TokInt {
		return false
	}
	v, ok := IntValue(hi.Lit.Raw)
	if !ok {
		return false
	}
	for _, bits := range []uint{8, 16, 32, 64} {
		if v == 1<<(bits-1)-1 || bits < 64 && v == 1<<bits-1 {
			return true
		}
	}
	return false
}

// rangeEnd reports whether an end of a range pattern is an Int or a Char
// literal, with a '-' only before an Int.
func rangeEnd(l *ast.LiteralPattern) bool {
	return l.Lit != nil && (l.Lit.Kind == ast.TokInt || l.Lit.Kind == ast.TokChar && !l.Neg)
}

// inclusivePatternHabit records E1220 for "..=" in a pattern: delete '=' and
// write the next value as the high end (`1..=9` is `1..10`, `'a'..='z'` is
// `'a'..'{'`). With no high end, or one whose next value has no literal,
// only the diagnostic is recorded.
func (p *parser) inclusivePatternHabit(eq source.Span, hi *ast.LiteralPattern) {
	h := habitHit{code: diag.E1220, at: p.span(eq.Start-2, eq.End), msg: "Mk ranges exclude the end: write the next value as the high end"}
	if hi != nil && rangeEnd(hi) {
		next := ""
		switch hi.Lit.Kind {
		case ast.TokInt:
			if v, ok := IntValue(hi.Lit.Raw); ok && v < 1<<63 {
				if hi.Neg {
					next = strconv.FormatInt(1-int64(v), 10)
				} else {
					next = strconv.FormatUint(v+1, 10)
				}
			}
		case ast.TokChar:
			if r, ok := CharValue(hi.Lit.Raw); ok && r < 0x10FFFF {
				r++
				if r == 0xD800 {
					r = 0xE000
				}
				next = fmt.Sprintf(`'\u{%X}'`, r)
				if r > ' ' && r < 0x7F && r != '\\' && r != '\'' {
					next = "'" + string(r) + "'"
				}
			}
		}
		switch {
		case next != "" && !typeMax(hi):
			h.edits = append(h.edits, diag.Edit{Span: eq}, p.edit(hi.Sp.Start, hi.Sp.End, next))
		case next != "":
			// The end may be the scrutinee type's maximum (255 of a u8),
			// whose next value is no value of it (E0303): which applies
			// needs the type, so neither is a proven fix.
			h.alts = append(h.alts, "if the high end is the type's maximum, leave it out: `..` reaches it",
				"otherwise write the next value, "+next+", as the high end")
		default:
			h.alts = append(h.alts, "leave the high end out: `..` reaches the type's maximum")
		}
	}
	p.hit(h)
}

// identPattern reads some(p), ok(p), err(p), none, a Path pattern, or a name.
func (p *parser) identPattern() ast.Pattern {
	text := p.curText()
	if p.peekKind(1) == ast.TokLParen {
		switch text {
		case "some", "ok", "err":
			var b ast.Base
			p.begin(&b)
			p.next()
			p.next()
			inner := p.parsePattern()
			p.expect(ast.TokRParen)
			switch text {
			case "some":
				n := &ast.SomePattern{Base: b, Pat: inner}
				p.finish(n, &n.Base)
				return n
			case "ok":
				n := &ast.OkPattern{Base: b, Pat: inner}
				p.finish(n, &n.Base)
				return n
			}
			n := &ast.ErrPattern{Base: b, Pat: inner}
			p.finish(n, &n.Base)
			return n
		}
	}
	if text == "none" && p.peekKind(1) != ast.TokDot {
		n := &ast.NonePattern{}
		p.begin(&n.Base)
		p.next()
		p.finish(n, &n.Base)
		return n
	}
	if p.peekKind(1) == ast.TokDot || p.peekKind(1) == ast.TokLParen || p.peekKind(1) == ast.TokColonColon {
		n := &ast.PathPattern{}
		p.begin(&n.Base)
		n.Path = p.parsePath()
		if p.at(ast.TokLBracket) {
			s0 := p.p
			p.errHere("a pattern takes no type arguments")
			p.skipBalanced()
			_ = s0
		}
		if p.at(ast.TokLParen) {
			n.HasFields = true
			n.Fields = p.parsePatFields()
		}
		p.finish(n, &n.Base)
		return n
	}
	n := &ast.NamePattern{}
	p.begin(&n.Base)
	n.Name = p.ident()
	p.finish(n, &n.Base)
	return n
}

// skipBalanced skips one bracketed group starting at the current opener.
func (p *parser) skipBalanced() {
	depth := 0
	for !p.at(ast.TokEOF) {
		switch p.kind() {
		case ast.TokLParen, ast.TokLBracket, ast.TokLBrace:
			depth++
		case ast.TokRParen, ast.TokRBracket, ast.TokRBrace:
			depth--
		}
		p.next()
		if depth <= 0 {
			return
		}
	}
}

// parsePatFields reads "(" PatField { "," PatField } [ "," ] ")".
func (p *parser) parsePatFields() []*ast.PatField {
	p.next()
	var out []*ast.PatField
	for !p.at(ast.TokRParen) && !p.at(ast.TokEOF) {
		switch {
		case p.at(ast.TokDotDot):
			p.errHere("`..` is only for a destructure")
			p.next()
		case p.at(ast.TokIdent) || p.kind().IsKeyword():
			out = append(out, p.parsePatField())
		default:
			p.errHere("patterns name fields: `Rect(w: 1.0, h: 2.0)`")
			for p.skip(stopList); p.accept(ast.TokComma); {
				p.skip(stopList)
			}
		}
		if !p.accept(ast.TokComma) {
			break
		}
		p.region()
	}
	if !p.expect(ast.TokRParen) {
		p.skip(stopList)
		p.accept(ast.TokRParen)
	}
	return out
}

// parsePatField reads Name [ ":" Pattern ].
func (p *parser) parsePatField() *ast.PatField {
	n := &ast.PatField{}
	p.begin(&n.Base)
	n.Name = p.parseName("a field name")
	if p.accept(ast.TokColon) {
		n.Pat = p.parsePattern()
	} else {
		p.checkDeclared(n.Name, declLocal)
	}
	p.finish(n, &n.Base)
	return n
}

// Destructures.

// parseDestructure reinterprets the statement head x as a RecordPat when
// ":=" follows it.
func (p *parser) parseDestructure(x ast.Expr, start uint32, isVar bool) ast.Stmt {
	n := &ast.DestructureStmt{Var: isVar, Reinterpreted: true}
	p.beginAt(&n.Base, start)
	n.Pat = p.recordPatOf(x)
	p.next()
	n.Value = p.parseValue() // a destructure is a binding: its right side is a value position
	p.finish(n, &n.Base)
	return n
}

// recordPatOf converts a call, record literal or array literal to a RecordPat.
func (p *parser) recordPatOf(x ast.Expr) *ast.RecordPat {
	rp := &ast.RecordPat{}
	sp := x.Span()
	var id ast.NodeID
	switch y := x.(type) {
	case *ast.CallExpr:
		id = y.NID
		switch f := y.Fun.(type) {
		case *ast.Ident:
			rp.Path = p.pathOf([]*ast.Ident{f}, f.Sp)
		case *ast.FieldExpr:
			if names, ok := fieldChain(f); ok {
				rp.Path = p.pathOf(names, f.Sp)
			} else {
				p.syntaxErr(f.Sp, "a destructure names a type")
			}
		case *ast.IndexExpr:
			p.syntaxErr(f.Sp, "a destructure takes no type arguments")
		default:
			p.syntaxErr(y.Fun.Span(), "a destructure names a type")
		}
		for _, a := range y.Args {
			if f := p.patFieldOfArg(a.Name, a.Value, a.NID, a.Sp); f != nil {
				rp.Fields = append(rp.Fields, f)
			}
		}
	case *ast.RecordLit:
		id = y.NID
		for _, f := range y.Fields {
			var pf *ast.PatField
			if f.Value == nil {
				pf = &ast.PatField{Name: f.Name}
				p.checkDeclared(f.Name, declLocal)
				p.adopt(pf, &pf.Base, f.Sp)
			} else {
				pf = p.patFieldOfArg(f.Name, f.Value, f.NID, f.Sp)
			}
			if pf != nil {
				rp.Fields = append(rp.Fields, pf)
			}
		}
	case *ast.ArrayLit:
		// [a, b] := e binds element i of a fixed array to the i-th name.
		id = y.NID
		rp.Elems = true
		for _, x := range y.Elems {
			v, ok := x.(*ast.Ident)
			if !ok {
				p.syntaxErr(x.Span(), "an array destructure binds one name per element")
				continue
			}
			pf := &ast.PatField{Name: v}
			p.checkDeclared(v, declLocal)
			p.adopt(pf, &pf.Base, v.Sp)
			rp.Fields = append(rp.Fields, pf)
		}
	default:
		p.syntaxErr(sp, "expected a name or a destructure before `:=`")
	}
	if p.coverRests[id] {
		rp.Rest = true
	}
	// A struct with no fields is taken apart by `T() := e`, which names
	// every field; any other pattern binds at least one.
	if len(rp.Fields) == 0 && (rp.Path == nil || rp.Rest) {
		p.syntaxErr(sp, "nothing to bind")
	}
	p.adopt(rp, &rp.Base, sp)
	return rp
}

// pathOf builds the Path that names spell, at sp, the span of the Ident or
// FieldExpr chain they came from (see pathTypeOf: a missing last name sits
// past that expression's end).
func (p *parser) pathOf(names []*ast.Ident, sp source.Span) *ast.Path {
	path := &ast.Path{Names: names}
	p.adopt(path, &path.Base, sp)
	return path
}

// patFieldOfArg converts one argument or field of the cover.
func (p *parser) patFieldOfArg(name *ast.Ident, value ast.Expr, id ast.NodeID, sp source.Span) *ast.PatField {
	if name == nil {
		if v, ok := value.(*ast.Ident); ok {
			pf := &ast.PatField{Name: v}
			p.checkDeclared(v, declLocal)
			p.adopt(pf, &pf.Base, sp)
			return pf
		}
		p.syntaxErr(value.Span(), "a positional field binds a name")
		return nil
	}
	pat := p.patternOf(value, id)
	pf := &ast.PatField{Name: name, Pat: pat}
	p.adopt(pf, &pf.Base, sp)
	return pf
}

// patternOf converts a cover value to a pattern.
func (p *parser) patternOf(x ast.Expr, owner ast.NodeID) ast.Pattern {
	if pat, ok := p.coverPats[owner]; ok {
		return pat
	}
	switch y := x.(type) {
	case *ast.Ident:
		switch y.Name {
		case source.SymNone:
			n := &ast.NonePattern{}
			p.adopt(n, &n.Base, y.Sp)
			return n
		case source.SymUnderscore:
			n := &ast.WildcardPattern{}
			p.adopt(n, &n.Base, y.Sp)
			return n
		}
		if p.in.Str(y.Name) == "_" {
			n := &ast.WildcardPattern{}
			p.adopt(n, &n.Base, y.Sp)
			return n
		}
		p.checkDeclared(y, declLocal)
		n := &ast.NamePattern{Name: y}
		p.adopt(n, &n.Base, y.Sp)
		return n
	case *ast.BasicLit:
		n := &ast.LiteralPattern{Lit: y}
		p.adopt(n, &n.Base, y.Sp)
		return n
	case *ast.UnaryExpr:
		if lit, ok := y.X.(*ast.BasicLit); ok && y.Op == ast.TokMinus && lit.Kind == ast.TokInt {
			n := &ast.LiteralPattern{Neg: true, Lit: lit}
			p.adopt(n, &n.Base, y.Sp)
			return n
		}
	case *ast.FieldExpr:
		if names, ok := fieldChain(y); ok {
			n := &ast.PathPattern{Path: p.pathOf(names, y.Sp)}
			p.adopt(n, &n.Base, y.Sp)
			return n
		}
	case *ast.CallExpr:
		if id, ok := y.Fun.(*ast.Ident); ok && len(y.Args) == 1 && y.Args[0].Name == nil {
			switch p.in.Str(id.Name) {
			case "some":
				n := &ast.SomePattern{Pat: p.patternOf(y.Args[0].Value, y.Args[0].NID)}
				p.adopt(n, &n.Base, y.Sp)
				return n
			case "ok":
				n := &ast.OkPattern{Pat: p.patternOf(y.Args[0].Value, y.Args[0].NID)}
				p.adopt(n, &n.Base, y.Sp)
				return n
			case "err":
				n := &ast.ErrPattern{Pat: p.patternOf(y.Args[0].Value, y.Args[0].NID)}
				p.adopt(n, &n.Base, y.Sp)
				return n
			}
		}
		var names []*ast.Ident
		switch f := y.Fun.(type) {
		case *ast.Ident:
			names = []*ast.Ident{f}
		case *ast.FieldExpr:
			names, _ = fieldChain(f)
		}
		if names != nil {
			n := &ast.PathPattern{Path: p.pathOf(names, y.Fun.Span()), HasFields: true}
			for _, a := range y.Args {
				if f := p.patFieldOfArg(a.Name, a.Value, a.NID, a.Sp); f != nil {
					n.Fields = append(n.Fields, f)
				}
			}
			p.adopt(n, &n.Base, y.Sp)
			return n
		}
	case *ast.BinaryExpr:
		if y.Op == ast.TokPipe {
			n := &ast.AltPattern{}
			left := p.patternOf(y.X, 0)
			if l, ok := left.(*ast.AltPattern); ok {
				n.Alts = append(n.Alts, l.Alts...)
			} else {
				n.Alts = append(n.Alts, left)
			}
			n.Alts = append(n.Alts, p.patternOf(y.Y, 0))
			p.adopt(n, &n.Base, y.Sp)
			p.checkAltBinds(n) // as parsePattern does
			return n
		}
	}
	p.syntaxErr(x.Span(), "expected a pattern")
	n := &ast.BadPattern{}
	p.adopt(n, &n.Base, x.Span())
	return n
}
