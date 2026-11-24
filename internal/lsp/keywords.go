// Why: a novice learns the language by hovering its keywords and operators.
//
// What: keywordHover, operatorHover, operatorTerms, the universe-name rules
// (universeWords, universeDocs), ruleWords and asmWords.
//
// How: the rule text comes from keywords_table.go, which TestKeywordTable
// generates from the language reference and the memory model; a few
// universe rules are quoted by hand and a test checks the quotes.

package lsp

import "strings"

// keywordDoc is one rule paragraph and the section it comes from.
type keywordDoc struct {
	Section string
	Text    string
}

// operatorTerms are the operators of the grammar that get a hover,
// longest first where one is a prefix of another.
var operatorTerms = []string{"+%=", "-%=", "*%=", "<<=", ">>=",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
	"+%", "-%", "*%", "<<", ">>", "==", "!=", "<=", ">=", "&&", "||", "..", ":=",
	"+", "-", "*", "/", "%", "&", "|", "^", "~", "!", "<", ">", "=", "?"}

// compoundOps are the assignment operators `op=`: each one's hover is the
// assignment rule followed by its operator's.
var compoundOps = setOf("+%= -%= *%= <<= >>= += -= *= /= %= &= |= ^=")

// operatorAt is the operator of operatorTerms that covers byte off of
// text, longest first, and its extent.
func operatorAt(text string, off int) (string, int, int, bool) {
	for _, op := range operatorTerms {
		for a := max(0, off-len(op)+1); a <= off && a+len(op) <= len(text); a++ {
			if text[a:a+len(op)] == op {
				return op, a, a + len(op), true
			}
		}
	}
	return "", 0, 0, false
}

// keywordHover renders a term's rule paragraphs.
func keywordHover(term string) (string, bool) {
	docs := keywordDocs[term]
	if compoundOps[term] {
		docs = append(append([]keywordDoc(nil), keywordDocs["+="]...), keywordDocs[strings.TrimSuffix(term, "=")]...)
	}
	if d, ok := contextDocs[term]; ok {
		docs = []keywordDoc{d}
	}
	if u, ok := universeDocs[term]; ok {
		docs = append([]keywordDoc{u}, docs...)
	} else if universeWords[term] && len(docs) == 0 {
		docs = []keywordDoc{universeLine}
	}
	if len(docs) == 0 {
		return "", false
	}
	var b strings.Builder
	for i, d := range docs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(d.Text + "\n\n*" + d.Section + "*")
	}
	return b.String(), true
}

// universeWords are the language's universe names; universeLine
// is the sentence that defines them, universeDocs each one's own rule,
// quoted from the reference (TestUniverseDocsQuoteTheReference).
var universeWords = setOf("true false some none ok err fields Assert Panic Cast Trunc SizeOf AlignOf Target Plain Send Share Fields EvalArena")

var universeLine = keywordDoc{Section: "reference: Lexical rules", Text: "Universe names: `true false some none ok err fields Assert Panic Cast Trunc SizeOf AlignOf Target Plain Send Share Fields EvalArena`. `ok` and `err` are pattern heads only, so they may be declared. Declaring any other universe name, or a primitive type name such as `int` or `str`, as an item, local, parameter or field is E0102."}

var universeDocs = map[string]keywordDoc{
	"true":      {Section: "reference: Types and kinds", Text: "`bool` is `true` or `false`. `!` negates it, and `&&` and `||` evaluate their right side only when needed."},
	"false":     {Section: "reference: Types and kinds", Text: "`bool` is `true` or `false`. `!` negates it, and `&&` and `||` evaluate their right side only when needed."},
	"Cast":      {Section: "reference: Types and kinds", Text: "Mk converts between types only when you say so. | `Cast` to an integer truncates a float toward zero, and panics on NaN, an infinity or a value out of range. | `Cast` from an integer to a float rounds to the nearest value, ties to even, and never panics: `Cast[f64](n)`."},
	"Trunc":     {Section: "reference: Types and kinds", Text: "Mk converts between types only when you say so. | Write `Cast[i32](a)` to check the value at run time, or `Trunc[i32](a)` to keep its low bits."},
	"Panic":     {Section: "reference: Errors", Text: "the text of `msg`: `Panic(msg)`, a function that returns `never` | A panic is not an error. It reports a bug: a state the program should never reach. It prints a report to standard error and ends the process with status 101. Nothing unwinds: no `defer` or `onerr` runs, and no caller can catch it."},
	"Assert":    {Section: "reference: Errors", Text: "`assert`: `Assert(c)` with `c` false | A panic is not an error. It reports a bug: a state the program should never reach. It prints a report to standard error and ends the process with status 101. Nothing unwinds: no `defer` or `onerr` runs, and no caller can catch it."},
	"Plain":     {Section: "reference: Generics and interfaces", Text: "Four bounds are not interfaces: the compiler computes them from a type's fields. `impl Plain` and `impl Fields[B]` are E0704. | `Plain`: `T` is copied on use, so the body may copy and drop it"},
	"Send":      {Section: "reference: Generics and interfaces", Text: "Four bounds are not interfaces: the compiler computes them from a type's fields. `impl Plain` and `impl Fields[B]` are E0704. | `Send`: A value of `T` may move to another task"},
	"Share":     {Section: "reference: Generics and interfaces", Text: "Four bounds are not interfaces: the compiler computes them from a type's fields. `impl Plain` and `impl Fields[B]` are E0704. | `Share`: A `&T` may be shared with another task"},
	"Fields":    {Section: "reference: Generics and interfaces", Text: "Four bounds are not interfaces: the compiler computes them from a type's fields. `impl Plain` and `impl Fields[B]` are E0704. | `Fields[B]`: Every exported field of `T` meets `B`, so `fields(&x)` can visit them"},
	"fields":    {Section: "reference: Compile time", Text: "`for name, value in fields(&x)` runs its body once for each field of `x`, in declaration order for a struct, and in name order for a record | `name` is the field's name as a `str`, and `value` a reference to the field. A generic function uses it under a `Fields[B]` bound, which requires every field to implement `B`."},
	"EvalArena": {Section: "reference: Compile time", Text: "`EvalArena()` gives an evaluation an arena that lasts as long as the evaluation. Pass it to a function that takes `a: &Arena`. Nothing frees it: `str`s and slices in the result become static data in the executable."},
	"SizeOf":    {Section: "reference: Types and kinds", Text: "`SizeOf[T]()` and `AlignOf[T]()` give a type's size and alignment in bytes, as an `int`."},
	"AlignOf":   {Section: "reference: Types and kinds", Text: "`SizeOf[T]()` and `AlignOf[T]()` give a type's size and alignment in bytes, as an `int`."},
	"ok":        {Section: "reference: Errors", Text: "`match e { ok(v) { ... } err(x) { ... } }`: Handle both cases, or each error separately"},
	"err":       {Section: "reference: Errors", Text: "`match e { ok(v) { ... } err(x) { ... } }`: Handle both cases, or each error separately"},
	"Self":      {Section: "reference: Lexical rules", Text: "Contextual: `from into ends fuel packed plain open self Self result`."},
}

