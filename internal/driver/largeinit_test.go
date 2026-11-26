// Why: a const's or global's initializer builds its value in its frame and
// copies it to the result, so a large table filled the default 1 MiB frame
// limit at 512 KiB (256 KiB when a function returned it), and a global
// cannot state the fuel that raises it.
// What: TestLargeInitializerValue.
// How: zeroed and `[7; n]` tables of 512 KiB and 16 MiB and a nested
// [256][256]u64, as var and as const, check clean, and so do tables a
// function returns or builds two calls down (four to eight copies at once;
// at 16 MiB the fuel floor binds too); a loop that fills a 4 MiB table
// runs out of that floor and E0902 reports it, not the default fuel; a
// recursion with a table-sized result still stops at the stack limit.

package driver

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"mk/internal/types"
)

func TestLargeInitializerValue(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1 << 16, 1 << 21} {
		for _, kw := range []string{"var", "const"} {
			for _, init := range []string{"mem.Zeroed[[%d]u64]()", "[7; %d]"} {
				src := fmt.Sprintf("import std.mem\n\n%s T: [%d]u64 = %s\n\nexport Read(i: int): u64 {\n    _ = mem.Zeroed[u8]()\n    return T[i]\n}\n", kw, n, fmt.Sprintf(init, n))
				t.Run(fmt.Sprintf("%s %d %s", kw, n, init[:3]), func(t *testing.T) {
					t.Parallel()
					res, _, _ := gen10Check(t, map[string]string{"a/a.mk": src}, types.Dev)
					clean(t, res)
				})
			}
		}
	}
	for _, kw := range []string{"var", "const"} {
		for _, init := range []string{"mem.Zeroed[[256][256]u64]()", "[[7; 256]; 256]"} {
			src := "import std.mem\n\n" + kw + " T: [256][256]u64 = " + init + "\n\nexport Read(i: int): u64 {\n    _ = mem.Zeroed[u8]()\n    return T[i][i]\n}\n"
			t.Run(kw+" nested "+init[:3], func(t *testing.T) {
				t.Parallel()
				res, _, _ := gen10Check(t, map[string]string{"a/a.mk": src}, types.Dev)
				clean(t, res)
			})
		}
	}
	helpers := "export Build(): [%[1]d]u64 {\n    var t: [%[1]d]u64 = mem.Zeroed[[%[1]d]u64]()\n    t[3] = 5\n    return t\n}\n\n" +
		"export Pass(t: [%[1]d]u64): [%[1]d]u64 {\n    var u := t\n    u[4] = 6\n    return u\n}\n\n" +
		"export Twice(): [%[1]d]u64 {\n    return Pass(Build())\n}\n\n"
	for _, c := range []struct {
		kw, init string
		n        int
	}{{"var", "Build()", 1 << 21}, {"const", "Build()", 1 << 16}, {"var", "Twice()", 1 << 16}} {
		src := fmt.Sprintf("import std.mem\n\n"+helpers+"%s T: [%d]u64 = %s\n\nexport Read(i: int): u64 {\n    return T[i]\n}\n", c.n, c.kw, c.n, c.init)
		t.Run(fmt.Sprintf("%s %d %s", c.kw, c.n, c.init), func(t *testing.T) {
			t.Parallel()
			res, _, _ := gen10Check(t, map[string]string{"a/a.mk": src}, types.Dev)
			clean(t, res)
		})
	}
	t.Run("a loop past the fuel floor reports that fuel", func(t *testing.T) {
		t.Parallel()
		src := "Fill(): [1048576]u32 {\n    var t: [1048576]u32 = [0; 1048576]\n    for i in 0..1048576 {\n        t[i] = Cast[u32](i % 251)\n    }\n    return t\n}\n\n" +
			"var T: [1048576]u32 = Fill()\n\nexport Read(i: int): u32 {\n    return T[i]\n}\n"
		res, _, _ := gen10Check(t, map[string]string{"a/a.mk": src}, types.Dev)
		text := res.Report.Text(res.Sources)
		if got := codes(res); !slices.Equal(got, []string{"E0902"}) || !strings.Contains(text, "fuel: 8388608") || !strings.Contains(text, "fuel 16777216") {
			t.Errorf("codes %q, want E0902 with the 4 MiB value's fuel of 8388608 and its double:\n%s", got, text)
		}
	})
	t.Run("recursion still stops", func(t *testing.T) {
		t.Parallel()
		h := "export Down(n: int): [64]u64 {\n    if n == 0 {\n        return [0; 64]\n    }\n    return Down(n - 1)\n}\n"
		res, _, _ := gen10Check(t, map[string]string{"h/h.mk": h, "a/a.mk": "import h\n\nconst A: [64]u64 = h.Down(50_000)\n"}, types.Dev)
		if got := codes(res); !slices.Equal(got, []string{"E0901"}) || !strings.Contains(res.Report.Text(res.Sources), "stack limit") {
			t.Errorf("codes %q, want the stack limit's E0901:\n%s", got, res.Report.Text(res.Sources))
		}
	})
}
