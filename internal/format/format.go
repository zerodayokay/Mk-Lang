// Why: mk fmt gives every Mk file one format with no options, so formatting
// must be deterministic, idempotent and unable to change what a file means.
//
// What: Source (format one file's bytes), Tokens (format a token list),
// SyntaxError and GuardError, and the passes behind them: scan, classify,
// dropEmptyRecords, layout, space, fuses, render and guard.
//
// How: scan turns the lexer's tokens with trivia into items, classify marks
// unary operators and type brackets, layout computes each line's indentation
// and space each gap's spacing, and render aligns trailing comments.
// dropEmptyRecords removes the empty record of a Print call whose string has
// no holes. guard re-lexes the output and Source re-parses it before anything
// is returned.

// Package format is mk fmt's formatter. The output is a function of three
// things only: the significant tokens (with the newline rule's terminators),
// the comment texts, and for each gap between two of them whether it holds a
// line break and whether it holds a blank line. No other byte of the input's
// whitespace reaches the output, so formatting the output again gives the
// output (idempotence by construction; TestFormatIdempotent* and FuzzFormat
// check it). Line breaks are never added or removed, only blank lines, so the
// terminators are unchanged; a guard re-lexes the output and refuses to
// return text whose tokens differ, and Source re-parses it, since the parser
// also reads adjacency.
package format

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"mk/internal/ast"
	"mk/internal/source"
	"mk/internal/syntax"
)

// Indent is one level of indentation: four spaces, std's style (no tabs).
const Indent = "    "

// SyntaxError is a file mk fmt refuses because it does not parse. Format
// never rewrites such a file; the driver reports the diagnostics itself.
type SyntaxError struct {
	Count int    // parse-phase diagnostics
	First string // "line:col: message" of the first
}

func (e *SyntaxError) Error() string {
	if e.Count == 1 {
		return "syntax error at " + e.First
	}
	return fmt.Sprintf("%d syntax errors, the first at %s", e.Count, e.First)
}

// GuardError is an internal failure: the formatted text would not lex to the
// input's tokens. The file is left as it was (an ICE, E0000, to the driver).
type GuardError struct{ Msg string }

func (e *GuardError) Error() string { return "mk fmt internal error: " + e.Msg }

// Source formats one file's bytes. path is used only in messages. A file
// that does not lex and parse cleanly is a *SyntaxError; a guard failure is
// a *GuardError. The result ends with one newline (an empty file stays
// empty) and uses LF line ends.
func Source(path string, raw []byte) ([]byte, error) {
	text := source.Normalize(raw)
	sm := source.NewSourceMap()
	f := sm.Add(cleanPath(path), []byte(text))
	file, ds := syntax.ParseFile(f, source.NewInterner(), syntax.Options{AllowBodiless: true})
	if len(ds) > 0 {
		line, col := lineCol(text, ds[0].At.Start)
		return nil, &SyntaxError{Count: len(ds), First: fmt.Sprintf("%d:%d: %s", line, col, ds[0].Message)}
	}
	out, err := tokens(text, file.Tokens, flowParens(file))
	if err != nil {
		return nil, err
	}
	// The token guard compares kinds and texts, so it cannot see a change of
	// adjacency the parser gives meaning to: the output of a file that
	// parsed must parse too, or nothing is returned.
	if _, ds := syntax.ParseFile(source.NewSourceMap().Add(cleanPath(path), out), source.NewInterner(), syntax.Options{AllowBodiless: true}); len(ds) > 0 {
		line, col := lineCol(string(out), ds[0].At.Start)
		return nil, &GuardError{Msg: fmt.Sprintf("the formatted text does not parse: %d:%d: %s", line, col, ds[0].Message)}
	}
	return out, nil
}

// Tokens formats text from its token list (syntax.Lex's or an ast.File's,
// trivia included). It does not check that the text parses.
func Tokens(text string, toks []ast.Token) ([]byte, error) {
	return tokens(text, toks, nil)
}

