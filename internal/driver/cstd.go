// Why: a std module may bind C, and its C binding exists only once the
// package that uses it has built its C imports; such modules cannot be
// checked with the rest of std.
//
// What: the late std modules: isCPath, stdBuild, splitLate, stdImports,
// lateClosure, missingC, reach and pipeline.addLate.
//
// How: a std module that imports c or c.<name>, and every std module that
// imports such a module, directly or not, is late. A late module leaves
// the std build at the import step. It is resolved and checked only when a
// module outside the late set imports it, and then in a build of its own
// that runs after the package's C build, whose binding modules it imports
// (the binding modules import std, so they cannot run before std). Without
// the C import and a C source in the package, the importer's import line is
// E0202 needs-cimport; a package that never imports the module never sees
// it.

package driver

import (
	"slices"
	"strings"

	"mk/internal/diag"
)

// isCPath reports whether import path p names a C import.
func isCPath(p string) bool { return p == "c" || strings.HasPrefix(p, "c.") }

// stdBuild is the std build b's std imports resolve against: b itself for
// std, the std of a late build, else b.std.
func (b *build) stdBuild() *build {
	switch {
	case b.lateOf != nil:
		return b.lateOf
	case b.isStd:
		return b
	}
	return b.std
}

// splitLate moves the std build's late modules out of b.mods into b.late,
// once, before the import step resolves any import. They stay in byName, so an importer
// still finds them. Only a std build of listed std modules splits.
func (b *build) splitLate() {
	if !b.isStd || b.lateOf != nil || b.reached != nil {
		return
	}
	b.reached = map[*modNode]bool{}
	late := map[*modNode]bool{}
	for _, m := range b.mods {
		for _, f := range m.files {
			for _, il := range b.w.importLines(f) {
				// A system view is in the std build itself (sysviews.go).
				if isCPath(il.Path) && !isSysViewPath(il.Path) {
					late[m] = true
				}
			}
		}
	}
	if len(late) == 0 {
		return
	}
	for grew := true; grew; {
		grew = false
		for _, m := range b.mods {
			if late[m] {
				continue
			}
			for _, d := range b.stdImports(m) {
				if late[d] {
					late[m], grew = true, true
					break
				}
			}
		}
	}
	keep := b.mods[:0:0]
	for _, m := range b.mods {
		if late[m] {
			m.late = true
			b.late = append(b.late, m)
			continue
		}
		keep = append(keep, m)
	}
	b.mods = keep
}

// stdImports are the std modules m's import lines name, in source order.
func (b *build) stdImports(m *modNode) []*modNode {
	var out []*modNode
	for _, f := range m.files {
		for _, il := range b.w.importLines(f) {
			if d := b.byName[il.Path]; d != nil && strings.HasPrefix(il.Path, "std.") {
				out = append(out, d)
			}
		}
	}
	return out
}

// lateClosure is m and the late modules it imports, directly or not.
func (b *build) lateClosure(m *modNode) []*modNode {
	seen := map[*modNode]bool{m: true}
	out := []*modNode{m}
	for i := 0; i < len(out); i++ {
		for _, d := range b.stdImports(out[i]) {
			if d.late && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// missingC is the name of the first C import that late module m, or a late
// module it imports, needs and the package does not provide with a C
// source; "" when every one is there.
func (b *build) missingC(m *modNode) string {
	if m == nil {
		return ""
	}
	for _, x := range b.lateClosure(m) {
		for _, f := range x.files {
			for _, il := range b.w.importLines(f) {
				if !isCPath(il.Path) || isSysViewPath(il.Path) {
					continue
				}
				if c := b.cBuild; c == nil || !c.cSources || c.byName[il.Path] == nil {
					return strings.TrimPrefix(il.Path, "c.")
				}
			}
		}
	}
	return ""
}

// reach records that an importer outside the late set imports late module
// m, so m and the late modules it imports are built.
func (b *build) reach(m *modNode) {
	for _, x := range b.lateClosure(m) {
		b.reached[x] = true
	}
}

// addLate forms the late build of the std build whose late modules an
// importer reached, links it, and places it in p.builds after std and the
// package's C build, which then come before every other build: a
// dependency's module may import a late module too, and the scheduler
// takes a dependency outside a build's own modules as done (schedule).
func (p *pipeline) addLate() []*diag.Diagnostic {
	var std *build
	for _, b := range p.builds {
		if b.isStd && len(b.reached) > 0 {
			std = b
			break
		}
	}
	if std == nil {
		return nil
	}
	lb := &build{w: std.w, byName: std.byName, selDirs: std.selDirs, isStd: true, lateOf: std, cBuild: std.cBuild}
	for _, m := range std.late {
		if std.reached[m] {
			lb.mods = append(lb.mods, m)
		}
	}
	ds := lb.link()
	var rest []*build
	for _, b := range p.builds {
		if b != std && b != std.cBuild {
			rest = append(rest, b)
		}
	}
	order := []*build{std}
	if std.cBuild != nil && slices.Contains(p.builds, std.cBuild) {
		order = append(order, std.cBuild)
	}
	p.builds = append(append(order, lb), rest...)
	return ds
}
