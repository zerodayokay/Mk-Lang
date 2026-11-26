// Why: an item a build could not use (an extern only a shim defines, or a
// declaration whose Mk does not check) must never ship, and nor may
// anything that refers to it; the items left must still name each other
// correctly.
//
// What: View (one view's metadata and items as the generator builds them),
// Dropped (an item removed, with why), and DropItems, which removes items
// and their dependents and renumbers every reference.
//
// How: the dependents are found on the reversed reference graph over all
// views at once (a dxgi item may depend on a windows item); then each view's
// kept items are renumbered in order and every Ref is rewritten.

package sysview

import (
	"fmt"
	"slices"
)

// View is one view: its metadata and its items, in order.
type View struct {
	Meta  Meta
	Items []Item
}

// Dropped is one item removed from a view, and why.
type Dropped struct {
	View, Name, C, Reason string
}

// DropItems removes from views (in table order; a view's Requires name
// views of the slice) the items drop lists, by view name and item index
// with the reason, and every item that depends on a removed one, directly or
// not. It returns the removed items, by view in slice order and then by
// index.
func DropItems(views []View, drop map[string]map[int]string) ([]View, []Dropped, error) {
	byName := map[string]int{}
	for i, v := range views {
		byName[v.Meta.Name] = i
	}
	type ref struct{ v, i int }
	target := func(v int, r Ref) (ref, error) {
		if r.View == 0 {
			return ref{v, r.Item}, nil
		}
		req := views[v].Meta.Requires
		if r.View > len(req) {
			return ref{}, fmt.Errorf("sysview: view %s refers to required view %d of %d", views[v].Meta.Name, r.View, len(req))
		}
		w, ok := byName[req[r.View-1]]
		if !ok {
			return ref{}, fmt.Errorf("sysview: view %s requires %s, which is not generated", views[v].Meta.Name, req[r.View-1])
		}
		return ref{w, r.Item}, nil
	}
	users := map[ref][]ref{}
	for v, view := range views {
		for i, it := range view.Items {
			for _, d := range it.Deps {
				t, err := target(v, d)
				if err != nil {
					return nil, nil, err
				}
				users[t] = append(users[t], ref{v, i})
			}
		}
	}
	why := map[ref]string{}
	var work []ref
	names := make([]string, 0, len(drop))
	for n := range drop {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		v, ok := byName[n]
		if !ok {
			return nil, nil, fmt.Errorf("sysview: no view %s to drop from", n)
		}
		idx := make([]int, 0, len(drop[n]))
		for i := range drop[n] {
			idx = append(idx, i)
		}
		slices.Sort(idx)
		for _, i := range idx {
			r := ref{v, i}
			if _, done := why[r]; !done {
				why[r] = drop[n][i]
				work = append(work, r)
			}
		}
	}
	for len(work) > 0 {
		r := work[0]
		work = work[1:]
		for _, u := range users[r] {
			if _, done := why[u]; !done {
				why[u] = "needs " + views[r.v].Meta.Name + "." + views[r.v].Items[r.i].Name + ", which is dropped"
				work = append(work, u)
			}
		}
	}
	// The new index of every kept item.
	renum := make([][]int, len(views))
	var out []Dropped
	for v, view := range views {
		renum[v] = make([]int, len(view.Items))
		n := 0
		for i, it := range view.Items {
			if w, gone := why[ref{v, i}]; gone {
				renum[v][i] = -1
				out = append(out, Dropped{View: view.Meta.Name, Name: it.Name, C: it.C, Reason: w})
				continue
			}
			renum[v][i] = n
			n++
		}
	}
	res := make([]View, len(views))
	for v, view := range views {
		res[v].Meta = view.Meta
		for i, it := range view.Items {
			if renum[v][i] < 0 {
				continue
			}
			deps := make([]Ref, 0, len(it.Deps))
			for _, d := range it.Deps {
				t, _ := target(v, d)
				deps = append(deps, Ref{View: d.View, Item: renum[t.v][t.i]})
			}
			it.Deps = deps
			res[v].Items = append(res[v].Items, it)
		}
	}
	return res, out, nil
}
