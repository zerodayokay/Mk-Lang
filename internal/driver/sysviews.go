// Why: `import c.windows` must work in any package with no manifest entry,
// no SDK and no Clang, through the system views mk carries; a std module
// must be able to import one too, so a view's types are one type in the
// whole program, whichever package names them.
//
// What: sysViews (the session's system views), the load step's
// loadSysViews (which views the program names, the items it uses, and the
// modules extracted for them), the import step's sysViewFor, isSysViewPath,
// sysViewArchive (which archive a target reads) and the cache of extracted
// texts.
//
// How: after every build's files are read, their `import c.<view>` lines
// and the names they use are collected (cimport.Uses, as the importer's
// prune does): every std module's, and every other module's whose build has
// no pkg.CImport of that name (the manifest's own C import wins). The views
// named, and those they require, are opened; the wanted items and their
// dependencies are extracted (sysview.Extract) into one module per view,
// c.sys.<view>, added to the std build after its own modules, with the
// marks of a C import's binding (cimport, unsafe-permitted). One module per
// view per program: a std module, a dependency and the package see one
// windows.Hwnd. The texts are cached by the archives' identities and the
// sorted names wanted. A program that names no view gets nothing: no
// module, no file, no read.

package driver

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"mk/internal/cache"
	"mk/internal/cimport"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/sysview"
)

// sysViewPrefix starts every system view module's path: c.sys.<view>.
// No package module, C import or dependency's C import has a path of this
// shape (c is reserved, a C import's name is one segment), so a package's
// own pkg.CImport named like a view coexists with the view.
const sysViewPrefix = "c.sys."

// sysViews are one session's system views.
type sysViews struct {
	dir string // the cache directory
	// mods are the modules extracted, by view name.
	mods map[string]*modNode
	// missing are the views the program names that this mk carries no
	// archive of, with the archive's path under internal/sysview/data.
	missing map[string]string
	// funcs maps each function a kept item binds to its view's name.
	funcs map[string]string
	// key is the extraction's cache key; hit: the texts came from the cache.
	key source.Hash
	hit bool
}

// isSysViewPath reports whether import path p names a system view of the
// table (on any target).
func isSysViewPath(p string) bool {
	name, ok := strings.CutPrefix(p, "c.")
	if !ok {
		return false
	}
	_, ok = sysview.Named(name)
	return ok
}

// sysViewArchive is the target whose archive of row a build for target
// reads: target itself when the row is generated for it, else the row's
// first target (a Windows view checked in a build for Linux reads the
// Windows view: its declarations are plain Mk).
func sysViewArchive(row sysview.Row, target string) string {
	if slices.Contains(row.Targets, target) {
		return target
	}
	return row.Targets[0]
}

// sysViewBytes is the archive of view name for target: the test hook's,
// else the one embedded in mk.
func (w *world) sysViewBytes(target, name string) ([]byte, bool) {
	if h := w.cfg.TestHooks.SysViews; h != nil {
		return h(target, name)
	}
	return sysview.Embedded(target, name)
}

// ownsCImport reports whether b's own C build (its package's pkg.CImport)
// provides c.<name>.
func (b *build) ownsCImport(name string) bool {
	return b.cBuild != nil && b.cBuild.byName[b.prefix+"c."+name] != nil
}

