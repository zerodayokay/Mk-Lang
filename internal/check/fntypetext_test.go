// Why: a fn type whose result is an error union held as a value cannot
// fail, and one written E!T can; a diagnostic that refuses one for the
// other must print them differently.
// What: TestValueResultFnTypeText.
// How: Copy[T] at T = Bad!int against a written fallible signature, as a
// tail call (E0312) and as a fn value (E0301): the two sides differ, and
// the side that cannot fail says so.

package check

import (
	"strings"
	"testing"
)

func TestValueResultFnTypeText(t *testing.T) {
	const pre = "error Bad\n\nCopy[T: Plain](x: &T): T {\n\treturn *x\n}\n\n" // lines 1-6
	const value = "fn(&(Bad!int)): Bad!int (cannot fail)"
	const fails = "fn(&(Bad!int)): Bad!int"
	for _, tc := range []struct{ name, src, want, a, b string }{
		{"tail call", pre + "Caller(x: &(Bad!int)): Bad!int {\n\tbecome Copy[Bad!int](x)\n}\n", "E0312@8:9", "callee", "caller"},
		{"fn value", pre + "Take(f: fn(&(Bad!int)): Bad!int): int {\n\treturn 0\n}\n\nUse(): int {\n\treturn Take(Copy[Bad!int])\n}\n", "E0301@12:14", "found", "expected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := checkBody(t, newWorld(t, true), tc.want, tc.src)
			d := b.bodyDiags(t)[0]
			if got := fact(d, tc.a); got != value {
				t.Errorf("%s %q, want %q", tc.a, got, value)
			}
			if got := fact(d, tc.b); got != fails {
				t.Errorf("%s %q, want %q", tc.b, got, fails)
			}
			if strings.Count(d.Message, "(cannot fail)") != 1 {
				t.Errorf("message %q: want one side marked (cannot fail)", d.Message)
			}
		})
	}
}