// tokens is Tokens with the token indexes of the "(" that opens a per-field
// clause, which the parse decided: `from (a: x)` keeps its space,
// where a call `from(a: x)` has none.
func tokens(text string, toks []ast.Token, flowParen map[int]bool) ([]byte, error) {
	items, removed := scan(text, toks)
	for i := range items {
		if flowParen[items[i].tok] {
			items[i].flowParen = true
		}
	}
	out := layout(items)
	if err := guard(text, toks, out, removed); err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// flowParens finds the "(" of every per-field clause `from (f: a)` in the
// parsed file: the first "(" token inside each FromFlow with Fields.
func flowParens(file *ast.File) map[int]bool {
	var out map[int]bool
	ast.Inspect(file, func(n ast.Node) bool {
		ff, ok := n.(*ast.FromFlow)
		if !ok || len(ff.Fields) == 0 {
			return true
		}
		for i, t := range file.Tokens {
			if t.Start >= ff.Sp.Start && t.Start < ff.Sp.End && t.Kind == ast.TokLParen {
				if out == nil {
					out = map[int]bool{}
				}
				out[i] = true
				break
			}
		}
		return true
	})
	return out
}

func cleanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" {
		return "input.mk"
	}
	return p
}

func lineCol(text string, off uint32) (int, int) {
	if int(off) > len(text) {
		off = uint32(len(text))
	}
	before := text[:off]
	line := strings.Count(before, "\n") + 1
	col := int(off) - strings.LastIndexByte(before, '\n')
	return line, col
}

// item is one significant token or comment, with what the gap before it
// held.
type item struct {
	kind ast.TokenKind
	text string
	tok  int  // index in the token list
	nl   int  // 0: same line; 1: a line break; 2: a blank line before it
	term bool // a terminator lies between the previous significant item and this one
	// unary: an operator in prefix position (- + * & ! ~ ? $ with no
	// operand-ending token on its left); infix !: the error union E!T.
	unary bool
	// typeClose: a ] closing a [ that had no operand on its left (a slice
	// or array type prefix, or an array literal): what follows is a type.
	typeClose bool
	// tight: a | of an error set before ! (A|B!T), written with no spaces.
	tight bool
	// asm: inside the parentheses of an asm(...) statement, where an
	// operand's in(reg) is written as a call is.
	asm bool
	// flowParen: the "(" of a per-field clause, `from (a: x)`.
	flowParen bool
}

func isComment(k ast.TokenKind) bool { return k == ast.TokComment || k == ast.TokDocComment }

// scan builds the item list and applies dropEmptyRecords. removed holds the
// token indexes it dropped.
func scan(text string, toks []ast.Token) ([]item, map[int]bool) {
	items := make([]item, 0, len(toks)) // one allocation: at most one item per token
	nl, term, termSinceSig := 0, false, false
	for i, t := range toks {
		switch t.Kind {
		case ast.TokEOF:
			continue
		case ast.TokWhitespace:
			continue
		case ast.TokNewline:
			nl++
			continue
		case ast.TokTerm:
			term, termSinceSig = true, true
			continue
		}
		it := item{kind: t.Kind, text: text[t.Start : t.Start+t.Len], tok: i, nl: min(nl, 2)}
		if it.kind == ast.TokFields { // the parser's retag of the word fields
			it.kind = ast.TokIdent
		}
		if isComment(t.Kind) {
			it.text = strings.TrimRight(it.text, " \t")
			it.term = termSinceSig
		} else {
			it.term = termSinceSig || term
			termSinceSig = false
		}
		term = false
		nl = 0
		items = append(items, it)
	}
	removed := dropEmptyRecords(&items)
	classify(items)
	return items, removed
}

// operandEnd reports whether a token of kind k (already classified) ends an
// operand, so that a following - + * & ! is binary. A } on the same line
// ends one: an if or match used as an operand (`match n { .. } + 1`); a
// block that ends a statement has nothing after it on its line.
func operandEnd(it *item) bool {
	switch it.kind {
	case ast.TokIdent, ast.TokUnderscore, ast.TokLifetime, ast.TokRParen, ast.TokRBrace:
		return true
	case ast.TokRBracket:
		return !it.typeClose
	}
	return it.kind.IsLiteral()
}

func isPrefixable(k ast.TokenKind) bool {
	switch k {
	case ast.TokMinus, ast.TokPlus, ast.TokStar, ast.TokAmp, ast.TokAmpAmp, ast.TokBang, ast.TokTilde, ast.TokQuestion, ast.TokDollar:
		return true
	}
	return false
}

