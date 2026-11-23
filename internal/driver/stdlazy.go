// Why: a typical Mk program imports only a handful of standard-library modules
// (often just std.core), and never runs std's own unit tests, so reading,
// parsing, resolving, checking and lowering all ~45 std modules and ~200 std
// test files on every clean build wastes over a second of compile time.
//
// What: isStdTestFile, scanStdImports, lazyStdRoots and pipeline.frontLazyStd,
// which loads and parses only the transitively reached std modules while
// preserving StageLoad/StageParse barrier order and SourceMap determinism.
//
// How: StageLoad reads the non-std, non-c-binding builds first in parallel,
// scans their raw source bytes for `import [alias =] std.<path>` lines and `%`
// (for std.math float remainder), seeds the mandatory runtime/prelude std
// roots, then BFS-reads each newly reached wave of std modules in parallel.
// stdB.mods is pruned to the reached set in its original listing order before
// files enter the SourceMap and before cimport.prune runs; StageParse and every
// later stage then run unchanged over only the kept std modules.

package driver

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"mk/internal/diag"
	"mk/internal/source"
)

// rawRead holds one pre-read file result from StageLoad's lazy-std pass so
// readAll reuses the bytes without reading the file a second time.
type rawRead struct {
	data []byte
	err  error
}

// isStdTestFile reports whether rel (a slash-separated path inside std) is a
// std unit-test file that only `mk test --std` or std browsing needs.
func isStdTestFile(rel string) bool {
	base := path.Base(rel)
	return strings.HasPrefix(base, "tests_") || strings.HasSuffix(base, "_test.mk")
}

// lazyStdRoots returns the std module paths that the compiler, runtime or
// lowering may reference implicitly without an explicit import in user code.
func (p *pipeline) lazyStdRoots(stdB *build, hasPercent bool, testing bool) []*modNode {
	w := p.w
	names := []string{preludeModule, rtModule, "std.sys", "std.raw"}
	if !isFree(w.cfg.Target) {
		names = append(names, "std.task")
	} else {
		names = append(names, "std.free")
	}
	if hasPercent {
		names = append(names, "std.math")
	}
	if w.cfg.Cover {
		names = append(names, "std.cover")
	}
	if testing {
		names = append(names, "std.testing")
	}
	if w.cimports != nil || len(w.depCImports) > 0 || len(w.links) > 0 {
		names = append(names, "std.libc")
	}
	var out []*modNode
	for _, n := range names {
		if m := stdB.byName[n]; m != nil {
			out = append(out, m)
		}
	}
	return out
}

// scanStdImports conservatively extracts every `std.<path>` import target from
// raw Mk source bytes, skipping comments and string/char literals. It never
// misses a syntactically valid `import [alias =] std.<path>` declaration.
func scanStdImports(raw []byte) []string {
	imps, _ := scanStdSource(raw)
	return imps
}

// isBarePercent reports whether raw[i] ('%') is not the second byte of one of
// Mk's wrapping integer operators (+%, -%, *%, +%=, -%=, *%=).
func isBarePercent(raw []byte, i int) bool {
	if i == 0 {
		return true
	}
	prev := raw[i-1]
	return prev != '+' && prev != '-' && prev != '*'
}

