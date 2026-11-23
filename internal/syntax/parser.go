// Why: every production file shares one parser state and the same helpers
// for tokens, nodes, errors and recovery; keeping them here keeps those
// rules in one place.
//
// What: Options, DefaultMaxDepth, the parser and fnCtx state, token access
// (tok, peek, at, next, lineEnd), node allocation (begin, finish) with the
// tree-depth limit, E0101 reporting (syntaxErr, expected) and recovery
// (skip, the Bad* node builders).
//
// How: the parser walks the significant tokens only (sig indexes into the
// lossless token list). Errors are quieted per region, so one mistake gives
// one E0101; recovery skips with the lexer's bracket rule to a statement or
// item start. A node deeper than the limit turns its item into a BadItem.

package syntax

import (
	"sort"
	"strconv"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
)

// DefaultMaxDepth is the nesting limit (a fixed bound, not tuned), counted
// in tree levels: the depth of a node is the number of nodes on the path from
// its item down to it, the item included. No tree the parser returns is
// deeper. Past it the item gets one E0101 "nesting too deep: more than N
// levels" (N the limit in force) and becomes an ast.BadItem, so neither the
// parser nor a later pass that recurses once per node can overflow the Go
// stack. The lexer has no limit: its bracket stack is a slice.
const DefaultMaxDepth = 1000

// Options says what a file is allowed to contain beyond user code.
type Options struct {
	// AllowBodiless accepts a function with no body outside interfaces and
	// bodiless externs. The driver sets it for std files; the corpus
	// sets it for the test prelude and for verdict: parse blocks. Elsewhere a
	// bodiless function is E0101 "function needs a body".
	AllowBodiless bool
	// MaxDepth overrides DefaultMaxDepth when positive (tests only); values
	// above 65535 are treated as 65535, the largest height a uint16 holds.
	MaxDepth int
}

// fnCtx is the innermost function body: a fn literal pushes a fresh
// one, so break inside it does not see the enclosing loops.
type fnCtx struct {
	outer    *fnCtx
	loops    int
	receiver bool
}

// parser is the state of one parse.
type parser struct {
	f        *source.File
	file     source.FileID
	text     string
	in       *source.Interner
	opts     Options
	maxDepth int

	toks []ast.Token
	sig  []uint32 // indices into toks of significant tokens, ending with EOF
	p    int      // index into sig of the current token

	lastEnd uint32 // end offset of the last consumed token other than a Term
	lastTok uint32 // toks index of that token

	nextID  ast.NodeID
	open    int
	height  []uint16
	tooDeep bool

	quiet    bool
	regionAt uint32
	lexAt    []uint32
	openAt   uint32
	reported map[uint32]bool

	// softTerm is set when a recovered habit or error left a line end that
	// stands for the statement's Term.
	softTerm bool

	diags []*diag.Diagnostic
	hits  []habitHit
	fn    *fnCtx

	// listCtx is true while a comma list (Params, FnParams, Fields, TypeArgs,
	// bracket and gen arguments) holds the current type: there a ',' after
	// a fn type's flow continues it only as more Sources.
	listCtx bool
	// inBracket is true inside a bracket argument, where '!' is the error
	// type operator rather than a stray suffix.
	inBracket bool
	// sigResult is set while the result type of a signature is read, so an
	// unparenthesized fn type there takes continuation lines.
	sigResult    bool
	sigContinued bool
	// inHead is true while the head of an if, for, match or scope is read,
	// where a '{' after a name opens the body, never a brace literal.
	inHead bool
	// litOK is true while a value (parseValue) is read, the one place a
	// brace literal habit is looked for; parseBlock clears it.
	litOK bool

	// coverOK is true when the next operand may open a RecordPat cover;
	// coverMarks are the cover-only forms read so far, coverPats the
	// patterns held for leading-'.' values, coverRests the lists ending "..".
	coverOK    bool
	coverMarks []coverMark
	coverPats  map[ast.NodeID]ast.Pattern
	coverRests map[ast.NodeID]bool

	// goSpans are the .go expressions read in the current statement; the
	// statement removes the one that is its whole expression.
	goSpans []source.Span

	// kwNames are the keywords read as names in the current item.
	kwNames []kwName

	inQuote int
}