// loadSysViews forms the system view modules the program's files name, in
// the std build, once every build's files are read. Its diagnostics are an
// archive that does not open.
func (p *pipeline) loadSysViews() []*diag.Diagnostic {
	w := p.w
	sv := w.sysviews
	if sv == nil || len(p.builds) == 0 || !p.builds[0].isStd {
		return nil
	}
	stdB := p.builds[0]
	want := map[string]map[string]bool{}
	for _, b := range p.builds {
		for _, m := range b.mods {
			for _, f := range m.files {
				if f.src == nil || !strings.Contains(f.src.Text, "c.") {
					continue
				}
				fw := cimport.Want{}
				cimport.Uses(f.src.Text, fw)
				for v, names := range fw {
					if _, ok := sysview.Named(v); !ok || !m.std && !m.sysview && b.ownsCImport(v) {
						continue
					}
					if want[v] == nil {
						want[v] = map[string]bool{}
					}
					maps.Copy(want[v], names)
				}
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	if w.cfg.TestHooks.SysViewWhole {
		for v := range want {
			want[v] = nil
		}
	}
	target := w.cfg.Target.Name
	set := sysview.Set{}
	sv.missing = map[string]string{}
	var open func(name string) *diag.Diagnostic
	open = func(name string) *diag.Diagnostic {
		if _, done := set[name]; done {
			return nil
		}
		if _, done := sv.missing[name]; done {
			return nil
		}
		row, _ := sysview.Named(name)
		at := sysViewArchive(row, target)
		data, ok := w.sysViewBytes(at, name)
		if !ok {
			sv.missing[name] = "internal/sysview/data/" + at + "/" + sysview.ArchiveName(name)
			return nil
		}
		a, err := sysview.Open(data)
		if err != nil {
			return diag.Toolchain("fs", "read", err.Error(), "the system view c."+name+" that this mk carries does not open: "+err.Error())
		}
		set[name] = a
		for _, r := range a.Meta().Requires {
			if d := open(r); d != nil {
				return d
			}
		}
		return nil
	}
	views := slices.Sorted(maps.Keys(want))
	for _, v := range views {
		if d := open(v); d != nil {
			return []*diag.Diagnostic{d}
		}
	}
	// A view whose required view is missing cannot be extracted: it is
	// missing too, with the archive it lacks.
	for changed := true; changed; {
		changed = false
		for name, a := range set {
			for _, r := range a.Meta().Requires {
				if why, gone := sv.missing[r]; gone {
					sv.missing[name] = why
					delete(set, name)
					changed = true
				}
			}
		}
	}
	names := map[string][]string{}
	for _, v := range views {
		if set[v] != nil {
			names[v] = slices.Sorted(maps.Keys(want[v]))
		}
	}
	if len(set) == 0 {
		return nil
	}
	mods, d := sv.extract(w, set, names, w.cfg.TestHooks.SysViewWhole)
	if d != nil {
		return []*diag.Diagnostic{d}
	}
	sv.mods = map[string]*modNode{}
	sv.funcs = map[string]string{}
	var added []*modNode
	for _, em := range mods {
		row, _ := sysview.Named(em.Name)
		at := sysViewArchive(row, target)
		prefix := "sysview/" + at + "/"
		file := em.Name + ".mk"
		t := &srcTree{mem: map[string]string{file: em.Text}, prefix: prefix, sitePrefix: prefix}
		m := moduleFrom(t, "", []string{file})
		m.name, m.path = sysViewPrefix+em.Name, sysViewPrefix+em.Name
		m.unsafe, m.cimport, m.sysview = true, true, true
		m.done = make(chan struct{})
		stdB.mods = append(stdB.mods, m)
		sv.mods[em.Name] = m
		added = append(added, m)
		for _, fn := range em.Funcs {
			sv.funcs[fn] = em.Name
		}
	}
	for _, m := range added {
		for _, f := range m.files {
			raw, _ := m.tree.readFile(f.rel)
			path := m.tree.path(f.rel)
			f.src = w.sm.Add(path, raw)
			w.noteSitePath(f.src.ID, m.tree.sitePath(f.rel))
		}
	}
	return nil
}

// extracted is one view's module as the cache stores it.
type extracted struct {
	Name  string
	Text  string
	Funcs []string // the functions its kept items bind
}

// extract is the modules of names (wanted names by view, sorted) from set,
// from the cache when its key is there. The key is the archives'
// identities, by view name, and the names wanted.
func (sv *sysViews) extract(w *world, set sysview.Set, names map[string][]string, whole bool) ([]extracted, *diag.Diagnostic) {
	h := source.NewHasher("mk.driver.sysview.v1")
	for _, v := range slices.Sorted(maps.Keys(set)) {
		id := set[v].ID()
		h.String(v)
		h.Bytes(id[:])
	}
	h.Bool(whole)
	for _, v := range slices.Sorted(maps.Keys(names)) {
		h.String(v)
		h.Uvarint(uint64(len(names[v])))
		for _, n := range names[v] {
			h.String(n)
		}
	}
	sv.key = h.Sum()
	st := w.openStore(sv.dir)
	if st != nil {
		if raw, ok, _ := st.Get(cache.KindCImport, sv.key); ok {
			var out []extracted
			if json.Unmarshal(raw, &out) == nil {
				sv.hit = true
				return out, nil
			}
		}
	}
	mods, err := sysview.Extract(set, names, whole)
	if err != nil {
		return nil, diag.Toolchain("fs", "read", err.Error(), "a system view that this mk carries is corrupt: "+err.Error())
	}
	out := make([]extracted, len(mods))
	for i, m := range mods {
		out[i] = extracted{Name: m.Name, Text: m.Text}
		a := set[m.Name]
		for _, it := range m.Items {
			if kind, defs, err := a.Head(it); err == nil && kind == "func" {
				out[i].Funcs = append(out[i].Funcs, defs...)
			}
		}
	}
	if st != nil {
		if data, err := json.Marshal(out); err == nil {
			_ = st.Put(cache.KindCImport, sv.key, data)
		}
	}
	return out, nil
}

// sysViewFor resolves import path p of module m to a system view module:
// for a std module or a view, any view name; for another module, a view
// name its own build's C imports do not provide. It returns nil and ""
// when p is no view's, nil and a reason for a view this mk lacks.
func (b *build) sysViewFor(m *modNode, p string) (*modNode, string) {
	if !isSysViewPath(p) {
		return nil, ""
	}
	name := strings.TrimPrefix(p, "c.")
	if !m.std && !m.sysview && b.ownsCImport(name) {
		return nil, ""
	}
	sv := b.w.sysviews
	if sv == nil {
		// A corpus unit's session forms no views: E0202 as before.
		return nil, ""
	}
	if dep := sv.mods[name]; dep != nil {
		return dep, ""
	}
	return nil, "no-sysview"
}

// sysViewMessage is the E0202 message of a view this mk carries no archive
// of.
func (w *world) sysViewMessage(p string) string {
	name := strings.TrimPrefix(p, "c.")
	where := ""
	if sv := w.sysviews; sv != nil {
		where = sv.missing[name]
	}
	row, _ := sysview.Named(name)
	if where == "" {
		where = "internal/sysview/data/" + sysViewArchive(row, w.cfg.Target.Name) + "/" + sysview.ArchiveName(name)
	}
	return fmt.Sprintf("module %s: this mk was built without the system view's archive %s: generate it with `go run ./tools/sysview` (%s) and build mk again, or declare a pkg.CImport named %s",
		p, where, sysViewInputHint(row.Input), name)
}

// sysViewInputHint names the flag of tools/sysview that gives input in.
func sysViewInputHint(in sysview.Input) string {
	switch in {
	case sysview.InputWindowsSDK:
		return `-sdk "<Windows Kits\10>"`
	case sysview.InputVulkanHeaders:
		return "-vulkan <Vulkan-Headers include directory>"
	}
	return "with the view's headers"
}

// sysViewNames lists the system views, as "c.<name>", for E0202's hint.
func sysViewNames() []string {
	var out []string
	for _, r := range sysview.Table {
		out = append(out, "c."+r.Name)
	}
	return out
}
