// Why: C headers define constants as macros, which Clang's AST does not
// hold as values; a binding needs them as Mk consts. A function-like macro
// binds only through a signature declared in the package manifest.
//
// What: readDefines over `clang -E -dD` output, probeLine and failedLines
// for the probe file, irValues and mkValue for values, parseProto for the
// declared prototypes, and emitMacros.
//
// How: one batched probe of object-like macros: each candidate is one line
// of the probe file, its type is read from the probe's AST, the lines
// Clang rejects are dropped, and each value is read from the probe's LLVM
// IR.

package cimport

import (
	"bufio"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// macro is one object-like macro the probe tries.
type macro struct {
	name  string
	body  string
	imp   int
	k     int    // probe index
	ok    bool   // its probe line compiled
	typ   *ctype // from the probe variable
	value string // Mk literal text, "" when the value is not one
	mk    string // Mk type
	// noProbe: the body, or a macro it names, holds a ; or a brace or
	// unbalanced parentheses, so it is no expression, and its probe line
	// would throw Clang's parser off the lines after it.
	noProbe bool
}

// readDefines parses `clang -E -dD` output: the #define lines after
// import i's marker belong to import i; #undef removes. It returns the
// object-like macros worth probing and the import index of function-like ones.
func readDefines(text string) (objs []*macro, funcs map[string]int) {
	funcs = map[string]int{}
	byName := map[string]*macro{}
	imp := -1
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var order []*macro
	bodies := map[string]string{} // every macro's body, function-like too
	for sc.Scan() {
		ln := sc.Text()
		if strings.HasPrefix(ln, "typedef int "+markerPrefix) {
			s := strings.TrimSuffix(strings.TrimPrefix(ln, "typedef int "+markerPrefix), ";")
			if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				imp = v
			}
			continue
		}
		if imp < 0 {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "#define "):
			rest := ln[len("#define "):]
			n := 0
			for n < len(rest) && isIdentByte(rest[n]) {
				n++
			}
			name := rest[:n]
			if name == "" {
				continue
			}
			if n < len(rest) && rest[n] == '(' {
				funcs[name] = imp
				delete(byName, name)
				if j := strings.IndexByte(rest, ')'); j > 0 {
					bodies[name] = rest[j+1:]
				}
				continue
			}
			body := strings.TrimSpace(rest[n:])
			delete(funcs, name)
			bodies[name] = body
			if body == "" || strings.HasPrefix(name, "_") {
				delete(byName, name)
				continue
			}
			m := &macro{name: name, body: body, imp: imp}
			byName[name] = m
			order = append(order, m)
		case strings.HasPrefix(ln, "#undef "):
			name := strings.TrimSpace(ln[len("#undef "):])
			delete(byName, name)
			delete(funcs, name)
			delete(bodies, name)
		}
	}
	memo := map[string]bool{}
	for _, m := range order {
		if byName[m.name] == m {
			m.noProbe = !exprShaped(m.name, bodies, memo)
			objs = append(objs, m)
		}
	}
	return objs, funcs
}

