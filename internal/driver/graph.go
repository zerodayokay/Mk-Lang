// Why: the module graph is known only once every import line is matched to
// a module; nothing can be scheduled before that, and every import error
// must be found in one run.
//
// What: the import step of loading (link): import paths matched byte for
// byte against the module table, E0202 with every reason, E0205 cycles
// (cycles, cycleThrough), module depths (depths, modOrder), and the
// import-time E1102 for unsafe code (unsafeUses).
//
// How: cycles are Tarjan's strongly connected components, each reported once
// at the import line in its lexicographically first module; depths order
// the modules by (depth, path), so slot order is a function of the source
// tree alone.

package driver

import (
	"fmt"
	"slices"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
)

// lookupImport finds the module an import path names, for importer m, or
// the E0202 reason and alternatives when there is none.
func (b *build) lookupImport(m *modNode, p string) (dep *modNode, reason string, alts []string) {
	switch {
	case p == "c" || strings.HasPrefix(p, "c."):
		// The package's C imports; a std module reaches the package's, and
		// without it is E0202 needs-cimport. A dependency's c.<name> is its
		// own (module <dep>.c.<name>). std is C-free, so the C a std module
		// binds must be the package's own c_sources: without one it is
		// needs-cimport too. A system view's name reaches the view for a
		// std module and a view, and for another module when its own C
		// imports do not have the name (sysviews.go).
		if dep, reason := b.sysViewFor(m, p); dep != nil || reason != "" {
			return dep, reason, nil
		}
		if b.cBuild != nil {
			if dep := b.cBuild.byName[b.prefix+p]; dep != nil && (!m.std || b.cBuild.cSources) {
				return dep, "", nil
			}
		}
		if m.std {
			return nil, "needs-cimport", nil
		}
		return nil, "no-cimport", b.cImportNames()
	case p == "std.pkg":
		return nil, "manifest-only", nil
	case strings.HasPrefix(p, "std.") && isFree(b.w.cfg.Target) && !slices.Contains(freeStd, p):
		// A freestanding program imports layers 0 and 1, std.free, derive,
		// dyn and std.raw only (free.go).
		return nil, "freestanding", nil
	case p == "std.raw" && (m.cimport || m.unsafe) && !m.std:
		// A module listed in unsafe_modules (and a C import's
		// binding, whose accessors read through raw pointers) may import
		// std.raw.
		std := b.std
		if b.isStd {
			std = b
		}
		if std != nil {
			if dep := std.byName[p]; dep != nil {
				return dep, "", nil
			}
		}
		return nil, "std-internal", nil
	case isStdInternal(p) && !m.std:
		// Whether or not this std provides the module yet.
		return nil, "std-internal", nil
	case strings.HasPrefix(p, "std."):
		if stdTargetRefused(p, b.w.cfg.Target.Name) {
			return nil, "target-selected", nil // std.linux off x86_64-linux
		}
		std := b
		if !b.isStd {
			std = b.std
		}
		if std != nil {
			if dep := std.byName[p]; dep != nil {
				if dep.late && !m.late {
					// The C a late std module binds is asked for at its
					// importer's import line.
					if std.missingC(dep) != "" {
						return nil, "needs-cimport", nil
					}
					std.reach(dep)
				}
				return dep, "", nil
			}
		}
		if slices.Contains(section17Modules, p) {
			return nil, "not-implemented", nil
		}
	default:
		if b.isStd {
			break
		}
		// A first segment that names a dependency of this package reaches
		// that package's modules by their full names; any other path is
		// this package's own, under its prefix (doc/packages.md).
		// The first segment is a local name of the importer's manifest;
		// the dependency's modules carry its canonical id (deps_walk.go).
		first, _, _ := strings.Cut(p, ".")
		if db := b.depBuilds[first]; db != nil {
			q := depModule(db, p)
			if dep := db.byName[q]; dep != nil {
				return dep, "", nil
			}
			if sel, ok := db.selDirs[q]; ok {
				return nil, "target-selected", []string{"import " + first + "." + sel}
			}
			var alts []string
			for _, a := range db.closest(m, q) {
				alts = append(alts, "import "+localModule(db, first, strings.TrimPrefix(a, "import ")))
			}
			return nil, "not-found", alts
		}
		if dep := b.byName[b.prefix+p]; dep != nil {
			return dep, "", nil
		}
		if sel, ok := b.selDirs[b.prefix+p]; ok {
			return nil, "target-selected", []string{"import " + sel}
		}
	}
	return nil, "not-found", b.closest(m, p)
}

