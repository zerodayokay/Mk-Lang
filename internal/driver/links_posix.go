// Why: referencing a native POSIX function may select the ELF libc entry,
// while an import or a type use must leave the executable static.
// What: the eligible POSIX symbols of a bin and their versioned stub names.
// How: read the exact system-view module's reachable extern instances and
// let explicit archives and shared objects supply their symbols first.

package driver

import (
	"fmt"
	"slices"
	"strings"

	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/types"
)

type posixMode uint8

const (
	posixFinal posixMode = iota
	posixDiscover
	posixSelected
)

// nativeInstances selects the runtime before Mk code generation. It only
// probes the link plan when a reached POSIX call could change the mode.
func (w *world) nativeInstances(j *nativeJob, env mono.Env, opt *mono.Options, buildC func() ([]string, []*diag.Diagnostic)) (*mono.Instances, posixMode, []*diag.Diagnostic) {
	var insts *mono.Instances
	var ds []*diag.Diagnostic
	collect := func() {
		if ie := source.Guard("instances of "+j.mainMod, source.Span{}, func() {
			insts, ds = mono.Collect(env, w.fromCRoots(w.buildRoots(j.mainMod, j.pkg, j.allFuncs, j.gro), j.lib != nil), *opt)
		}); ie != nil {
			ds = []*diag.Diagnostic{diag.FromInternal(ie)}
		}
	}
	j.timed("instances", func() {
		// Decode reachable cached bodies in parallel before serial collection.
		w.prefetchBinBodies(*j)
		collect()
	})
	if len(ds) > 0 {
		return nil, posixFinal, ds
	}
	if ds := w.unsupportedPosix(insts); len(ds) > 0 {
		return nil, posixFinal, ds
	}
	if opt.LibcMode || len(w.posixNames(insts)) == 0 {
		return insts, posixFinal, nil
	}
	cobjs, ds := buildC()
	if len(ds) > 0 {
		return nil, posixFinal, ds
	}
	// Archive selection needs referenced extern vars, but no pack ownership.
	members := make([]int, len(insts.List))
	for i := range members {
		members[i] = i
	}
	globals := w.globalsOf(insts, []mono.Pack{{Members: members}}, j.pkg)
	trial, ds := w.linkPlanFor(insts, globals, cobjs, posixDiscover)
	if len(ds) > 0 {
		return nil, posixFinal, ds
	}
	if trial == nil || len(trial.sos) == 0 {
		return insts, posixFinal, nil
	}
	if j.lib != nil {
		return nil, posixFinal, []*diag.Diagnostic{diag.New(diag.E0202, source.Span{},
			"bin "+j.name+": reaching a c.posix function requires system-libc mode, which only an executable takes").
			Text("target", w.cfg.Target.Name).Text("reason", "not-built")}
	}
	// linkedMods attached std exports for C's static fallback. A libc-mode
	// bin keeps the program's roots and libc's allocator.
	j.pkg = slices.DeleteFunc(slices.Clone(j.pkg), func(m *modNode) bool { return m.std })
	opt.LibcMode = true
	j.timed("instances", collect)
	return insts, posixSelected, ds
}

// posixNames returns symbol -> version for the bin's reached libc functions.
// A hand-written or package C import's extern of the same name cannot
// activate automatic libc.
func (w *world) posixNames(insts *mono.Instances) map[string]string {
	sv := w.sysviews
	if _, ok := libcFor(w.cfg.Target); !ok || sv == nil || len(sv.posixLinks) == 0 {
		return nil
	}
	mod := sv.mods["posix"].id
	var out map[string]string
	for i := range insts.List {
		in := &insts.List[i]
		it := w.items.Get(in.Item)
		if !posixExtern(it, in, mod) {
			continue
		}
		lk := sv.posixLinks[in.Symbol]
		if lk.Lib != libcSoname || lk.Lazy || !strings.HasPrefix(lk.Version, "GLIBC_") || !validVersion(lk.Version) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[in.Symbol] = lk.Version
	}
	return out
}

// unsupportedPosix stops fallback declarations from resolving by accident
// to a same-named function of another system.
func (w *world) unsupportedPosix(insts *mono.Instances) []*diag.Diagnostic {
	sv := w.sysviews
	if _, ok := libcFor(w.cfg.Target); ok || sv == nil || sv.mods["posix"] == nil {
		return nil
	}
	var ds []*diag.Diagnostic
	for i := range insts.List {
		in := &insts.List[i]
		it := w.items.Get(in.Item)
		if posixExtern(it, in, sv.mods["posix"].id) {
			msg := fmt.Sprintf("%s is a function of c.posix for Linux; this build targets %s", in.Symbol, w.cfg.Target.Name)
			ds = append(ds, diag.New(diag.E0001, it.Span, msg).Text("tool", "mk").Text("symbol", in.Symbol).Text("view", "c.posix").
				Alt("call c.posix functions only when Target.os == Os.Linux"))
		}
	}
	return ds
}

func posixExtern(it *types.Item, in *mono.Instance, mod types.ModuleID) bool {
	fd, ok := it.Decl.(*types.FuncDecl)
	return it.Module == mod && in.Body == nil && ok && fd.Sig.Extern
}

// addPosix supplies unresolved functions after the package's own symbols and
// links have had their turn. A previously selected mode keeps the libc
// entry even if a folded branch removes its last POSIX reference.
func (p *linkPlan) addPosix(w *world, names map[string]string, mode posixMode) *diag.Diagnostic {
	if mode == posixSelected {
		if d := p.ensureLibc(w); d != nil {
			return d
		}
	}
	for _, name := range p.cand {
		version, ok := names[name]
		if !ok || p.sharedSupplies(name) {
			continue
		}
		if d := p.ensureLibc(w); d != nil {
			return d
		}
		p.sos[0].add([]string{name + "@" + version}, nil)
	}
	return nil
}

func (p *linkPlan) sharedSupplies(name string) bool {
	for _, so := range p.sos {
		if so.supplies(name) {
			return true
		}
	}
	return false
}