// scanStdSource extracts every `std.<path>` import target and reports whether
// raw contains a bare `%` (remainder operator, not wrapping `+%`, `-%`, `*%`)
// outside comments. Bare `%` inside string literals is also treated as true so
// compile-time generators emitting `%` text never miss std.math.
func scanStdSource(raw []byte) (out []string, hasRem bool) {
	n := len(raw)
	isIdent := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
	}
	skipWSAndComments := func(i int) int {
		for i < n {
			b := raw[i]
			if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
				i++
				continue
			}
			if b == '/' && i+1 < n {
				if raw[i+1] == '/' {
					i += 2
					for i < n && raw[i] != '\n' {
						i++
					}
					continue
				}
				if raw[i+1] == '*' {
					depth := 1
					i += 2
					for i < n && depth > 0 {
						if raw[i] == '/' && i+1 < n && raw[i+1] == '*' {
							depth++
							i += 2
						} else if raw[i] == '*' && i+1 < n && raw[i+1] == '/' {
							depth--
							i += 2
						} else {
							i++
						}
					}
					continue
				}
			}
			break
		}
		return i
	}
	i := 0
	for i < n {
		b := raw[i]
		// Skip comments.
		if b == '/' && i+1 < n && (raw[i+1] == '/' || raw[i+1] == '*') {
			i = skipWSAndComments(i)
			continue
		}
		// Skip raw strings r"..." (no backslash escapes, one line).
		if b == 'r' && i+1 < n && raw[i+1] == '"' && (i == 0 || !isIdent(raw[i-1])) {
			i += 2
			for i < n && raw[i] != '"' && raw[i] != '\n' {
				if !hasRem && raw[i] == '%' && isBarePercent(raw, i) {
					hasRem = true
				}
				i++
			}
			if i < n && raw[i] == '"' {
				i++
			}
			continue
		}
		// Skip multi-line strings """...""".
		if b == '"' && i+2 < n && raw[i+1] == '"' && raw[i+2] == '"' {
			i += 3
			for i < n {
				if raw[i] == '"' && i+2 < n && raw[i+1] == '"' && raw[i+2] == '"' {
					i += 3
					break
				}
				if !hasRem && raw[i] == '%' && isBarePercent(raw, i) {
					hasRem = true
				}
				i++
			}
			continue
		}
		// Skip quoted strings and char literals (one line in Mk).
		if b == '"' || b == '\'' {
			q := b
			i++
			for i < n && raw[i] != '\n' {
				if raw[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if raw[i] == q {
					i++
					break
				}
				if !hasRem && raw[i] == '%' && isBarePercent(raw, i) {
					hasRem = true
				}
				i++
			}
			continue
		}
		if b == '%' {
			if !hasRem && isBarePercent(raw, i) {
				hasRem = true
			}
			i++
			continue
		}
		// Match keyword `import` at identifier boundary.
		if b == 'i' && i+6 <= n && string(raw[i:i+6]) == "import" &&
			(i == 0 || !isIdent(raw[i-1])) && (i+6 == n || !isIdent(raw[i+6])) {
			j := skipWSAndComments(i + 6)
			// Optional `alias =`.
			if j < n && isIdent(raw[j]) {
				k := j
				for k < n && isIdent(raw[k]) {
					k++
				}
				k2 := skipWSAndComments(k)
				if k2 < n && raw[k2] == '=' {
					j = skipWSAndComments(k2 + 1)
				}
			}
			// Check for `std.<segment>(.<segment>)*`.
			if j+4 <= n && string(raw[j:j+4]) == "std." {
				end := j + 4
				for end < n && (isIdent(raw[end]) || raw[end] == '.') {
					end++
				}
				p := strings.TrimRight(string(raw[j:end]), ".")
				if len(p) > 4 {
					out = append(out, p)
				}
				i = end
				continue
			}
			i += 6
			continue
		}
		i++
	}
	return out, hasRem
}

// isCBuild reports whether b is one of the pipeline's C-import binding builds.
func (p *pipeline) isCBuild(b *build) bool {
	for _, ci := range p.w.allCImports() {
		if b == ci.b {
			return true
		}
	}
	return false
}

