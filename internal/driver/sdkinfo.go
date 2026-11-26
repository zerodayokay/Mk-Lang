// Why: a user must be able to see which system views this mk carries,
// what they were generated from, and which system headers a C import would
// read, without reading binary archives or guessing an SDK's path.
//
// What: SDKText, the text of `mk sdk` and `mk sdk views`, and
// sysViewVersionLine, mk version's line about them.
//
// How: the views from sysview.Table, each target's embedded archive's
// metadata (inputs, counts, identity) or why it is absent; then, per target
// of C imports, the system headers a pkg.CImport reads today
// (MK_SYSROOT_<target>, else the evidence directory) and those one with
// sdk: true would read (hostSDKFor), with the flavour mk reads them as.

package driver

import (
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"mk/internal/cimport"
	"mk/internal/sysview"
)

// SDKText is the output of `mk sdk views` (views only) and of `mk sdk`.
func SDKText(viewsOnly bool) string {
	return sdkText(viewsOnly, realHostEnv())
}

func sdkText(viewsOnly bool, e hostEnv) string {
	var b strings.Builder
	b.WriteString("system views (import c.<view>, no pkg.CImport needed):\n")
	for _, r := range sysview.Table {
		for _, t := range r.Targets {
			data, ok := sysview.Embedded(t, r.Name)
			if !ok {
				fmt.Fprintf(&b, "  c.%-12s %-15s not carried: generate it with go run ./tools/sysview %s\n", r.Name, t, sysViewInputHint(r.Input))
				continue
			}
			a, err := sysview.Open(data)
			if err != nil {
				fmt.Fprintf(&b, "  c.%-12s %-15s does not open: %v\n", r.Name, t, err)
				continue
			}
			m, id := a.Meta(), a.ID()
			var in []string
			for _, k := range slices.Sorted(maps.Keys(m.Inputs)) {
				in = append(in, k+" "+m.Inputs[k])
			}
			fmt.Fprintf(&b, "  c.%-12s %-15s %d items (%d dropped)\n", r.Name, t, a.Len(), m.Counts["dropped"])
			fmt.Fprintf(&b, "      headers  %s\n", strings.Join(m.Headers, " "))
			if len(m.Requires) > 0 {
				fmt.Fprintf(&b, "      requires %s\n", strings.Join(m.Requires, " "))
			}
			fmt.Fprintf(&b, "      inputs   %s\n", strings.Join(in, ", "))
			fmt.Fprintf(&b, "      identity %s\n", hex.EncodeToString(id[:8]))
		}
	}
	if viewsOnly {
		return b.String()
	}
	b.WriteString("\nsystem headers of a pkg.CImport, by target:\n")
	for _, t := range []string{"x86_64-windows", "x86_64-linux", "aarch64-linux"} {
		fmt.Fprintf(&b, "  %s\n", t)
		dirs := sysrootFor(t, e.getenv)
		why := "MK_SYSROOT_" + t
		if e.getenv("MK_SYSROOT_"+t) == "" && e.getenv("MK_SYSROOT_"+strings.ReplaceAll(t, "-", "_")) == "" {
			why = "$MK_EVIDENCE/c-import/sysroots/" + t
		}
		writeSysroot(&b, "without sdk: true", t, dirs, why)
		h := hostSDKFor(t, e)
		writeSysroot(&b, "with sdk: true   ", t, h.Dirs, h.Source)
	}
	return b.String()
}

// writeSysroot prints one setup: its directories, where they come from and
// the flavour and triple the headers are read as.
func writeSysroot(b *strings.Builder, label, target string, dirs []string, why string) {
	if len(dirs) == 0 {
		fmt.Fprintf(b, "    %s none: only Clang's and the package's headers\n", label)
		return
	}
	f := cimport.SysrootFlavour(target, dirs)
	fmt.Fprintf(b, "    %s %s (from %s; %s flavour, triple %s)\n", label, strings.Join(dirs, string(listSep)), why, f, cimport.HeaderTriple(target, f))
}

// listSep separates the directories of a sysroot as mk sdk prints them.
const listSep = ';'

// sysViewVersionLine is mk version's line about the system views.
func sysViewVersionLine() string {
	n := 0
	var inputs []string
	for _, r := range sysview.Table {
		for _, t := range r.Targets {
			data, ok := sysview.Embedded(t, r.Name)
			if !ok {
				continue
			}
			n++
			if a, err := sysview.Open(data); err == nil {
				for k, v := range a.Meta().Inputs {
					if k == string(r.Input) {
						s := k + " " + v
						if !slices.Contains(inputs, s) {
							inputs = append(inputs, s)
						}
					}
				}
			}
		}
	}
	slices.Sort(inputs)
	if n == 0 {
		return "system views: none carried (mk sdk)\n"
	}
	return fmt.Sprintf("system views: %d carried, from %s (mk sdk lists them and the system headers C imports read)\n", n, strings.Join(inputs, ", "))
}
