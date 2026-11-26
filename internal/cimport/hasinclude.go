// Why: a unit that asks `__has_include("opt.h")` while opt.h is absent
// compiles one branch, and -MD names no file for the lookup (Clang lists a
// probed file only when it is found), so a C cache would never notice opt.h
// appear.
//
// What: probes, the existence lookups (__has_include, __has_include_next,
// __has_embed) a file's text spells, each with its name and form, and
// whether every one could be read.
//
// How: a light scan of the text that skips comments, string and character
// literals and tracks the #define line it is on. A lookup's argument must
// be a quoted or angled name; a bare mention (#ifdef __has_include,
// defined(__has_include)) and the definition of a fallback
// (#define __has_include(x) 0) look nothing up. A lookup whose argument is
// a parameter of the function-like macro being defined makes that macro a
// wrapper, whose invocations with a literal name are lookups too (glibc's
// __glibc_has_include). Any other form (a macro argument, a wrapper called
// with one, a continuation the scan cannot follow) is not modelled: the
// caller leaves its unit uncached.

package cimport

import (
	"slices"
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

// probeIdents are the built-in lookups and whether each is an _next form.
var probeIdents = map[string]bool{"__has_include": false, "__has_include_next": true, "__has_embed": false}

// wrapper is a function-like macro that passes one of its parameters to a
// lookup.
type wrapper struct {
	next  bool
	param int // the index of the parameter it passes
}

// scanState is one file's scan.
type scanState struct {
	text string
	i    int
	// The #define being read: its name and parameters, or name "" outside one.
	defName   string
	defParams []string
}

// probes are the lookups texts spell (each file's text), and false when one
// is not modelled. Wrappers defined in any of the texts apply to all of
// them: a header may define one that another uses.
func probes(texts []string) ([]probe, bool) {
	wrappers := map[string]wrapper{}
	var out []probe
	// Two passes: the first finds the wrappers, the second their calls.
	for pass := 0; pass < 2; pass++ {
		out = out[:0]
		for _, t := range texts {
			if !strings.Contains(t, "__has_") && len(wrappers) == 0 {
				continue
			}
			ps, ok := scanProbes(t, wrappers, pass == 0)
			if !ok {
				return nil, false
			}
			out = append(out, ps...)
		}
		if len(wrappers) == 0 {
			break
		}
	}
	return out, true
}

// scanProbes scans one text; with learn, a wrapper it defines is added to
// wrappers (and its calls are not yet checked).
func scanProbes(text string, wrappers map[string]wrapper, learn bool) ([]probe, bool) {
	s := &scanState{text: text}
	var out []probe
	lineStart := true
	for s.i < len(s.text) {
		c := s.text[s.i]
		switch {
		case c == '\n':
			s.i++
			lineStart = true
			s.defName, s.defParams = "", nil
			continue
		case c == '\\' && s.at(s.i+1, "\n"), c == '\\' && s.at(s.i+1, "\r\n"):
			s.i++ // a continuation: the logical line goes on
			if s.text[s.i] == '\r' {
				s.i++
			}
			s.i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			s.i++
			continue
		case c == '/' && s.at(s.i+1, "/"):
			s.skipLineComment()
			continue
		case c == '/' && s.at(s.i+1, "*"):
			end := strings.Index(s.text[s.i+2:], "*/")
			if end < 0 {
				return out, true // an open comment runs to the end
			}
			s.i += 2 + end + 2
			continue
		case c == '"' || c == '\'':
			s.skipLiteral(c)
			lineStart = false
			continue
		case c == '#' && lineStart:
			s.i++
			s.directive()
			lineStart = false
			continue
		case isIdentStart(c):
			id := s.ident()
			lineStart = false
			if next, ok := probeIdents[id]; ok {
				p, form, param := s.argument()
				switch form {
				case argNone:
				case argName:
					out = append(out, probe{name: p.name, quoted: p.quoted, next: next})
				case argParam:
					if !learn {
						continue // learnt in the first pass
					}
					wrappers[s.defName] = wrapper{next: next, param: param}
				default:
					return nil, false
				}
				continue
			}
			if w, ok := wrappers[id]; ok && !learn && id != s.defName {
				p, form := s.callArgument(w.param)
				switch form {
				case argNone:
				case argName:
					out = append(out, probe{name: p.name, quoted: p.quoted, next: w.next})
				default:
					return nil, false
				}
			}
			continue
		}
		s.i++
		lineStart = false
	}
	return out, true
}

// The forms of what follows a lookup's name.
const (
	argNone  = iota // no parenthesis: a mention, or a fallback's definition
	argName         // ("x") or (<x>)
	argParam        // a parameter of the macro being defined
	argOther        // anything else: not modelled
)

// argument reads what follows a lookup's name at s.i; for argParam, the
// parameter's index too.
func (s *scanState) argument() (probe, int, int) {
	j := s.skipSpace(s.i)
	if j >= len(s.text) || s.text[j] != '(' {
		return probe{}, argNone, 0
	}
	j = s.skipSpace(j + 1)
	if j >= len(s.text) {
		return probe{}, argOther, 0
	}
	switch s.text[j] {
	case '"', '<':
		close := byte('"')
		if s.text[j] == '<' {
			close = '>'
		}
		end := strings.IndexAny(s.text[j+1:], string(close)+"\n")
		if end < 0 || s.text[j+1+end] != close || end == 0 {
			return probe{}, argOther, 0
		}
		p := probe{name: s.text[j+1 : j+1+end], quoted: close == '"'}
		j = s.skipSpace(j + 1 + end + 1)
		if j >= len(s.text) || s.text[j] != ')' {
			return probe{}, argOther, 0
		}
		s.i = j + 1
		return p, argName, 0
	}
	if isIdentStart(s.text[j]) {
		k := j
		for k < len(s.text) && isIdent(s.text[k]) {
			k++
		}
		id := s.text[j:k]
		k = s.skipSpace(k)
		if k < len(s.text) && s.text[k] == ')' && s.defName != "" {
			if n := slices.Index(s.defParams, id); n >= 0 {
				s.i = k + 1
				return probe{}, argParam, n
			}
		}
	}
	return probe{}, argOther, 0
}

// callArgument reads a wrapper's call at s.i, whose argument param must be
// a quoted or angled name. The call is one logical line, as it must be in
// an #if.
func (s *scanState) callArgument(param int) (probe, int) {
	j := s.skipSpace(s.i)
	if j >= len(s.text) || s.text[j] != '(' {
		return probe{}, argNone
	}
	var args []string
	depth, start, end := 0, j+1, -1
	for k := j + 1; k < len(s.text) && end < 0; k++ {
		switch s.text[k] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				args, end = append(args, s.text[start:k]), k
			}
			depth--
		case ',':
			if depth == 0 {
				args, start = append(args, s.text[start:k]), k+1
			}
		case '\n':
			if !strings.HasSuffix(s.text[:k], "\\") && !strings.HasSuffix(s.text[:k], "\\\r") {
				return probe{}, argOther
			}
		}
	}
	if end < 0 || param >= len(args) {
		return probe{}, argOther
	}
	s.i = end + 1
	a := strings.TrimSpace(strings.NewReplacer("\\\r\n", "", "\\\n", "").Replace(args[param]))
	if len(a) < 3 || !(a[0] == '"' && a[len(a)-1] == '"' || a[0] == '<' && a[len(a)-1] == '>') ||
		strings.ContainsAny(a[1:len(a)-1], "\"<>") {
		return probe{}, argOther
	}
	return probe{name: a[1 : len(a)-1], quoted: a[0] == '"'}, argName
}

