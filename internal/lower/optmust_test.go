// Why: a dropped `o.Must()` or `o else { return }` on a ?T of a linear
// payload must be borrow's E0402 on a T, with T's release facts, as its
// E!T counterparts are, not on the ?T temporary with none.
// What: TestOptDroppedLinearPayload.
// How: Must and the statement form of else on a ?Res parameter; the MIR
// must move the some payload into a Res temporary.

package lower

import (
	"strings"
	"testing"

	"mk/internal/mir"
)

func TestOptDroppedLinearPayload(t *testing.T) {
	l := lowerSrc(t, resSrc+`Must(o: ?Res) {
	o.Must()
}

Else(o: ?Res) {
	o else { return }
}

Drop(o: ?Res) {
	_ = o.Must()
}
`)
	for _, k := range []string{"Must", "Else", "Drop"} {
		s := l.print(k)
		d := mir.NoLocal
		for i, lc := range l.body(k).Locals {
			if lc.Kind == mir.LocalTemp && l.w.tab.TypeString(lc.Type, nil) == "Res" {
				d = mir.LocalID(i)
				break
			}
		}
		if d == mir.NoLocal || !strings.Contains(s, "_"+itoa(int(d))+" = move (_2 as some).0\n") {
			t.Errorf("%s: the some payload is not moved into a Res temporary:\n%s", k, s)
		}
	}
}
