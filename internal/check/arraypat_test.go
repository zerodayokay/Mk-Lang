// Why: an array pattern `[a, b] := xs` binds every element of a fixed
// array exactly once, and its errors must say which of its demands failed,
// since E0301 and E0305 are shared with other patterns.
// What: TestArrayPatternDiagnostics.
// How: each case checks one body and compares the codes at their
// positions, then a word of the message or of an alternative.

package check

import (
	"strings"
	"testing"
)

func TestArrayPatternDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, src, want, says string }{
		{"names every element", "F(xs: [2]int): int {\n\t[a, b] := xs\n\treturn a + b\n}\n", "", ""},
		{"not an array", "F(r: (a: int, b: int)): int {\n\t[a, b] := r\n\treturn a + b\n}\n", "E0301@2:2", "needs a fixed array"},
		{"const generic length", "F[const N: int](xs: [N]int): int {\n\t[a, b] := xs\n\treturn a + b\n}\n", "E0301@2:2", "a length known here"},
		{"too few names", "F(xs: [2]int): int {\n\t[a] := xs\n\treturn a\n}\n", "E0305@2:2", "every element of `[2]int`"},
		{"record pattern on an array", "F(xs: [2]int): int {\n\t(a, b) := xs\n\treturn a + b\n}\n", "E0301@2:2", "`[a, b] := e`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			b := checkBody(t, w, tc.want, tc.src)
			if tc.says == "" {
				return
			}
			for _, d := range b.bodyDiags(t) {
				if strings.Contains(d.Message, tc.says) || strings.Contains(strings.Join(d.Alternatives, "\n"), tc.says) {
					return
				}
			}
			t.Errorf("no diagnostic says %q", tc.says)
		})
	}
}
