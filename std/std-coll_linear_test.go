// Why: a container must never drop or free linear contents silently.
// What: TestStdColl_LinearContentsPanic over HeapList, HeapMap and TextMap.
// How: each case expects exit 101 with its panic message and nothing after;
// the same containers emptied first clear and free with no panic.

package std

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const collLinearProgram = `linear struct Tok {
    n: int
}

const Keys: [3]str = ["k0", "", "k2"]

Drop(t: Tok) {
    Tok(n: _) := t
}

export Main(sys: &System) {
    var out := sys.Stdout()
    a := sys.heap.Alloc()
    which := sys.args[1]
    Print(mut out, "before\n", ()) else {
        return
    }
    out.Flush() else {
        return
    }
    if which == "heaplist-clear" {
        var l := HeapList[Tok].New(a)
        t := Tok(n: 1)
        l.Push(a, t) else {
            Drop(t)
            l.FreeEmpty(a)
            return
        }
        l.Clear()
        for x := l.Pop() {
            Drop(x)
        }
        l.FreeEmpty(a)
    } else if which == "heapmap-freeempty" {
        var m := HeapMap[int, Tok].New(a)
        t := Tok(n: 1)
        o := m.Replace(a, 1, t) else {
            Drop(t)
            m.FreeEmpty(a)
            return
        }
        if v := o {
            Drop(v)
        }
        m.FreeEmpty(a)
    } else if which == "textmap-freeempty" {
        var m := TextMap[Tok].New(a)
        t := Tok(n: 1)
        o := m.Replace(a, "k", t) else {
            Drop(t)
            m.FreeEmpty(a)
            return
        }
        if v := o {
            Drop(v)
        }
        m.FreeEmpty(a)
    } else {
        var h := HeapList[Tok].New(a)
        var m := HeapMap[int, Tok].New(a)
        var tm := TextMap[Tok].New(a)
        for i in 0..3 {
            t2 := Tok(n: i)
            h.Push(a, t2) else {
                Drop(t2)
            }
            t3 := Tok(n: i)
            o3 := m.Replace(a, i, t3) else {
                Drop(t3)
                continue
            }
            if v := o3 {
                Drop(v)
            }
            t4 := Tok(n: i)
            o4 := tm.Replace(a, Keys[i], t4) else {
                Drop(t4)
                continue
            }
            if v := o4 {
                Drop(v)
            }
        }
        for x := h.Pop() {
            Drop(x)
        }
        h.Clear()
        h.FreeEmpty(a)
        for i in 0..3 {
            if v := m.Remove(&i) {
                Drop(v)
            }
        }
        m.FreeEmpty(a)
        for k in Keys {
            if v := tm.Remove(a, k) {
                Drop(v)
            }
        }
        tm.FreeEmpty(a)
    }
    Print(mut out, "after\n", ()) else {
        return
    }
}
`

// TestStdColl_LinearContentsPanic: HeapList.Clear of linear elements and
// FreeEmpty of a non-empty HeapMap or TextMap are code 11 (free non-empty);
// their Free on linear values that are not Free is E0701. Each panics with
// exit 101 and its message and prints nothing after; the same containers
// emptied first (Pop, Remove) clear and free with no panic. An arena List
// holds no linear element (MKH-596), so it has no case here. HeapList.Free's
// own case is conformance's tests/programs/panics/free_nonempty.mk, and the
// map drops of code 8 are tested in regress_test.go.
func TestStdColl_LinearContentsPanic(t *testing.T) {
	t.Parallel()
	exe := collBuild(t, collLinearProgram)
	cases := []struct{ name, msg string }{
		{"heaplist-clear", "free non-empty: HeapList.Clear of linear elements"},
		{"heapmap-freeempty", "free non-empty: HeapMap.FreeEmpty of a non-empty map"},
		{"textmap-freeempty", "free non-empty: TextMap.FreeEmpty of a non-empty map"},
		{"emptied", ""},
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