func newParser(f *source.File, in *source.Interner, opts Options, lx *lexer) *parser {
	p := &parser{f: f, file: f.ID, text: f.Text, in: in, opts: opts, toks: lx.toks,
		lexAt: lx.lexAt, openAt: lx.openAt, reported: map[uint32]bool{}}
	p.maxDepth = DefaultMaxDepth
	if opts.MaxDepth > 0 {
		p.maxDepth = min(opts.MaxDepth, 65535)
	}
	p.sig = make([]uint32, 0, len(p.toks)/2+1)
	for i, t := range p.toks {
		if !t.Kind.IsTrivia() {
			p.sig = append(p.sig, uint32(i))
		}
	}
	p.height = make([]uint16, 1, len(p.sig)+2)
	sort.Slice(p.lexAt, func(i, j int) bool { return p.lexAt[i] < p.lexAt[j] })
	return p
}

// Token access.

func (p *parser) tok() ast.Token { return p.toks[p.sig[p.p]] }

func (p *parser) kind() ast.TokenKind { return p.toks[p.sig[p.p]].Kind }

func (p *parser) peek(k int) ast.Token {
	i := p.p + k
	if i >= len(p.sig) {
		i = len(p.sig) - 1
	}
	if i < 0 {
		i = 0
	}
	return p.toks[p.sig[i]]
}

func (p *parser) peekKind(k int) ast.TokenKind { return p.peek(k).Kind }

func (p *parser) at(k ast.TokenKind) bool { return p.kind() == k }

func (p *parser) tokText(t ast.Token) string { return p.text[t.Start : t.Start+t.Len] }

func (p *parser) curText() string { return p.tokText(p.tok()) }

// atWord tests an Ident by its text.
func (p *parser) atWord(s string) bool {
	t := p.tok()
	return t.Kind == ast.TokIdent && p.tokText(t) == s
}

func (p *parser) peekWord(k int, s string) bool {
	t := p.peek(k)
	return t.Kind == ast.TokIdent && p.tokText(t) == s
}

// next consumes the current token; it never moves past EOF.
func (p *parser) next() {
	if p.p >= len(p.sig)-1 {
		return
	}
	t := p.tok()
	if t.Kind != ast.TokTerm {
		p.lastEnd = t.End()
		p.lastTok = p.sig[p.p]
	}
	p.p++
}

func (p *parser) curStart() uint32 { return p.tok().Start }

func (p *parser) span(start, end uint32) source.Span {
	if end < start {
		end = start
	}
	return source.Span{File: p.file, Start: start, End: end}
}

func (p *parser) curSpan() source.Span { return p.tok().Span(p.file) }

// isLineTerm reports whether the current Term was made for a real newline.
func (p *parser) isLineTerm() bool {
	return p.at(ast.TokTerm) && IsLineTerm(p.toks, int(p.sig[p.p]))
}

// IsLineTerm reports whether toks[i] is a TokTerm the lexer produced for a
// real newline (the next token is TokNewline), as opposed to one synthesized
// before '}' or at end of file. Messages say "newline" only for a real one.
func IsLineTerm(toks []ast.Token, i int) bool {
	return i >= 0 && i+1 < len(toks) && toks[i].Kind == ast.TokTerm && toks[i+1].Kind == ast.TokNewline
}

// lineEnd is true when a newline lies between the previous significant token
// and the current one.
func (p *parser) lineEnd() bool {
	if p.p == 0 {
		return false
	}
	for i := p.sig[p.p-1] + 1; i < p.sig[p.p]; i++ {
		if p.toks[i].Kind == ast.TokNewline {
			return true
		}
	}
	return p.toks[p.sig[p.p-1]].Kind == ast.TokTerm && IsLineTerm(p.toks, int(p.sig[p.p-1]))
}

// Node allocation.

// begin numbers a node and opens it at the current token.
func (p *parser) begin(b *ast.Base) {
	p.beginAt(b, p.curStart())
}

func (p *parser) beginAt(b *ast.Base, start uint32) {
	p.nextID++
	b.NID = p.nextID
	b.Sp = source.Span{File: p.file, Start: start, End: start}
	p.open++
	if p.open > p.maxDepth {
		p.depthError()
	}
}

// finish closes a node at the end of the last consumed token and checks the
// height of the subtree under it.
func (p *parser) finish(n ast.Node, b *ast.Base) {
	p.finishAt(n, b, p.lastEnd)
}

func (p *parser) finishAt(n ast.Node, b *ast.Base, end uint32) {
	if end < b.Sp.Start {
		end = b.Sp.Start
	}
	b.Sp.End = end
	h := uint16(0)
	ast.EachChild(n, func(c ast.Node) {
		if id := int(c.ID()); id < len(p.height) && p.height[id] > h {
			h = p.height[id]
		}
	})
	if h < 65535 {
		h++
	}
	for int(b.NID) >= len(p.height) {
		p.height = append(p.height, 0)
	}
	p.height[b.NID] = h
	p.open--
	if p.open+int(h) > p.maxDepth {
		p.depthError()
	}
}

