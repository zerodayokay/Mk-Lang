// Why: an Experimental std module may change in any minor release, so a
// package that depends on one should be told, and the tools must agree on
// which modules are Experimental.
//
// What: the module classes (moduleClasses, moduleClass, experimental,
// classSuffix) and the notes `mk check` prints (experimentalNotes,
// experimentalNote, joinAnd).
//
// How: one table of module paths and classes. `mk check` notes each
// package that imports an Experimental module, directly or through a
// dependency; `mk query` shows a module's class; `mk apidiff` reports no
// change to an Experimental module as breaking (apidiff_rules.go).

package driver

import (
	"slices"
	"sort"
	"strings"
)

// A module class: the compatibility promise a std module makes.
const (
	ClassStable       = "Stable"
	ClassProvisional  = "Provisional"
	ClassExperimental = "Experimental"
)

// moduleClasses are the std modules that are not Stable, each with why a
// note names it. A module under one of them (std.tls.x) has its class.
// Provisional items inside a Stable module (proc.Pty, net.Transport) are a
// reference fact, not a module's class.
var moduleClasses = []struct{ path, class, why string }{
	{"std.dl", ClassProvisional, ""},
	{"std.free", ClassProvisional, ""},
	{"std.gpu", ClassExperimental, "its API may change before stabilisation"},
	{"std.image", ClassProvisional, ""},
	{"std.mmio", ClassProvisional, ""},
	{"std.sim", ClassProvisional, ""},
	{"std.simd", ClassProvisional, ""},
	{"std.tls", ClassExperimental, "not yet reviewed by an outside cryptographer"},
	{"std.window", ClassExperimental, "its API may change before stabilisation"},
	{"std.x509", ClassExperimental, "not yet reviewed by an outside cryptographer"},
}

// moduleClass is the class of the module at path, and the reason an
// Experimental one gives.
func moduleClass(path string) (class, why string) {
	for _, c := range moduleClasses {
		if path == c.path || strings.HasPrefix(path, c.path+".") {
			return c.class, c.why
		}
	}
	return ClassStable, ""
}

// experimental reports whether the module at path is Experimental.
func experimental(path string) bool {
	c, _ := moduleClass(path)
	return c == ClassExperimental
}

// classSuffix is " (Class)" for a module that is not Stable, else "".
func classSuffix(path string) string {
	if c, _ := moduleClass(path); c != ClassStable {
		return " (" + c + ")"
	}
	return ""
}

// experimentalNotes are mk check's notes: one per package of the build (the
// root first, then its dependencies by name) whose modules reach an
// Experimental std module, directly or through a dependency package. A
// module reached only through another Experimental module (std.x509 under
// std.tls) is not named again.
func (s *Session) experimentalNotes() []string {
	if s.pkgB == nil {
		return nil
	}
	type pkg struct {
		name string
		b    *build
	}
	root := "main"
	if s.pkgMan != nil && s.pkgMan.Name != "" {
		root = s.pkgMan.Name
	}
	pkgs := []pkg{{root, s.pkgB}}
	owner := map[*modNode]string{}
	for _, m := range s.pkgB.mods {
		owner[m] = root
	}
	var deps []pkg
	for _, b := range s.depBs {
		if b.prefix == "" {
			continue // a C build
		}
		name := strings.TrimSuffix(b.prefix, ".")
		deps = append(deps, pkg{name, b})
		for _, m := range b.mods {
			owner[m] = name
		}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].name < deps[j].name })
	pkgs = append(pkgs, deps...)
	var notes []string
	for _, p := range pkgs {
		// entries: Experimental module -> the dependency package whose
		// module imports it ("" when the package's own module does).
		entries := map[string]string{}
		seen := map[*modNode]bool{}
		var walk func(m *modNode)
		walk = func(m *modNode) {
			if seen[m] {
				return
			}
			seen[m] = true
			if experimental(m.path) {
				return
			}
			for _, d := range m.deps {
				if d.std && experimental(d.path) {
					via := owner[m]
					if m.std || via == p.name {
						via = ""
					}
					if old, ok := entries[d.path]; !ok || via == "" && old != "" {
						entries[d.path] = via
					}
					continue
				}
				walk(d)
			}
		}
		for _, m := range p.b.mods {
			walk(m)
		}
		if len(entries) == 0 {
			continue
		}
		notes = append(notes, experimentalNote(p.name, entries))
	}
	return notes
}

// experimentalNote is one package's note: `note: std.tls is Experimental,
// not yet reviewed by an outside cryptographer (package app imports it)`.
func experimentalNote(pkgName string, entries map[string]string) string {
	var mods []string
	for m := range entries {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	_, why := moduleClass(mods[0])
	verb, obj := "is", "it"
	if len(mods) > 1 {
		verb, obj = "are", "them"
	}
	var vias []string
	for _, m := range mods {
		if v := entries[m]; v != "" && !slices.Contains(vias, v) {
			vias = append(vias, v)
		}
	}
	direct := false
	for _, m := range mods {
		direct = direct || entries[m] == ""
	}
	how := "package " + pkgName + " imports " + obj
	if !direct {
		how += " through dependency " + strings.Join(vias, ", ")
	}
	text := "note: " + joinAnd(mods) + " " + verb + " " + ClassExperimental
	if why != "" {
		text += ", " + why
	}
	return text + " (" + how + ")"
}

// joinAnd is "a", "a and b", "a, b and c".
func joinAnd(xs []string) string {
	if len(xs) == 1 {
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