// ruleWords are names that are not keywords but whose hover is a rule:
// the receiver, the optional's two forms, and the declaration modifiers.
var ruleWords = setOf("self some none fields naked packed plain align open cpu section true false Assert Panic Cast Trunc SizeOf AlignOf Target Plain Send Share Fields EvalArena Self i8 i16 i32 i64 u8 u16 u32 u64 int uint f32 f64 bool char void never str")

// operatorHover answers a hover on an operator outside strings and
// comments with its rule, or nil.
func (s *Server) operatorHover(f srcFile, p Position) any {
	u8 := s.utf8.Load()
	off := f.text.off(p, u8)
	for _, t := range f.toks {
		if t.Start <= off && off < t.End && t.Kind != tPunct {
			return nil
		}
	}
	op, a, b, ok := operatorAt(f.text.s, off)
	if !ok {
		return nil
	}
	v, ok := keywordHover(op)
	if !ok {
		return nil
	}
	rng := f.text.span(a, b, u8)
	return hover{Contents: markupContent{Kind: "markdown", Value: v}, Range: &rng}
}

// asmWords are the operand and option words of
// `asm(...)`, plain names everywhere else.
var asmWords = setOf("out inout lateout inlateout options att nomem nostack preserves_flags pure readonly noreturn sym clobber_abi reg reg_byte kreg xmm_reg ymm_reg zmm_reg")

// insideAsm reports whether ct[i] is inside an `asm(...)`'s parentheses,
// at any depth (`options(nomem)` within it).
func insideAsm(text string, ct []token, i int) bool {
	d := 0
	for j := i - 1; j >= 0; j-- {
		switch word(text, ct, j) {
		case ")", "]":
			d++
		case "(", "[":
			if d > 0 {
				d--
				continue
			}
			if word(text, ct, j-1) == "asm" {
				return true
			}
		case "{", "}":
			return false
		}
	}
	return false
}

// contextualWord reports whether the name at c is a contextual word in
// the place that makes it one: `result` in a `requires` or `ensures`
// clause, `fuel` before the step count of a compile-time evaluation.
// Elsewhere both are plain names.
func contextualWord(c cursor) bool {
	text := c.f.text.s
	switch c.name {
	case "result":
		start := strings.LastIndexByte(text[:c.ct[c.i].Start], '\n') + 1
		line := strings.TrimLeft(text[start:c.ct[c.i].Start], " \t")
		return strings.HasPrefix(line, "ensures ") || strings.HasPrefix(line, "requires ")
	case "fuel":
		return c.i+1 < len(c.ct) && c.ct[c.i+1].Kind == tNumber && c.i > 0 && word(text, c.ct, c.i-1) == ")"
	}
	return false
}

// contextDocs are the rules of the contextual words whose reference text
// is spread over several sections, written here in a few lines.
var contextDocs = map[string]keywordDoc{
	"self":   {Section: "reference: Declarations", Text: "`self`: a method's receiver. In `T.M(...)` the prefix `T.` gives the receiver's type, and `self`, `&self` or `mut self` as the first parameter its mode: by value, shared or mutable. This is the only way to write a receiver."},
	"Self":   {Section: "reference: Generics and interfaces", Text: "`Self`: in an interface, the type that implements it, as in `Eq(&self, other: &Self): bool`; in a method, the method's own type."},
	"result": {Section: "reference: Contracts and tests", Text: "`result`: in an `ensures` clause, the value the function returns, as in `ensures result >= 0`. Elsewhere `result` is an ordinary name."},
	"open":   {Section: "reference: Statements", Text: "`open`: an `open enum` may gain variants, and an `open type E = A | B` error set may gain members. Outside the package that declares it, a `match` on an open type needs `_` or a binding to cover what may be added (E0306)."},
	"fuel":   {Section: "reference: Compile time", Text: "`fuel n` after a compile-time evaluation bounds it to n steps, as in `const T = Build() fuel 100_000_000`. The default is 4 million steps; running out is E0902."},
}
