// Why: systems code (kernels, boot code, drivers) needs inline assembly,
// naked functions, alignment, linker sections and external globals; these
// forms share contextual words and are kept apart from the core grammar.
//
// What: the systems productions: parseAsm with its operands (Asm,
// AsmOperand, AsmDir), the naked modifier, Aligned, the Section clause
// after a const or var initializer, and extern var (parseExternVar). The
// Cpu and Section clauses of a signature are in sig.go.
//
// How: asm is a keyword; naked, align and section are plain identifiers
// except in the exact token shapes tested here (nakedModifier,
// alignModifier, sectionClause), so `naked(...)` or `align(...)` stays a
// call of a function with that name.

package syntax

import (
	"mk/internal/ast"
)

// asmDirs maps the direction words of AsmDir to their kinds. in is the
// keyword TokIn; the others are identifiers read here only (04b).
var asmDirs = map[string]ast.AsmOperandKind{
	"out":       ast.AsmOut,
	"lateout":   ast.AsmLateOut,
	"inout":     ast.AsmInOut,
	"inlateout": ast.AsmInLateOut,
}

// parseAsm reads "asm" "(" String { "," AsmOperand } [ "," ] ")". The
// first argument must be a string literal: plain, raw or a """ text;
// a byte string or anything else is E0101.
func (p *parser) parseAsm() ast.Stmt {
	n := &ast.AsmStmt{}
	p.begin(&n.Base)
	p.next()
	if !p.at(ast.TokLParen) {
		p.expected("`(`")
		n.Template = p.missingLit()
		p.finish(n, &n.Base)
		return n
	}
	p.next()
	savedList, savedBracket := p.listCtx, p.inBracket
	p.listCtx, p.inBracket = false, false
	defer func() { p.listCtx, p.inBracket = savedList, savedBracket }()
	switch p.kind() {
	case ast.TokStr, ast.TokRawStr, ast.TokMultiStr:
		n.Template = p.basicLit()
	default:
		p.errHere("the first argument of asm is its template, a string literal")
		n.Template = p.missingLit()
		if !p.at(ast.TokComma) && !p.at(ast.TokRParen) {
			p.skip(stopList)
		}
	}
	for p.at(ast.TokComma) {
		p.next()
		p.region()
		if p.at(ast.TokRParen) {
			break
		}
		op := p.parseAsmOperand()
		if op != nil {
			n.Operands = append(n.Operands, op)
		}
		if !p.at(ast.TokComma) && !p.at(ast.TokRParen) && !p.at(ast.TokEOF) {
			p.expected("`,`", "`)`")
			p.skip(stopList)
		}
	}
	p.expect(ast.TokRParen)
	p.finish(n, &n.Base)
	return n
}

// missingLit is an empty string literal at the current token, for a
// required string that is absent.
func (p *parser) missingLit() *ast.BasicLit {
	lit := &ast.BasicLit{Kind: ast.TokStr}
	p.begin(&lit.Base)
	p.finishAt(lit, &lit.Base, lit.Sp.Start)
	return lit
}

// parseAsmOperand reads one AsmOperand; nil after an error that consumed
// the operand's tokens (the caller resumes at "," or ")").
func (p *parser) parseAsmOperand() *ast.AsmOperand {
	op := &ast.AsmOperand{}
	p.begin(&op.Base)
	if p.at(ast.TokIdent) && p.peekKind(1) == ast.TokColon {
		op.Label = p.ident()
		p.next()
	}
	switch {
	case p.at(ast.TokIn) && p.peekKind(1) == ast.TokLParen:
		op.Kind = ast.AsmIn
		p.asmDirection(op)
	case p.at(ast.TokIdent) && p.peekKind(1) == ast.TokLParen && asmDirs[p.curText()] != 0:
		op.Kind = asmDirs[p.curText()]
		p.asmDirection(op)
	case p.at(ast.TokConst):
		op.Kind = ast.AsmConst
		p.next()
		op.Value = p.parseExpr()
	case p.atWord("sym"):
		op.Kind = ast.AsmSym
		p.next()
		op.Sym = p.parsePath()
	case p.atWord("clobber_abi") && p.peekKind(1) == ast.TokLParen:
		op.Kind = ast.AsmClobberABI
		p.next()
		p.next()
		switch p.kind() {
		case ast.TokStr, ast.TokRawStr:
			op.ABI = p.basicLit()
		default:
			p.expected("an ABI name, a string")
			op.ABI = p.missingLit()
		}
		p.expect(ast.TokRParen)
	case p.atWord("options") && p.peekKind(1) == ast.TokLParen:
		op.Kind = ast.AsmOptions
		p.next()
		p.next()
		for !p.at(ast.TokRParen) && !p.at(ast.TokEOF) {
			op.Options = append(op.Options, p.parseName("an option"))
			if !p.accept(ast.TokComma) {
				break
			}
		}
		if len(op.Options) == 0 {
			p.expected("an option")
		}
		p.expect(ast.TokRParen)
	default:
		p.errHere("an asm operand is in, out, lateout, inout, inlateout, const, sym, clobber_abi or options")
		p.skip(stopList)
		return nil
	}
	if op.Label != nil && (op.Kind == ast.AsmClobberABI || op.Kind == ast.AsmOptions) {
		p.syntaxErr(op.Label.Sp, "clobber_abi and options take no label")
	}
	p.finish(op, &op.Base)
	return op
}

