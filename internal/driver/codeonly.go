// Why: tools that look for a call in Mk source text (mk bench's selection
// matches testing.Bench( and testing.BenchSys( only in code) must not count
// a call that is only mentioned in a comment or a string.
//
// What: codeOnly, which blanks comments and literal bodies, and
// codeNameByte. tools/gate's mkCodeOnly is a copy (the gate imports no
// internal package).
//
// How: blanked text is replaced by spaces with newlines kept, so a match
// on the result has the same offsets as in the source.

package driver

import "strings"

// codeOnly is t with every line comment, and with keepStrings false every
// literal's body, replaced by spaces (newlines kept), so a pattern matched
// on it sees only code, at the same offsets: a call mentioned in a
// comment or a string is no call. The literals are Mk's:
// `"..."` and `'...'` with escapes, `r"..."` with none, and `"""`
// multi-line text to the next `"""`. Mk has no block comments.
func codeOnly(t string, keepStrings bool) string {
	b := []byte(t)
	blank := func(i int) {
		if !keepStrings && b[i] != '\n' {
			b[i] = ' '
		}
	}
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		case c == '"' && strings.HasPrefix(t[i:], `"""`):
			i += 3
			for ; i < len(b) && !strings.HasPrefix(t[i:], `"""`); i++ {
				if b[i] == '\\' && i+1 < len(b) {
					blank(i)
					i++
				}
				blank(i)
			}
			i += 2 // the closing quotes
		case c == '"' && i > 0 && b[i-1] == 'r' && (i < 2 || !codeNameByte(b[i-2])):
			for i++; i < len(b) && b[i] != '"' && b[i] != '\n'; i++ {
				blank(i) // a raw string: no escapes
			}
		case c == '"' || c == '\'':
			for i++; i < len(b) && b[i] != c && b[i] != '\n'; i++ {
				if b[i] == '\\' && i+1 < len(b) && b[i+1] != '\n' {
					blank(i)
					i++
				}
				blank(i)
			}
		}
	}
	return string(b)
}

// codeNameByte reports whether c can be part of a name.
func codeNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