// adopt numbers a node built after the fact (a cover conversion) with an
// explicit span, as a finished node.
func (p *parser) adopt(n ast.Node, b *ast.Base, sp source.Span) {
	p.nextID++
	b.NID = p.nextID
	b.Sp = sp
	p.open++
	p.finishAt(n, b, sp.End)
}

func (p *parser) depthError() {
	if p.tooDeep {
		return
	}
	p.tooDeep = true
	d := diag.New(diag.E0101, p.curSpan(), "nesting too deep: more than "+strconv.Itoa(p.maxDepth)+" levels")
	p.diags = append(p.diags, d)
	p.quiet = true
}

// Diagnostics.

// syntaxErr reports an E0101 unless the current region is quiet, a lexical
// error in the region explains it, or it lies inside a bracket the lexer
// reported unclosed. It always quiets the region.
func (p *parser) syntaxErr(at source.Span, msg string, alts ...string) *diag.Diagnostic {
	drop := p.quiet || p.tooDeep || p.reported[at.Start]
	if !drop {
		i := sort.Search(len(p.lexAt), func(i int) bool { return p.lexAt[i] >= p.regionAt })
		if i < len(p.lexAt) && p.lexAt[i] <= at.End {
			drop = true
		}
	}
	if !drop && p.openAt != noOffset && at.Start >= p.openAt {
		drop = true
	}
	p.quiet = true
	if drop {
		return nil
	}
	d := diag.New(diag.E0101, at, msg)
	for _, a := range alts {
		d.Alt(a)
	}
	p.reported[at.Start] = true
	p.diags = append(p.diags, d)
	return d
}

// errHere reports an E0101 at the current token.
func (p *parser) errHere(msg string, alts ...string) *diag.Diagnostic {
	return p.syntaxErr(p.curSpan(), msg, alts...)
}

// region begins a new recovery region at the current token.
func (p *parser) region() {
	p.quiet = false
	p.regionAt = p.curStart()
}

// describe names the current token for "found".
func (p *parser) describe() string {
	t := p.tok()
	switch t.Kind {
	case ast.TokEOF:
		return "end of file"
	case ast.TokTerm:
		if p.isLineTerm() {
			return "newline"
		}
		if p.peekKind(1) == ast.TokRBrace {
			return "`}`"
		}
		return "end of file"
	}
	return "`" + p.tokText(t) + "`"
}

// expected reports "expected X, found Y" at the current token.
//
// A ".." found where something else was expected is rangeMsg's E0101
// instead: it stands outside a for head or a slice.
func (p *parser) expected(what ...string) {
	if p.at(ast.TokDotDot) {
		p.errHere(rangeMsg)
		return
	}
	msg := "expected " + joinOr(what) + ", found " + p.describe()
	d := p.errHere(msg)
	if d != nil {
		d.Fact("expected", diag.TextList(what...))
		d.Text("found", p.describe())
	}
}

func joinOr(what []string) string {
	switch len(what) {
	case 0:
		return "something else"
	case 1:
		return what[0]
	}
	s := ""
	for i, w := range what {
		switch {
		case i == 0:
		case i == len(what)-1:
			s += " or "
		default:
			s += ", "
		}
		s += w
	}
	return s
}

// expect consumes a token of kind k or reports it missing without consuming.
func (p *parser) expect(k ast.TokenKind) bool {
	if p.at(k) {
		p.next()
		return true
	}
	p.expected("`" + k.FixedText() + "`")
	return false
}

// accept consumes a token of kind k when it is current.
func (p *parser) accept(k ast.TokenKind) bool {
	if p.at(k) {
		p.next()
		return true
	}
	return false
}

// Recovery. Bracket depth is counted over sig with the lexer's rule:
// a closer pops down to its matching opener when one is open, and is ignored
// otherwise; with nothing open it is the enclosing closer.

type skipStop uint8

const (
	stopItem skipStop = iota
	stopStmt
	stopList
)