// loadLazyStd runs StageLoad's parallel file reads when lazyStd is enabled:
// non-std, non-C builds are read first to discover every std.* import and `%`,
// then only the transitively reached std modules are read wave-by-wave,
// stdB.mods is pruned in its listing order, and every build commits its files
// into the SourceMap in p.builds order.
func (p *pipeline) loadLazyStd(testing bool) (fails, tooLarge []*diag.Diagnostic) {
	stdB := p.builds[0]
	type job struct {
		m   *modNode
		f   *fileNode
		raw []byte
		err error
	}
	readMods := func(mods []*modNode) ([]job, []*diag.Diagnostic) {
		var jobs []job
		for _, m := range mods {
			for _, f := range m.files {
				jobs = append(jobs, job{m: m, f: f})
			}
		}
		if len(jobs) == 0 {
			return nil, nil
		}
		faults := p.w.parallel(len(jobs), func(i int) string { return "read " + jobs[i].m.tree.path(jobs[i].f.rel) }, func(i int) {
			jobs[i].raw, jobs[i].err = jobs[i].m.tree.readFile(jobs[i].f.rel)
		})
		return jobs, faults
	}

	pre := map[*fileNode]rawRead{}
	var nonStdMods []*modNode
	for _, b := range p.builds[1:] {
		if p.isCBuild(b) {
			continue
		}
		nonStdMods = append(nonStdMods, b.mods...)
	}
	nonStdJobs, faults := readMods(nonStdMods)
	if len(faults) > 0 {
		return faults, nil
	}
	need := map[*modNode]bool{}
	var wave []*modNode
	enqueue := func(m *modNode) {
		if m != nil && !need[m] {
			need[m] = true
			wave = append(wave, m)
		}
	}
	hasPercent := false
	for _, j := range nonStdJobs {
		pre[j.f] = rawRead{data: j.raw, err: j.err}
		if j.err != nil {
			continue
		}
		imps, rem := scanStdSource(j.raw)
		if rem {
			hasPercent = true
		}
		for _, imp := range imps {
			enqueue(stdB.byName[imp])
		}
	}
	for _, m := range p.lazyStdRoots(stdB, hasPercent, testing) {
		enqueue(m)
	}
	for len(wave) > 0 {
		cur := wave
		wave = nil
		jobs, wf := readMods(cur)
		if len(wf) > 0 {
			return wf, nil
		}
		for _, j := range jobs {
			pre[j.f] = rawRead{data: j.raw, err: j.err}
			if j.err != nil {
				continue
			}
			for _, imp := range scanStdImports(j.raw) {
				enqueue(stdB.byName[imp])
			}
		}
	}
	// Prune stdB.mods to reached modules, keeping deterministic listing order,
	// and filter caseDups/dupPaths so caseDupDiags never touches a pruned module.
	kept := stdB.mods[:0:0]
	for _, m := range stdB.mods {
		if need[m] {
			kept = append(kept, m)
		}
	}
	stdB.mods = kept
	stdB.caseDups = slices.DeleteFunc(stdB.caseDups, func(c caseDup) bool { return !need[c.later] })
	stdB.dupPaths = slices.DeleteFunc(stdB.dupPaths, func(d dupPath) bool { return !need[d.later] })

	// Commit every build's files into SourceMap in p.builds order (stdB first,
	// then depBs with ci.prune, then pkgB), reusing pre-read bytes.
	for _, b := range p.builds {
		for _, ci := range p.w.allCImports() {
			if b == ci.b {
				ci.prune(p.w, p.builds)
				fails = append(fails, ci.mismatch...)
			}
		}
		f, big := b.readAllWith(pre)
		fails = append(fails, f...)
		tooLarge = append(tooLarge, big...)
	}
	return fails, tooLarge
}

// readAllWith is readAll reusing bytes already read into pre when present.
func (b *build) readAllWith(pre map[*fileNode]rawRead) (readFailures, tooLarge []*diag.Diagnostic) {
	b.w.cfg.hook("read")
	type job struct {
		m   *modNode
		f   *fileNode
		raw []byte
		err error
	}
	var jobs []job
	var missing []int
	for _, m := range b.mods {
		for _, f := range m.files {
			j := job{m: m, f: f}
			if r, ok := pre[f]; ok {
				j.raw, j.err = r.data, r.err
			} else {
				missing = append(missing, len(jobs))
			}
			jobs = append(jobs, j)
		}
	}
	if len(missing) > 0 {
		faults := b.w.parallel(len(missing), func(k int) string {
			j := jobs[missing[k]]
			return "read " + j.m.tree.path(j.f.rel)
		}, func(k int) {
			idx := missing[k]
			jobs[idx].raw, jobs[idx].err = jobs[idx].m.tree.readFile(jobs[idx].f.rel)
		})
		if len(faults) > 0 {
			return faults, nil
		}
	}
	for _, j := range jobs {
		m, f, raw := j.m, j.f, j.raw
		p := m.tree.path(f.rel)
		if err := j.err; err != nil {
			readFailures = append(readFailures, diag.Toolchain("fs", "read "+p, pathErrText(err),
				fmt.Sprintf("cannot read source file %s", p)))
			continue
		}
		if uint64(len(raw)) > b.w.maxFile && uint64(len(source.Normalize(raw))) > b.w.maxFile {
			f.tooLarge = true
			f.src = b.w.sm.Add(p, nil)
			b.w.noteSitePath(f.src.ID, m.tree.sitePath(f.rel))
			tooLarge = append(tooLarge, diag.New(diag.E0101, source.Span{File: f.src.ID},
				fmt.Sprintf("source file %s is larger than %d bytes", p, b.w.maxFile)).Text("reason", "file-too-large"))
			continue
		}
		f.src = b.w.sm.Add(p, raw)
		b.w.noteSitePath(f.src.ID, m.tree.sitePath(f.rel))
	}
	return readFailures, tooLarge
}
