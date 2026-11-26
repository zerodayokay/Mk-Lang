// Why: a const's or global's initializer builds its value in its frame and
// copies it to the result, so a large table filled the default 1 MiB frame
// limit at 512 KiB, and a global cannot state the fuel that raises it.
// What: TestLargeInitializerValue.
// How: zeroed and `[7; n]` tables of 512 KiB and 16 MiB and a nested
// [256][256]u64, as var and as const, check clean; a recursion with a
// table-sized result still stops at the stack limit.

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
	t.Run("recursion still stops", func(t *testing.T) {
		t.Parallel()
		h := "export Down(n: int): [64]u64 {\n    if n == 0 {\n        return [0; 64]\n    }\n    return Down(n - 1)\n}\n"
		res, _, _ := gen10Check(t, map[string]string{"h/h.mk": h, "a/a.mk": "import h\n\nconst A: [64]u64 = h.Down(50_000)\n"}, types.Dev)
		if got := codes(res); !slices.Equal(got, []string{"E0901"}) || !strings.Contains(res.Report.Text(res.Sources), "stack limit") {
			t.Errorf("codes %q, want the stack limit's E0901:\n%s", got, res.Report.Text(res.Sources))
		}
	})
}
