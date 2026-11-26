// Why: only a record, a struct or an array pattern destructures, so an
// error's pattern on the left of `:=` must say so and name the forms that
// work, not report a mismatch whose two sides print the same name.
// What: TestErrorPatternDestructure.
// How: body cases over an error value of one error and of a set of two, at
// the top of the pattern and nested in a struct's field, and an error's
// pattern over a value that holds no such error (a mismatch, with two
// different names).

package check

import (
	"strings"
	"testing"
)

func TestErrorPatternDestructure(t *testing.T) {
	const pre = "error Bad(code: int)\n\nerror Other\n\nstruct W {\n\te: Bad\n}\n\n" // lines 1-8
	for _, tc := range []struct{ name, src, want, msg string }{
		{"one error", pre + "F(e: Bad): int {\n\tBad(code: c) := e\n\treturn c\n}\n", "E0306@10:2", "`Bad` is an error; take it apart with `match`"},
		{"a set of two", pre + "F(e: Bad | Other): int {\n\tBad(code: c) := e\n\treturn c\n}\n", "E0306@10:2", "the value may be any of `Bad|Other`"},
		{"nested", pre + "F(w: W): int {\n\tW(e: Bad(code: c)) := w\n\treturn c\n}\n", "E0306@10:7", "`Bad` is an error"},
		{"no such error", pre + "F(e: Other): int {\n\tBad(code: c) := e\n\treturn c\n}\n", "E0301@10:2", "the pattern names `Bad`, the value is `Other`"},
		{"the forms that work", pre + "F(e: Bad): int {\n\tn := e.code\n\treturn match e {\n\t\tBad(code) { code + n }\n\t}\n}\n", "", ""},
		{"a set of two, matched", pre + "F(e: Bad | Other): int {\n\treturn match e {\n\t\tBad(code) { code }\n\t\t_ { 0 }\n\t}\n}\n", "", ""},
		{"a set of two, one arm", pre + "F(e: Bad | Other): int {\n\treturn match e {\n\t\tBad(code) { code }\n\t}\n}\n", "E0306@10:9", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := checkBody(t, newWorld(t, true), tc.want, tc.src)
			if tc.msg == "" {
				return
			}
			d := b.bodyDiags(t)[0]
			if !strings.Contains(d.Message, tc.msg) {
				t.Fatalf("message %q, want it to say %q", d.Message, tc.msg)
			}
			// The alternatives are forms that compile ("the forms that
			// work" checks them): a match arm naming the fields, and for
			// one error a field read.
			// For a set of several, one arm alone is non-exhaustive, so the
			// alternative names the other arms ("a set of two, matched"
			// checks that such a match compiles, "a set of two, one arm"
			// that one arm does not).
			alts := strings.Join(d.Alternatives, "; ")
			switch tc.name {
			case "one error":
				if !strings.Contains(alts, "match e { Bad(code) { ... } }") || !strings.Contains(alts, "`e.code`") {
					t.Errorf("alternatives %q", alts)
				}
			case "a set of two":
				if !strings.Contains(alts, "an arm `Bad(code) { ... }` and an arm for each other error, or `_ { ... }`") || strings.Contains(alts, "e.code") {
					t.Errorf("alternatives %q", alts)
				}
			}
		})
	}
}