// classify sets unary and typeClose, left to right over significant items.
func classify(items []item) {
	var opens []bool // per open [: true when it had no operand on its left
	var prev *item
	asmDepth := 0 // paren depth inside asm(...), 0 outside
	for i := range items {
		it := &items[i]
		if isComment(it.kind) {
			continue
		}
		switch {
		case asmDepth > 0:
			it.asm = true
			if it.kind == ast.TokLParen {
				asmDepth++
			} else if it.kind == ast.TokRParen {
				asmDepth--
			}
		case it.kind == ast.TokLParen && prev != nil && prev.kind == ast.TokAsm:
			asmDepth = 1
		}
		// A token after a terminator starts a statement: prefix position.
		after := prev != nil && operandEnd(prev) && !it.term
		switch it.kind {
		case ast.TokLBracket:
			// impl[T] opens generic parameters, not a type prefix.
			opens = append(opens, !after && (prev == nil || prev.kind != ast.TokImpl))
		case ast.TokRBracket:
			if n := len(opens); n > 0 {
				it.typeClose = opens[n-1]
				opens = opens[:n-1]
			}
		case ast.TokQuestion, ast.TokDollar, ast.TokTilde:
			it.unary = true
		case ast.TokPipe:
			it.tight = errorSetPipe(items, i)
		case ast.TokStar:
			// A flow clause's *x into y follows a signature's ).
			it.unary = !after || flowStar(items, i)
		default:
			if isPrefixable(it.kind) {
				it.unary = !after
			}
		}
		prev = it
	}
}

// errorSetPipe reports whether the | at i joins error names before the !
// of an error union type: Name{.Name} { | Name{.Name} } !.
func errorSetPipe(items []item, i int) bool {
	j := i + 1
	for {
		if j >= len(items) || items[j].kind != ast.TokIdent {
			return false
		}
		j++
		for j+1 < len(items) && items[j].kind == ast.TokDot && items[j+1].kind == ast.TokIdent {
			j += 2
		}
		if j >= len(items) {
			return false
		}
		switch items[j].kind {
		case ast.TokBang:
			return true
		case ast.TokPipe:
			j++
		default:
			return false
		}
	}
}

// flowStar reports whether the * at i starts a flow clause: * Name into.
func flowStar(items []item, i int) bool {
	return i+2 < len(items) && items[i+1].kind == ast.TokIdent && items[i+2].kind == ast.TokIdent &&
		items[i+2].text == "into" && items[i+1].nl == 0
}

// dropEmptyRecords: in Print(w, "s", ()) the ", ()" goes when the
// callee is literally Print (not a selector), the last argument is the
// empty record, the argument before it is one string literal whose decoded
// text holds no {name} hole and no scan problem (the checker's rule, so
// "{{x}}" loses its record), and no comment sits among the removed
// tokens.
//
// The formatter sees tokens, not names, so it rewrites only where the
// callee cannot be anything but the prelude's Print. A module-level name
// may hide a prelude name, so a file that spells Print
// anywhere other than as a call inside a body (a declaration at brace
// depth 0, a binding, a parameter) keeps every record. A .Fmt selector is
// never rewritten: only Arena.Fmt takes the short form, and the receiver's
// type is not in the tokens.
func dropEmptyRecords(items *[]item) map[int]bool {
	in := *items
	removed := map[int]bool{}
	if printHidden(in) {
		return removed
	}
	var keep []item
	skip := map[int]bool{}
	for i := 0; i < len(in); i++ {
		if !isCallee(in, i) || i+1 >= len(in) || in[i+1].kind != ast.TokLParen {
			continue
		}
		j := matchParen(in, i+1)
		if j < 0 || j-5 < i+1 {
			continue
		}
		if in[j-1].kind != ast.TokRParen || in[j-2].kind != ast.TokLParen || in[j-3].kind != ast.TokComma {
			continue
		}
		s := in[j-4]
		if !holeless(s.kind, s.text) || (in[j-5].kind != ast.TokComma && in[j-5].kind != ast.TokLParen) {
			continue
		}
		for _, k := range []int{j - 3, j - 2, j - 1} {
			skip[k] = true
			removed[in[k].tok] = true
		}
	}
	if len(skip) == 0 {
		return removed
	}
	for i, it := range in {
		if skip[i] {
			continue
		}
		keep = append(keep, it)
	}
	*items = keep
	return removed
}

