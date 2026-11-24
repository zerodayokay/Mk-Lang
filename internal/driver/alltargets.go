// Why: a package with pkg.Select modules must check cleanly and expose the
// same exported API across every declared target OS, even when mk check runs
// on only one host.
//
// What: Session.checkAllTargetsSelect, which runs under mk check --all-targets
// for packages with pkg.Select entries, verifies missing target selections,
// checks each unselected OS with its canonical target, and reports exported
// Breaking API mismatches via E0213.
//
// How: reuses sessionAPIFacts from apidiff.go over both the host session and a
// per-target child Session, restricting comparison to pkg.Select modules and
// ignoring target-dependent Recompile (size/abi) and Reported (const value)
// facts so OS-specific private internals and constants stay allowed.

package driver

import (
	"fmt"
	"slices"
	"strings"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// checkAllTargetsSelect checks every declared pkg.Select target OS when
// --all-targets is enabled and compares exported API facts against the host.
func (s *Session) checkAllTargetsSelect() []*diag.Diagnostic {
	man := s.pkgMan
	if !s.cfg.AllTargets || man == nil || len(man.Targets) == 0 || s.root.kind != rootPackage {
		return nil
	}
	at := man.spanOf("targets")
	selectedOS := map[string]bool{}
	selectedMods := map[string]bool{}
	for _, ts := range man.Targets {
		selectedOS[ts.OS] = true
		for _, ms := range ts.Modules {
			selectedMods[ms.Name] = true
		}
	}
	if len(selectedMods) == 0 {
		return nil
	}

	declaredOS := map[string]bool{}
	for osName := range selectedOS {
		declaredOS[osName] = true
	}
	declaredOS[targetOS(s.cfg.Target)] = true
	for _, l := range man.Links {
		if t, ok := types.TargetByName(l.Target); ok {
			declaredOS[targetOS(t)] = true
		}
	}
	for _, a := range man.AsmSources {
		if t, ok := types.TargetByName(a.Target); ok {
			declaredOS[targetOS(t)] = true
		}
	}

	var osNames []string
	for osName := range declaredOS {
		osNames = append(osNames, osName)
	}
	slices.Sort(osNames)

	var diags []*diag.Diagnostic
	for _, osName := range osNames {
		if !selectedOS[osName] {
			diags = append(diags, diag.New(diag.E0213, at,
				fmt.Sprintf("--all-targets: package uses target OS %q without a pkg.Select entry in targets", osName)).
				In(diag.PhaseTypes).Text("target", osName).Text("reason", "missing-target-select"))
		}
	}

	hostOS := targetOS(s.cfg.Target)
	var baseName string
	var baseFacts map[string][]apiFact
	var baseMeta map[string]apiMeta
	for _, osName := range osNames {
		if osName == hostOS || !selectedOS[osName] {
			continue
		}
		tgt, ok := targetForOS(osName)
		if !ok {
			diags = append(diags, diag.New(diag.E0213, at,
				fmt.Sprintf("--all-targets: pkg.Select OS %q matches no supported target", osName)).
				In(diag.PhaseTypes).Text("target", osName).Text("reason", "unknown-target"))
			continue
		}
		cpu := ""
		if tgt.Arch == s.cfg.Target.Arch {
			cpu = s.cfg.CPU
		}
		childCfg := Config{
			Root:     s.cfg.Root,
			Target:   tgt,
			Profile:  s.cfg.Profile,
			CPU:      cpu,
			Jobs:     s.cfg.Jobs,
			NoCache:  s.cfg.NoCache,
			Offline:  s.cfg.Offline,
			Locked:   s.cfg.Locked,
			CacheDir: s.cfg.CacheDir,
			StdDir:   s.cfg.StdDir,
			Tools:    s.cfg.Tools,
			LLVMDir:  s.cfg.LLVMDir,
			MaxDiags: s.cfg.MaxDiags,
			BuildID:  s.cfg.BuildID,
			Overlay:  s.cfg.Overlay,
			LazyStd:  true,
		}
		if s.customReadFile {
			childCfg.ReadFile = s.cfg.ReadFile
		}
		cs, err := NewSession(childCfg)
		if err != nil {
			diags = append(diags, diag.New(diag.E0213, at,
				fmt.Sprintf("--all-targets: %s: %v", tgt.Name, err)).
				In(diag.PhaseTypes).Text("target", tgt.Name).Text("reason", "target-check-failed"))
			continue
		}
		cres := cs.Check()
		if len(cres.Report.Diags) > 0 || cs.w == nil || cs.w.sem5 == nil || cs.stdB == nil || !checkedToTypes(cres) {
			detail := targetFailSummary(cres)
			diags = append(diags, diag.New(diag.E0213, at,
				fmt.Sprintf("--all-targets: check failed for %s: %s", tgt.Name, detail)).
				In(diag.PhaseTypes).Text("target", tgt.Name).Text("reason", "target-check-failed").Text("detail", detail))
			continue
		}
		if baseFacts == nil && selectedOS[hostOS] {
			baseName = s.cfg.Target.Name
			baseFacts, baseMeta = sessionAPIFacts(s, false)
		}
		otherFacts, otherMeta := sessionAPIFacts(cs, false)
		if baseFacts == nil {
			baseName = tgt.Name
			baseFacts, baseMeta = otherFacts, otherMeta
			continue
		}
		diags = append(diags, compareSelectedAPIs(at, baseName, tgt.Name, selectedMods, baseFacts, baseMeta, otherFacts, otherMeta)...)
	}
	return diags
}

// targetForOS returns the canonical target for osName, preferring x86_64 when
// multiple architectures exist for that OS.
func targetForOS(osName string) (types.Target, bool) {
	var first types.Target
	found := false
	for _, t := range types.AllTargets() {
		if targetOS(t) != osName {
			continue
		}
		if t.Arch == types.ArchX64 {
			return t, true
		}
		if !found {
			first, found = t, true
		}
	}
	return first, found
}

// targetFailSummary formats the first error from a child target check for an
// E0213 message.
func targetFailSummary(res Result) string {
	if len(res.Report.Diags) == 0 {
		return "check did not reach type-checking"
	}
	d := res.Report.Diags[0].Diag
	loc := diag.Location(res.Sources, d.At)
	if loc != "" {
		return fmt.Sprintf("%s: %s %s: %s", loc, d.Code, d.Code.Name(), d.Message)
	}
	return fmt.Sprintf("%s %s: %s", d.Code, d.Code.Name(), d.Message)
}

// compareSelectedAPIs compares exported Breaking facts of every pkg.Select
// module between hostName and targetName and emits one E0213 per differing
// module.
func compareSelectedAPIs(at source.Span, hostName, targetName string, selectedMods map[string]bool, hostFacts map[string][]apiFact, hostMeta map[string]apiMeta, otherFacts map[string][]apiFact, otherMeta map[string]apiMeta) []*diag.Diagnostic {
	type modDiffs struct {
		items   []string
		details []string
	}
	byMod := map[string]*modDiffs{}
	addDiff := func(mod, item, detail string) {
		md := byMod[mod]
		if md == nil {
			md = &modDiffs{}
			byMod[mod] = md
		}
		if !slices.Contains(md.items, item) {
			md.items = append(md.items, item)
		}
		md.details = append(md.details, detail)
	}

	keys := map[string]string{}
	for k, m := range hostMeta {
		if selectedMods[m.mod] {
			keys[k] = m.mod
		}
	}
	for k, m := range otherMeta {
		if selectedMods[m.mod] {
			keys[k] = m.mod
		}
	}
	var sortedKeys []string
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	slices.Sort(sortedKeys)

	for _, k := range sortedKeys {
		mod := keys[k]
		hf, inHost := hostFacts[k]
		of, inOther := otherFacts[k]
		switch {
		case inHost && !inOther:
			addDiff(mod, k, k+": missing on "+targetName)
		case !inHost && inOther:
			addDiff(mod, k, k+": missing on "+hostName)
		default:
			if diff := diffBreakingFacts(k, hostName, targetName, hf, of); diff != "" {
				addDiff(mod, k, diff)
			}
		}
	}

	var mods []string
	for m := range byMod {
		mods = append(mods, m)
	}
	slices.Sort(mods)
	var out []*diag.Diagnostic
	for _, m := range mods {
		md := byMod[m]
		shown := md.items
		if len(shown) > 10 {
			shown = shown[:10]
		}
		out = append(out, diag.New(diag.E0213, at,
			fmt.Sprintf("selected module %s differs between %s and %s in %d item(s): %s",
				m, hostName, targetName, len(md.items), strings.Join(shown, ", "))).
			In(diag.PhaseTypes).
			Text("module", m).
			Text("targets", hostName+" "+targetName).
			Text("items", strings.Join(md.items, " ")).
			Text("diff", strings.Join(md.details, "; ")))
	}
	return out
}

// diffBreakingFacts compares all Breaking facts of item key across two targets,
// returning a concise human-readable mismatch description or "" if identical.
func diffBreakingFacts(key, hostName, targetName string, hf, of []apiFact) string {
	hm := map[string]string{}
	om := map[string]string{}
	var order []string
	seen := map[string]bool{}
	for _, f := range hf {
		if f.sev == Breaking {
			hm[f.what] = f.value
			if !seen[f.what] {
				seen[f.what] = true
				order = append(order, f.what)
			}
		}
	}
	for _, f := range of {
		if f.sev == Breaking {
			om[f.what] = f.value
			if !seen[f.what] {
				seen[f.what] = true
				order = append(order, f.what)
			}
		}
	}
	var diffs []string
	for _, what := range order {
		hv, hok := hm[what]
		ov, ook := om[what]
		if hok != ook || hv != ov {
			diffs = append(diffs, fmt.Sprintf("%s: %s (%s) vs %s (%s)", what, hv, hostName, ov, targetName))
		}
	}
	if len(diffs) == 0 {
		return ""
	}
	return key + " [" + strings.Join(diffs, ", ") + "]"
}
