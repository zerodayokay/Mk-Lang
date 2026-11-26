// Why: some std panics end the process, so Mk test blocks cannot observe them.
// What: TestStd_ListTruncatePanics and TestStd_ExactDigitsPanics.
// How: builds small programs that panic on purpose and checks the exit code,
// the message, and that nothing prints after the panic.

package std

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const std10TruncateProgram = `linear struct Tok {
    n: int
}

Drop(t: Tok) {
    Tok(n: _) := t
}

export Main(sys: &System) {
    var out := sys.Stdout()
    which := sys.args[1]
    Print(mut out, "before\n", ()) else {
        return
    }
    out.Flush() else {
        return
    }
    if which == "negative" {
        var l := List[int].New(sys.arena)
        l.Push(1) else {
            return
        }
        l.Truncate(-1)
    } else {
        var l := List[Tok].New(sys.arena)
        for i in 0..3 {
            t := Tok(n: i)
            l.Push(t) else {
                Drop(t)
            }
        }
        l.Truncate(3)
        l.Truncate(100)
        if which == "linear" {
            l.Truncate(1)
        }
        for x := l.Pop() {
            Drop(x)
        }
        l.Truncate(0)
    }
    Print(mut out, "after\n", ()) else {
        return
    }
}
`

// std10DigitsPanicProgram calls math.ExactDigits with a digit count its
// mode refuses, or math.ShortestDigitsInto with an out of 16 bytes: code 8,
// exit 101.
const std10DigitsPanicProgram = `import std.math

export Main(sys: &System) {
    var out := sys.Stdout()
    which := sys.args[1]
    Print(mut out, "before\n", ()) else {
        return
    }
    out.Flush() else {
        return
    }
    var d := math.ExactDigits(1.5, 0, math.DigitMode.Fraction)
    if which == "significant" {
        d = math.ExactDigits(1.5, 0, math.DigitMode.Significant)
    } else if which == "halfup" {
        d = math.ExactDigits(1.5, 0, math.DigitMode.SignificantHalfUp)
    } else if which == "negative" {
        d = math.ExactDigits(1.5, -1, math.DigitMode.FractionHalfUp)
    } else if which == "short" {
        var b: [16]u8 = [0; 16]
        d.len = math.ShortestDigitsInto(mut b[..], 1.5).len
    }
    Print(mut out, "after {n}\n", (n: d.len)) else {
        return
    }
}
`

// TestStd_ExactDigitsPanics: fewer than one significant digit, or a
// negative count, panics with code 8, and so does a ShortestDigitsInto
// whose out holds fewer than 17 bytes; zero fraction digits does not.
func TestStd_ExactDigitsPanics(t *testing.T) {
	t.Parallel()
	exe := collBuild(t, std10DigitsPanicProgram)
	cases := []struct{ name, msg string }{
		{"significant", "math.ExactDigits: fewer than one significant digit"},
		{"halfup", "math.ExactDigits: fewer than one significant digit"},
		{"negative", "math.ExactDigits: a negative digit count"},
		{"short", "math.ShortestDigitsInto: out holds fewer than 17 bytes"},
		{"fraction", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), collTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, c.name)
			cmd.Dir = filepath.Dir(exe)
			var out, errb bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errb
			err := cmd.Run()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if c.msg == "" {
				if code != 0 || out.String() != "before\nafter 1\n" {
					t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and \"before\\nafter 1\\n\" (1.5 to 0 digits is 2)", code, out.String(), errb.String())
				}
				return
			}
			if code != 101 || out.String() != "before\n" || !strings.Contains(errb.String(), "panic: "+c.msg) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 101, \"before\\n\" and %q", code, out.String(), errb.String(), "panic: "+c.msg)
			}
		})
	}
}

// TestStd_ListTruncatePanics: List.Truncate to a negative length panics
// with code 2, and a Truncate that would drop a linear element with code 8,
// as List.Clear does; a Truncate that drops nothing (n at or above Len) is a
// no-op even on linear elements. Each panic exits 101 with its message and
// prints nothing after. The other List, text, sync and Deque checks are test
// blocks in tests_list.mk, tests_text.mk, tests_sync.mk and tests_deque.mk.
func TestStd_ListTruncatePanics(t *testing.T) {
	t.Parallel()
	exe := collBuild(t, std10TruncateProgram)
	cases := []struct{ name, msg string }{
		{"negative", "bounds: List.Truncate: negative length"},
		{"linear", "List.Truncate would drop a linear value"},
		{"noop", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), collTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, c.name)
			cmd.Dir = filepath.Dir(exe)
			var out, errb bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errb
			err := cmd.Run()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if c.msg == "" {
				if code != 0 || out.String() != "before\nafter\n" || errb.Len() != 0 {
					t.Fatalf("exit %d, stdout %q, stderr %q; want 0, \"before\nafter\n\" and no stderr", code, out.String(), errb.String())
				}
				return
			}
			if code != 101 || out.String() != "before\n" || !strings.Contains(errb.String(), "panic: "+c.msg) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 101, \"before\n\" and %q", code, out.String(), errb.String(), "panic: "+c.msg)
			}
		})
	}
}