func isCallee(in []item, i int) bool {
	return in[i].kind == ast.TokIdent && in[i].text == "Print" && (i == 0 || in[i-1].kind != ast.TokDot)
}

// printHidden reports whether the file may give the bare name Print another
// meaning: a Print not after a dot and not followed by "(" (a binding, a
// parameter, a generic Print[T]), or one followed by "(" at brace depth 0
// (a declaration). Every call inside a body is at depth 1 or more.
func printHidden(in []item) bool {
	depth := 0
	for i := range in {
		switch in[i].kind {
		case ast.TokLBrace:
			depth++
		case ast.TokRBrace:
			depth--
		}
		if !isCallee(in, i) {
			continue
		}
		if i+1 >= len(in) || in[i+1].kind != ast.TokLParen || depth <= 0 {
			return true
		}
	}
	return false
}

// holeless reports whether the string literal tok of kind k decodes to a
// format string with no hole and no scan problem: the checker's scanFormat
// (check/format.go) over the decoded text, where \x7B and \u{7D} are
// braces. Anything this cannot decode is not holeless.
func holeless(k ast.TokenKind, tok string) bool {
	var body string
	escapes := true
	switch {
	case k == ast.TokStr && len(tok) >= 2:
		body = tok[1 : len(tok)-1]
	case k == ast.TokRawStr && len(tok) >= 3:
		body, escapes = tok[2:len(tok)-1], false
	case k == ast.TokMultiStr && len(tok) >= 6:
		body = tok[3 : len(tok)-3]
	default:
		return false
	}
	text, ok := []byte(body), true
	if escapes {
		text, ok = decodeBraces(body)
	}
	return ok && formatClean(text)
}

// decodeBraces decodes the escapes of Mk's string literals
// (\n \r \t \0 \\ \' \", \xHH, \u{H}) as far as braces go: an
// escape that names { or } gives it, any other a placeholder byte. An
// unknown or malformed escape gives ok false.
func decodeBraces(s string) ([]byte, bool) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		if i+1 >= len(s) {
			return nil, false
		}
		i++
		var v uint64
		switch s[i] {
		case 'n', 'r', 't', '0', '\\', '\'', '"':
			out = append(out, 'x')
			continue
		case 'x':
			if i+2 >= len(s) {
				return nil, false
			}
			n, ok := hexVal(s[i+1 : i+3])
			if !ok {
				return nil, false
			}
			v, i = n, i+2
		case 'u':
			end := strings.IndexByte(s[i:], '}')
			if i+1 >= len(s) || s[i+1] != '{' || end < 2 {
				return nil, false
			}
			n, ok := hexVal(s[i+2 : i+end])
			if !ok {
				return nil, false
			}
			v, i = n, i+end
		default:
			return nil, false
		}
		if v == '{' || v == '}' {
			out = append(out, byte(v))
		} else {
			out = append(out, 'x')
		}
	}
	return out, true
}