// closest is the module path whose folded spelling equals p's, if any.
func (b *build) closest(m *modNode, p string) []string {
	want := resolve.Fold(p)
	var names []string
	for _, x := range b.mods {
		if x.sysview {
			continue // imported by the view's import path, c.<view>
		}
		if m.tree == x.tree {
			names = append(names, strings.TrimPrefix(x.name, b.prefix)) // as this package spells it
		} else {
			names = append(names, x.name) // a dependency, from its importer
		}
	}
	if b.std != nil {
		if len(b.std.allNames) > 0 {
			for _, name := range b.std.allNames {
				if !isStdInternal(name) || m.std {
					names = append(names, name)
				}
			}
		} else {
			for _, x := range b.std.mods {
				if !x.sysview && (!isStdInternal(x.name) || m.std) {
					names = append(names, x.name)
				}
			}
		}
	}
	slices.Sort(names)
	for _, n := range names {
		if n != p && resolve.Fold(n) == want {
			return []string{"import " + n}
		}
	}
	return nil
}

// importSite is one import line resolved to a module.
type importSite struct {
	line resolve.ImportLine
	dep  *modNode
}

// link runs the import step over the build: imports, cycles, depths, E1102. The
// diagnostics are in module order; the build stops on any of them.
func (b *build) link() []*diag.Diagnostic {
	b.w.cfg.hook("link")
	b.splitLate()
	out := b.caseDupDiags()
	sites := map[*modNode][]importSite{}
	for _, m := range b.mods {
		m.imports = map[string]*modNode{}
		for _, f := range m.files {
			for _, il := range b.w.importLines(f) {
				dep, reason, alts := b.lookupImport(m, il.Path)
				if _, seen := m.imports[il.Path]; !seen || dep != nil {
					m.imports[il.Path] = dep
				}
				if dep == nil {
					d := diag.New(diag.E0202, il.PathSpan, b.unknownMessage(il.Path, reason)).
						Text("module", il.Path).Text("reason", reason)
					for _, a := range alts {
						d.Alt(a)
					}
					out = append(out, d)
					continue
				}
				if d := b.internalImport(m, dep, il); d != nil {
					out = append(out, d) // E0214 (deps_internal.go)
					continue
				}
				sites[m] = append(sites[m], importSite{il, dep})
			}
		}
	}
	for _, m := range b.mods {
		var deps []*modNode
		for _, d := range m.imports {
			if d != nil && !slices.Contains(deps, d) {
				deps = append(deps, d)
			}
		}
		slices.SortFunc(deps, modOrder)
		m.deps = deps
	}
	out = append(out, b.cycles(sites)...)
	out = append(out, b.unsafeUses()...)
	return out
}

// unknownMessage is E0202's message for a reason.
func (b *build) unknownMessage(p, reason string) string {
	switch reason {
	case "no-cimport":
		return fmt.Sprintf("module %s: no pkg.CImport in pkg.mk's c_imports is named %s, and no system view is (the system views are %s; mk sdk views lists them)",
			p, strings.TrimPrefix(p, "c."), strings.Join(sysViewNames(), ", "))
	case "no-sysview":
		return b.w.sysViewMessage(p)
	case "needs-cimport":
		name := strings.TrimPrefix(p, "c.")
		if std := b.stdBuild(); std != nil && strings.HasPrefix(p, "std.") {
			name = std.missingC(std.byName[p])
		}
		return fmt.Sprintf("module %s: this standard library module needs the package to declare the C import %s in pkg.mk (and its C source)", p, name)
	case "manifest-only":
		return "module std.pkg is reachable only from pkg.mk, through its implicit binding `pkg`"
	case "std-internal":
		return fmt.Sprintf("module %s is internal to the standard library", p)
	case "not-implemented":
		return fmt.Sprintf("module %s is listed in doc/reference.md but this standard library does not provide it yet", p)
	case "freestanding":
		return fmt.Sprintf("module %s is not available on x86_64-freestanding, which has std's layers 0 and 1, std.free, derive and dyn only", p)
	case "target-selected":
		if t, ok := stdTargetOnly[p]; ok {
			return fmt.Sprintf("module %s exists only on %s, not on %s: import it only in a module that pkg.mk's targets select for that target", p, t, b.w.cfg.Target.Name)
		}
		return fmt.Sprintf("module %s is a target-selected directory; import it by its selection name", p)
	}
	return fmt.Sprintf("no module %s in this package or the standard library", p)
}

