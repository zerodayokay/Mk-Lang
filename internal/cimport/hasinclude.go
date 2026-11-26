// Why: a unit that asks `__has_include("opt.h")` while opt.h is absent
// compiles one branch, and -MD names no file for the lookup (Clang lists a
// probed file only when it is found), so a C cache would never notice opt.h
// appear.
//
// What: probes, the existence lookups (__has_include, __has_include_next,
// __has_embed) a unit's texts spell, each with its name and form, and
// whether every one could be read.
//
// How: the scan fails closed. Each text is spliced first (a backslash, any
// blanks and a newline join two lines, as Clang does), then read with its
// comments, string and character literals skipped, tracking the directive
// of the line and the #define being read. A lookup name, or the name of a
// macro that stands for one, may appear in three places only: called with
// one literal name, as the operand of defined, #ifdef, #ifndef or #undef,
// or as the name a #define defines. A function-like macro that passes one
// of its parameters to a lookup is a wrapper (glibc's older
// __glibc_has_include), and an object-like macro whose whole body is a
// lookup name is an alias (glibc's newer one): their calls with a literal
// are lookups too. Anything else is not modelled and the caller leaves the
// unit uncached: a lookup name anywhere else (an argument to another
// macro, a bare name in a body), a name from a macro, an angled name whose
// tokens a macro expansion would expand (in a body, or a wrapper's
// argument), a trigraph, and a token paste (##) in a unit that also holds
// a proper prefix of a lookup name, from which a paste could spell one.

package cimport

import (
	"strings"
)

// probe is one existence lookup: its name as spelled, whether it was
// quoted ("x" searches beside the includer first) and whether it is an
// _next form (it searches after the directory its file was found in).
type probe struct {
	name   string
	quoted bool
	next   bool
}

// lookup is one meaning of a name that looks a file up when called: a
// built-in or an alias (param -1, called with the name itself) or a
// wrapper, which passes its parameter param.
type lookup struct {
	next  bool
	param int
}

// builtinLookups are Clang's existence lookups.
var builtinLookups = map[string]lookup{
	"__has_include":      {next: false, param: -1},
	"__has_include_next": {next: true, param: -1},
	"__has_embed":        {next: false, param: -1},
}

// lookups are the names that look a file up, each with every meaning a
// definition in the unit gave it (a name defined twice has both).
type lookups map[string][]lookup

func (l lookups) add(name string, m lookup) bool {
	for _, have := range l[name] {
		if have == m {
			return false
		}
	}
	l[name] = append(l[name], m)
	return true
}

// probes are the lookups texts spell (each file's text, and any text the
// compile reads that no file holds, such as its -D definitions), and false
// when one is not modelled. Wrappers and aliases defined in any text apply
// to all of them: a header may define one that another uses.
func probes(texts []string) ([]probe, bool) {
	norm := make([]string, len(texts))
	for i, t := range texts {
		// A trigraph (-ftrigraphs is a plain flag) can spell a directive's
		// # or a line splice the scan does not see.
		if strings.Contains(t, "??=") || strings.Contains(t, "??/") {
			return nil, false
		}
		norm[i] = splice(t)
	}
	names := lookups{}
	for n, m := range builtinLookups {
		names.add(n, m)
	}
	// Learn the wrappers and aliases to a fixed point: an alias of a
	// wrapper, or a wrapper of an alias, may be defined in any order.
	for round := 0; ; round++ {
		grew := false
		for _, t := range norm {
			if mentions(t, names) {
				s := &scanner{text: t, names: names, learn: true}
				if !s.run() {
					return nil, false
				}
				grew = grew || s.grew
			}
		}
		if !grew {
			break
		}
		if round == 8 {
			return nil, false
		}
	}
	var out []probe
	for _, t := range norm {
		if mentions(t, names) {
			s := &scanner{text: t, names: names}
			if !s.run() {
				return nil, false
			}
			out = append(out, s.out...)
		}
	}
	if pasteRisk(norm, names) {
		return nil, false
	}
	return out, true
}