// asmDirection reads AsmDir "(" ( Name | String ) ")" ( Expr | "_" ), the
// direction word current.
func (p *parser) asmDirection(op *ast.AsmOperand) {
	p.next()
	p.next()
	switch p.kind() {
	case ast.TokIdent:
		op.Reg = p.ident()
	case ast.TokStr, ast.TokRawStr:
		op.Reg = p.basicLit()
	default:
		p.expected("a register class or a register name")
		if !p.at(ast.TokRParen) {
			p.skip(stopList)
		}
	}
	p.expect(ast.TokRParen)
	if p.at(ast.TokUnderscore) && (p.peekKind(1) == ast.TokComma || p.peekKind(1) == ast.TokRParen) {
		p.next()
		op.Discard = true
		return
	}
	if p.at(ast.TokComma) || p.at(ast.TokRParen) {
		p.expected("a value, a place or `_`")
		return
	}
	op.Value = p.parseExpr()
}

// alignModifier decides Aligned at item position: align "(" Int ")" followed
// by struct, linear, view, mut view or packed. Any other align "(" is a
// function named align (LL(3) after align, then one more).
func (p *parser) alignModifier() bool {
	if !p.atWord("align") || p.peekKind(1) != ast.TokLParen || p.peekKind(2) != ast.TokInt || p.peekKind(3) != ast.TokRParen {
		return false
	}
	switch k := p.peekKind(4); {
	case k == ast.TokStruct || k == ast.TokLinear || k == ast.TokView:
		return true
	case k == ast.TokMut && (p.peekKind(5) == ast.TokView || p.peekKind(5) == ast.TokStruct):
		return true
	case p.peekWord(4, "packed"):
		return true
	}
	return false
}

// parseAligned reads "align" "(" Int ")". The value's range is
// check's (E0706), not the parser's.
func (p *parser) parseAligned() *ast.Aligned {
	n := &ast.Aligned{}
	p.begin(&n.Base)
	p.next()
	p.next()
	n.Align = p.basicLit()
	p.expect(ast.TokRParen)
	p.finish(n, &n.Base)
	return n
}

// nakedModifier decides the naked modifier at item position: the
// identifier naked followed by a FuncName, so `naked(...)` stays a function
// named naked.
func (p *parser) nakedModifier() bool {
	return p.atWord("naked") && p.peekKind(1) == ast.TokIdent
}

// sectionClause reads [ Section ] after a const or var initializer:
// the identifier section followed by a string literal, which ends the
// expression since no expression continues with an identifier.
func (p *parser) sectionClause() *ast.BasicLit {
	if !p.atWord("section") {
		return nil
	}
	switch p.peekKind(1) {
	case ast.TokStr, ast.TokRawStr:
		p.next()
		return p.basicLit()
	}
	return nil
}

// parseExternVar reads "var" Name ":" Type after "extern": an
// external global with no value and no section.
func (p *parser) parseExternVar(start uint32) ast.Item {
	n := &ast.GlobalDecl{Extern: true}
	p.itemStart(&n.Base, start)
	p.next()
	n.Name = p.parseDeclName(declItem)
	if p.expect(ast.TokColon) {
		n.Type = p.parseType()
	} else {
		n.Type = p.badTypeSkipList()
	}
	if p.at(ast.TokAssign) {
		p.errHere("an extern var has no value: the linker script or a .s file defines it")
		p.skip(stopItem)
	}
	p.finish(n, &n.Base)
	return n
}
