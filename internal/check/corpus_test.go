// Why: check's share of the corpus verdicts must not regress, and mk corpus
// runs the whole compiler; this keeps a fast check-only judge.
// What: TestCorpusRejectBlocksCheckCodes, TestCorpusOkBlocksCheckClean,
// TestGateProgramsTypeCheck, with corpusPrelude and std stubs.
// How: runs the mk blocks of doc/ and the reject units of
// tests/conformance/codes/ through the harness (fake prelude, no evaluator)
// and judges check's codes only: each verdict code on each marked line, and
// no check code the verdict lacks. internal/corpus stays the real judge.

package check

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/syntax"
)

// corpusExcepted are the blocks that cannot pass here, each with why. An
// excepted block that passes fails the test, so the list stays current. A
// doc block's key is "doc.md: line", one of its lines trimmed, so an edit
// that moves the block keeps the key (docExcepted resolves it).
var corpusExcepted = map[string]string{
	"memory.md: s.out[0] = 1 // <- E0507":                    "a write through a shared reference in a body is borrow's E0507",
	"memory.md: try out.Push(&x) // <- E0609":                "a literal storing a loan on a capture into its parameter is borrow's E0609, flow group (borrow's TestE0609_StoredLoan)",
	"memory.md: best = some(p) // <- E0609":                  "a literal storing a parameter loan into a captured variable is borrow's E0609, flow group (borrow's TestE0609_StoreIntoCapture)",
	"reference.md: const Big: u8 = 200 + 100 // <- E0901":    "a constant's overflow is an evaluation failure, reported by EvalConst, which needs the evaluator; this harness has none",
	"reference.md: Count = 1 // <- E0507":                    "a write to a non-var global in a body is borrow's E0507",
	"reference.md: n += 1 // <- E0404":                       "a write to a parameter is borrow's E0404; check's is only the extern var write",
	"reference.md: n = 1 // <- E0404":                        "a second assignment to a non-var local is borrow's E0404; check's is only the extern var write",
	"reference.md: p.x = 0 // <- E0507":                      "a write through a shared reference in a body is borrow's E0507",
	"reference.md: c.Bump() // <- E0508":                     "a mut borrow of a non-var local outside a capture is borrow's E0508; check's is a literal mutating a non-var capture",
	"codes/E0508.mk":                                         "a mut borrow of a non-var local outside a capture is borrow's E0508; check's is a literal mutating a non-var capture",
	"reference.md: return Nest(1, 3) // <- E0708":            "an instance-limit E0708 is mono's",
	"reference.md: const Bad = Pick(5) // <- E0901":          "an evaluation failure is reported by EvalConst, which needs the evaluator; this harness has none",
	"reference.md: const H = Read() // <- E0901":             "an evaluation failure is reported by EvalConst, which needs the evaluator; this harness has none",
	"reference.md: const Total = Spin() // <- E0902":         "fuel exhaustion is the evaluator's; this harness has none",
	"reference.md: const A: int = B + 1 // <- E0909":         "a const cycle is reported by EvalConst, which needs the evaluator; this harness has none",
	"reference.md: gen derive.Eq(P) // <- E0704":             "the conflicting impl is gen output; this harness runs no generator (mk corpus judges it)",
	"reference.md: gen derive.Eq(time.Duration) // <- E0704": "the orphan impl is gen output; this harness runs no generator (mk corpus judges it)",
	"reference.md: const P = Pause() // <- E0901":            "an evaluation failure is reported by EvalConst, which needs the evaluator; this harness has none",
	"codes/E0406.mk": "a move of a value a defer uses is borrow's E0406; check's is a defer pending at a become",
	"examples.md: become raw.Compare(a, b, n) // <- E0314": "std.raw's Compare, the intrinsic become refuses (E0314), is not in this harness's prelude (mk corpus passes it; TestLangE0314's intrinsic case)",
	"codes/E0313_packed.mk":                                "rule 4 asks the driver's ABI classifier (Config.ParamClass), which this harness has not; mk corpus and TestBecome_PackedArgumentPerTarget judge it",
	"codes/E1101_vm.mk":                                    "the unsafe doors are std.vm's, std.runtime's and std.mem's exports, which this harness's prelude does not load; mk corpus judges it",
	"codes/E0303_float_conversion.mk":                      "`f32(K)` of a named float const is judged by K's value, which needs the evaluator; this harness has none",
	"codes/E1101_fault_filter.mk":                          "the unsafe doors are std.vm's, std.runtime's and std.mem's exports, which this harness's prelude does not load; mk corpus judges it",
	"codes/E1101_foreign_io.mk":                            "the unsafe doors are std.vm's, std.runtime's and std.mem's exports, which this harness's prelude does not load; mk corpus judges it",
	"codes/E1101_mem_custom.mk":                            "the unsafe doors are std.vm's, std.runtime's and std.mem's exports, which this harness's prelude does not load; mk corpus judges it",
	"codes/E0507.mk":                                       "a write through a shared reference in a body is borrow's E0507",
	"codes/E0901_global_literal.mk":                        "a global var's overflow is an evaluation failure, reported by the evaluator; this harness has none (mk corpus judges it)",
	"codes/E0708.mk":                                       "an instance-limit E0708 is mono's",
	"codes/E0706_fn_value_simd.mk":                         "std.simd's lane-count requires is imported, so only the evaluator judges it at the fn value; this harness has none (mk corpus passes it; TestE0706_FnValueSimd)",
	"codes/E0901.mk":                                       "an evaluation failure is reported by EvalConst, which needs the evaluator; this harness has none",
	"codes/E0901_used.mk":                                  "an evaluation failure is reported by EvalConst, which needs the evaluator; this harness has none (TestE0901_ConstReadInBody runs it with one)",
	"codes/E0902.mk":                                       "fuel exhaustion is the evaluator's; this harness has none",
	"codes/E0902_default.mk":                               "fuel exhaustion is the evaluator's; this harness has none",
	"codes/E0902_fuel_named.mk":                            "fuel exhaustion is the evaluator's; this harness has none",
	"codes/E0908_const_pattern.mk":                         "a const pattern's value is evaluated, and the evaluation reports the stage violation; this harness has no evaluator (mk corpus judges it)",
	"codes/E0909.mk":                                       "a const cycle is reported by EvalConst, which needs the evaluator; this harness has none",
	"codes/E0910.mk":                                       "a data file read is the evaluator's (build.File through the Program); this harness has none",
	"codes/E0704_gen_conflict.mk":                          "the conflicting impl is gen output; this harness runs no generator; mk corpus --all judges it, and TestE0704_GeneratedDuplicateCitesGenLine is check's half",
	"codes/E0704_gen_orphan.mk":                            "the orphan impl is gen output; this harness runs no generator; mk corpus --all judges it, and TestE0704_GeneratedOrphanAtGenLine is check's half",
	"codes/E0404.mk":                                       "a second assignment to a non-var local is borrow's E0404; check's is only the extern var write",
	"codes/E1102.mk":                                       "an unsafe block outside unsafe_modules is the driver's E1102; check's is only a section clause or an extern var",
	"codes/E0701_math_integer.mk":                          "math.Integer is std.math's, which this harness's prelude does not load; mk corpus judges it",
	"codes/E0704_math_integer.mk":                          "math.Integer is std.math's, which this harness's prelude does not load; mk corpus judges it",
	"codes/E0706_atomic_unordered.mk":                      "atomic.Unordered and the CanX clauses are std.atomic's, read through the evaluator; this harness has neither (mk corpus judges it)",
	"codes/E0701_offsetof_generic.mk":                      "mem.OffsetOf is std.mem's, which this harness's prelude does not load; mk corpus judges it",
	"codes/E0705_offsetof.mk":                              "mem.OffsetOf is std.mem's, which this harness's prelude does not load; mk corpus judges it",
	"codes/E0901_divwide.mk":                               "math.DivWide's panics are evaluation failures, which need the evaluator; this harness has none (mk corpus judges it)",
	"codes/E0901_offsetof.mk":                              "mem.OffsetOf's E0901 is an evaluation failure, which needs the evaluator; this harness has none (mk corpus judges it)",
	"codes/E1002_const_pointee.mk":                         "E1002 on a const's evaluated value is the declaration evaluation's (EvalConst, constPointee), which this harness does not run (mk corpus passes it; driver TestE1002_ConstPointeeIsReadOnly)",
	"codes/E0609.mk":                                       "closure syntax `|x| e` is the parser's E0609; check's E0609 is a method named without a call",
}

// stdStubs are std modules a judged block imports, declared as far as the
// block needs (the real std is not loaded here).
var stdStubs = map[string]string{
	// Error is open, with three of std's members, so a match on it
	// needs a `_` arm (E0306).
	"std.fs": "export error NotFound\nexport error Exists\nexport error Denied\n" +
		"export open type Error = NotFound | Exists | Denied\n" +
		"export Remove(io: Io, path: str): Error!void\n" +
		// fs.Kind, open as std declares it.
		"export open enum Kind {\n\tFile\n\tDir\n\tLink\n\tOther\n}\n" +
		"export ReadText(a: &Arena, io: Io, p: str): Error|OutOfMemory!str from a\n" +
		// The reference's File, linear, as internal/lower's stub has
		// it: the release blocks and the style guide's CloseAll.
		"export linear struct File {\n\th: int\n}\n" +
		"export File.Close(self, io: Io): Error!void\n" +
		"export File.Abandon(self)\n" +
		// The examples' log file and the cookbook's line reader.
		"export File.ReadAt(&self, io: Io, pos: i64, buf: mut []u8): Error!int\n" +
		"export File.WriteAt(&self, io: Io, pos: i64, data: []u8): Error!int\n" +
		"export File.Sync(mut self, io: Io): Error!void\n" +
		"export File.Reader(mut self, io: Io, buf: mut []u8): Reader from self, buf\n" +
		// The E1101 block: fs.Map and its linear Mapping, as
		// the reference declares them.
		"export linear struct Mapping {\n\tp: int\n}\n" +
		"export Map(io: Io, f: &File, offset: i64, len: int): Error!Mapping\n" +
		"export Mapping.Bytes(&self): []u8 from self\n" +
		"export Mapping.Unmap(self)\n",
	// The reference's Pool and Handle, as far as the Sweep block uses
	// them.
	"std.mem": "export struct Handle {\n\tslot: u32\n\tgeneration: u32\n}\n" +
		"export view struct Pool[T] {\n\tp: *T\n\tn: int\n}\n" +
		"export Pool[T].Free(mut self, h: Handle): bool\n" +
		"export Pool[T].Sweep(mut self, const keep: fn(Handle, &T): bool): int\n",
}

// Some blocks import std.mmio: its declarations are the tree's
// file, which holds bodiless intrinsics only. The cookbook's job queue
// imports std.dyn as far as Ref goes (std/dyn/ref.mk), as bodiless
// intrinsics, so its struct's fn field is typed (internal/lower's stub).
func init() {
	stdStubs["std.dyn"] = "export mut view struct Ref {\n\tp: *u8\n\tkey: u64\n}\n" +
		"export Ref.Of[T](x: mut T): Ref from x\n" +
		"export Ref.Mut[T](mut self): mut T from self\n" +
		"export Ref.Get[T](&self): &T from self\n"
	if b, err := os.ReadFile(filepath.Join("..", "..", "std", "mmio", "mmio.mk")); err == nil {
		stdStubs["std.mmio"] = strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	if b, err := os.ReadFile(filepath.Join("..", "..", "std", "simd", "vec.mk")); err == nil {
		stdStubs["std.simd"] = strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	// One block rejects Fence(Relaxed): std.atomic's
	// orders and Fence's compile-time requires, without std.raw's body; other
	// blocks need Order's clause methods and Atomic's New, Load, Store and FetchAdd.
	stdStubs["std.atomic"] = "export struct Order {\n    v: u8\n}\n\n" +
		"export const Relaxed: Order = Order(v: 0)\nexport const Acquire: Order = Order(v: 1)\n" +
		"export const Release: Order = Order(v: 2)\nexport const AcqRel: Order = Order(v: 3)\n" +
		"export const SeqCst: Order = Order(v: 4)\n\nexport Fence(const o: Order)\n    requires o.CanFence() {\n}\n\n" +
		"export Order.CanLoad(self): bool {\n    return self.v != 2 && self.v != 3\n}\n\n" +
		"export Order.CanStore(self): bool {\n    return self.v != 1 && self.v != 3\n}\n\n" +
		"export Order.CanFence(self): bool {\n    return self.v != 0\n}\n\n" +
		"export struct Atomic[T: Plain] {\n    v: T\n}\n\n" +
		"export Atomic[T].Load(&self, const o: Order): T\n    requires o.CanLoad() {\n    return self.v\n}\n\n" +
		"export Atomic[T].Store(&self, x: T, const o: Order)\n    requires o.CanStore() {\n}\n\n" +
		"export Atomic[T].New(x: T): Atomic[T] {\n    return Atomic[T](v: x)\n}\n\n" +
		"export Atomic[T].FetchAdd(&self, x: T, const o: Order): T {\n    return self.v\n}\n"
}

// stubPaths are stdStubs' keys in byte order, so modules register in a
// fixed order.
func stubPaths() []string {
	var out []string
	for p := range stdStubs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var (
	verdictRe = regexp.MustCompile(`^// verdict: reject((?: E[0-9]{4})+)\s*$`)
	markerRe  = regexp.MustCompile(`// <- (E[0-9]{4})`)
)

// rejectUnits are the reject blocks and conformance code units, keyed by
// "file:line" (a doc block, the opening fence's line) or "codes/<name>".
func rejectUnits(t *testing.T) (keys []string, texts map[string]string) {
	keys, texts = languageDocBlocks(t)
	files, _ := filepath.Glob(filepath.Join("..", "..", "tests", "conformance", "codes", "*.mk"))
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		k := "codes/" + filepath.Base(f)
		keys = append(keys, k)
		texts[k] = strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	return keys, texts
}

// testPrelude is the reference's test prelude, which every doc block
// and every conformance unit with `// prelude: yes` compiles with.
func testPrelude(t *testing.T) string {
	keys, texts := languageDocBlocks(t)
	for _, k := range keys {
		if strings.HasPrefix(k, "reference.md:") && strings.Contains(texts[k], "MakePoint(): Point\n") {
			return texts[k]
		}
	}
	t.Fatal("no test prelude block in doc/reference.md")
	return ""
}

// judgeCheckCodes is "" when src's check codes match its verdict, else
// what differs. design says the unit is a doc block, judged as the one
// module of a package whose pkg.mk lists it in unsafe_modules; a
// conformance codes/ unit is a plain module.
func judgeCheckCodes(t *testing.T, own map[diag.Code]bool, core, prelude, src string, design bool) (judged bool, problem string) {
	lines := strings.Split(src, "\n")
	m := verdictRe.FindStringSubmatch(lines[0])
	if m == nil {
		return false, ""
	}
	want := map[diag.Code]bool{}
	for _, f := range strings.Fields(m[1]) {
		if c, ok := diag.ParseCode(f); ok && own[c] {
			want[c] = true
		}
	}
	if len(want) == 0 {
		return false, ""
	}
	// A unit judged in the parse group (`codes/E1204.mk`, the parser's
	// prefix `async`) is the parser's verdict: a code check also emits in
	// another form (E1204's postfix `.await`) does not make it check's.
	if strings.Contains(src, "\n// needs: parse\n") {
		return false, ""
	}
	w, b, ds := checkNoICEWith(t, opt{testPrelude: prelude, core: core, unsafe: design}, src, func(w *world) {
		for _, path := range stubPaths() {
			text := stdStubs[path]
			if strings.Contains(src, "import "+path+"\n") {
				w.add(path, opt{std: true}, text)
			}
		}
	})
	file := b.srcs[len(b.srcs)-1].ID
	gotAt := map[diag.Code]map[int]bool{}
	for _, d := range ds {
		if !own[d.Code] || d.At.File != file || noEvaluator(d) {
			continue
		}
		if gotAt[d.Code] == nil {
			gotAt[d.Code] = map[int]bool{}
		}
		if f := w.sm.File(d.At.File); f != nil {
			gotAt[d.Code][int(f.LineCol(d.At.Start).Line)] = true
		}
	}
	var probs []string
	for c := range want {
		if gotAt[c] == nil {
			probs = append(probs, "missing "+c.String())
		}
	}
	for i, l := range lines {
		for _, mm := range markerRe.FindAllStringSubmatch(l, -1) {
			if c, ok := diag.ParseCode(mm[1]); ok && own[c] && gotAt[c] != nil && !gotAt[c][i+1] {
				probs = append(probs, c.String()+" not on marked line "+itoa(i+1))
			}
		}
	}
	for c := range gotAt {
		if !want[c] {
			var at []string
			for l := range gotAt[c] {
				at = append(at, itoa(l))
			}
			sort.Strings(at)
			probs = append(probs, "extra "+c.String()+" at "+strings.Join(at, ","))
		}
	}
	sort.Strings(probs)
	return true, strings.Join(probs, "; ")
}

func TestCorpusRejectBlocksCheckCodes(t *testing.T) {
	own := map[diag.Code]bool{}
	for _, s := range snaps {
		own[s.code] = true
	}
	// E0101 is the parser's; check's E0101 (a contract's form) has
	// its own tests, and a recovered tree's other codes are not judged.
	delete(own, diag.E0101)
	// E1221 is mostly the parser's (macros, `dyn I`) and resolve's (names);
	// check's member form has its own tests.
	delete(own, diag.E1221)
	prelude := testPrelude(t)
	core := corpusPrelude(t)
	keys, texts := rejectUnits(t)
	excepts := docExcepted(t, texts, corpusExcepted)
	judged, passed := 0, 0
	for _, k := range keys {
		pre := prelude
		if strings.HasPrefix(k, "codes/") && !strings.Contains(texts[k], "\n// prelude: yes\n") {
			pre = "" // conformance codes/ units default to no prelude
		}
		ok, prob := judgeCheckCodes(t, own, core, pre, texts[k], !strings.HasPrefix(k, "codes/"))
		if !ok {
			continue
		}
		judged++
		why, excepted := excepts[k]
		switch {
		case prob == "" && excepted:
			t.Errorf("%s passes now: remove it from corpusExcepted (%s)", k, why)
		case prob == "":
			passed++
		case !excepted:
			t.Errorf("%s: %s", k, prob)
		}
	}
	t.Logf("%d reject units with a check code judged, %d pass, %d excepted", judged, passed, len(corpusExcepted))
}

// docExcepted keys list's entries as the harness names units ("doc.md:<fence
// line>" or "codes/<name>"). An entry "doc.md: line" names the one block of
// doc.md that has a line equal to line, trimmed; an entry that names no
// unit, or several blocks, fails the test.
func docExcepted(t *testing.T, texts map[string]string, list map[string]string) map[string]string {
	out := map[string]string{}
	for k, why := range list {
		doc, line, ok := strings.Cut(k, ".md: ")
		if !ok {
			if _, ok := texts[k]; !ok {
				t.Errorf("an exception names %s, which is not a unit", k)
			}
			out[k] = why
			continue
		}
		var found []string
		for unit, src := range texts {
			if !strings.HasPrefix(unit, doc+".md:") {
				continue
			}
			for _, l := range strings.Split(src, "\n") {
				if strings.TrimSpace(l) == line {
					found = append(found, unit)
					break
				}
			}
		}
		if len(found) != 1 {
			sort.Strings(found)
			t.Errorf("the exception %q names %d blocks (%s), not one", k, len(found), strings.Join(found, ", "))
			continue
		}
		out[found[0]] = why
	}
	return out
}

// stdGap reports whether d is only the fake prelude missing a std member:
// E0203 on a prelude type, a builtin type or a slice, or E0702 for a std
// interface on a builtin type (the real std declares them).
func stdGap(d *diag.Diagnostic, prelude map[string]bool) bool {
	full := fact(d, "type")
	ty := full
	if i := strings.IndexByte(ty, '['); i > 0 {
		ty = ty[:i] // List[T] is List
	}
	builtin := full == "str" || strings.HasPrefix(full, "[") || strings.HasPrefix(full, "mut [")
	if n := strings.TrimLeft(full, "iuf"); len(n) < len(full) && n != "" {
		if _, err := strconv.Atoi(n); err == nil {
			builtin = true // i32, u8, f64
		}
	}
	switch d.Code {
	case diag.E0203:
		return prelude[ty] || builtin
	case diag.E0702:
		return builtin && prelude[fact(d, "interface")]
	}
	return false
}

// noEvaluator reports whether d is E0901 for want of an evaluator: this
// harness has none, so a compile-time requires (std.simd's lane counts)
// or a const that calls a function is judged by mk corpus, which
// runs the evaluator, not here.
func noEvaluator(d *diag.Diagnostic) bool {
	return d.Code == diag.E0901 && fact(d, "refused") == "no-evaluator"
}

// genBlock reports whether a block has a gen line (a line starting `gen `).
func genBlock(src string) bool {
	return strings.HasPrefix(src, "gen ") || strings.Contains(src, "\ngen ")
}

// TestCorpusOkBlocksCheckClean: every ok block of doc/ draws no check
// code with the test prelude, but for the std members the fake prelude
// does not declare (stdGap), which are counted.
func TestCorpusOkBlocksCheckClean(t *testing.T) {
	own := map[diag.Code]bool{}
	for _, s := range snaps {
		own[s.code] = true
	}
	delete(own, diag.E0101)
	names := map[string]bool{}
	for _, n := range preludeNames {
		names[n] = true
	}
	pre := testPrelude(t)
	core := corpusPrelude(t)
	keys, texts := languageDocBlocks(t)
	excepts := docExcepted(t, texts, okExcepted)
	judged, clean, gaps := 0, 0, 0
	excused := map[string]bool{}
	for _, k := range keys {
		src := texts[k]
		if !strings.HasPrefix(src, "// verdict: ok\n") {
			continue
		}
		judged++
		w, b, ds := checkNoICEWith(t, opt{testPrelude: pre, core: core, unsafe: true}, src, func(w *world) {
			for _, path := range stubPaths() {
				text := stdStubs[path]
				if strings.Contains(src, "import "+path+"\n") {
					w.add(path, opt{std: true}, text)
				}
			}
		})
		file := b.srcs[len(b.srcs)-1].ID
		gap := false
		for _, d := range ds {
			if !own[d.Code] || d.At.File != file {
				continue
			}
			if noEvaluator(d) {
				gap = true
				t.Logf("%s: no evaluator here for %s at %s (mk corpus judges it)", k, d.Code, w.lc(d.At))
				continue
			}
			if stdGap(d, names) {
				gap = true
				t.Logf("%s: std gap %s at %s: %s", k, d.Code, w.lc(d.At), d.Message)
				continue
			}
			// What a block's gen lines emit (`T.FromJson`,
			// `T.IsMatch`, `impl json.Json for T`) does not exist here: this
			// harness runs no generator, as corpusExcepted says of the
			// codes/E0704_gen_* units; mk corpus judges those blocks.
			if genBlock(src) && (d.Code == diag.E0203 || d.Code == diag.E0703) {
				gap = true
				t.Logf("%s: gen output %s at %s: %s", k, d.Code, w.lc(d.At), d.Message)
				continue
			}
			if why, ok := excepts[k]; ok {
				excused[k] = true
				t.Logf("%s: excepted %s at %s (%s)", k, d.Code, w.lc(d.At), why)
				continue
			}
			t.Errorf("%s: %s at %s: %s", k, d.Code, w.lc(d.At), d.Message)
		}
		if why, ok := excepts[k]; ok && !excused[k] {
			t.Errorf("%s passes now: remove it from okExcepted (%s)", k, why)
		}
		if gap {
			gaps++
		} else {
			clean++
		}
	}
	for k := range excepts {
		if !strings.HasPrefix(texts[k], "// verdict: ok\n") {
			t.Errorf("okExcepted names %s, which is not an ok block", k)
		}
	}
	t.Logf("%d ok blocks judged: %d with no check code, %d with only std members the fake prelude lacks", judged, clean, gaps)
}

// okExcepted are the ok blocks that draw a check code here only for want
// of the real std, each with why. An excepted block that comes out clean
// fails the test, so the list stays current; mk corpus judges each one.
var okExcepted = map[string]string{
	"reference.md: impl Slice[int] for Ring {":                         "std's Slice[T] declares Items and ItemsMut; the fake prelude's Slice is empty, so the impl's methods are E0707 here",
	"reference.md: export Describe(g: mut Gen, t: Type) {":             "a generator function: `quote` and Gen's Block, Str and Fields are std's generator API, which this harness does not declare",
	"reference.md: export Count(g: mut Gen, t: Type) {":                "a generator function: `quote` and Gen's Block, Str and Fields are std's generator API, which this harness does not declare",
	"overview.md: Same(a: &Color, b: &Color): bool {":                  "gen derive.Eq writes the impl Eq the block calls; this harness runs no generator",
	"reference.md: gen derive.Eq(Tile)     // writes impl Eq for Tile": "gen derive.Eq writes the impl Eq the block uses; this harness runs no generator",
	"reference.md: gen derive.Eq(Person) // #[derive(PartialEq)]":      "gen derive.Eq writes the impl Eq the block uses; this harness runs no generator",
}

// corpusPrelude is fakePrelude() with the reference's std.core surface
// added: System's fields, every function line before the block's first
// "// std." group comment, and the std.sync group's Mutex, Guard and Chan
// lines (prelude types), exported and bodiless (std's intrinsic form). The
// names the fake prelude declares itself (Print, Arena.Fmt, Writer.Str,
// Cell's functions, the interfaces and Cell) keep the fake's text.
func corpusPrelude(t *testing.T) string {
	keys, texts := languageDocBlocks(t)
	var lib string
	for _, k := range keys {
		if strings.HasPrefix(k, "reference.md:") && strings.Contains(texts[k], "System.Stdout(&self): Writer\n") {
			lib = texts[k]
		}
	}
	if lib == "" {
		t.Fatal("no library surface block in doc/reference.md")
	}
	core := fakePrelude()
	const fakeSystem = "export struct System {}\n"
	if !strings.Contains(core, fakeSystem) {
		t.Fatal("fakePrelude declares System differently")
	}
	var b strings.Builder
	depth := 0
	inSystem := false
	other := false // past the std.core lines, in another module's group
	for _, l := range strings.Split(lib, "\n") {
		if strings.HasPrefix(l, "// std.") {
			other = true
			continue
		}
		switch {
		case strings.HasPrefix(l, "struct System {"):
			inSystem = true
			b.WriteString("export " + l + "\n")
			continue
		case inSystem:
			b.WriteString(l + "\n")
			if l == "}" {
				inSystem = false
			}
			continue
		}
		if depth > 0 || strings.HasSuffix(l, "{") {
			depth += strings.Count(l, "{") - strings.Count(l, "}")
			continue
		}
		if l == "" || strings.HasPrefix(l, "//") || strings.HasPrefix(l, " ") || !strings.Contains(l, "(") {
			continue
		}
		// The std.sync group lists Mutex, Guard and Chan, which are
		// std.core's prelude types; every other group's lines are its
		// module's.
		if other && !strings.HasPrefix(l, "Mutex[") && !strings.HasPrefix(l, "Guard[") && !strings.HasPrefix(l, "Chan[") {
			continue
		}
		if strings.HasPrefix(l, "Print[") || strings.HasPrefix(l, "Arena.Fmt") || strings.HasPrefix(l, "Writer.Str(") || strings.HasPrefix(l, "Cell[") ||
			strings.HasPrefix(l, "Swap[") || strings.HasPrefix(l, "Replace[") {
			continue
		}
		if impl, ok := freeImplOf(l); ok {
			b.WriteString(impl)
			continue
		}
		b.WriteString("export " + l + "\n")
	}
	core = strings.Replace(core, fakeSystem, "", 1)
	// The iterators' fields as std lays them out, so the
	// surface's `from self.text` and `from self.map` clauses resolve.
	for _, r := range [][2]string{
		{"export struct Lines {}\n", "export view struct Lines {\n\ttext: str\n\tpos: int\n}\n"},
		{"export struct Split {}\n", "export view struct Split {\n\ttext: str\n\tpos: int\n}\n"},
		{"export struct Words {}\n", "export view struct Words {\n\ttext: str\n\tpos: int\n}\n"},
		{"export struct Scalars {}\n", "export view struct Scalars {\n\ttext: str\n\tpos: int\n}\n"},
		{"export view struct MapIter[K, V] {\n\tk: *K\n\tv: *V\n}\n", "export view struct MapIter[K, V] {\n\tmap: &Map[K, V]\n\ti: int\n}\n"},
		{"export view struct TextMapIter[V] {\n\tp: *V\n}\n", "export view struct TextMapIter[V] {\n\tmap: &TextMap[V]\n\ti: int\n}\n"},
		// The reference's enums, and Format for every integer width std
		// implements it for.
		{"export enum Arch { A }\n", "export enum Arch { X64\n\tArm64\n\tRiscv64 }\n"},
		{"export enum Os { A }\n", "export enum Os { Linux\n\tWindows\n\tMacos\n\tFreestanding }\n" +
			"impl Format for i32 {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n"},
		// The reference's Hash, which a key type's impl must match (E0707).
		{"export interface Hash {}\n", "export interface Hash {\n\tHash(&self, h: mut Hasher)\n}\n"},
		// Limits and StackSize as std declares them, and a Mutex word whose
		// name does not fold to the method Lock's.
		{"export struct Limits {}\n", "export struct Limits {\n\texport tasks: int\n\texport stack: StackSize\n}\n"},
		{"export enum StackSize { A }\n", "export enum StackSize { Default\n\tLarge\n\tBytes(n: int) }\n"},
		{"export struct Mutex[T] {\n\tlock: u32\n\tv: T\n}\n", "export struct Mutex[T] {\n\tword: u32\n\tv: T\n}\n"},
	} {
		if !strings.Contains(core, r[0]) {
			t.Fatalf("fakePrelude has no %q", r[0])
		}
		core = strings.Replace(core, r[0], r[1], 1)
	}
	// Format for references, optionals, slices and the lists, as std
	// implements it.
	for _, t := range []string{"&T", "?T", "[]T", "List[T]", "HeapList[T]"} {
		core += "impl[T: Format] Format for " + t + " {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n"
	}
	// Format and Eq for the built-in scalars fakePrelude leaves out, as
	// std implements them.
	for _, t := range []string{"bool", "char", "i8", "i16", "u8", "u16", "u32", "u64", "uint", "f32"} {
		core += "impl Format for " + t + " {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t}\n}\n"
	}
	for _, t := range []string{"bool", "char", "i8", "i16", "i64", "u16", "u32", "u64", "f32", "f64"} {
		core += "impl Eq for " + t + " {\n\tEq(&self, other: &" + t + "): bool {\n\t\treturn true\n\t}\n}\n"
	}
	// Chan and Mutex are Send and Share when T is Send, and the heap
	// containers Send, as std declares them.
	for _, t := range []string{"Chan[T]", "Mutex[T]"} {
		core += "unsafe impl[T: Send] Send for " + t + " {}\nunsafe impl[T: Send] Share for " + t + " {}\n"
	}
	core += "unsafe impl[T: Send] Send for HeapList[T] {}\nunsafe impl[V: Send] Send for TextMap[V] {}\nunsafe impl[T: Send] Send for Box[T] {}\n"
	core += b.String()
	sm := source.NewSourceMap()
	if _, ds := syntax.ParseFile(sm.Add("core.mk", []byte(core)), source.NewInterner(), syntax.Options{AllowBodiless: true}); len(ds) > 0 {
		t.Fatalf("the corpus prelude does not parse: %s at %d", ds[0].Message, ds[0].At.Start)
	}
	return core
}

// freeImplOf turns a surface line `HeapList[T].Free(self, a: &Alloc)` into
// the unbounded impl std writes for it (`impl[T] Free for HeapList[T]`),
// so a container satisfies a `Free` bound as in std, with check's
// element condition, and its Free call is the release row's.
func freeImplOf(l string) (string, bool) {
	const tail = ".Free(self, a: &Alloc)"
	if !strings.HasSuffix(l, tail) {
		return "", false
	}
	typ := strings.TrimSuffix(l, tail)
	gens := ""
	if i := strings.IndexByte(typ, '['); i >= 0 {
		gens = typ[i:]
	}
	return "impl" + gens + " Free for " + typ + " {\n\tFree(self, a: &Alloc) {\n\t}\n}\n", true
}

// TestGateProgramsTypeCheck: tests/programs/fizz.mk and wc.mk draw no
// diagnostic at all through stages 1 to 6 with the corpus prelude
// (corpusPrelude, the std.fs stub), and std.core itself draws no E0000.
func TestGateProgramsTypeCheck(t *testing.T) {
	core := corpusPrelude(t)
	w := newWorldCore(t, true, core)
	for _, d := range w.mods["std.core"].diags() {
		if d.Code == diag.E0000 {
			t.Fatalf("std.core: %s", dump(w, []*diag.Diagnostic{d}))
		}
	}
	for _, p := range []string{"fizz.mk", "wc.mk"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "tests", "programs", p))
		if err != nil {
			t.Fatal(err)
		}
		src := strings.ReplaceAll(string(data), "\r\n", "\n")
		w, _, ds := checkNoICEWith(t, opt{bin: true, core: core}, src, func(w *world) {
			for _, path := range stubPaths() {
				text := stdStubs[path]
				if strings.Contains(src, "import "+path+"\n") {
					w.add(path, opt{std: true}, text)
				}
			}
		})
		if len(ds) != 0 {
			t.Errorf("%s:\n%s", p, dump(w, ds))
		}
	}
}