// skip advances to the synchronization point of its context and returns the
// toks range skipped. It always consumes at least one token unless already at
// the sync point.
func (p *parser) skip(mode skipStop) {
	var stack []ast.TokenKind
	first := true
	for !p.at(ast.TokEOF) {
		k := p.kind()
		if len(stack) == 0 {
			switch mode {
			case stopItem:
				if k == ast.TokTerm {
					return
				}
				if !first && p.lineEnd() && (itemStarter(k) || k == ast.TokMut && p.peekKind(1) == ast.TokView) {
					return
				}
			case stopStmt:
				if k == ast.TokTerm || k == ast.TokRBrace {
					return
				}
				if !first && p.lineEnd() && !p.toks[p.sig[p.p-1]].Kind.CanEndStatement() {
					p.softTerm = true
					return
				}
			case stopList:
				if k == ast.TokComma || k == ast.TokRParen || k == ast.TokRBracket || k == ast.TokRBrace {
					return
				}
			}
		}
		first = false
		switch k {
		case ast.TokLParen, ast.TokLBracket, ast.TokLBrace:
			stack = append(stack, k)
		case ast.TokRParen, ast.TokRBracket, ast.TokRBrace:
			open := ast.TokLParen
			if k == ast.TokRBracket {
				open = ast.TokLBracket
			} else if k == ast.TokRBrace {
				open = ast.TokLBrace
			}
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == open {
					stack = stack[:i]
					break
				}
			}
		}
		p.next()
	}
}

func itemStarter(k ast.TokenKind) bool {
	switch k {
	case ast.TokStruct, ast.TokEnum, ast.TokError, ast.TokInterface, ast.TokImpl, ast.TokType,
		ast.TokConst, ast.TokVar, ast.TokExtern, ast.TokLinear, ast.TokView, ast.TokExport,
		ast.TokImport, ast.TokGen, ast.TokTest, ast.TokUnsafe:
		return true
	}
	return false
}

// toksFrom is the toks range from sig index s0 to the last consumed token.
func (p *parser) toksFrom(s0 int) ast.TokRange {
	first := p.sig[s0]
	end := first
	if p.p > s0 {
		end = p.lastTok + 1
		if end < first {
			end = first
		}
	}
	return ast.TokRange{First: first, End: end}
}

// Bad nodes. Each covers the tokens from sig index s0 to the last consumed
// one; with nothing consumed it is empty at the current token.

func (p *parser) badExprFrom(s0 int) *ast.BadExpr {
	n := &ast.BadExpr{}
	p.newBad(&n.Base, s0)
	n.Toks = p.toksFrom(s0)
	p.closeBad(n, &n.Base, s0)
	return n
}

func (p *parser) badTypeFrom(s0 int) *ast.BadType {
	n := &ast.BadType{}
	p.newBad(&n.Base, s0)
	n.Toks = p.toksFrom(s0)
	p.closeBad(n, &n.Base, s0)
	return n
}

func (p *parser) badStmtFrom(s0 int) *ast.BadStmt {
	n := &ast.BadStmt{}
	p.newBad(&n.Base, s0)
	n.Toks = p.toksFrom(s0)
	p.closeBad(n, &n.Base, s0)
	return n
}

func (p *parser) badPatFrom(s0 int) *ast.BadPattern {
	n := &ast.BadPattern{}
	p.newBad(&n.Base, s0)
	n.Toks = p.toksFrom(s0)
	p.closeBad(n, &n.Base, s0)
	return n
}

func (p *parser) newBad(b *ast.Base, s0 int) {
	p.nextID++
	b.NID = p.nextID
	start := p.toks[p.sig[s0]].Start
	b.Sp = source.Span{File: p.file, Start: start, End: start}
	p.open++
}

func (p *parser) closeBad(n ast.Node, b *ast.Base, s0 int) {
	end := b.Sp.Start
	if p.p > s0 {
		end = p.lastEnd
	}
	p.finishAt(n, b, end)
}

// badExprSkip skips a malformed element in a list or statement and returns a
// BadExpr over it.
func (p *parser) badExprSkip(mode skipStop) *ast.BadExpr {
	s0 := p.p
	p.skip(mode)
	return p.badExprFrom(s0)
}

// ident builds an Ident from the current token and consumes it.
func (p *parser) ident() *ast.Ident {
	n := &ast.Ident{}
	p.begin(&n.Base)
	n.Name = p.in.Intern(p.curText())
	p.next()
	p.finish(n, &n.Base)
	return n
}

// missingIdent is an empty Ident at the current token, for a required name
// that is absent.
func (p *parser) missingIdent() *ast.Ident {
	n := &ast.Ident{}
	p.begin(&n.Base)
	p.finishAt(n, &n.Base, n.Sp.Start)
	return n
}

// parseName reads a name where a non-declaring name is required (a method
// after "Type.", a member, a label): an Ident, or a keyword reported E0102.
func (p *parser) parseName(what string) *ast.Ident {
	if p.at(ast.TokIdent) {
		return p.ident()
	}
	if p.kind().IsKeyword() {
		p.keywordAsName()
		return p.ident()
	}
	p.expected(what)
	return p.missingIdent()
}