// directive reads a directive's name after '#'; for #define, the macro's
// name and, for a function-like one, its parameters.
func (s *scanState) directive() {
	s.i = s.skipSpace(s.i)
	if s.i >= len(s.text) || !isIdentStart(s.text[s.i]) {
		return
	}
	if s.ident() != "define" {
		return
	}
	s.i = s.skipSpace(s.i)
	if s.i >= len(s.text) || !isIdentStart(s.text[s.i]) {
		return
	}
	s.defName = s.ident()
	if _, ok := probeIdents[s.defName]; ok {
		// #define __has_include(x) 0, the fallback of a compiler without
		// it: its "(x)" is a parameter list, scanned as plain text.
		return
	}
	if s.i >= len(s.text) || s.text[s.i] != '(' {
		s.defParams = []string{} // object-like: no parameters
		return
	}
	end := strings.IndexByte(s.text[s.i:], ')')
	if end < 0 {
		return
	}
	s.defParams = []string{}
	for _, prm := range strings.Split(s.text[s.i+1:s.i+end], ",") {
		s.defParams = append(s.defParams, strings.TrimSpace(prm))
	}
	s.i += end + 1
}

// skipLineComment skips a // comment to the end of its logical line.
func (s *scanState) skipLineComment() {
	for s.i < len(s.text) && s.text[s.i] != '\n' {
		if s.text[s.i] == '\\' && (s.at(s.i+1, "\n") || s.at(s.i+1, "\r\n")) {
			s.i++
		}
		s.i++
	}
}

// skipLiteral skips a string or character literal opened by q, which ends
// at its closing quote or at the end of the line (an apostrophe in #error
// text opens none past it).
func (s *scanState) skipLiteral(q byte) {
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
// a blank nor a line continuation.
func (s *scanState) skipSpace(j int) int {
	for j < len(s.text) {
		switch {
		case s.text[j] == ' ' || s.text[j] == '\t':
			j++
		case s.text[j] == '\\' && s.at(j+1, "\n"):
			j += 2
		case s.text[j] == '\\' && s.at(j+1, "\r\n"):
			j += 3
		default:
			return j
		}
	}
	return j
}

// ident reads the identifier at s.i.
func (s *scanState) ident() string {
	j := s.i
	for s.i < len(s.text) && isIdent(s.text[s.i]) {
		s.i++
	}
	return s.text[j:s.i]
}

// at reports whether the text at j starts with w.
func (s *scanState) at(j int, w string) bool {
	return j <= len(s.text) && strings.HasPrefix(s.text[j:], w)
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }
