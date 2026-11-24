// Why: invoking clang's driver per pack on Windows repeats target/SDK discovery
// and driver setup on every invocation (~7-10 ms per pack across both compile
// and ThinLTO backends). Probing the exact -cc1 command line once via
// `clang -###` and substituting the per-pack paths skips the driver while
// producing byte-identical objects with zero hardcoded clang internal flags.
//
// What: Tools.WarmCC1, Tools.WarmThinCC1, compileCC1Args, thinCC1Args,
// probeCC1 and -### command-line parsing/serialization.
//
// How: a template is keyed by (Tools.Identity, driver args with sentinel paths)
// and cached in memory on Tools (singleflight per key) and optionally in the
// driver's layout store via Options.CC1Get / Options.CC1Put. If probing fails
// or produces anything other than one cleanly substitutable -cc1 command line,
// callers fall back silently to the normal clang driver invocation.

package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mk/internal/source"
)

const (
	cc1SentIn     = "__MK_CC1_IN__.ll"
	cc1SentOut    = "__MK_CC1_OUT__.o"
	cc1SentIdx    = "__MK_CC1_IDX__.thinlto.bc"
	cc1SentPrefix = "__MK_CC1_"
	cc1WireHeader = "mk.cc1.v1"
)

// cc1Template is a probed `clang -cc1` argv with sentinel tokens marking where
// input path, -main-file-name base, output path, and optional -fthinlto-index
// path are substituted per invocation.
type cc1Template struct {
	args         []string
	inSlot       int
	mainFileSlot int
	outSlot      int
	idxSlot      int
}

type cc1Slot struct {
	done chan struct{}
	tmpl *cc1Template
}

// cc1Cache holds singleflight -cc1 templates for a Tools instance (kept behind
// a pointer so Tools remains value-copyable).
type cc1Cache struct {
	mu  sync.Mutex
	m   map[source.Hash]*cc1Slot
	get func(source.Hash) ([]byte, bool)
	put func(source.Hash, []byte)
}

func newCC1Cache(get func(source.Hash) ([]byte, bool), put func(source.Hash, []byte)) *cc1Cache {
	return &cc1Cache{m: map[source.Hash]*cc1Slot{}, get: get, put: put}
}

// defaultCC1Cache serves manually constructed Tools values (without Discover).
var defaultCC1Cache = newCC1Cache(nil, nil)

// cc1CanonicalArgs returns the canonical driver argument slice with fixed
// sentinel placeholders (independent of TempDir) for keying the -cc1 cache.
func cc1CanonicalArgs(j CompileJob) ([]string, error) {
	// Use deterministic absolute sentinel paths only to satisfy compileArgs's
	// IsAbs check, then normalize them to bare sentinels for keying/probing.
	dir := filepath.Join(os.TempDir(), "__mk_cc1__")
	inAbs := filepath.Join(dir, cc1SentIn)
	outAbs := filepath.Join(dir, cc1SentOut)
	probeJob := j
	probeJob.Input, probeJob.Output = inAbs, outAbs
	args, err := compileArgs(probeJob)
	if err != nil {
		return nil, err
	}
	for i, a := range args {
		switch a {
		case inAbs:
			args[i] = cc1SentIn
		case outAbs:
			args[i] = cc1SentOut
		}
	}
	return args, nil
}

// cc1TemplateKey hashes Tools.Identity and the canonical sentinel driver args
// so any toolchain, target, CPU, profile, or tuning change invalidates the
// cached -cc1 template.
func (t *Tools) cc1TemplateKey(canonicalDriver []string) source.Hash {
	h := source.NewHasher("mk.toolchain.cc1.v1")
	h.Hash(t.Identity())
	h.Uvarint(uint64(len(canonicalDriver)))
	for _, a := range canonicalDriver {
		h.String(a)
	}
	return h.Sum()
}

// instantiate returns a concrete -cc1 argv for (in, out, idx).
func (tmpl *cc1Template) instantiate(in, out, idx string) []string {
	args := make([]string, len(tmpl.args))
	copy(args, tmpl.args)
	args[tmpl.inSlot] = in
	if tmpl.mainFileSlot >= 0 {
		args[tmpl.mainFileSlot] = filepath.Base(in)
	}
	args[tmpl.outSlot] = out
	if tmpl.idxSlot >= 0 {
		args[tmpl.idxSlot] = "-fthinlto-index=" + idx
	}
	return args
}

// compileCC1Args returns direct -cc1 args for CompileJob j when available, else
// driverArgs unchanged.
func (t *Tools) compileCC1Args(ctx context.Context, j CompileJob, driverArgs []string) []string {
	if t.Clang == "" || ctx.Err() != nil {
		return driverArgs
	}
	canonical, err := cc1CanonicalArgs(j)
	if err != nil {
		return driverArgs
	}
	tmpl := t.getCC1Template(ctx, canonical, false)
	if tmpl == nil {
		return driverArgs
	}
	return tmpl.instantiate(j.Input, j.Output, "")
}

// thinCC1Args returns direct -cc1 args for a COFF ThinLTO backend compile when
// available, else driverArgs unchanged.
func (t *Tools) thinCC1Args(ctx context.Context, tune Tuning, module, index, out string, driverArgs []string) []string {
	if t.Clang == "" || ctx.Err() != nil {
		return driverArgs
	}
	canonical := thinBackendArgs(cc1SentIn, cc1SentIdx, cc1SentOut, tune)
	tmpl := t.getCC1Template(ctx, canonical, true)
	if tmpl == nil {
		return driverArgs
	}
	return tmpl.instantiate(module, out, index)
}

// WarmCC1 proactively probes and caches the -cc1 template for j's compile
// options (Input and Output are ignored). Safe to call concurrently with
// front-end stages so cold builds pay zero driver-probing time on the critical
// path.
func (t *Tools) WarmCC1(ctx context.Context, j CompileJob) {
	if t.Clang == "" || ctx.Err() != nil {
		return
	}
	canonical, err := cc1CanonicalArgs(j)
	if err != nil {
		return
	}
	_ = t.getCC1Template(ctx, canonical, false)
}

// WarmThinCC1 proactively probes and caches the -cc1 template for COFF ThinLTO
// backend compiles with tune.
func (t *Tools) WarmThinCC1(ctx context.Context, tune Tuning) {
	if t.Clang == "" || ctx.Err() != nil {
		return
	}
	canonical := thinBackendArgs(cc1SentIn, cc1SentIdx, cc1SentOut, tune)
	_ = t.getCC1Template(ctx, canonical, true)
}

// getCC1Template returns the cached or freshly probed -cc1 template for
// canonicalDriver, deduplicating concurrent probes via per-key cc1Slot.
func (t *Tools) getCC1Template(ctx context.Context, canonicalDriver []string, hasIdx bool) *cc1Template {
	c := t.cc1
	if c == nil {
		c = defaultCC1Cache
	}
	key := t.cc1TemplateKey(canonicalDriver)
	c.mu.Lock()
	if slot, ok := c.m[key]; ok {
		c.mu.Unlock()
		<-slot.done
		return slot.tmpl
	}
	if c.m == nil {
		c.m = map[source.Hash]*cc1Slot{}
	}
	slot := &cc1Slot{done: make(chan struct{})}
	c.m[key] = slot
	c.mu.Unlock()

	defer close(slot.done)
	sentIdx := ""
	if hasIdx {
		sentIdx = cc1SentIdx
	}
	if c.get != nil {
		if raw, ok := c.get(key); ok {
			if tmpl, ok := decodeCC1Template(raw, cc1SentIn, cc1SentOut, sentIdx); ok {
				slot.tmpl = tmpl
				return tmpl
			}
		}
	}
	tmpl := t.probeCC1(ctx, canonicalDriver, hasIdx)
	slot.tmpl = tmpl
	if tmpl != nil && c.put != nil {
		c.put(key, encodeCC1Template(tmpl))
	}
	return tmpl
}

// probeCC1 runs `clang <args> -###` over an empty temporary input file and
// parses its single -cc1 command line into a normalized template (using bare
// cc1SentIn / cc1SentOut / cc1SentIdx placeholders), returning nil on any
// unexpected shape.
func (t *Tools) probeCC1(ctx context.Context, canonicalDriver []string, hasIdx bool) *cc1Template {
	dir, err := os.MkdirTemp("", "mk-cc1-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	inPath := filepath.Join(dir, cc1SentIn)
	outPath := filepath.Join(dir, cc1SentOut)
	if err := os.WriteFile(inPath, nil, 0o644); err != nil {
		return nil
	}
	idxPath := ""
	if hasIdx {
		idxPath = filepath.Join(dir, cc1SentIdx)
	}
	args := make([]string, len(canonicalDriver)+1)
	for i, a := range canonicalDriver {
		switch {
		case a == cc1SentIn:
			args[i] = inPath
		case a == cc1SentOut:
			args[i] = outPath
		case hasIdx && a == "-fthinlto-index="+cc1SentIdx:
			args[i] = "-fthinlto-index=" + idxPath
		default:
			args[i] = a
		}
	}
	args[len(canonicalDriver)] = "-###"
	_, stderr, err := runBoth(ctx, NameClang, t.Clang, args, dir)
	if err != nil {
		return nil
	}
	return parseCC1Probe(string(stderr), inPath, outPath, idxPath)
}

// parseCC1Probe extracts a single -cc1 command line from `clang -###` stderr
// and verifies that every sentinel appears in an expected slot.
func parseCC1Probe(stderr, sentIn, sentOut, sentIdx string) *cc1Template {
	var cc1Tokens []string
	found := 0
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "-cc1") {
			continue
		}
		toks, ok := splitClangDryRun(line)
		if !ok || len(toks) < 2 || toks[1] != "-cc1" {
			continue
		}
		found++
		if found > 1 {
			return nil
		}
		cc1Tokens = toks[1:] // drop clang binary path
	}
	if found != 1 {
		return nil
	}
	return buildCC1Template(cc1Tokens, sentIn, sentOut, sentIdx)
}

func buildCC1Template(raw []string, sentIn, sentOut, sentIdx string) *cc1Template {
	if len(raw) == 0 || raw[0] != "-cc1" {
		return nil
	}
	inBase := filepath.Base(sentIn)
	wantIdx := ""
	if sentIdx != "" {
		wantIdx = "-fthinlto-index=" + sentIdx
	}
	tmpl := &cc1Template{
		args:         make([]string, 0, len(raw)),
		inSlot:       -1,
		mainFileSlot: -1,
		outSlot:      -1,
		idxSlot:      -1,
	}
	for i := 0; i < len(raw); i++ {
		a := raw[i]
		// Drop -fcoverage-compilation-dir=<probe-cwd>: irrelevant to IR
		// compiles and would embed the probe's working directory.
		if strings.HasPrefix(a, "-fcoverage-compilation-dir=") {
			continue
		}
		pos := len(tmpl.args)
		switch {
		case pos > 0 && tmpl.args[pos-1] == "-main-file-name" && a == inBase:
			if tmpl.mainFileSlot >= 0 {
				return nil
			}
			tmpl.mainFileSlot = pos
			a = cc1SentIn
		case a == sentIn:
			if tmpl.inSlot >= 0 {
				return nil
			}
			tmpl.inSlot = pos
			a = cc1SentIn
		case a == sentOut:
			if tmpl.outSlot >= 0 {
				return nil
			}
			tmpl.outSlot = pos
			a = cc1SentOut
		case wantIdx != "" && a == wantIdx:
			if tmpl.idxSlot >= 0 {
				return nil
			}
			tmpl.idxSlot = pos
			a = "-fthinlto-index=" + cc1SentIdx
		default:
			if strings.Contains(a, cc1SentPrefix) {
				return nil
			}
		}
		tmpl.args = append(tmpl.args, a)
	}
	if tmpl.inSlot < 0 || tmpl.outSlot < 0 || (wantIdx != "" && tmpl.idxSlot < 0) {
		return nil
	}
	return tmpl
}

// splitClangDryRun tokenizes one command line printed by `clang -###`, where
// arguments may be double-quoted with `\\` and `\"` escapes.
func splitClangDryRun(line string) ([]string, bool) {
	var out []string
	i, n := 0, len(line)
	for i < n {
		for i < n && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		var b strings.Builder
		if line[i] == '"' {
			i++
			closed := false
			for i < n {
				c := line[i]
				i++
				if c == '"' {
					closed = true
					break
				}
				if c == '\\' && i < n {
					next := line[i]
					if next == '\\' || next == '"' {
						b.WriteByte(next)
						i++
						continue
					}
				}
				b.WriteByte(c)
			}
			if !closed {
				return nil, false
			}
		} else {
			for i < n && line[i] != ' ' && line[i] != '\t' {
				b.WriteByte(line[i])
				i++
			}
		}
		out = append(out, b.String())
	}
	return out, len(out) > 0
}

func encodeCC1Template(tmpl *cc1Template) []byte {
	var b strings.Builder
	b.WriteString(cc1WireHeader)
	for _, a := range tmpl.args {
		b.WriteByte(0)
		b.WriteString(a)
	}
	return []byte(b.String())
}

func decodeCC1Template(raw []byte, sentIn, sentOut, sentIdx string) (*cc1Template, bool) {
	parts := strings.Split(string(raw), "\x00")
	if len(parts) < 2 || parts[0] != cc1WireHeader {
		return nil, false
	}
	tmpl := buildCC1Template(parts[1:], sentIn, sentOut, sentIdx)
	return tmpl, tmpl != nil
}