func hexVal(h string) (uint64, bool) {
	if h == "" || len(h) > 6 {
		return 0, false
	}
	var v uint64
	for i := 0; i < len(h); i++ {
		c := h[i]
		switch {
		case c >= '0' && c <= '9':
			v = v*16 + uint64(c-'0')
		case c >= 'a' && c <= 'f':
			v = v*16 + uint64(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v*16 + uint64(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

// formatClean is the checker's scanFormat reduced to its verdict: true when
// f holds no {name} hole and no lone brace. {{ and }} are escaped braces; a
// single { or } is a hole or a scan problem, either way not clean.
func formatClean(f []byte) bool {
	for i := 0; i < len(f); i++ {
		if c := f[i]; c == '{' || c == '}' {
			if i+1 < len(f) && f[i+1] == c {
				i++
				continue
			}
			return false
		}
	}
	return true
}

// matchParen returns the index of the ) closing the ( at i, or -1; a
// comment anywhere inside makes it -1 (nothing is removed near a comment).
func matchParen(in []item, i int) int {
	depth := 0
	for j := i; j < len(in); j++ {
		switch in[j].kind {
		case ast.TokLParen, ast.TokLBracket, ast.TokLBrace:
			depth++
		case ast.TokRParen, ast.TokRBracket, ast.TokRBrace:
			depth--
			if depth == 0 {
				if in[j].kind != ast.TokRParen {
					return -1
				}
				return j
			}
		case ast.TokComment, ast.TokDocComment:
			return -1
		}
	}
	return -1
}

func isOpen(k ast.TokenKind) bool {
	return k == ast.TokLParen || k == ast.TokLBracket || k == ast.TokLBrace
}

func isClose(k ast.TokenKind) bool {
	return k == ast.TokRParen || k == ast.TokRBracket || k == ast.TokRBrace
}

// open is one bracket still open during layout.
type open struct {
	kind   ast.TokenKind
	indent int // the indentation level of the line it was opened on
	// stmt is the statement's level when it was opened: a ( closed on a
	// continuation line gives the line's { back the statement's level, not
	// the continuation's (as in std/task/sched.mk's Linger).
	stmt int
}

// line is one output line before trailing comments are aligned.
type line struct {
	indent  int
	code    string // the line's text after the indentation, without a trailing comment
	comment string // a trailing comment, after code
	multi   bool   // code holds a multi-line token: never aligned
}

// layout writes the items.
func layout(items []item) string {
	var lines []line
	var stack []open
	var cur *line
	var b strings.Builder
	flush := func() {
		if cur != nil {
			cur.code = b.String()
			lines = append(lines, *cur)
			b.Reset()
		}
	}
	var prev *item // previous item on the current line
	var lastSig *item
	// at is the indentation a ( or [ opened now records, stmt the one a {
	// records: a { opened on a continuation line, or after the line's
	// earlier brackets closed, belongs to the statement, so its body is one
	// level in from the statement and its } lines up with it.
	at, stmt := 0, 0
	for i := range items {
		it := &items[i]
		if i == 0 || it.nl > 0 {
			flush()
			if i > 0 && it.nl >= 2 && !lineEndsOpen(lastOnLine(items, i)) && !isClose(it.kind) {
				lines = append(lines, line{})
			}
			var cont bool
			at, cont = indentOf(items, i, stack, lastSig)
			cur = &line{indent: at}
			stmt = at
			if cont {
				stmt--
			}
			prev = nil
		}
		if prev != nil {
			if isComment(it.kind) && !strings.HasPrefix(it.text, "/*") {
				flush()
				lines[len(lines)-1].comment = it.text
				cur = nil
				prev = nil
				continue
			}
			if space(prev, it) || fuses(prev, it) {
				b.WriteByte(' ')
			}
		}
		b.WriteString(it.text)
		if strings.Contains(it.text, "\n") {
			cur.multi = true
		}
		switch {
		case it.kind == ast.TokLBrace:
			stack = append(stack, open{kind: it.kind, indent: stmt, stmt: stmt})
		case isOpen(it.kind):
			stack = append(stack, open{kind: it.kind, indent: at, stmt: stmt})
		case isClose(it.kind):
			if n := len(stack); n > 0 {
				at, stmt = stack[n-1].indent, stack[n-1].stmt
				stack = stack[:n-1]
			}
		}
		prev = it
		if !isComment(it.kind) {
			lastSig = it
		}
	}
	flush()
	return render(lines)
}

// lastOnLine is the item before index i (the last item of the previous line).
func lastOnLine(items []item, i int) *item { return &items[i-1] }

func lineEndsOpen(it *item) bool { return isOpen(it.kind) }

// indentOf is the indentation level of the line that starts at items[i],
// and whether it is a continuation line (one level past its statement).
func indentOf(items []item, i int, stack []open, lastSig *item) (int, bool) {
	k := 0
	for j := i; j < len(items) && isClose(items[j].kind) && (j == i || items[j].nl == 0); j++ {
		k++
	}
	if k > 0 {
		if k > len(stack) {
			k = len(stack)
		}
		if k == 0 {
			return 0, false
		}
		return stack[len(stack)-k].indent, false
	}
	base := 0
	inStmt := true
	if n := len(stack); n > 0 {
		base = stack[n-1].indent + 1
		inStmt = stack[n-1].kind == ast.TokLBrace
	}
	if inStmt && continues(items, i, lastSig, len(stack) == 0) {
		return base + 1, true
	}
	return base, false
}

// continues reports whether the line starting at items[i] continues the
// statement or signature of the line before it: no terminator since the
// last significant token (an operator ended the line), or a signature's
// requires, ensures, from, ends or * line. A line starting
// with { (a body after such lines) never continues.
func continues(items []item, i int, lastSig *item, topLevel bool) bool {
	it := &items[i]
	if lastSig == nil || it.kind == ast.TokLBrace {
		return false
	}
	switch it.kind {
	case ast.TokRequires, ast.TokEnsures:
		return true
	case ast.TokStar:
		if topLevel {
			return true
		}
	case ast.TokIdent:
		// A signature's cpu clause on its own line, as requires is.
		if it.text == "cpu" && topLevel && i+1 < len(items) && items[i+1].nl == 0 && items[i+1].kind == ast.TokStr {
			return true
		}
		if (it.text == "from" || it.text == "ends") && i+1 < len(items) && items[i+1].nl == 0 {
			switch items[i+1].kind {
			case ast.TokIdent, ast.TokAmp, ast.TokMut:
				return true
			case ast.TokLParen:
				return items[i+1].flowParen // `from (a: x)` on its own line
			}
		}
	}
	if isComment(it.kind) {
		// A comment between a signature's clause lines stays with them:
		// it continues when the code after it does.
		j := i + 1
		for j < len(items) && isComment(items[j].kind) {
			j++
		}
		if topLevel && j < len(items) && items[j].nl > 0 && sigClause(items, j) {
			return true
		}
		return !it.term && !isOpen(lastSig.kind) && !lastSig.kind.CanEndStatement()
	}
	return !it.term && !isOpen(lastSig.kind)
}

// sigClause reports whether items[j] starts a signature's requires,
// ensures or cpu line.
func sigClause(items []item, j int) bool {
	switch it := &items[j]; it.kind {
	case ast.TokRequires, ast.TokEnsures:
		return true
	case ast.TokIdent:
		return it.text == "cpu" && j+1 < len(items) && items[j+1].nl == 0 && items[j+1].kind == ast.TokStr
	}
	return false
}

// space reports whether one space separates prev and cur on one line.
func space(prev, cur *item) bool {
	p, c := prev.kind, cur.kind
	switch {
	case isComment(c):
		return true
	case isComment(p): // a block comment before code
		return c != ast.TokRParen && c != ast.TokRBracket && c != ast.TokComma
	case p == ast.TokLBrace: // quote { .x } and quote { , x } keep their space
		return c != ast.TokRBrace
	case c == ast.TokRParen || c == ast.TokRBracket || c == ast.TokComma || c == ast.TokSemi:
		return false
	case p == ast.TokComma && c == ast.TokDotDot: // a pattern's `, ..`
		return true
	case p == ast.TokComma && c == ast.TokEllipsis: // an extern's variadic `, ...`
		return true
	case p == ast.TokLParen || p == ast.TokLBracket:
		return false
	case c == ast.TokDot && p == ast.TokPipe && !prev.tight:
		// An or-pattern of variants, `.A | .B`, is spaced on both sides as
		// any binary | is.
		return true
	case p == ast.TokDotDot && c == ast.TokLBrace, p == ast.TokPipe && c == ast.TokDotDot, p == ast.TokColon && c == ast.TokDotDot:
		// An open range pattern before its arm's block, `200.. {`, one
		// after an or-pattern's bar, `1 | ..0`, and one after a field's
		// colon, `P(b: ..0)`.
		return true
	case c == ast.TokDot || p == ast.TokDot || c == ast.TokDotDot || p == ast.TokDotDot || c == ast.TokEllipsis || p == ast.TokEllipsis:
		return false
	case c == ast.TokColonColon || p == ast.TokColonColon:
		return false
	case c == ast.TokColon:
		return false
	case p == ast.TokColon || p == ast.TokComma || p == ast.TokSemi:
		return true
	case c == ast.TokLBrace:
		return true
	case c == ast.TokRBrace:
		return p != ast.TokLBrace
	case p == ast.TokRBrace && (c == ast.TokLBracket || c == ast.TokLParen):
		// A suffix of an if, match or fn literal used as an operand,
		// `match k { .. }[1]`, `if c { F } else { G }(x)`: on its line a }
		// is followed by [ or ( nowhere else (one statement and one arm
		// per line), so it is tight, as after any operand.
		return false
	case p == ast.TokLBrace || p == ast.TokRBrace:
		return true
	case prev.tight || cur.tight:
		return false
	case p == ast.TokBang && !prev.unary: // E!T
		return false
	case c == ast.TokBang && !cur.unary:
		// `vec ![1]` in a bracket is an error union; written tight it
		// would be the macro habit E1221 (FuzzFormat, syntax.MacroHabit).
		return p == ast.TokIdent && syntax.MacroHabit(prev.text)
	case prev.unary:
		return false
	case isBinary(c) && !cur.unary:
		return true
	case p == ast.TokRBracket && prev.typeClose:
		// An array literal's initializer before its section clause keeps
		// the space, `[1, 2] section ".x"`; a type prefix
		// []T does not.
		return c == ast.TokIdent && cur.text == "section"
	case isBinary(p):
		return true
	case c == ast.TokLParen && cur.flowParen:
		return true // `from (a: x)`
	case c == ast.TokLParen || c == ast.TokLBracket:
		switch p {
		case ast.TokIdent, ast.TokUnderscore, ast.TokRParen, ast.TokRBracket, ast.TokFn, ast.TokImpl, ast.TokLifetime:
			return false
		case ast.TokAsm:
			return false // asm("...") is call-like
		case ast.TokIn:
			if prev.asm {
				return false // an asm operand's in(reg)
			}
		}
		// A string literal is indexed or sliced as written: "ab"[..1].
		if c == ast.TokLBracket && (p == ast.TokStr || p == ast.TokRawStr || p == ast.TokMultiStr) {
			return false
		}
		return true
	}
	return true
}

// fuses reports whether prev and cur written with no space would lex as
// other tokens (& &x as &&x, a/ /b as a comment): then a space is kept
// whatever the table says. Only adjacent operator bytes or adjacent word
// bytes can fuse, so only those pairs are lexed.
func fuses(prev, cur *item) bool {
	// A number beside a lone . can fuse across three tokens (0 . 0 into
	// the float 0.0), which a pairwise check cannot see: keep the space.
	num := func(k ast.TokenKind) bool { return k == ast.TokInt || k == ast.TokFloat }
	// A float before a dot cannot: 1.0.Bits() lexes as 1.0, ., Bits.
	if (prev.kind == ast.TokInt && cur.kind == ast.TokDot) || (prev.kind == ast.TokDot && num(cur.kind)) {
		return true
	}
	// The parser reads adjacency as meaning where the tokens are the same:
	// - -x negates twice but --x is E0101 ("Mk has no `--`"), and so is
	// + +x against ++x (syntax/expr.go parseUnary). The pair lexes as two
	// tokens either way, so the lexing below cannot see it. * * stays
	// fusable: **p is two prefix derefs, which the parser accepts.
	if prev.kind == cur.kind && (prev.kind == ast.TokMinus || prev.kind == ast.TokPlus) {
		return true
	}
	a, b := prev.text[len(prev.text)-1], cur.text[0]
	dotNum := (a == '.' && isDigitByte(b)) || (isDigitByte(a) && b == '.') // .5, 1.x
	if !(isOpByte(a) && isOpByte(b)) && !(isWordByte(a) && isWordByte(b)) && !dotNum {
		return false
	}
	toks, _ := syntax.Lex(source.NoFile, prev.text+cur.text)
	n := 0
	for _, t := range toks {
		if t.Kind.IsTrivia() || t.Kind == ast.TokTerm || t.Kind == ast.TokEOF {
			continue
		}
		if n == 0 && int(t.Len) != len(prev.text) {
			return true
		}
		n++
	}
	return n != 2
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

func isOpByte(c byte) bool { return strings.IndexByte("!%&*+-./:<=>?^|~$", c) >= 0 }

func isWordByte(c byte) bool {
	return c == '_' || c == '\'' || c == '"' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isBinary reports whether k is an operator written with a space on each
// side when infix: the binary operators, assignments, := and =>.
func isBinary(k ast.TokenKind) bool {
	switch k {
	case ast.TokAmp, ast.TokPipe, ast.TokCaret, ast.TokStar, ast.TokSlash, ast.TokPercent, ast.TokPlus, ast.TokMinus,
		ast.TokPlusWrap, ast.TokMinusWrap, ast.TokStarWrap, ast.TokShl, ast.TokShr, ast.TokAmpAmp, ast.TokPipePipe,
		ast.TokAssign, ast.TokEqEq, ast.TokNotEq, ast.TokLt, ast.TokLtEq, ast.TokGt, ast.TokGtEq,
		ast.TokColonEq, ast.TokFatArrow, ast.TokArrow:
		return true
	}
	return k.IsAssignOp()
}

// render writes the lines: indentation, code, and trailing comments aligned
// one space past the longest code of each run of consecutive lines at one
// indentation that all carry one (gofmt's rule, with a one-space gap).
// Blank lines are never doubled and none starts or ends the file.
func render(lines []line) string {
	for len(lines) > 0 && lines[0].code == "" && lines[0].comment == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1].code == "" && lines[len(lines)-1].comment == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	for i := 0; i < len(lines); {
		j := i
		width := 0
		for j < len(lines) && alignable(lines[j]) && lines[j].indent == lines[i].indent {
			width = max(width, utf8.RuneCountInString(lines[j].code))
			j++
		}
		if j == i {
			writeLine(&b, lines[i], 0)
			i++
			continue
		}
		for ; i < j; i++ {
			writeLine(&b, lines[i], width)
		}
	}
	return b.String()
}

func alignable(l line) bool { return l.code != "" && l.comment != "" && !l.multi }

func writeLine(b *strings.Builder, l line, width int) {
	if l.code == "" && l.comment == "" {
		b.WriteByte('\n')
		return
	}
	b.WriteString(strings.Repeat(Indent, l.indent))
	b.WriteString(l.code)
	if l.comment != "" {
		if l.code != "" {
			pad := 1
			if width > 0 {
				pad = width - utf8.RuneCountInString(l.code) + 1
			}
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString(l.comment)
	}
	b.WriteByte('\n')
}

// guard re-lexes out and compares its significant tokens, terminators and
// comment texts with the input's, less the tokens dropEmptyRecords removed.
func guard(text string, toks []ast.Token, out string, removed map[int]bool) error {
	outToks, ds := syntax.Lex(source.NoFile, out)
	if len(ds) > 0 {
		return &GuardError{Msg: "the formatted text does not lex: " + ds[0].Message}
	}
	want := sig(text, toks, removed)
	got := sig(out, outToks, nil)
	if len(want) != len(got) {
		return &GuardError{Msg: fmt.Sprintf("token count %d, formatted %d", len(want), len(got))}
	}
	for i := range want {
		if want[i] != got[i] {
			return &GuardError{Msg: fmt.Sprintf("token %d is %q, formatted %q", i, want[i].String(), got[i].String())}
		}
	}
	return nil
}

// sigTok is one token the guard compares: its kind and its text (a
// terminator has kind TokTerm and text ";"). Compared as a struct, so the
// guard allocates one slice per side and no string per token.
type sigTok struct {
	kind ast.TokenKind
	text string
}

func (t sigTok) String() string { return t.kind.String() + " " + t.text }

// sig spells the tokens the guard compares: significant ones by kind and
// text, terminators by kind, comments by their text without trailing blanks.
func sig(text string, toks []ast.Token, removed map[int]bool) []sigTok {
	out := make([]sigTok, 0, len(toks))
	for i, t := range toks {
		if removed[i] {
			continue
		}
		switch t.Kind {
		case ast.TokWhitespace, ast.TokNewline, ast.TokEOF:
			continue
		case ast.TokTerm:
			out = append(out, sigTok{ast.TokTerm, ";"})
			continue
		}
		k := t.Kind
		if k == ast.TokFields { // the parser's retag of the word fields
			k = ast.TokIdent
		}
		s := text[t.Start : t.Start+t.Len]
		if isComment(t.Kind) {
			s = strings.TrimRight(s, " \t")
		}
		out = append(out, sigTok{k, s})
	}
	return out
}