// exprShaped reports whether macro name's body, and every macro it names,
// could be an expression: no ; or brace, and balanced parentheses and
// brackets outside literals. A macro in a cycle is taken as shaped (the
// preprocessor does not expand it again).
func exprShaped(name string, bodies map[string]string, memo map[string]bool) bool {
	if v, ok := memo[name]; ok {
		return v
	}
	memo[name] = true
	body := bodies[name]
	ok := true
	depth := 0
	for i := 0; i < len(body) && ok; i++ {
		c := body[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(body) && body[j] != c {
				if body[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case c == ';' || c == '{' || c == '}':
			ok = false
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
			ok = depth >= 0
		case isIdentByte(c) && (c < '0' || c > '9'):
			j := i
			for j < len(body) && isIdentByte(body[j]) {
				j++
			}
			if id := body[i:j]; id != name {
				if _, isMacro := bodies[id]; isMacro {
					ok = exprShaped(id, bodies, memo)
				}
			}
			i = j - 1
		case c >= '0' && c <= '9':
			// A number, with its suffix letters.
			j := i
			for j < len(body) && (isIdentByte(body[j]) || body[j] == '.') {
				j++
			}
			i = j - 1
		}
	}
	ok = ok && depth == 0
	memo[name] = ok
	return ok
}

// probeLine is macro m's line of the probe file.
func probeLine(m *macro) string {
	return fmt.Sprintf("__typeof__((%s)) const %s%d = (%s);", m.name, probePrefix, m.k, m.name)
}

// failedLines reads the probe lines Clang's errors name: "probe.c:L:C:
// error" and the notes after it. Errors in no probe line are returned as
// text: the headers themselves failed.
func failedLines(stderr, probeName string, first int) (map[int]bool, string) {
	bad := map[int]bool{}
	var other []string
	var group []string
	groupHit := false
	flush := func() {
		if len(group) > 0 && !groupHit {
			other = append(other, group...)
		}
		group, groupHit = nil, false
	}
	for _, ln := range strings.Split(stderr, "\n") {
		ln = strings.TrimRight(ln, "\r")
		isErr := strings.Contains(ln, ": error: ") || strings.Contains(ln, ": fatal error: ")
		if isErr {
			flush()
			group = append(group, ln)
		} else if strings.Contains(ln, ": note: ") || strings.HasPrefix(ln, "In file included from") {
			if len(group) > 0 {
				group = append(group, ln)
			}
		} else if strings.Contains(ln, ": warning: ") {
			flush()
			continue
		}
		if strings.HasPrefix(ln, probeName+":") {
			rest := ln[len(probeName)+1:]
			if c := strings.IndexByte(rest, ':'); c > 0 {
				if v, err := strconv.Atoi(rest[:c]); err == nil && v >= first {
					if isErr || len(group) > 0 {
						bad[v] = true
						groupHit = true
					}
				}
			}
		}
	}
	flush()
	return bad, strings.Join(other, "\n")
}

// irValues reads each probe global's initializer from the probe's IR:
// "@__mk_m_3 = dso_local constant i32 42, align 4".
func irValues(ir string) map[int]string {
	out := map[int]string{}
	for _, ln := range strings.Split(ir, "\n") {
		if !strings.HasPrefix(ln, "@"+probePrefix) {
			continue
		}
		rest := ln[1+len(probePrefix):]
		sp := strings.IndexByte(rest, ' ')
		if sp < 0 {
			continue
		}
		k, err := strconv.Atoi(rest[:sp])
		if err != nil {
			continue
		}
		i := strings.Index(rest, " constant ")
		if i < 0 {
			continue
		}
		v := rest[i+len(" constant "):]
		if j := strings.LastIndex(v, ", align "); j >= 0 {
			v = v[:j]
		}
		out[k] = v
	}
	return out
}

// mkValue turns an IR initializer of a macro of Mk type mk into a Mk
// literal, or "" when it is none (a pointer, an aggregate, a non-finite
// float, a string that is not UTF-8).
func mkValue(ir, mk string) string {
	sp := strings.IndexByte(ir, ' ')
	if sp < 0 {
		return ""
	}
	ty, v := ir[:sp], strings.TrimSpace(ir[sp+1:])
	switch {
	case strings.HasPrefix(ty, "i") && mk != "" && mk != "str" && mk != "f32" && mk != "f64" && !strings.HasPrefix(mk, "[]"):
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return ""
		}
		bits, _ := strconv.Atoi(ty[1:])
		if mk == "bool" {
			if n == 0 {
				return "false"
			}
			return "true"
		}
		if mk[0] == 'u' {
			u := uint64(n)
			if bits < 64 {
				u &= 1<<uint(bits) - 1
			}
			return strconv.FormatUint(u, 10)
		}
		if n == math.MinInt64 {
			return "-9223372036854775807 - 1"
		}
		return strconv.FormatInt(n, 10)
	case ty == "double" || ty == "float":
		var f float64
		if strings.HasPrefix(v, "0x") {
			u, err := strconv.ParseUint(v[2:], 16, 64)
			if err != nil {
				return ""
			}
			f = math.Float64frombits(u)
		} else {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return ""
			}
			f = x
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return ""
		}
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if len(s) > 64 {
			return ""
		}
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	case ty == "ptr" && mk == "uint":
		if v == "null" {
			return "0"
		}
		if strings.HasPrefix(v, "inttoptr (") && strings.HasSuffix(v, " to ptr)") {
			inner := strings.TrimSuffix(strings.TrimPrefix(v, "inttoptr ("), " to ptr)")
			if isp := strings.IndexByte(inner, ' '); isp > 0 && strings.HasPrefix(inner, "i") {
				bits, _ := strconv.Atoi(inner[1:isp])
				num := strings.TrimSpace(inner[isp+1:])
				if n, err := strconv.ParseInt(num, 10, 64); err == nil {
					u := uint64(n)
					if bits > 0 && bits < 64 {
						u &= 1<<uint(bits) - 1
					}
					return strconv.FormatUint(u, 10)
				}
				if u, err := strconv.ParseUint(num, 10, 64); err == nil {
					return strconv.FormatUint(u, 10)
				}
			}
		}
	case strings.HasPrefix(ty, "[") && strings.HasPrefix(mk, "[]"):
		k := strings.Index(ir, "] ")
		if k < 0 {
			return ""
		}
		header := strings.TrimSpace(ir[1:k])
		xPos := strings.Index(header, " x ")
		if xPos < 0 {
			return ""
		}
		n, err := strconv.Atoi(strings.TrimSpace(header[:xPos]))
		if err != nil || n < 0 || n > 4096 {
			return ""
		}
		elemIR := strings.TrimSpace(header[xPos+3:])
		if !strings.HasPrefix(elemIR, "i") {
			return ""
		}
		bits, _ := strconv.Atoi(elemIR[1:])
		elemMk := mk[2:]
		v = strings.TrimSpace(ir[k+2:])
		if v == "zeroinitializer" {
			if n == 0 {
				return "[]"
			}
			return "[" + strings.Repeat("0, ", n-1) + "0]"
		}
		if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
			return ""
		}
		parts := strings.Split(v[1:len(v)-1], ",")
		if len(parts) != n {
			return ""
		}
		out := make([]string, n)
		for idx, part := range parts {
			part = strings.TrimSpace(part)
			psp := strings.IndexByte(part, ' ')
			if psp < 0 || part[:psp] != elemIR {
				return ""
			}
			valStr := strings.TrimSpace(part[psp+1:])
			val, err := strconv.ParseInt(valStr, 10, 64)
			if err != nil {
				return ""
			}
			if elemMk[0] == 'u' {
				u := uint64(val)
				if bits > 0 && bits < 64 {
					u &= 1<<uint(bits) - 1
				}
				out[idx] = strconv.FormatUint(u, 10)
			} else {
				out[idx] = strconv.FormatInt(val, 10)
			}
		}
		return "[" + strings.Join(out, ", ") + "]"
	case strings.HasPrefix(ty, "[") && mk == "str":
		// "[4 x i8] c\"hi\\0A\\00\"" or "[1 x i8] zeroinitializer"
		k := strings.Index(ir, "] ")
		if k < 0 {
			return ""
		}
		v = strings.TrimSpace(ir[k+2:])
		if v == "zeroinitializer" {
			return `""`
		}
		if !strings.HasPrefix(v, "c\"") || !strings.HasSuffix(v, "\"") {
			return ""
		}
		return irString(v[2 : len(v)-1])
	}
	return ""
}