// splice joins the physical lines a backslash ends (blanks may follow it),
// and makes a lone carriage return a newline, as Clang reads both.
func splice(t string) string {
	if !strings.Contains(t, "\\") && !strings.Contains(t, "\r") {
		return t
	}
	var b strings.Builder
	b.Grow(len(t))
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c == '\\' {
			j := i + 1
			for j < len(t) && (t[j] == ' ' || t[j] == '\t' || t[j] == '\f' || t[j] == '\v') {
				j++
			}
			switch {
			case j < len(t) && t[j] == '\n':
				i = j
				continue
			case j+1 < len(t) && t[j] == '\r' && t[j+1] == '\n':
				i = j + 1
				continue
			case j < len(t) && t[j] == '\r':
				i = j
				continue
			}
		}
		if c == '\r' {
			if i+1 < len(t) && t[i+1] == '\n' {
				continue
			}
			c = '\n'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// mentions reports whether t holds any lookup name at all.
func mentions(t string, names lookups) bool {
	for n := range names {
		if strings.Contains(t, n) {
			return true
		}
	}
	return false
}

// pasteRisk reports whether a token paste (##) could spell a lookup name
// in an #if, where a lookup is evaluated. That takes two things. A paste
// is expanded there: an #if or #elif line reaches, through the macros
// their bodies name, a macro whose body pastes (the first paste of an
// #if's expansion is in such a macro; a pasted name expanded next needs
// a paste first). And the pasted name's leftmost piece, a proper prefix
// of a lookup name (_, __, __has_ and so on) or __USER_LABEL_PREFIX__
// (_ on some targets), is a token of a body, an #if or a -D definition
// where a paste can take it: before a ##, or last in a body or in a
// call's argument (an argument's last token is the one a ## pastes), with
// only parameters, which may be empty, after it. A whole lookup name is no
// such piece: the scan has refused every use of one but a call with a
// literal. Clang's own macros do not paste.
func pasteRisk(texts []string, names lookups) bool {
	paste := false
	for _, t := range texts {
		if strings.Contains(t, "##") || strings.Contains(t, "%:%:") {
			paste = true
			break
		}
	}
	if !paste {
		return false
	}
	prefixes := map[string]bool{"__USER_LABEL_PREFIX__": true}
	for n := range names {
		for k := 1; k < len(n); k++ {
			if _, whole := names[n[:k]]; !whole {
				prefixes[n[:k]] = true
			}
		}
	}
	type macro struct {
		paste bool
		names []string
	}
	macros := map[string]*macro{}
	var roots []string
	piece := false
	for _, t := range texts {
		directives(t, func(dir string, toks []ppTok) {
			switch dir {
			case "if", "elif":
				for k, tk := range toks {
					if tk.kind != 'i' || definedOperand(toks, k) {
						continue
					}
					roots = append(roots, tk.text)
					if prefixes[tk.text] && leftmost(toks, k, nil) {
						piece = true
					}
				}
			case "define":
				if len(toks) == 0 || toks[0].kind != 'i' {
					return
				}
				m := macros[toks[0].text]
				if m == nil {
					m = &macro{}
					macros[toks[0].text] = m
				}
				body, params := toks[1:], map[string]bool{}
				if len(body) > 0 && body[0].text == "(" && !body[0].space {
					k := 1
					for ; k < len(body) && body[k].text != ")"; k++ {
						if body[k].kind == 'i' {
							params[body[k].text] = true
						} else if body[k].text == "..." {
							params["__VA_ARGS__"] = true
						}
					}
					params["__VA_OPT__"] = params["__VA_ARGS__"]
					body = body[min(k+1, len(body)):]
				}
				for k, tk := range body {
					switch tk.kind {
					case 'i':
						m.names = append(m.names, tk.text)
						if prefixes[tk.text] && !params[tk.text] && leftmost(body, k, params) {
							piece = true
						}
					case 'p':
						m.paste = true
					}
				}
			}
		})
	}
	if !piece {
		return false
	}
	seen := map[string]bool{}
	for len(roots) > 0 {
		n := roots[len(roots)-1]
		roots = roots[:len(roots)-1]
		m := macros[n]
		if m == nil || seen[n] {
			continue
		}
		seen[n] = true
		if m.paste {
			return true
		}
		roots = append(roots, m.names...)
	}
	return false
}

// leftmost reports whether toks[k] can be the leftmost piece of a pasted
// token: no literal is pasted before it (a ## chain whose operands before
// it are all parameters, which may be empty, or none), and a ## follows
// it, or only parameters come after it before the end, a ',' or a ')'
// (params is nil on an #if line, which no paste reads but whose call
// arguments a macro's paste may).
func leftmost(toks []ppTok, k int, params map[string]bool) bool {
	for j := k - 1; j >= 1 && toks[j].kind == 'p'; j -= 2 {
		if op := toks[j-1]; op.kind != 'i' || !params[op.text] {
			return false
		}
	}
	j := k + 1
	if j < len(toks) && toks[j].kind == 'p' {
		return true
	}
	for j < len(toks) && toks[j].kind == 'i' && params[toks[j].text] {
		j++
	}
	return j == len(toks) && params != nil || j < len(toks) && (toks[j].text == "," || toks[j].text == ")")
}

// definedOperand reports whether toks[k] is defined's operand.
func definedOperand(toks []ppTok, k int) bool {
	j := k - 1
	if j >= 0 && toks[j].text == "(" {
		j--
	}
	return j >= 0 && toks[j].text == "defined"
}

// ppTok is a preprocessing token of a directive: kind 'i' an identifier,
// 'n' a number, 'p' the paste ## (or %:%:), 'o' anything else; space when
// a blank or a comment comes before it.
type ppTok struct {
	kind  byte
	text  string
	space bool
}

// directives calls visit with each directive of a spliced text: its name
// and the tokens after it, comments read as blanks (a /* */ one may span
// lines) and string and character literals as one token.
func directives(t string, visit func(dir string, toks []ppTok)) {
	i := 0
	for i < len(t) {
		// The start of a line: blanks and comments, then # or not.
		i = skipBlank(t, i)
		isDir := false
		if i < len(t) && t[i] == '#' {
			isDir, i = true, i+1
		} else if strings.HasPrefix(t[i:], "%:") && !strings.HasPrefix(t[i:], "%:%:") {
			isDir, i = true, i+2
		}
		var toks []ppTok
		space := false
		for i < len(t) && t[i] != '\n' {
			c := t[i]
			switch {
			case c == ' ' || c == '\t' || c == '\f' || c == '\v' || c == '\r':
				i, space = i+1, true
				continue
			case c == '/' && strings.HasPrefix(t[i:], "//"):
				for i < len(t) && t[i] != '\n' {
					i++
				}
				continue
			case c == '/' && strings.HasPrefix(t[i:], "/*"):
				end := strings.Index(t[i+2:], "*/")
				if end < 0 {
					i = len(t)
				} else {
					i += 2 + end + 2
				}
				space = true
				continue
			}
			if !isDir {
				if c == '"' || c == '\'' {
					i = skipQuoted(t, i)
				} else {
					i++
				}
				continue
			}
			start := i
			kind := byte('o')
			switch {
			case c == '"' || c == '\'':
				i = skipQuoted(t, i)
			case isIdentStart(c):
				for i < len(t) && isIdent(t[i]) {
					i++
				}
				kind = 'i'
			case c >= '0' && c <= '9' || c == '.' && i+1 < len(t) && t[i+1] >= '0' && t[i+1] <= '9':
				for i < len(t) && (isIdent(t[i]) || t[i] == '.' ||
					(t[i] == '+' || t[i] == '-') && strings.ContainsRune("eEpP", rune(t[i-1]))) {
					i++
				}
				kind = 'n'
			case strings.HasPrefix(t[i:], "##"):
				i, kind = i+2, 'p'
			case strings.HasPrefix(t[i:], "%:%:"):
				i, kind = i+4, 'p'
			case strings.HasPrefix(t[i:], "..."):
				i += 3
			default:
				i++
			}
			toks = append(toks, ppTok{kind: kind, text: t[start:i], space: space})
			space = false
		}
		if isDir && len(toks) > 0 && toks[0].kind == 'i' {
			visit(toks[0].text, toks[1:])
		}
		i++
	}
}

// skipBlank skips blanks and comments from i, a // one to its line's end.
func skipBlank(t string, i int) int {
	for i < len(t) {
		switch {
		case t[i] == ' ' || t[i] == '\t' || t[i] == '\f' || t[i] == '\v' || t[i] == '\r':
			i++
		case strings.HasPrefix(t[i:], "/*"):
			end := strings.Index(t[i+2:], "*/")
			if end < 0 {
				return len(t)
			}
			i += 2 + end + 2
		case strings.HasPrefix(t[i:], "//"):
			for i < len(t) && t[i] != '\n' {
				i++
			}
			return i
		default:
			return i
		}
	}
	return i
}

// skipQuoted is the index after the literal at i, which ends at its
// closing quote or before the end of its line.
func skipQuoted(t string, i int) int {
	q := t[i]
	i++
	for i < len(t) && t[i] != '\n' {
		switch t[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		}
		i++
	}
	return i
}

// scanner is one text's scan. With learn it adds the wrappers and aliases
// the text defines to names (grew when it added one) and checks nothing
// else; without, it collects the lookups in out and refuses what it does
// not model.
type scanner struct {
	text  string
	i     int
	names lookups
	learn bool
	grew  bool
	out   []probe

	lineStart bool
	// dir is the directive of the current line ("" outside one); operand
	// is set until the first token after #ifdef, #ifndef, #undef and
	// their #elif forms.
	dir     string
	operand bool
	// The #define being read: its name and parameters (nil for an
	// object-like macro) and where its body starts; defName "" outside one.
	defName   string
	defParams []string
	funcLike  bool
	bodyStart int
}

// run scans the text; false when it is not modelled.
func (s *scanner) run() bool {
	s.lineStart = true
	for s.i < len(s.text) {
		c := s.text[s.i]
		switch {
		case c == '\n':
			s.i++
			s.lineStart = true
			s.dir, s.operand, s.defName, s.defParams, s.funcLike = "", false, "", nil, false
			continue
		case c == ' ' || c == '\t' || c == '\f' || c == '\v':
			s.i++
			continue
		case c == '/' && s.at(s.i+1, "/"), c == '/' && s.at(s.i+1, "*"):
			s.skipComment()
			continue
		case c == '"' || c == '\'':
			s.skipLiteral(c)
			s.lineStart = false
			continue
		case (c == '#' || c == '%' && s.at(s.i+1, ":")) && s.lineStart:
			if c == '%' {
				s.i++
			}
			s.i++
			s.lineStart = false
			s.directive()
			continue
		case isIdentStart(c):
			start := s.i
			id := s.ident()
			s.lineStart = false
			if s.operand {
				s.operand = false
				continue // #ifdef X, #undef X: no expansion
			}
			ms, ok := s.names[id]
			if !ok {
				continue
			}
			if !s.use(start, id, ms) {
				return false
			}
			continue
		}
		s.i++
		s.lineStart = false
		s.operand = false
	}
	return true
}

// use reads the use at start of lookup name id, whose meanings are ms.
func (s *scanner) use(start int, id string, ms []lookup) bool {
	j := s.skipSpace(s.i)
	if j >= len(s.text) || s.text[j] != '(' {
		// A bare name: defined's operand, or the whole body of an
		// object-like macro (an alias); else not modelled.
		if s.afterDefined(start) {
			return true
		}
		if s.defName != "" && !s.funcLike && start == s.skipSpace(s.bodyStart) && s.restBlank(s.i) {
			if s.learn {
				for _, m := range ms {
					s.grew = s.names.add(s.defName, m) || s.grew
				}
			}
			return true
		}
		return s.learn
	}
	args, end, ok := s.callArgs(j)
	if !ok {
		return s.learn
	}
	s.i = end
	for _, m := range ms {
		k := m.param
		if k < 0 {
			if len(args) != 1 {
				return s.learn
			}
			k = 0
		}
		if k >= len(args) {
			return s.learn
		}
		a := strings.TrimSpace(args[k])
		if n := s.paramIndex(a); n >= 0 {
			// The lookup takes a parameter of the macro being defined,
			// which makes it a wrapper.
			if s.learn {
				s.grew = s.names.add(s.defName, lookup{next: m.next, param: n}) || s.grew
			}
			continue
		}
		if s.learn {
			continue
		}
		p, ok := literalName(a)
		if !ok {
			return false
		}
		// An angled name is a header-name only when the file's own lexer
		// reads it: in a macro's body, or as a wrapper's argument, its
		// tokens are macro-expanded first (linux is 1 in gnu11), so the
		// name looked up is not the one spelled.
		if !p.quoted && (s.defName != "" || m.param >= 0) {
			return false
		}
		p.next = m.next
		s.out = append(s.out, p)
	}
	return true
}

// literalName is a lookup's argument "x" or <x> as a probe, false for any
// other argument (a macro, concatenated strings, an empty name, comments).
func literalName(a string) (probe, bool) {
	if len(a) < 3 || strings.Contains(a, "/*") || strings.Contains(a, "//") {
		return probe{}, false
	}
	open, close := a[0], a[len(a)-1]
	inner := a[1 : len(a)-1]
	switch {
	case open == '"' && close == '"' && !strings.ContainsAny(inner, "\"\n"):
		return probe{name: inner, quoted: true}, true
	case open == '<' && close == '>' && !strings.ContainsAny(inner, ">\n"):
		return probe{name: inner}, true
	}
	return probe{}, false
}

// paramIndex is the index of a among the parameters of the function-like
// macro being defined, -1 when it is none.
func (s *scanner) paramIndex(a string) int {
	if !s.funcLike {
		return -1
	}
	for n, p := range s.defParams {
		if p == a {
			return n
		}
	}
	return -1
}

// callArgs reads the arguments of the call whose '(' is at j, on one
// logical line (a lookup is evaluated in an #if, which is one), with
// literals kept whole; the index after its ')'. False when it does not
// close on the line.
func (s *scanner) callArgs(j int) ([]string, int, bool) {
	var args []string
	depth, start := 0, j+1
	for k := j + 1; k < len(s.text); k++ {
		switch c := s.text[k]; c {
		case '\n':
			return nil, 0, false
		case '"', '\'':
			k++
			for k < len(s.text) && s.text[k] != c && s.text[k] != '\n' {
				if s.text[k] == '\\' {
					k++
				}
				k++
			}
			if k >= len(s.text) || s.text[k] != c {
				return nil, 0, false
			}
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return append(args, s.text[start:k]), k + 1, true
			}
			depth--
		case ',':
			if depth == 0 {
				args, start = append(args, s.text[start:k]), k+1
			}
		}
	}
	return nil, 0, false
}

// afterDefined reports whether the text before j ends with the operator
// defined, as in "defined X" or "defined(X)".
func (s *scanner) afterDefined(j int) bool {
	t := strings.TrimRight(s.text[:j], " \t")
	t = strings.TrimRight(strings.TrimSuffix(t, "("), " \t")
	if !strings.HasSuffix(t, "defined") {
		return false
	}
	k := len(t) - len("defined")
	return k == 0 || !isIdent(t[k-1])
}

// restBlank reports whether the logical line holds nothing but blanks and
// comments from j on.
func (s *scanner) restBlank(j int) bool {
	j = s.skipSpace(j)
	return j >= len(s.text) || s.text[j] == '\n'
}

// directive reads a directive's name after '#'; for #define, the macro's
// name and, for a function-like one, its parameters.
func (s *scanner) directive() {
	s.i = s.skipSpace(s.i)
	if s.i >= len(s.text) || !isIdentStart(s.text[s.i]) {
		return
	}
	s.dir = s.ident()
	switch s.dir {
	case "ifdef", "ifndef", "elifdef", "elifndef", "undef":
		s.operand = true
		return
	case "define":
	default:
		return
	}
	s.i = s.skipSpace(s.i)
	if s.i >= len(s.text) || !isIdentStart(s.text[s.i]) {
		return
	}
	s.defName = s.ident()
	if s.i < len(s.text) && s.text[s.i] == '(' {
		end := strings.IndexAny(s.text[s.i:], ")\n")
		if end < 0 || s.text[s.i+end] != ')' {
			s.defName = "" // a broken definition: its body is read as text
			return
		}
		s.funcLike = true
		s.defParams = []string{}
		for _, prm := range strings.Split(s.text[s.i+1:s.i+end], ",") {
			s.defParams = append(s.defParams, strings.TrimSpace(prm))
		}
		s.i += end + 1
	}
	s.bodyStart = s.i
}

// skipComment skips the comment at s.i: a // one to the end of its line
// (left for run, which ends the line), a /* */ one past its end.
func (s *scanner) skipComment() {
	if s.at(s.i+1, "/") {
		for s.i < len(s.text) && s.text[s.i] != '\n' {
			s.i++
		}
		return
	}
	end := strings.Index(s.text[s.i+2:], "*/")
	if end < 0 {
		s.i = len(s.text) // an open comment runs to the end
		return
	}
	s.i += 2 + end + 2
}

// skipLiteral skips a string or character literal opened by q, which ends
// at its closing quote or at the end of the line (an apostrophe in #error
// text opens none past it).
func (s *scanner) skipLiteral(q byte) {
	s.i++
	for s.i < len(s.text) && s.text[s.i] != '\n' {
		switch s.text[s.i] {
		case '\\':
			s.i += 2
			continue
		case q:
			s.i++
			return
		}
		s.i++
	}
}

// skipSpace is the index of the first byte at or after j that is neither
// a blank nor in a /* */ comment.
func (s *scanner) skipSpace(j int) int {
	for j < len(s.text) {
		switch {
		case s.text[j] == ' ' || s.text[j] == '\t' || s.text[j] == '\f' || s.text[j] == '\v':
			j++
		case s.text[j] == '/' && s.at(j+1, "*"):
			end := strings.Index(s.text[j+2:], "*/")
			if end < 0 {
				return len(s.text)
			}
			j += 2 + end + 2
		default:
			return j
		}
	}
	return j
}

// ident reads the identifier at s.i.
func (s *scanner) ident() string {
	j := s.i
	for s.i < len(s.text) && isIdent(s.text[s.i]) {
		s.i++
	}
	return s.text[j:s.i]
}

// at reports whether the text at j starts with w.
func (s *scanner) at(j int, w string) bool {
	return j <= len(s.text) && strings.HasPrefix(s.text[j:], w)
}

// isIdentStart and isIdent are Clang's identifier bytes: a letter, _, $
// and any byte of a UTF-8 sequence, and digits after the first.
func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }
