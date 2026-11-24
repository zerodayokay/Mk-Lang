// Why: a function's requires and ensures clauses (and a struct's
// invariant) state a property; mk test --prop tests that property on
// generated inputs instead of hand-written cases.
//
// What: mk test --prop (Session.testProp): the contracts to test
// (propTargets), case generation from parameter types under requires
// (propPrepare, draw; a generic T is instantiated with propInstSet),
// rounds of checking (propRun, propRound, propCheck), shrinking
// (propShrink, shrinks), one report per contract, and the kept corpus
// (propCorpusPath, propCorpusRead, propCorpusKeep).
//
// How: each case is a const of an in-memory scratch file in the contract's
// module, evaluated by the check; a diagnostic is mapped back to its case
// by line (propCaseAt) and classify turns it into the case's outcome.

package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"mk/internal/types"
)

// PropDefaultSeed is the generator's seed when mk test --prop runs without
// --sim SEED, so a run is reproducible by default.
const PropDefaultSeed uint64 = 0x6d6b2d70726f70 // "mk-prop"

// PropDefaultCases is how many valid cases each contract instance gets.
const PropDefaultCases = 100

const (
	propScratch     = ".mk-prop.mk" // the scratch file's base name, never on disk
	propGenRounds   = 5             // generation rounds before reporting what ran
	propShrinkMax   = 40            // shrink rounds per finding
	propShrinkWidth = 64            // candidates per finding per shrink round
	propCorpusMax   = 32            // corpus entries kept per contract
)

// propInstSet is the small set a generic T is instantiated with.
var propInstSet = []types.TypeID{types.Int, types.I32, types.U8, types.Bool, types.Str}

// propTarget is one contract: a function or method of the package with a
// requires or ensures clause, or a result or receiver struct with an
// invariant.
type propTarget struct {
	mod     *modNode
	item    types.ItemID
	name    string // the item's own name, as a trace frame spells it
	display string // Clamp, Span.Len
	sig     *types.Signature
	clauses []string
	insts   []*propInst
	skip    string
	helper  bool // a mut argument: the case is a function holding it in a var

	valid, discarded, gaveUp int
	gaveUpWhy                string // the first not-judged case's reason
	finding                  *propCase
	firstSize                uint64
	rounds                   int
	shrinkCut                bool
	fromCorpus               bool
	staleCorpus              []string // keys of corpus entries that no longer check
}

// propInst is one instance: type arguments for the contract's own generics.
type propInst struct {
	text    string // "" or "[i32]"
	sub     map[uint32]types.TypeID
	params  []*propShape // receiver first, as sig.Params
	dropped string
	gen     *propGen
	edges   int
	seen    map[string]bool
}

// propCase is one generated input and, after a round, its outcome.
type propCase struct {
	t      *propTarget
	inst   *propInst
	args   []pv
	id     int
	corpus bool // replayed from the corpus
	from   int  // first line in the scratch file, 1-based
	to     int  // last line
	// outcome: "" (passed), discard, fuel, gen, fail
	out    string
	kind   string // the finding: ensures, invariant, requires of F, panic: overflow
	at     string // innermost frame's location
	detail string
}

// propDiag is the part of a diagnostic in ReportJSON the tester reads.
type propDiag struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	At      struct {
		File string `json:"file"`
		Line int    `json:"line"`
	} `json:"at"`
	Facts struct {
		Panic   string `json:"panic"`
		Message string `json:"message"`
		Trace   []struct {
			Name string `json:"name"`
			At   string `json:"at"`
		} `json:"trace"`
	} `json:"facts"`
}

// testProp is mk test --prop.
func (s *Session) testProp(opt TestOptions) (Result, *TestReport, error) {
	if s.cfg.Profile == types.Release {
		return Result{}, nil, usagef("--prop: contracts are off in release builds")
	}
	s.cfg.Profile = types.TestProfile
	s.prop = true
	r := s.runCheck()
	if len(r.Report.Diags) > 0 {
		return r, nil, nil
	}
	seed := PropDefaultSeed
	if opt.SimSet {
		seed = opt.Sim
	}
	want := opt.PropCases
	if want <= 0 {
		want = PropDefaultCases
	}
	rep := &TestReport{PropSeed: &seed}
	if s.w == nil || s.pkgB == nil || s.cfg.StdTests != "" {
		return r, rep, nil
	}
	ts := s.propTargets(opt.Run, seed)
	if err := s.propRun(ts, want); err != nil {
		return r, nil, err
	}
	for _, t := range ts {
		tc := t.report(want)
		switch tc.Result {
		case "pass":
			rep.Passed++
		case "skip":
			rep.Skipped++
		default:
			rep.Failed++
		}
		rep.Tests = append(rep.Tests, tc)
	}
	return r, rep, nil
}

// propTargets lists the package's contracts in module and item order.
func (s *Session) propTargets(run string, seed uint64) []*propTarget {
	var out []*propTarget
	for _, m := range s.pkgB.mods {
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			it := s.w.items.Get(id)
			d, ok := it.Decl.(*types.FuncDecl)
			if !ok || it.Kind != types.KindFunc || d.Bodiless || d.Naked || it.Origin != nil {
				continue
			}
			if it.Owner != types.NoItem {
				if _, isStruct := s.w.items.Get(it.Owner).Decl.(*types.StructDecl); !isStruct {
					if _, isEnum := s.w.items.Get(it.Owner).Decl.(*types.EnumDecl); !isEnum {
						continue // a contract predicate, an invariant, an impl method
					}
				}
			}
			t := &propTarget{mod: m, item: id, name: s.w.in.Str(it.Name), sig: &d.Sig}
			t.display = t.name
			if it.Owner != types.NoItem {
				t.display = s.w.in.Str(s.w.items.Get(it.Owner).Name) + "." + t.name
			}
			for _, c := range d.Sig.Contracts {
				kw := "requires "
				if c.Kind == types.Ensures {
					kw = "ensures "
				}
				t.clauses = append(t.clauses, kw+c.Text)
			}
			if len(t.clauses) == 0 && !s.invariantTouched(&d.Sig, m.id) {
				continue
			}
			if !strings.Contains(t.display, run) {
				continue
			}
			s.propPrepare(t, seed)
			out = append(out, t)
		}
	}
	return out
}

// invariantTouched: the result, or the receiver, is a struct of the module
// with an invariant, which lowering checks at each exit.
func (s *Session) invariantTouched(sig *types.Signature, mod types.ModuleID) bool {
	has := func(t types.TypeID) bool {
		ty := s.w.tab.Get(t)
		for ty.Kind == types.KRef || ty.Kind == types.KMut {
			ty = s.w.tab.Get(ty.Elem)
		}
		if ty.Kind != types.KNamed {
			return false
		}
		it := s.w.items.Get(ty.Item)
		d, ok := it.Decl.(*types.StructDecl)
		return ok && it.Module == mod && len(d.Invariants) > 0
	}
	if sig.Result != types.NoType && has(sig.Result) {
		return true
	}
	return sig.Receiver == types.ByMut && has(sig.Params[0].Type)
}

// propPrepare resolves the contract's instances and parameter shapes, or
// sets why it is skipped.
func (s *Session) propPrepare(t *propTarget, seed uint64) {
	sig := t.sig
	own := int(sig.OwnGenerics)
	if len(sig.Generics) > own {
		t.skip = "a method of a generic type"
		return
	}
	for i, p := range sig.Params {
		ty := s.w.tab.Get(p.Type)
		switch {
		case p.Const:
			t.skip = "a const parameter"
		case p.Var || p.Into != types.NoParam:
			t.skip = "a var or into parameter"
		case ty.Kind == types.KMut || ty.Kind == types.KMutSlice || (i == 0 && sig.Receiver == types.ByMut):
			t.helper = true
		}
		if t.skip != "" {
			return
		}
	}
	var subs []map[uint32]types.TypeID
	var texts []string
	if own == 0 {
		subs, texts = []map[uint32]types.TypeID{nil}, []string{""}
	} else {
		for _, g := range sig.Generics {
			if g.Const {
				t.skip = "a const generic parameter"
				return
			}
		}
		for _, it := range propInstSet {
			sub := map[uint32]types.TypeID{}
			var names []string
			for i := range own {
				sub[uint32(i)] = it
				names = append(names, s.w.tab.TypeString(it, nil))
			}
			subs, texts = append(subs, sub), append(texts, "["+strings.Join(names, ", ")+"]")
		}
	}
	var why string
	for k, sub := range subs {
		in := &propInst{text: texts[k], sub: sub, seen: map[string]bool{}}
		in.gen = &propGen{r: newPropRng(seed ^ fnv64(t.mod.path+"."+t.display+in.text))}
		for i, p := range sig.Params {
			pt := p.Type
			if i == 0 && sig.Receiver == types.ByShared {
				pt = s.w.tab.Get(pt).Elem // r.M() borrows the value it is called on
			}
			sh, w := s.shapeOf(pt, sub, t.mod.id, 0)
			if sh == nil {
				in.dropped = "cannot generate parameter `" + s.w.in.Str(p.Name) + "`: " + w
				why = in.dropped
				break
			}
			in.params = append(in.params, sh)
			if n := propEdgeCount(sh); n > 0 {
				if in.edges == 0 {
					in.edges = n
				} else {
					in.edges = min(in.edges*n, propEdgeCases)
				}
			}
		}
		if in.dropped == "" {
			t.insts = append(t.insts, in)
		}
	}
	if len(t.insts) == 0 {
		t.skip = why
	}
}

// propRun runs the corpus, the generation rounds and the shrinking.
func (s *Session) propRun(ts []*propTarget, want int) error {
	live := func() []*propTarget {
		var out []*propTarget
		for _, t := range ts {
			if t.skip == "" && t.finding == nil {
				out = append(out, t)
			}
		}
		return out
	}
	edgeBudget := min(propEdgeCases, want)
	for _, t := range ts {
		for _, in := range t.insts {
			in.gen.edgeLimit = min(in.edges, edgeBudget)
		}
	}
	// Round 0: the corpus, then one case per instance (an instance whose
	// line does not check is dropped: a bound the type does not meet).
	var cases []*propCase
	for _, t := range live() {
		cases = append(cases, s.propCorpusCases(t)...)
		for _, in := range t.insts {
			if c := t.draw(in); c != nil {
				cases = append(cases, c)
			}
		}
	}
	if err := s.propRound(cases); err != nil {
		return err
	}
	s.propTally(cases)
	for range propGenRounds {
		cases = cases[:0]
		for _, t := range live() {
			if t.valid >= want*len(t.insts) {
				continue
			}
			n := min(2*(want*len(t.insts)-t.valid), 400)
			for i := 0; i < n; i++ {
				in := t.insts[i%len(t.insts)]
				if in.dropped != "" {
					continue
				}
				if c := t.draw(in); c != nil {
					cases = append(cases, c)
				}
			}
		}
		if len(cases) == 0 {
			break
		}
		if err := s.propRound(cases); err != nil {
			return err
		}
		s.propTally(cases)
	}
	if err := s.propShrink(ts); err != nil {
		return err
	}
	for _, t := range ts {
		if t.finding != nil || len(t.staleCorpus) > 0 {
			s.propCorpusKeep(t)
		}
	}
	return nil
}

// draw makes a new case of in, or nil after many duplicates (a small
// domain, such as one bool, is exhausted).
func (t *propTarget) draw(in *propInst) *propCase {
	for range 20 {
		c := &propCase{t: t, inst: in}
		in.gen.radix = 1
		for _, sh := range in.params {
			c.args = append(c.args, in.gen.draw(sh, 0))
			if n := propEdgeCount(sh); n > 0 {
				in.gen.radix = min(in.gen.radix*n, propEdgeCases)
			}
		}
		in.gen.i++
		k := c.key()
		if !in.seen[k] {
			in.seen[k] = true
			return c
		}
	}
	return nil
}

func (c *propCase) key() string {
	parts := []string{c.inst.text}
	for _, a := range c.args {
		parts = append(parts, a.render())
	}
	return strings.Join(parts, "\x00")
}

// propTally counts a generation round's outcomes. The first failing case of
// a contract (in case order) is its finding.
func (s *Session) propTally(cases []*propCase) {
	for _, c := range cases {
		t := c.t
		if t.finding != nil || t.skip != "" {
			continue
		}
		switch c.out {
		case "":
			t.valid++
		case "discard":
			t.discarded++
		case "fuel":
			t.gaveUp++
			if t.gaveUpWhy == "" {
				t.gaveUpWhy = c.detail
			}
		case "gen":
			if c.corpus {
				t.staleCorpus = append(t.staleCorpus, c.key()) // the contract changed: drop it
				continue
			}
			if c.inst.text != "" {
				c.inst.dropped = c.detail
				if !slices.ContainsFunc(t.insts, func(in *propInst) bool { return in.dropped == "" }) {
					t.skip = "no instance of the small set checks: " + c.detail
				}
				continue
			}
			t.skip = "cannot generate: " + c.detail
		case "fail":
			t.finding, t.fromCorpus = c, c.corpus
			t.firstSize = c.size()
		}
	}
}

func (c *propCase) size() uint64 {
	var n uint64
	for _, a := range c.args {
		n += a.size()
	}
	return n
}

// propShrink shrinks every finding, one check per round over all of them.
func (s *Session) propShrink(ts []*propTarget) error {
	for round := 0; round < propShrinkMax; round++ {
		var cases []*propCase
		var owners []*propTarget
		for _, t := range ts {
			if t.finding == nil || t.shrinkCut {
				continue
			}
			cs := t.finding.shrinks()
			if len(cs) == 0 {
				t.shrinkCut = true
				continue
			}
			for _, c := range cs {
				cases, owners = append(cases, c), append(owners, t)
			}
		}
		if len(cases) == 0 {
			return nil
		}
		if err := s.propRound(cases); err != nil {
			return err
		}
		moved := map[*propTarget]bool{}
		for i, c := range cases {
			t := owners[i]
			if moved[t] || c.out != "fail" || c.kind != t.finding.kind || c.at != t.finding.at {
				continue
			}
			t.finding, moved[t] = c, true
			t.rounds++
		}
		for _, t := range ts {
			if t.finding != nil && !t.shrinkCut && !moved[t] {
				t.shrinkCut = true
			}
		}
	}
	for _, t := range ts {
		if t.finding != nil && !t.shrinkCut {
			t.rounds = -t.rounds // stopped at the round limit
		}
	}
	return nil
}

// shrinks are the finding's candidates: one argument changed at a time,
// interleaved across arguments, at most propShrinkWidth.
func (c *propCase) shrinks() []*propCase {
	per := make([][]pv, len(c.args))
	for i, a := range c.args {
		per[i] = a.shrinks()
	}
	var out []*propCase
	for j := 0; len(out) < propShrinkWidth; j++ {
		any := false
		for i := range per {
			if j < len(per[i]) && len(out) < propShrinkWidth {
				any = true
				n := &propCase{t: c.t, inst: c.inst, args: slices.Clone(c.args)}
				n.args[i] = per[i][j]
				if (n.inst.params[i].min1) && n.args[i].K == "q" && len(n.args[i].E) == 0 {
					continue
				}
				out = append(out, n)
			}
		}
		if !any {
			break
		}
	}
	return out
}

// propRound evaluates cases in one check: each module's cases go into its
// scratch file. Cases that do not check are marked gen and the rest run
// again without them, since a diagnostic before the types phase stops the
// evaluation of every const (at most three tries).
func (s *Session) propRound(cases []*propCase) error {
	todo := cases
	for try := 0; try < 3 && len(todo) > 0; try++ {
		ds, paths, err := s.propCheck(todo)
		if err != nil {
			return err
		}
		scratch := map[string]bool{}
		for _, p := range paths {
			scratch[p] = true
		}
		var bad bool
		for _, c := range todo {
			c.out, c.kind, c.at, c.detail = "", "", "", ""
		}
		for _, d := range ds {
			c := propCaseAt(todo, paths, d)
			if c == nil {
				// Outside every case (another file, or the scratch file's
				// header): no case can be judged from this round, so it is
				// the tester's error, never a round of passes.
				where := "outside the scratch file"
				if scratch[d.At.File] {
					where = "outside every case"
				}
				return fmt.Errorf("--prop: the scratch check reported %s at %s:%d %s: %s", d.Code, d.At.File, d.At.Line, where, d.Message)
			}
			if c.out != "" {
				continue // the first diagnostic of a case decides it
			}
			c.classify(d)
			if c.out == "gen" {
				bad = true
			}
		}
		if !bad {
			return nil
		}
		var next []*propCase
		for _, c := range todo {
			if c.out != "gen" {
				next = append(next, c)
			}
		}
		todo = next
	}
	// Three tries and still a case that does not check: the rest ran in a
	// check that may have stopped before the evaluation, so none is judged.
	for _, c := range todo {
		if c.out != "gen" {
			c.out, c.detail = "fuel", "not evaluated"
		}
	}
	return nil
}

// propCaseAt is the case whose lines hold d, in the scratch file of its
// module.
func propCaseAt(cases []*propCase, paths map[*modNode]string, d propDiag) *propCase {
	for _, c := range cases {
		if paths[c.t.mod] == d.At.File && d.At.Line >= c.from && d.At.Line <= c.to {
			return c
		}
	}
	return nil
}

// classify sets the case's outcome from its first diagnostic.
func (c *propCase) classify(d propDiag) {
	switch d.Code {
	case "E0902":
		c.out, c.detail = "fuel", "E0902 "+d.Message
		return
	case "E0901":
	default:
		c.out, c.detail = "gen", d.Code+" "+d.Message
		return
	}
	f := d.Facts
	if f.Panic == "" || len(f.Trace) == 0 {
		c.out, c.detail = "fuel", "refused: "+d.Message // refused (Io, asm, volatile): not judged
		return
	}
	inner := f.Trace[0]
	switch {
	case strings.HasPrefix(inner.Name, "MkPropK"):
		c.out = "discard" // the input itself broke an invariant
		return
	case f.Panic == "contract" && f.Message == "requires" && inner.Name == c.t.name &&
		len(f.Trace) >= 2 && strings.HasPrefix(f.Trace[1].Name, "MkPropK"):
		c.out = "discard" // the input does not meet requires
		return
	}
	c.out, c.at = "fail", inner.At
	switch {
	case f.Panic != "contract":
		c.kind = "panic: " + f.Panic
		if f.Message != "" {
			c.kind += " (" + f.Message + ")"
		}
	case f.Message == "requires":
		c.kind = "requires of " + inner.Name + ", called by " + c.t.name
	default:
		c.kind = f.Message
	}
}

var propDebugN atomic.Int32

// propCheck writes each module's scratch file and checks the package with
// them as the overlay. It returns the diagnostics and each module's scratch
// path as diagnostics spell it.
func (s *Session) propCheck(cases []*propCase) ([]propDiag, map[*modNode]string, error) {
	texts := map[*modNode]*strings.Builder{}
	lines := map[*modNode]int{}
	var order []*modNode
	for i, c := range cases {
		c.id = i + 1
		b := texts[c.t.mod]
		if b == nil {
			b = &strings.Builder{}
			b.WriteString("// mk test --prop: generated cases, never on disk\n")
			texts[c.t.mod], lines[c.t.mod] = b, 1
			order = append(order, c.t.mod)
		}
		text := c.source(s)
		c.from = lines[c.t.mod] + 1
		n := strings.Count(text, "\n")
		c.to = lines[c.t.mod] + n
		lines[c.t.mod] += n
		b.WriteString(text)
	}
	ov := map[string][]byte{}
	for k, v := range s.cfg.Overlay {
		ov[k] = v
	}
	paths := map[*modNode]string{}
	for _, m := range order {
		rel := propScratch
		if m.dir != "" {
			rel = m.dir + "/" + propScratch
		}
		ov[rel] = []byte(texts[m].String())
		paths[m] = s.root.prefix + rel
	}
	cfg := s.cfg
	// The same Root as given, so the scratch file's diagnostics carry the
	// prefix s.root has.
	cfg.Overlay, cfg.MaxDiags, cfg.Timings = ov, 0, nil
	ns, err := NewSession(cfg)
	if err != nil {
		return nil, nil, err
	}
	ns.prop = true
	res := ns.Check()
	if d := os.Getenv("MK_PROP_DEBUG"); d != "" {
		// MK_PROP_DEBUG=dir keeps each round's scratch files and report
		// there, numbered by round, for debugging the tester itself.
		n := propDebugN.Add(1)
		for _, m := range order {
			_ = os.WriteFile(filepath.Join(d, fmt.Sprintf("round%d-%s.mk", n, m.path)), []byte(texts[m].String()), 0o644)
		}
		_ = os.WriteFile(filepath.Join(d, fmt.Sprintf("round%d.json", n)), res.ReportJSON(), 0o644)
	}
	var doc struct {
		Diagnostics []propDiag `json:"diagnostics"`
	}
	if err := json.Unmarshal(res.ReportJSON(), &doc); err != nil {
		return nil, nil, err
	}
	return doc.Diagnostics, paths, nil
}

// source is the case's lines: one const, or (with a mut argument) a
// function holding the arguments in vars and a const calling it.
func (c *propCase) source(s *Session) string {
	t, k := c.t, "MkPropK"+strconv.Itoa(c.id)
	sig := t.sig
	callee := func(args []string) string {
		if sig.Receiver != types.NoReceiver {
			return args[0] + "." + t.name + c.inst.text + "(" + strings.Join(args[1:], ", ") + ")"
		}
		return t.display + c.inst.text + "(" + strings.Join(args, ", ") + ")"
	}
	if !t.helper {
		var args []string
		for _, a := range c.args {
			args = append(args, a.render())
		}
		return "const " + k + " = " + callee(args) + "\n"
	}
	var b strings.Builder
	var args []string
	var body []string
	for i, a := range c.args {
		p := sig.Params[i]
		pk := s.w.tab.Get(p.Type).Kind
		name := k + "A" + strconv.Itoa(i)
		switch {
		case i == 0 && sig.Receiver == types.ByMut:
			body = append(body, "    var a0 := "+a.render())
			args = append(args, "a0")
		case pk == types.KMutSlice:
			lit := a
			lit.K = "a"
			body = append(body, "    var a"+strconv.Itoa(i)+" := "+lit.render())
			args = append(args, "mut a"+strconv.Itoa(i)+"[..]")
		case pk == types.KMut:
			body = append(body, "    var a"+strconv.Itoa(i)+" := "+a.render())
			args = append(args, "mut a"+strconv.Itoa(i))
		default:
			b.WriteString("const " + name + ": " + c.inst.params[i].text + " = " + a.render() + "\n")
			args = append(args, name)
		}
	}
	b.WriteString(k + "F(): bool {\n")
	for _, l := range body {
		b.WriteString(l + "\n")
	}
	call := callee(args)
	void := sig.Result == types.Void || sig.Result == types.NoType
	switch {
	case sig.Errors != types.EmptyErrors && void:
		b.WriteString("    " + call + " else {\n        return true\n    }\n")
	case sig.Errors != types.EmptyErrors:
		b.WriteString("    _ = " + call + " else {\n        return true\n    }\n")
	case void:
		b.WriteString("    " + call + "\n")
	default:
		b.WriteString("    _ = " + call + "\n")
	}
	b.WriteString("    return true\n}\nconst " + k + " = " + k + "F()\n")
	return b.String()
}

// call spells the case as a call, for the report.
func (c *propCase) call() string {
	var args []string
	for _, a := range c.args {
		args = append(args, a.render())
	}
	if c.t.sig.Receiver != types.NoReceiver {
		return args[0] + "." + c.t.name + c.inst.text + "(" + strings.Join(args[1:], ", ") + ")"
	}
	return c.t.display + c.inst.text + "(" + strings.Join(args, ", ") + ")"
}

// report is the contract's one TestCase.
func (t *propTarget) report(want int) TestCase {
	tc := TestCase{Module: t.mod.path, Name: "prop " + t.display, Status: 0}
	var b strings.Builder
	counts := func() {
		fmt.Fprintf(&b, "%d cases, %d discarded by requires or invariant", t.valid, t.discarded)
		if t.gaveUp > 0 {
			fmt.Fprintf(&b, ", %d not judged (fuel or refused)", t.gaveUp)
		}
		if len(t.insts) > 1 || (len(t.insts) == 1 && t.insts[0].text != "") {
			var ins []string
			for _, in := range t.insts {
				if in.dropped == "" {
					ins = append(ins, strings.Trim(in.text, "[]"))
				}
			}
			fmt.Fprintf(&b, "; instances %s", strings.Join(ins, ", "))
		}
	}
	switch {
	case t.skip != "":
		tc.Result, tc.Detail = "skip", t.skip
	case t.finding != nil:
		f := t.finding
		tc.Result, tc.Status, tc.Detail = "fail", 1, f.kind
		fmt.Fprintf(&b, "%s  at %s\n", f.call(), f.at)
		for _, cl := range t.clauses {
			fmt.Fprintf(&b, "clause: %s\n", cl)
		}
		switch {
		case t.fromCorpus:
			b.WriteString("from the corpus; ")
		case t.rounds < 0:
			fmt.Fprintf(&b, "shrunk in %d rounds (stopped at the limit); ", -t.rounds)
		default:
			fmt.Fprintf(&b, "shrunk in %d rounds; ", t.rounds)
		}
		counts()
		b.WriteByte('\n')
	case t.valid == 0:
		// Say what kept every case from a verdict: requires (or an
		// invariant) rejecting the inputs, or fuel and refusals, which
		// requires had no part in.
		tc.Result = "skip"
		switch {
		case t.gaveUp == 0:
			tc.Detail = "no generated input met requires"
		case t.discarded == 0:
			tc.Detail = "no case was judged (fuel or refused): " + t.gaveUpWhy
		default:
			tc.Detail = fmt.Sprintf("no generated input met requires, and %d were not judged (fuel or refused): %s", t.gaveUp, t.gaveUpWhy)
		}
		counts()
		b.WriteByte('\n')
	default:
		tc.Result = "pass"
		counts()
		if t.valid < want*len(t.insts) {
			b.WriteString(" (fewer than wanted: requires rejected most inputs, or the domain is small)")
		}
		b.WriteByte('\n')
	}
	tc.Output = b.String()
	return tc
}

// propCorpusEntry is one line of a contract's corpus file.
type propCorpusEntry struct {
	Inst string `json:"inst"`
	Args []pv   `json:"args"`
}

// propCorpusPath is the contract's corpus file under the cache directory.
// The bare display name is unique even on a case-insensitive filesystem:
// two items of a module whose names differ only in case are E0206.
func (s *Session) propCorpusPath(t *propTarget) string {
	return filepath.Join(s.cacheDir(), "prop", t.mod.path, t.display+".json")
}

func (s *Session) propCorpusRead(t *propTarget) []propCorpusEntry {
	if s.cfg.NoCache {
		return nil
	}
	raw, err := os.ReadFile(s.propCorpusPath(t))
	if err != nil {
		return nil
	}
	var out []propCorpusEntry
	for _, l := range strings.Split(string(raw), "\n") {
		var e propCorpusEntry
		if l != "" && json.Unmarshal([]byte(l), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// propCorpusCases are the corpus entries whose instance and arity still
// fit, as cases evaluated first.
func (s *Session) propCorpusCases(t *propTarget) []*propCase {
	var out []*propCase
	for _, e := range s.propCorpusRead(t) {
		for _, in := range t.insts {
			if in.text == e.Inst && len(e.Args) == len(in.params) {
				c := &propCase{t: t, inst: in, args: e.Args, corpus: true}
				if !in.seen[c.key()] {
					in.seen[c.key()] = true
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// propCorpusKeep adds the shrunk finding to the corpus, keeping the newest
// propCorpusMax entries.
func (s *Session) propCorpusKeep(t *propTarget) {
	if s.cfg.NoCache {
		return
	}
	var fb []byte
	if t.finding != nil {
		fb, _ = json.Marshal(propCorpusEntry{Inst: t.finding.inst.text, Args: t.finding.args})
	}
	var lines []string
	for _, e := range s.propCorpusRead(t) {
		b, _ := json.Marshal(e)
		c := &propCase{inst: &propInst{text: e.Inst}, args: e.Args}
		if string(b) != string(fb) && !slices.Contains(t.staleCorpus, c.key()) {
			lines = append(lines, string(b))
		}
	}
	if fb != nil {
		lines = append(lines, string(fb))
	}
	if len(lines) > propCorpusMax {
		lines = lines[len(lines)-propCorpusMax:]
	}
	p := s.propCorpusPath(t)
	if len(lines) == 0 {
		_ = os.Remove(p)
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
}
