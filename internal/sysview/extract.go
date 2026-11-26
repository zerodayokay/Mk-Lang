// Why: a build that imports a system view checks only the items its files
// name and what those need, so the size of windows.h costs it nothing.
//
// What: Set (the opened views of a build, by name), Module (one view's
// extracted Mk text) and Extract, which keeps the wanted items and their
// dependencies across required views and writes each view's module text.
//
// How: a walk over item indexes from the wanted names' items (found by
// folded name, as the importer's prune matches them); each kept item's
// dependencies are read from its index record, so the walk costs the size of
// what it keeps. Texts are read last, per view in item order, each chunk
// inflated once. The text a build sees is a function of the archives and the
// set of wanted names, never of map or walk order.

package sysview

import (
	"fmt"
	"slices"
	"strings"
)

// Set is the opened views of one build, by name.
type Set map[string]*Archive

// Module is one view's extracted text.
type Module struct {
	Name string
	Text string
	// Items are the kept items, in item order, and Lines the 1-based line
	// of Text each starts on.
	Items []int
	Lines []int
}

// Extract keeps, in each view want names, the items that define a wanted
// name (by Fold) and every item those depend on, in that view or one it
// requires, and returns a Module for each view want names or a kept item
// lies in, sorted by name. With all set every item of every view of views
// is kept and want is ignored.
func Extract(views Set, want map[string][]string, all bool) ([]Module, error) {
	keep := map[string][]bool{}
	type ref struct {
		view string
		item int
	}
	var work []ref
	mark := func(view string, i int) error {
		a := views[view]
		if a == nil {
			return fmt.Errorf("sysview: no archive for the system view %s", view)
		}
		k := keep[view]
		if k == nil {
			k = make([]bool, a.Len())
			keep[view] = k
		}
		if i < 0 || i >= len(k) {
			return fmt.Errorf("sysview: view %s has no item %d", view, i)
		}
		if !k[i] {
			k[i] = true
			work = append(work, ref{view, i})
		}
		return nil
	}
	names := make([]string, 0, len(want))
	if all {
		for v := range views {
			names = append(names, v)
		}
	} else {
		for v := range want {
			names = append(names, v)
		}
	}
	slices.Sort(names)
	for _, v := range names {
		a := views[v]
		if a == nil {
			return nil, fmt.Errorf("sysview: no archive for the system view %s", v)
		}
		if keep[v] == nil {
			keep[v] = make([]bool, a.Len())
		}
		if all {
			for i := 0; i < a.Len(); i++ {
				if err := mark(v, i); err != nil {
					return nil, err
				}
			}
			continue
		}
		for _, n := range want[v] {
			for _, i := range a.Lookup(n) {
				if err := mark(v, i); err != nil {
					return nil, err
				}
			}
		}
	}
	for len(work) > 0 {
		r := work[len(work)-1]
		work = work[:len(work)-1]
		a := views[r.view]
		deps, _, err := a.ItemDeps(r.item)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			v := r.view
			if d.View > 0 {
				req := a.meta.Requires
				if d.View > len(req) {
					return nil, fmt.Errorf("sysview: view %s item %d refers to required view %d of %d", r.view, r.item, d.View, len(req))
				}
				v = req[d.View-1]
			}
			if err := mark(v, d.Item); err != nil {
				return nil, err
			}
		}
	}
	var out []Module
	views2 := make([]string, 0, len(keep))
	for v := range keep {
		views2 = append(views2, v)
	}
	slices.Sort(views2)
	for _, v := range views2 {
		m, err := moduleText(views[v], keep[v])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// moduleText writes the module of view a holding the items kept marks:
// the head comment, the imports the kept items use (std.raw, then the
// required views in Requires order), then the items in order, one blank
// line apart.
func moduleText(a *Archive, kept []bool) (Module, error) {
	m := Module{Name: a.meta.Name}
	for i, k := range kept {
		if k {
			m.Items = append(m.Items, i)
		}
	}
	raw := false
	usesReq := make([]bool, len(a.meta.Requires))
	for _, i := range m.Items {
		deps, r, err := a.ItemDeps(i)
		if err != nil {
			return Module{}, err
		}
		raw = raw || r
		for _, d := range deps {
			if d.View > 0 {
				usesReq[d.View-1] = true
			}
		}
	}
	texts, err := a.Text(m.Items)
	if err != nil {
		return Module{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// The system view c.%s for %s, shipped with mk (see doc/reference.md, \"System views\").\n", a.meta.Name, a.meta.Target)
	fmt.Fprintf(&b, "// headers: %s\n", strings.Join(a.meta.Headers, ", "))
	line := 3
	var imports []string
	if raw {
		imports = append(imports, "import std.raw")
	}
	for k, r := range a.meta.Requires {
		if usesReq[k] {
			imports = append(imports, "import c."+r)
		}
	}
	if len(imports) > 0 {
		b.WriteString("\n")
		line++
		for _, im := range imports {
			b.WriteString(im)
			b.WriteString("\n")
			line++
		}
	}
	for _, t := range texts {
		b.WriteString("\n")
		line++
		m.Lines = append(m.Lines, line)
		b.WriteString(t)
		b.WriteString("\n")
		line += strings.Count(t, "\n") + 1
	}
	m.Text = b.String()
	return m, nil
}