// irString turns the bytes of an IR c"..." string (NUL-terminated) into a
// Mk string literal.
func irString(s string) string {
	var raw []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+2 < len(s) {
			b, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return ""
			}
			raw = append(raw, byte(b))
			i += 2
			continue
		}
		raw = append(raw, s[i])
	}
	if n := len(raw); n > 0 && raw[n-1] == 0 {
		raw = raw[:n-1]
	}
	if strings.IndexByte(string(raw), 0) >= 0 || !utf8Valid(raw) {
		return ""
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range string(raw) {
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7F:
			fmt.Fprintf(&b, `\x%02X`, c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func utf8Valid(b []byte) bool { return strings.ToValidUTF8(string(b), "�") == string(b) }

// emitMacros writes each probed macro with a value as a const.
func (e *emitter) emitMacros() {
	for _, m := range e.macros {
		st := &e.stats[m.imp]
		st.Decls++
		if !m.ok {
			if e.typeMacro(m) {
				st.Direct++
				continue
			}
			e.skippedDecl(m.imp, "macro", m.name, "macro is no constant")
			continue
		}
		if m.value == "" {
			e.skippedDecl(m.imp, "macro", m.name, "macro value")
			continue
		}
		n := e.scopes[m.imp].fresh(pascal(m.name), "Const")
		w := &e.mods[m.imp].items
		body := m.body
		if len(body) > 120 {
			body = body[:117] + "..."
		}
		fmt.Fprintf(w, "\n// C: #define %s %s\nexport const %s: %s = %s\n", m.name, body, n, m.mk, m.value)
		st.Direct++
	}
}

// parseProto reads a declared function-like macro prototype, "int MAX(int
// a, int b)": its name, result and parameters, in the C type spellings
// parseType reads.
func parseProto(s string) (name string, ret *ctype, ps []param, ok bool) {
	// Every offset below is into the trimmed prototype.
	s = strings.TrimSpace(s)
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(s, ")") {
		return "", nil, nil, false
	}
	head := strings.TrimSpace(s[:open])
	n := len(head)
	for n > 0 && isIdentByte(head[n-1]) {
		n--
	}
	name = head[n:]
	if name == "" {
		return "", nil, nil, false
	}
	ret = parseType(strings.TrimSpace(head[:n]))
	if ret.kind == tBad {
		return "", nil, nil, false
	}
	if open+1 > len(s)-1 {
		return "", nil, nil, false
	}
	inner := strings.TrimSpace(s[open+1 : len(s)-1])
	if inner == "" || inner == "void" {
		return name, ret, nil, true
	}
	depth, start := 0, 0
	var parts []string
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, inner[start:])
	for _, p := range parts {
		p = strings.TrimSpace(p)
		k := len(p)
		for k > 0 && isIdentByte(p[k-1]) {
			k--
		}
		pn := p[k:]
		pt := parseType(strings.TrimSpace(p[:k]))
		if pn == "" || pt.kind == tBad {
			return "", nil, nil, false
		}
		ps = append(ps, param{name: pn, t: pt})
	}
	return name, ret, ps, true
}

// typeMacro binds an object-like macro whose body is one typedef name
// (windows.h's `#define LPTSTR LPSTR`, `#define BOOL WINBOOL`) as a type
// alias of that typedef's binding, and reports whether it did.
func (e *emitter) typeMacro(m *macro) bool {
	b := m.body
	if b == "" || !isIdentByte(b[0]) || (b[0] >= '0' && b[0] <= '9') {
		return false
	}
	for i := 0; i < len(b); i++ {
		if !isIdentByte(b[i]) {
			return false
		}
	}
	if e.u.typedefs[b] == nil {
		return false
	}
	s, why := e.mkType(&ctype{kind: tTypedef, name: b}, m.imp)
	if why != "" || s == "" || s == "void" {
		return false
	}
	n := e.scopes[m.imp].fresh(pascal(m.name), "Type")
	fmt.Fprintf(&e.mods[m.imp].items, "\n// C: #define %s %s\nexport type %s = %s\n", m.name, b, n, s)
	return true
}