// cycles reports each strongly connected component of size above one, and
// each self-import, as one E0205 at the import line, in the component's
// lexicographically first module, that starts the cycle.
func (b *build) cycles(sites map[*modNode][]importSite) []*diag.Diagnostic {
	own := map[*modNode]bool{}
	for _, m := range b.mods {
		own[m] = true
	}
	index := map[*modNode]int{}
	low := map[*modNode]int{}
	on := map[*modNode]bool{}
	var stack []*modNode
	var comps [][]*modNode
	next := 0
	var visit func(m *modNode)
	visit = func(m *modNode) {
		index[m], low[m] = next, next
		next++
		stack = append(stack, m)
		on[m] = true
		for _, d := range m.deps {
			if !own[d] {
				continue
			}
			if _, ok := index[d]; !ok {
				visit(d)
				low[m] = min(low[m], low[d])
			} else if on[d] {
				low[m] = min(low[m], index[d])
			}
		}
		if low[m] == index[m] {
			var c []*modNode
			for {
				x := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[x] = false
				c = append(c, x)
				if x == m {
					break
				}
			}
			comps = append(comps, c)
		}
	}
	order := slices.Clone(b.mods)
	slices.SortFunc(order, modOrder)
	for _, m := range order {
		if _, ok := index[m]; !ok {
			visit(m)
		}
	}
	var out []*diag.Diagnostic
	for _, c := range comps {
		self := len(c) == 1 && slices.Contains(c[0].deps, c[0])
		if len(c) == 1 && !self {
			continue
		}
		slices.SortFunc(c, modOrder)
		first := c[0]
		var at importSite
		for _, s := range sites[first] {
			if slices.Contains(c, s.dep) {
				at = s
				break
			}
		}
		cycle := cycleThrough(first, at.dep, c, sites)
		out = append(out, diag.New(diag.E0205, at.line.At,
			"import cycle: "+strings.Join(cycle, " -> ")).Fact("cycle", diag.TextList(cycle...)))
		for _, m := range c {
			m.deps = slices.DeleteFunc(m.deps, func(d *modNode) bool { return slices.Contains(c, d) })
		}
	}
	return out
}

// cycleThrough is the cycle in import order that starts with first's import
// of next: the shortest import path from next back to first inside the
// component c, found breadth first with each module's imports in source
// order, so every consecutive pair is a real import edge. In a
// strongly connected component such a path always exists.
func cycleThrough(first, next *modNode, c []*modNode, sites map[*modNode][]importSite) []string {
	parent := map[*modNode]*modNode{next: nil}
	queue := []*modNode{next}
	for len(queue) > 0 && next != first {
		cur := queue[0]
		queue = queue[1:]
		if cur == first {
			break
		}
		for _, s := range sites[cur] {
			if _, seen := parent[s.dep]; seen || !slices.Contains(c, s.dep) {
				continue
			}
			parent[s.dep] = cur
			queue = append(queue, s.dep)
		}
	}
	var back []string // first, then its predecessors, back to next
	for x := first; x != nil && x != next; x = parent[x] {
		back = append(back, x.name)
	}
	cycle := []string{first.name, next.name}
	for i := len(back) - 1; i >= 0; i-- {
		cycle = append(cycle, back[i])
	}
	return cycle
}

// modOrder orders modules by path, then by directory: total even when two
// modules share a path (a duplicate, reported by the import step), so no order
// depends on map iteration.
func modOrder(x, y *modNode) int {
	if c := strings.Compare(x.path, y.path); c != 0 {
		return c
	}
	return strings.Compare(x.dir, y.dir)
}

// depths numbers each module 0 when it imports nothing, else one more than
// its deepest import, and orders the build's modules by (depth,
// path), so slot order is a function of the source tree only. The graph is
// acyclic here: link stops the build on E0205.
func (b *build) depths() {
	memo := map[*modNode]int{}
	var depth func(m *modNode) int
	depth = func(m *modNode) int {
		if d, ok := memo[m]; ok {
			return d
		}
		memo[m] = 0
		d := 0
		for _, x := range m.deps {
			d = max(d, depth(x)+1)
		}
		memo[m] = d
		return d
	}
	for _, m := range b.mods {
		m.depth = depth(m)
	}
	slices.SortStableFunc(b.mods, func(x, y *modNode) int {
		if x.depth != y.depth {
			return x.depth - y.depth
		}
		return modOrder(x, y)
	})
}

// unsafeUses is the import step's E1102: an unsafe block or an unsafe impl
// in a module that pkg.mk's unsafe_modules does not permit. Single-file and
// directory mode permit no module.
func (b *build) unsafeUses() []*diag.Diagnostic {
	var out []*diag.Diagnostic
	for _, m := range b.mods {
		if m.unsafe {
			continue
		}
		for _, f := range m.files {
			if f.sum == nil {
				continue
			}
			for _, u := range f.sum.Unsafe {
				what := "an unsafe block"
				if u.Impl {
					what = "an unsafe impl"
				}
				at := source.Span{File: f.src.ID, Start: u.Start, End: u.End}
				out = append(out, diag.New(diag.E1102, at,
					fmt.Sprintf("%s in module %s, which pkg.mk's unsafe_modules does not list", what, m.name)).
					Text("module", m.name).Text("manifest", "unsafe_modules"))
			}
		}
	}
	return out
}

// unsafeKeyword is the span of the first unsafe token inside s.
func unsafeKeyword(f *fileNode, s source.Span) source.Span {
	toks := f.ast.Tokens
	i, _ := slices.BinarySearchFunc(toks, s.Start, func(t ast.Token, off uint32) int {
		switch {
		case t.Start < off:
			return -1
		case t.Start > off:
			return 1
		}
		return 0
	})
	for ; i < len(toks) && toks[i].Start < s.End; i++ {
		if toks[i].Kind == ast.TokUnsafe {
			return source.Span{File: s.File, Start: toks[i].Start, End: toks[i].Start + toks[i].Len}
		}
	}
	return s
}
