// Why: a conditional jump on a secret leaks it through timing; the source has
// no `if` on a secret, but LLVM can still turn a mask select into a branch.
// What: the classifier ctParse, ctClassify and ctClassifyBack, the allowance
// tables (including ctBackTable and ctBackCheck) with their presence rule
// (ctRequirePresent, ctPresence) and the probe-function rule (ctProbeFn),
// and TestSTDCRYPTO_ConstantTimeDisassembly over std.crypto's secret paths.
// How: llvm-objdump on an x86_64-linux build; each jump is "check" (reaches a
// Panic), "back" (a loop) or "other", and a new "other" fails for a human.

package std

import (
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// cryptoCTProbe reaches Seal, Open, Equal, X25519, FromSeed, Sign, Verify
// and Hmac256, with inputs partly from the argument count so nothing folds
// to a constant.
const cryptoCTProbe = `import std.crypto

export Main(sys: &System): WriteError|crypto.Error!void {
    var out := sys.Stdout()
    var key: [32]u8 = [7; 32]
    var nonce: [12]u8 = [1; 12]
    n := sys.args.Len()
    var plain: [40]u8 = [3; 40]
    plain[0] = Trunc[u8](n)
    var sealed: [56]u8 = [0; 56]
    try crypto.ChaCha20Poly1305.Seal(&key, &nonce, plain[0..7], plain[..], mut sealed[..])
    var back: [40]u8 = [0; 40]
    try crypto.ChaCha20Poly1305.Open(&key, &nonce, plain[0..7], sealed[..], mut back[..])
    eq := crypto.Equal(back[..], plain[..])
    x := crypto.X25519(&key, &key)
    var seed: [32]u8 = [5; 32]
    seed[2] = Trunc[u8](n)
    var sk := crypto.Ed25519.FromSeed(&seed)
    sk[1] = x[0]
    sig := crypto.Ed25519.Sign(&sk, plain[..])
    var pk: [32]u8 = [0; 32]
    ok := crypto.Ed25519.Verify(&pk, plain[..], &sig)
    h := crypto.Hmac256(key[..], plain[..])
    try Print(mut out, "{eq} {ok} {a} {b}\n", (eq: eq, ok: ok, a: sig[3], b: h[0]))
}
`

// ctAllow is the reading (mk and std of the tree, LLVM 21.1.8): for each
// function on a secret path, the "other" jumps it may have, every one read
// by hand as a test of a length or a loop bound. Where release builds
// differ, the larger count is allowed. A function the release build inlines
// everywhere
// (ctAbsent's "*": Seal, FromSeed, Equal, FeSquareN) is held at 0, its
// dev-build count kept in its comment, since no body of it is read here:
// a build that holds it out of line again fails
// until that copy is read. A function missing from the build was
// inlined into its caller, which is in this table too (Main last of all).
// std.hash's SHA-2 functions take secrets (HMAC keys, Ed25519 seeds) and
// are checked the same way. SHA-256's block loop
// (Sha256Compress, with the SHA-NI kernel's guard) is read by
// std-hash_ct_test.go on its four builds, so it is not listed here.
var ctAllow = map[string]int{
	// Main: Open's error tag and the probe's own Print. The other
	// branches concern the formatted bools and bytes it writes out or
	// Writer.Bytes' remaining capacity and empty-buffer tests.
	"mk.main.Main": 14,

	"mk.std.crypto.AeadTag":               2,  // aad and ciphertext lengths mod 16; no build reads more than 2
	"mk.std.crypto.ChaXor":                13, // 13 with frame pointers on (the default; rbp is not free, so register allocation and block layout change), read on the release disassembly: each flag-setter is an index, a length or a constant, among them the 256-byte four-block loop's length test (i against n) and the loop-versioning guards (an index, a length or their difference against another of them or a constant: 0x100, 0x3f, 3, 0x20, a length's low bits); none tests a key or data byte
	"mk.std.crypto.ChaXor4":               0,  // straight-line lanes, no conditional jump
	"mk.std.crypto.ChaOut4":               0,  // the transpose and XOR, no conditional jump
	"mk.std.crypto.PolyBlocks":            1,  // length
	"mk.std.crypto.Hmac256":               1,  // key length above 64, data and block lengths; no build reads more than 1
	"mk.std.crypto.ChaCha20Poly1305:Open": 4,  // three lengths, then the verdict (one ptest over the tag difference)
	"mk.std.crypto.ChaCha20Poly1305:Seal": 0,  // inlined in the release build (ctAbsent); 1 when out of line, the dev build 4 (lengths)
	"mk.std.crypto.PtBaseMult":            1,  // the digit loop's bound
	"mk.std.crypto.Ed25519:FromSeed":      0,  // inlined (ctAbsent); the dev build: its copy loop's exits, 2
	"mk.std.crypto.Equal":                 0,  // inlined (ctAbsent); the dev build: the length test and the loop's exit, 3
	"mk.std.crypto.FeSquareN":             0,  // inlined (ctAbsent); the dev build: the loop's exit on the public count, 1
	"mk.std.crypto.ScReduce64":            0,  // dev build: a loop exit; no build reads more than 0
	"mk.std.crypto.ChaBlock":              0,
	"mk.std.crypto.CombAt":                0,
	"mk.std.crypto.CombEntry":             0,
	"mk.std.crypto.Ed25519:Sign":          0,
	"mk.std.crypto.FeInvert":              0,
	"mk.std.crypto.FeMul":                 0,
	"mk.std.crypto.FePow22523":            0,
	"mk.std.crypto.FeSqrtRatio":           0,
	"mk.std.crypto.FeSquare":              0,
	"mk.std.crypto.P1Add":                 0,
	"mk.std.crypto.P1AddAffine":           0,
	"mk.std.crypto.P1Double":              0,
	"mk.std.crypto.P1Sub":                 0,
	"mk.std.crypto.P1SubAffine":           0,
	"mk.std.crypto.PtBytes":               0,
	"mk.std.crypto.ScLimbs32":             0,
	"mk.std.crypto.ScMulAdd":              0,
	"mk.std.crypto.ScReduceLimbs":         0,
	"mk.std.crypto.X25519":                0,
	"mk.std.hash.Sha256State:Bytes":       5, // lengths; no build reads more than 5
	"mk.std.hash.Sha256State:Sum":         1, // buffered length
	"mk.std.hash.Sha512Blocks":            3,
	"mk.std.hash.Sha512State:Bytes":       5, // no build reads more than 5
	"mk.std.hash.Sha512State:Sum":         1,
}

// ctBack is this test's ctBackTable: exact per-build counts of non-induction
// back edges (ctClassifyBack [2]), each read as public or non-secret.
var ctBack = ctBackTable{
	"mk.main.Main":                  {"release": 12}, // 11 backward jumps to the shared error-return block on Print/Open tags, 1 Writer.Bytes capacity test
	"mk.std.crypto.ChaBlock":        {"release": 1},  // fixed 10 double rounds (spilled loop counter decq -0x8(%rsp))
	"mk.std.crypto.ChaXor":          {"release": 1},  // 256-byte block loop (public src.Len())
	"mk.std.crypto.X25519":          {"release": 1},  // fixed 255-step Montgomery ladder counter reloaded across field calls
	"mk.std.hash.Sha256State:Bytes": {"release": 1},  // tail CopyBytes branch on public length
	"mk.std.hash.Sha512Blocks":      {"release": 2},  // CPU feature test (avx2/bmi2) and 128-byte block loop on public length
	"mk.std.hash.Sha512State:Bytes": {"release": 1},  // tail CopyBytes branch on public length
}

// ctPublic are std.crypto's functions that see public data only (Verify's
// path: the public key, the signature, the message).
var ctPublic = map[string]bool{
	"mk.std.crypto.Ed25519:Verify": true,
	"mk.std.crypto.Naf":            true,
	"mk.std.crypto.NafEntry":       true,
	"mk.std.crypto.PtDoubleMult":   true,
	"mk.std.crypto.PtFromBytes":    true,
	"mk.std.crypto.ScCanonical":    true,
}

// ctAbsent are the ctAllow functions this probe's release build holds no
// body of, each inlined into a checked caller (ctRequirePresent).
var ctAbsent = map[string][]string{
	"mk.std.crypto.ChaCha20Poly1305:Seal": {"*"},
	"mk.std.crypto.ChaOut4":               {"*"},
	"mk.std.crypto.CombEntry":             {"*"}, // CombAt
	"mk.std.crypto.Ed25519:FromSeed":      {"*"},
	"mk.std.crypto.Equal":                 {"*"},
	"mk.std.crypto.FePow22523":            {"*"},
	"mk.std.crypto.FeSqrtRatio":           {"*"},
	"mk.std.crypto.FeSquareN":             {"*"},
	"mk.std.crypto.P1Sub":                 {"*"}, // PtDoubleMult, public
	"mk.std.crypto.P1SubAffine":           {"*"}, // PtDoubleMult, public
	"mk.std.crypto.ScLimbs32":             {"*"},
	"mk.std.crypto.ScReduce64":            {"*"}, // Ed25519:Sign
}

// ctRequirePresent is the rule of the constant-time disassembly tests: a
// function an allowance table lists must be in the build out of line,
// since its reading covers only a body the test reads, unless absent lists
// it for that build (by name, or "*" for every build). A listed function
// missing from a build that absent does not name fails: otherwise a
// function such as ChaCha20Poly1305:Open could leave the build and the test
// still pass. table's keys are matched with instance hashes stripped.
//
// An absent function must be inlined into a function the build holds and
// the test reads (read), whose count then holds its jumps, by the build's
// DWARF (into, ctInlined). Where no function the test reads holds a copy
// of it, unrecorded must name the build, with the reason in its comment: a
// path the build's forces leave out, a function the probe does not reach,
// an inlined copy whose debug locations the optimizer dropped, or one
// inlined only into a public function (ctPublic), which the test does not
// read. A function unrecorded names that a read function does hold fails
// too, so the table stays exact. So an absence for any other reason fails.
//
// A function absent or unrecorded names for a build must not be held out
// of line there either: no reading of that build covers the body, and its
// allowance is the count of another build, so the body could gain jumps up
// to that count unread. It fails until its jumps are read and the build
// comes off the list.
func ctRequirePresent(t *testing.T, table, build string, allow map[string]int, seen map[string]bool, absent, unrecorded map[string][]string, into ctInlinedInto, read func(string) bool) {
	t.Helper()
	problems, notes := ctPresence(table, build, allow, seen, absent, unrecorded, into, read)
	for _, n := range notes {
		t.Log(n)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// ctPresence is ctRequirePresent's rule: the problems it finds, sorted, and
// a note for each absent function on where its copies are.
func ctPresence(table, build string, allow map[string]int, seen map[string]bool, absent, unrecorded map[string][]string, into ctInlinedInto, read func(string) bool) (problems, notes []string) {
	held := map[string]bool{}
	for fn := range seen {
		held[cr2CtInstance.ReplaceAllString(fn, "")] = true
	}
	inBuild := func(m map[string][]string, fn string) bool {
		return slices.Contains(m[fn], build) || slices.Contains(m[fn], "*")
	}
	for fn := range allow {
		switch {
		case held[fn] && inBuild(unrecorded, fn):
			problems = append(problems, fmt.Sprintf("%s: listed as unrecorded in the %s build, but the build holds it out of line", fn, build))
		case held[fn] && inBuild(absent, fn):
			problems = append(problems, fmt.Sprintf("%s: listed absent from the %s build, but the build holds it out of line: read its jumps there and take the build off its absent list", fn, build))
		case held[fn]:
		case inBuild(unrecorded, fn):
			for _, c := range slices.Sorted(maps.Keys(into[fn])) {
				if seen[c] && read(c) {
					problems = append(problems, fmt.Sprintf("%s: listed as unrecorded in the %s build, but %s, which the test reads, holds it inlined: drop it from the table", fn, build, c))
				}
			}
		case inBuild(absent, fn):
			var readers []string
			for c := range into[fn] {
				if seen[c] && read(c) {
					readers = append(readers, c)
				}
			}
			sort.Strings(readers)
			if len(readers) == 0 {
				problems = append(problems, fmt.Sprintf("%s: listed absent from the %s build, but no function the test reads holds it inlined (inlined into %v): find where it went", fn, build, slices.Sorted(maps.Keys(into[fn]))))
				continue
			}
			notes = append(notes, fmt.Sprintf("%s: not in the %s build, inlined into %s", fn, build, strings.Join(readers, ", ")))
		default:
			problems = append(problems, fmt.Sprintf("%s is in %s but not in the %s build, so its reading covers no body here: find where it went, and if it is inlined into a checked caller list it as absent for this build", fn, table, build))
		}
	}
	sort.Strings(problems)
	sort.Strings(notes)
	return problems, notes
}

// ctInlinedInto maps a function, by name with its instance hash stripped,
// to the out-of-line functions of a build that hold an inlined copy of it,
// by their symbol names.
type ctInlinedInto map[string]map[string]bool

// ctInlined reads a --debug ELF's DWARF: for each inlined subroutine, the
// outermost subprogram (the out-of-line function) whose body holds it.
func ctInlined(t *testing.T, exe string) ctInlinedInto {
	t.Helper()
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := f.DWARF()
	if err != nil {
		t.Fatalf("%s: DWARF: %v", exe, err)
	}
	name := func(e *dwarf.Entry) string {
		if n, ok := e.Val(dwarf.AttrLinkageName).(string); ok {
			return n
		}
		n, _ := e.Val(dwarf.AttrName).(string)
		return n
	}
	// Names by offset first: an abstract origin can come later in a unit.
	names := map[dwarf.Offset]string{}
	for r := d.Reader(); ; {
		e, err := r.Next()
		if err != nil {
			t.Fatalf("%s: DWARF: %v", exe, err)
		}
		if e == nil {
			break
		}
		if e.Tag == dwarf.TagSubprogram || e.Tag == dwarf.TagInlinedSubroutine {
			names[e.Offset] = name(e)
		}
	}
	of := func(e *dwarf.Entry) string {
		if o, ok := e.Val(dwarf.AttrAbstractOrigin).(dwarf.Offset); ok {
			return names[o]
		}
		return name(e)
	}
	into := ctInlinedInto{}
	// outer[i] is the out-of-line function open at depth i, "" if none.
	var outer []string
	for r := d.Reader(); ; {
		e, _ := r.Next()
		if e == nil {
			break
		}
		if e.Tag == 0 {
			if len(outer) > 0 {
				outer = outer[:len(outer)-1]
			}
			continue
		}
		cur := ""
		if len(outer) > 0 {
			cur = outer[len(outer)-1]
		}
		switch {
		case e.Tag == dwarf.TagSubprogram && cur == "" && e.Val(dwarf.AttrLowpc) != nil:
			cur = of(e)
		case e.Tag == dwarf.TagInlinedSubroutine && cur != "":
			fn := cr2CtInstance.ReplaceAllString(of(e), "")
			if into[fn] == nil {
				into[fn] = map[string]bool{}
			}
			into[fn][cur] = true
		}
		if e.Children {
			outer = append(outer, cur)
		}
	}
	return into
}

// ctChecked reports whether TestSTDCRYPTO_ConstantTimeDisassembly reads
// fn: a function ctAllow lists, any of std.crypto's, or one of the
// probe's own (ctProbeFn).
func ctChecked(fn string) bool {
	_, known := ctAllow[fn]
	return known || strings.HasPrefix(fn, "mk.std.crypto.") || ctProbeFn(fn)
}

// ctProbeFn reports whether fn is a function of a probe's own module, main,
// which every constant-time test reads, since the probe hands it secrets.
// Main#report, the compiler's wrapper that calls Main and prints the error
// it returns, is left out: it formats that error alone, and Main is in every
// table, so a Main inlined into it fails as a missing function.
func ctProbeFn(fn string) bool {
	return strings.HasPrefix(fn, "mk.main.") && fn != "mk.main.Main#report"
}

// ctByBuild refines an allowance table per build: for a function whose
// builds read different counts, the
// count each build was read at, keyed by the name with instance hashes
// stripped. A build it does not name keeps the table's count, which is the
// most any build has. Without it one raise covered every build, so a build
// whose count was lower (AesGcm:Ctr under PGO at 4 and 5 against 11) could
// gain jumps up to the raise unread.
type ctByBuild map[string]map[string]int

// ctAllowed is fn's allowance in build: by's count for the build when it
// names one below the table's allowed, else allowed.
func ctAllowed(allowed int, by ctByBuild, fn, build string) int {
	if n, ok := by[cr2CtInstance.ReplaceAllString(fn, "")][build]; ok && n < allowed {
		return n
	}
	return allowed
}

// ctTables is each constant-time test's allowance lookup, in its order,
// with its absent list, its per-build table and its builds, for
// TestSTDCRYPTO_AllowanceTables.
var ctTables = []struct {
	test   string
	chain  []map[string]int
	absent map[string][]string
	unrec  map[string][]string
	by     ctByBuild
	back   ctBackTable
	builds []string
}{
	{"TestSTDCRYPTO_ConstantTimeDisassembly", []map[string]int{ctAllow}, ctAbsent, ctUnrecorded, nil, ctBack, []string{"release"}},
	{"TestSTDCRYPTO_TlsConstantTimeDisassembly", []map[string]int{cr2CtAllow, ctAllow}, cr2CtAbsent, cr2CtUnrecorded, cr2CtByBuild, cr2CtBack, []string{"kernel", "kernel-pgo", "masked", "masked-pgo"}},
	{"TestSTDCRYPTO_EcConstantTimeDisassembly", []map[string]int{cr2EcCtAllow, cr2CtAllow, ctAllow}, cr2EcCtAbsent, cr2EcCtUnrecorded, cr2EcCtByBuild, cr2EcCtBack, []string{"release", "release-pgo"}},
	{"TestSTDHASH_ConstantTimeDisassembly", []map[string]int{h2CtAllow}, h2CtAbsent, h2CtUnrecorded, h2CtByBuild, h2CtBack, []string{"kernel", "kernel-pgo", "masked", "masked-pgo"}},
}

// TestSTDCRYPTO_AllowanceTables checks the tables themselves, with no
// build: each per-build count names a build of its test
// and a function its lookup knows, and lowers that function's allowance
// (a raise needs a reading, recorded in the table); and no table keeps a
// nonzero allowance for a function its own test lists absent from every
// build, since that count covers no body the test
// reads: such a function is held at 0, so a build that holds it out of
// line again fails until its jumps are read.
func TestSTDCRYPTO_AllowanceTables(t *testing.T) {
	for _, tb := range ctTables {
		for fn, per := range tb.by {
			allowed, known := 0, false
			for _, m := range tb.chain {
				if allowed, known = m[fn]; known {
					break
				}
			}
			if !known {
				t.Errorf("%s: per-build count for %s, which its tables do not list", tb.test, fn)
				continue
			}
			for b, n := range per {
				if !slices.Contains(tb.builds, b) {
					t.Errorf("%s: per-build count for %s names build %q, not one of %v", tb.test, fn, b, tb.builds)
				}
				if n >= allowed {
					t.Errorf("%s: per-build count %d for %s in %s is not below its allowance %d: a raise goes in the table, with its reading", tb.test, n, fn, b, allowed)
				}
			}
		}
		// An unrecorded entry is an absent one with its builds.
		for fn, bs := range tb.unrec {
			for _, b := range bs {
				if !slices.Contains(tb.absent[fn], b) && !slices.Contains(tb.absent[fn], "*") {
					t.Errorf("%s: %s is unrecorded in %s but not listed absent there", tb.test, fn, b)
				}
			}
		}
		for fn, n := range tb.chain[0] {
			if n > 0 && slices.Contains(tb.absent[fn], "*") {
				t.Errorf("%s: %s is allowed %d jumps but listed absent from every build, so the count covers no body read here: hold it at 0", tb.test, fn, n)
			}
		}
		// A back-edge count names a function of the test's lookup and a
		// build of the test the function is not absent from, and is above
		// 0 (0 is every function's default).
		for fn, per := range tb.back {
			known := false
			for _, m := range tb.chain {
				_, ok := m[fn]
				known = known || ok
			}
			if !known {
				t.Errorf("%s: back-edge count for %s, which its tables do not list", tb.test, fn)
			}
			if len(per) == 0 {
				t.Errorf("%s: back-edge entry for %s has no builds: drop the empty entry", tb.test, fn)
			}
			for b, n := range per {
				switch {
				case !slices.Contains(tb.builds, b):
					t.Errorf("%s: back-edge count for %s names build %q, not one of %v", tb.test, fn, b, tb.builds)
				case slices.Contains(tb.absent[fn], b) || slices.Contains(tb.absent[fn], "*"):
					t.Errorf("%s: back-edge count for %s in %s, a build its absent list names", tb.test, fn, b)
				case n <= 0:
					t.Errorf("%s: back-edge count %d for %s in %s: a function with none is left out", tb.test, n, fn, b)
				}
			}
		}
	}
}

// cryptoObjdump finds llvm-objdump where mk looks for clang: MK_LLVM, the
// host's default LLVM directory, then PATH.
func cryptoObjdump() string {
	name := "llvm-objdump"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var dirs []string
	if d := os.Getenv("MK_LLVM"); d != "" {
		dirs = append(dirs, d, filepath.Join(d, "bin"))
	}
	if runtime.GOOS == "windows" {
		dirs = append(dirs, `C:\Program Files\LLVM\bin`)
	} else {
		dirs = append(dirs, "/usr/lib/llvm-21/bin", "/usr/local/bin", "/usr/bin")
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

type ctInsn struct {
	addr uint64
	op   string
	args string
	fn   string
}

var (
	ctFnRe   = regexp.MustCompile(`^[0-9a-f]+ <(.+)>:$`)
	ctInsnRe = regexp.MustCompile(`^\s*([0-9a-f]+):\s+(\S+)\s*(.*)$`)
	ctAddrRe = regexp.MustCompile(`\b0x([0-9a-f]+)\b`)
	ctRegRe  = regexp.MustCompile(`%[a-z0-9]+`)
)

// ctParse reads llvm-objdump -d --no-show-raw-insn output.
func ctParse(dis string) []ctInsn {
	var out []ctInsn
	fn := ""
	for _, l := range strings.Split(strings.ReplaceAll(dis, "\r\n", "\n"), "\n") {
		if m := ctFnRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			fn = m[1]
			continue
		}
		m := ctInsnRe.FindStringSubmatch(l)
		if m == nil || fn == "" {
			continue
		}
		a, err := strconv.ParseUint(m[1], 16, 64)
		if err != nil {
			continue
		}
		out = append(out, ctInsn{addr: a, op: m[2], args: strings.TrimSpace(m[3]), fn: fn})
	}
	return out
}

// ctClassify returns, per function, its "other" jumps (with the
// instruction before each) and its counts: [0] checks (a jump to or past a
// panic), [1] back edges, [2] the back edges whose flag-setter is not an
// induction compare. A
// check is found from the jump's target or its fall-through, following one
// unconditional jmp there (a panic block reached through a jump), and
// before the direction is looked at, so a backward jump to a panic block
// (CtrNi's Load checks) is a check, not a back edge. A back edge's
// flag-setter (the instruction right before it) is an induction compare
// when it steps a counter (ctStep: an add or sub of an immediate, an inc or
// a dec), or is a cmp or test of a counter outside any memory operand; a
// counter is a register the loop body (target to the edge) writes at least
// once and only with steps targeting the exact same register width (any
// write to an aliased subregister disqualifies it), and, when it is
// caller-saved, holds no call. Any other back edge may exit on data (a
// rejection loop's test of a drawn secret, a compare of a loaded byte), and
// is counted in [2] for its reading (ctClassifyBack lists them, ctBackTable
// bounds them).
func ctClassify(ins []ctInsn) (map[string][]string, map[string][3]int) {
	other, _, counts := ctClassifyBack(ins)
	return other, counts
}

var (
	ctImmRe = regexp.MustCompile(`^\$-?(0x[0-9a-f]+|[0-9]+),`)
	ctMemRe = regexp.MustCompile(`\([^)]*\)`)
	// ctCallerSaved are the registers a call may change (System V).
	ctCallerSaved = map[string]bool{"rax": true, "rcx": true, "rdx": true, "rsi": true, "rdi": true, "r8": true, "r9": true, "r10": true, "r11": true}
)

// ctOperands returns args with any trailing objdump comment (# ...) outside
// symbol names (<...>) removed.
func ctOperands(args string) string {
	depth := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case '#':
			if depth == 0 {
				return strings.TrimSpace(args[:i])
			}
		}
	}
	return strings.TrimSpace(args)
}

// ctUnwrap strips trailing comments and leading non-write instruction prefixes.
func ctUnwrap(x ctInsn) (string, string) {
	op, args := x.op, ctOperands(x.args)
	for {
		switch op {
		case "lock", "bnd", "notrack", "data16", "rex", "rex64":
			parts := strings.Fields(args)
			if len(parts) == 0 {
				return op, ""
			}
			op = parts[0]
			args = strings.TrimSpace(args[len(op):])
			continue
		}
		break
	}
	return op, args
}

var ctImplicitWrites = map[string][]string{
	"cqto": {"rdx"}, "cltd": {"rdx"}, "cwtd": {"rdx"},
	"cltq": {"rax"}, "cwtl": {"rax"}, "cbtw": {"rax"},
	"cpuid": {"rax", "rbx", "rcx", "rdx"},
	"rdtsc": {"rax", "rdx"}, "rdtscp": {"rax", "rdx", "rcx"},
	"xgetbv": {"rax", "rdx"}, "rdpmc": {"rax", "rdx"},
	"lahf": {"rax"}, "xlat": {"rax"}, "xlatb": {"rax"},
	"syscall": {"rax", "rcx", "r11"},
	"enter":   {"rbp", "rsp"}, "leave": {"rbp", "rsp"},
	"loop": {"rcx"}, "loope": {"rcx"}, "loopz": {"rcx"}, "loopne": {"rcx"}, "loopnz": {"rcx"},
}

// ctReg is the 64-bit register a register operand (with its %) is part of.
func ctReg(r string) string {
	r = strings.TrimPrefix(r, "%")
	switch r {
	case "al", "ah", "ax", "eax":
		return "rax"
	case "bl", "bh", "bx", "ebx":
		return "rbx"
	case "cl", "ch", "cx", "ecx":
		return "rcx"
	case "dl", "dh", "dx", "edx":
		return "rdx"
	case "sil", "si", "esi":
		return "rsi"
	case "dil", "di", "edi":
		return "rdi"
	case "bpl", "bp", "ebp":
		return "rbp"
	case "spl", "sp", "esp":
		return "rsp"
	}
	if len(r) > 1 && r[0] == 'r' && r[1] >= '0' && r[1] <= '9' {
		return strings.TrimRight(r, "dwb")
	}
	return r
}

// ctStep reports whether x steps a register by a constant (an add or sub
// of an immediate, an inc or a dec), returning its 64-bit register family
// and exact register operand.
func ctStep(x ctInsn) (base, raw string, ok bool) {
	op, args := ctUnwrap(x)
	fields := strings.Split(args, ",")
	dst := strings.TrimSpace(fields[len(fields)-1])
	if !strings.HasPrefix(dst, "%") {
		return "", "", false
	}
	switch {
	case strings.HasPrefix(op, "inc"), strings.HasPrefix(op, "dec"):
		return ctReg(dst), dst, true
	case strings.HasPrefix(op, "add"), strings.HasPrefix(op, "sub"):
		if ctImmRe.MatchString(args) {
			return ctReg(dst), dst, true
		}
	}
	return "", "", false
}

// ctWrites reports whether x may write r: as its last operand (not for a
// compare, a test, a jump or a push, whose last operand is read), or as
// one an instruction writes without naming it last (mul and div, the sign
// extensions, xchg, xadd, cmpxchg, mulx, cpuid, rdtsc/rdtscp, xgetbv,
// rdpmc, syscall, enter/leave, loop, the string instructions); a call is
// ctCounter's.
func ctWrites(x ctInsn, r string) bool {
	op, args := ctUnwrap(x)
	if regs, ok := ctImplicitWrites[op]; ok {
		return slices.Contains(regs, r)
	}
	fields := strings.Split(args, ",")
	named := func() bool {
		for _, f := range fields {
			if f = strings.TrimSpace(ctMemRe.ReplaceAllString(f, "")); strings.HasPrefix(f, "%") && ctReg(f) == r {
				return true
			}
		}
		return false
	}
	switch {
	case strings.HasPrefix(op, "xchg"), strings.HasPrefix(op, "xadd"), strings.HasPrefix(op, "mulx"):
		return named()
	case op == "cmpxchg8b", op == "cmpxchg16b":
		return r == "rax" || r == "rdx"
	case strings.HasPrefix(op, "cmpxchg"):
		return r == "rax" || named()
	case strings.HasPrefix(op, "rep"), strings.HasPrefix(op, "stos"), strings.HasPrefix(op, "lods"), strings.HasPrefix(op, "scas"),
		op == "movsb", op == "movsw", op == "movsl", op == "movsq",
		op == "cmpsb", op == "cmpsw", op == "cmpsl", op == "cmpsq",
		strings.Contains(args, "%es:"), strings.Contains(args, "%ds:"):
		return r == "rsi" || r == "rdi" || r == "rcx" || r == "rax"
	case (strings.HasPrefix(op, "mul") || strings.HasPrefix(op, "div") || strings.HasPrefix(op, "idiv") || strings.HasPrefix(op, "imul")) && len(fields) == 1:
		return r == "rax" || r == "rdx"
	case strings.HasPrefix(op, "push"):
		return r == "rsp"
	case strings.HasPrefix(op, "pop") && r == "rsp":
		return true
	}
	if strings.HasPrefix(op, "bt") && !strings.HasPrefix(op, "bts") && !strings.HasPrefix(op, "btr") && !strings.HasPrefix(op, "btc") {
		return false
	}
	for _, p := range []string{"cmp", "test", "ptest", "ucomis", "comis", "j", "nop"} {
		if strings.HasPrefix(op, p) && !strings.HasPrefix(op, "cmov") {
			return false
		}
	}
	dst := strings.TrimSpace(fields[len(fields)-1])
	return strings.HasPrefix(dst, "%") && ctReg(dst) == r
}

// ctCounter reports whether ins[from:to] writes base at least once and only
// with steps (ctStep) targeting raw, with no call when base is caller-saved.
func ctCounter(ins []ctInsn, base, raw string, from, to int) bool {
	steps := 0
	for k := from; k < to; k++ {
		y := ins[k]
		yOp, _ := ctUnwrap(y)
		if strings.HasPrefix(yOp, "call") {
			if ctCallerSaved[base] {
				return false
			}
			continue
		}
		if !ctWrites(y, base) {
			continue
		}
		if sBase, sRaw, ok := ctStep(y); !ok || sBase != base || sRaw != raw {
			return false
		}
		steps++
	}
	return steps > 0
}

// ctClassifyBack is ctClassify with, per function, the back edges counted
// in [2], each with its flag-setter (the instruction right before it, and
// before that the last instruction that is no mov or lea, which is the one
// whose flags the jump reads when only moves come between).
func ctClassifyBack(ins []ctInsn) (map[string][]string, map[string][]string, map[string][3]int) {
	at := map[uint64]int{}
	for i, x := range ins {
		at[x.addr] = i
	}
	targetOf := func(x ctInsn) (int, bool) {
		m := ctAddrRe.FindStringSubmatch(x.args)
		if m == nil {
			return 0, false
		}
		v, err := strconv.ParseUint(m[1], 16, 64)
		if err != nil {
			return 0, false
		}
		j, ok := at[v]
		return j, ok
	}
	var panics func(i int, follow bool) bool
	panics = func(i int, follow bool) bool {
		for k := i; k < len(ins) && k < i+5; k++ {
			switch op := ins[k].op; {
			case strings.HasPrefix(op, "call") && strings.Contains(ins[k].args, "Panic"):
				return true
			case strings.HasPrefix(op, "ret"):
				return false
			case strings.HasPrefix(op, "jmp"):
				j, ok := targetOf(ins[k])
				return follow && ok && panics(j, false)
			}
		}
		return false
	}
	induction := func(i, from int) bool {
		if i == 0 {
			return false
		}
		set := ins[i-1]
		if base, raw, ok := ctStep(set); ok {
			return ctCounter(ins, base, raw, from, i)
		}
		setOp, setArgs := ctUnwrap(set)
		if !strings.HasPrefix(setOp, "cmp") && !strings.HasPrefix(setOp, "test") {
			return false
		}
		for _, r := range ctRegRe.FindAllString(ctMemRe.ReplaceAllString(setArgs, ""), -1) {
			if ctCounter(ins, ctReg(r), r, from, i-1) {
				return true
			}
		}
		return false
	}
	// setter is the back edge's text: the instruction before it and, when
	// that is a mov or lea, the last one before them that is neither.
	setter := func(i int) string {
		var parts []string
		for k := i - 1; k >= 0 && k >= i-4 && ins[k].fn == ins[i].fn; k-- {
			parts = append([]string{ins[k].op + " " + ins[k].args}, parts...)
			if !strings.HasPrefix(ins[k].op, "mov") && !strings.HasPrefix(ins[k].op, "lea") {
				break
			}
		}
		return strings.Join(parts, "; ")
	}
	other := map[string][]string{}
	back := map[string][]string{}
	counts := map[string][3]int{}
	for i, x := range ins {
		if !strings.HasPrefix(x.op, "j") || strings.HasPrefix(x.op, "jmp") {
			continue
		}
		j, hasTarget := targetOf(x)
		c := counts[x.fn]
		switch {
		case panics(i+1, true) || hasTarget && panics(j, true):
			c[0]++
		case hasTarget && ins[j].addr <= x.addr:
			c[1]++
			if !induction(i, j) {
				c[2]++
				back[x.fn] = append(back[x.fn], setter(i)+"  |  "+x.op+" "+x.args)
			}
		default:
			prev := ""
			if i > 0 {
				prev = ins[i-1].op + " " + ins[i-1].args
			}
			other[x.fn] = append(other[x.fn], prev+"  |  "+x.op+" "+x.args)
		}
		counts[x.fn] = c
	}
	return other, back, counts
}

// ctBackTable bounds the back edges counted in ctClassify's [2] of each
// function a constant-time test reads, by build: function (instance hash
// stripped) to build to the count the build has, every edge read (does the
// loop exit on a secret?). A function or build it does not name has none.
// The bound is exact (ctBackProblems): a build with more fails with the
// edges to read, and one with fewer fails until its count is lowered, so
// no count stands above what its build reaches.
type ctBackTable map[string]map[string]int

// ctBackProblems is the rule of ctBackTable for one build: back holds each
// function's [2] edges (ctClassifyBack) and read the functions the test
// reads in the build. A function above its count fails with its edges; a
// count no instance of the function reaches fails, as does a count for a
// function the build holds no read body of.
func ctBackProblems(table string, tb ctBackTable, build string, back map[string][]string, read []string) []string {
	var problems []string
	most := map[string]int{}
	for _, fn := range read {
		name := cr2CtInstance.ReplaceAllString(fn, "")
		allowed := tb[name][build]
		n := len(back[fn])
		if n > allowed {
			problems = append(problems, fmt.Sprintf("%s (%s build) has %d back edges whose compare is not an induction, %d allowed in %s; read each (does the loop exit on a secret?) and record the reading:\n    %s",
				fn, build, n, allowed, table, strings.Join(back[fn], "\n    ")))
		}
		if m, ok := most[name]; !ok || n > m {
			most[name] = n
		}
	}
	for name, per := range tb {
		allowed, ok := per[build]
		if !ok {
			continue
		}
		m, held := most[name]
		switch {
		case !held:
			problems = append(problems, fmt.Sprintf("%s: %s allows %d back edges in the %s build, which holds no body of it that the test reads: drop the build from its entry", name, table, allowed, build))
		case m < allowed:
			problems = append(problems, fmt.Sprintf("%s (%s build) has %d back edges whose compare is not an induction, %s allows %d: lower the count to the build's", name, build, m, table, allowed))
		}
	}
	sort.Strings(problems)
	return problems
}

// ctBackCheck fails t with ctBackProblems' problems.
func ctBackCheck(t *testing.T, table string, tb ctBackTable, build string, back map[string][]string, read []string) {
	t.Helper()
	for _, p := range ctBackProblems(table, tb, build, back, read) {
		t.Error(p)
	}
}

// TestSTDCRYPTO_ConstantTimeDisassembly builds, for x86_64-linux in release
// (built only, never run, so no WSL), a program that reaches every secret
// path, and disassembles it with llvm-objdump. Every conditional jump in a
// function on a secret path is classified (ctClassify): "check" when it or
// its fall-through reaches a Panic call within five instructions, through
// at most one jmp (a bounds, overflow or shift check, whose operands in
// these functions are lengths, constants and loop counters), "back" when it
// jumps backwards (a loop; one whose compare is no induction is counted
// apart), and "other" otherwise. The source has no `if` on a secret
// (crypto.mk's rules), so a new "other" jump is either a new length test or
// LLVM turning a mask select into a branch, and a human must read it: the
// test fails with the instructions, and the reading goes into the
// allowance's comment. It skips without llvm-objdump.
func TestSTDCRYPTO_ConstantTimeDisassembly(t *testing.T) {
	objdump := cryptoObjdump()
	if objdump == "" {
		t.Skip("needs llvm-objdump (LLVM 21)")
	}
	// --debug keeps the symbol table: a release ELF link without it passes
	// --strip-all, and the disassembly is read by symbol. But --debug turns
	// off the leaf inliner (lower.InlineLeaves), so a
	// --debug build can differ from the shipped code.
	// TestSTDCRYPTO_ReadBodiesAreShipped compares the bodies across all
	// eleven builds and requires matching conditional jumps if the masked
	// instructions differ.
	exe, out, status := cryptoTry(t, cryptoCTProbe, "--release", "--debug", "-target", "x86_64-linux")
	if status != 0 {
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		t.Fatalf("mk build --release --debug -target x86_64-linux: exit %d\n%s", status, out)
	}
	dis, err := exec.Command(objdump, "-d", "--no-show-raw-insn", exe).Output()
	if err != nil {
		t.Fatalf("%s -d: %v", objdump, err)
	}
	ins := ctParse(string(dis))
	if len(ins) == 0 {
		t.Fatal("llvm-objdump printed no instruction")
	}
	other, back, counts := ctClassifyBack(ins)
	seen := map[string]bool{}
	for _, x := range ins {
		seen[x.fn] = true
	}
	var names, read []string
	for fn := range seen {
		names = append(names, fn)
	}
	sort.Strings(names)
	for _, fn := range names {
		allowed, known := ctAllow[fn]
		switch {
		case ctPublic[fn]:
			continue
		case !known && (strings.HasPrefix(fn, "mk.std.crypto.") || ctProbeFn(fn)):
			t.Errorf("%s is a new std.crypto or probe function: read its jumps (%d other: %q) and add it to ctAllow or ctPublic", fn, len(other[fn]), other[fn])
			continue
		case !known:
			continue
		}
		if n := len(other[fn]); n > allowed {
			t.Errorf("%s has %d conditional jumps that neither guard a panic nor close a loop, %d allowed; read each (is it a secret?) and record the reading:\n    %s",
				fn, n, allowed, strings.Join(other[fn], "\n    "))
		}
		read = append(read, fn)
		t.Logf("%s: %d checks, %d loop edges (%d on a compare that is not an induction, %d allowed), %d other (%d allowed)", fn, counts[fn][0], counts[fn][1], counts[fn][2], ctBack[cr2CtInstance.ReplaceAllString(fn, "")]["release"], len(other[fn]), allowed)
	}
	ctBackCheck(t, "ctBack", ctBack, "release", back, read)
	into := ctInlined(t, exe)
	ctRequirePresent(t, "ctAllow", "release", ctAllow, seen, ctAbsent, ctUnrecorded, into, func(fn string) bool { return ctChecked(fn) && !ctPublic[fn] })
	for fn := range ctUnrecorded {
		publicCopy := false
		for holder := range into[fn] {
			publicCopy = publicCopy || seen[holder] && ctPublic[holder]
		}
		if !publicCopy {
			t.Errorf("%s has no recorded inlined copy in a public function", fn)
		}
	}
	if !seen["mk.main.Main"] {
		t.Error("the probe's Main is not in the disassembly: the symbol names changed, so nothing above was checked")
	}
}

// TestSTDCRYPTO_ConstantTimeClassifier: the classifier on hand-written
// listings, so the check above cannot pass by classifying nothing: a jump
// to a panic, forward, backward or through one jmp, is a check; a backward
// one otherwise a loop edge, counted apart when its compare is no
// induction; a forward jump over a mask (what a select turned into a
// branch looks like) is "other"; and the scan for a panic stops at a jmp
// it does not follow.
func TestSTDCRYPTO_ConstantTimeClassifier(t *testing.T) {
	const dis = `
0000000000001000 <mk.std.crypto.FeMul>:
    1000:      	cmpq	$0x8, %rax
    1004:      	jae	0x1030 <mk.std.crypto.FeMul+0x30>
    1006:      	decq	%rcx
    1009:      	jne	0x1000 <mk.std.crypto.FeMul>
    100b:      	testq	%rdx, %rdx
    100e:      	je	0x1020 <mk.std.crypto.FeMul+0x20>
    1010:      	movq	%rsi, %rax
    1020:      	retq
    1030:      	movl	$0x2, %edi
    1035:      	callq	0x9000 <mk.std.rt.PanicAt>
`
	other, counts := ctClassify(ctParse(dis))
	if c := counts["mk.std.crypto.FeMul"]; c != [3]int{1, 1, 0} || len(other["mk.std.crypto.FeMul"]) != 1 {
		t.Fatalf("got checks and loop edges %v, other %q; want [1 1 0] and the je", c, other["mk.std.crypto.FeMul"])
	}
	if !strings.Contains(other["mk.std.crypto.FeMul"][0], "je") || !strings.Contains(other["mk.std.crypto.FeMul"][0], "testq") {
		t.Fatalf("other jump %q: want the je and the test before it", other["mk.std.crypto.FeMul"][0])
	}
	// A backward jump to a panic block and a
	// jump to a jmp that reaches one are checks; a back edge on a compare of
	// a register the loop does not count is counted apart, one on a
	// counted register is not.
	const dis2 = `
0000000000002000 <mk.std.crypto.Ctr>:
    2000:      	movl	$0x3, %edi
    2005:      	callq	0x9000 <mk.std.rt.PanicAt>
    200a:      	cmpq	%rcx, %rax
    200d:      	jae	0x2000 <mk.std.crypto.Ctr>
    2010:      	cmpq	%rdx, %rax
    2013:      	jb	0x2040 <mk.std.crypto.Ctr+0x40>
    2015:      	movq	(%rsi), %r8
    2018:      	addq	$0x1, %rax
    201c:      	cmpq	%rdx, %rax
    201f:      	jne	0x2015 <mk.std.crypto.Ctr+0x15>
    2021:      	movq	(%rsi), %r9
    2024:      	cmpq	%r9, %r8
    2027:      	jne	0x2021 <mk.std.crypto.Ctr+0x21>
    2029:      	retq
    2040:      	jmp	0x2000 <mk.std.crypto.Ctr>
`
	other2, counts2 := ctClassify(ctParse(dis2))
	if c := counts2["mk.std.crypto.Ctr"]; c != [3]int{2, 2, 1} || len(other2["mk.std.crypto.Ctr"]) != 0 {
		t.Fatalf("got counts %v, other %q; want 2 checks (the backward jae, the jb through a jmp), 2 back edges, 1 of them on a data compare, and no other", c, other2["mk.std.crypto.Ctr"])
	}
	// The scan stops at a jmp: a forward jump to `mov; jmp` back into the
	// body is "other", though a panic block is laid out right after the
	// jmp (a scan running on past the jmp took it for a check).
	const dis3 = `
0000000000003000 <mk.std.crypto.Tag>:
    3000:      	testq	%rax, %rax
    3003:      	je	0x3020 <mk.std.crypto.Tag+0x20>
    3005:      	movq	%rsi, %rax
    3008:      	retq
    3020:      	movq	%rdi, %rax
    3023:      	jmp	0x3005 <mk.std.crypto.Tag+0x5>
    3025:      	movl	$0x2, %edi
    302a:      	callq	0x9000 <mk.std.rt.PanicAt>
`
	other3, counts3 := ctClassify(ctParse(dis3))
	if c := counts3["mk.std.crypto.Tag"]; c[0] != 0 || len(other3["mk.std.crypto.Tag"]) != 1 {
		t.Fatalf("got counts %v, other %q; want no check and the je as other: the scan stops at the jmp", c, other3["mk.std.crypto.Tag"])
	}
	// A memory-operand compare referencing a stepped index, a caller-saved
	// counter across a call, a stepped register also overwritten in the loop,
	// and a memory-operand dec all go into [2] rather than [1].
	const dis4 = `
0000000000004000 <mk.std.crypto.SecretLoop>:
    4000:      	addq	$0x1, %rax
    4004:      	cmpb	$0x0, (%rsi,%rax)
    4008:      	jne	0x4000 <mk.std.crypto.SecretLoop>
    4010:      	callq	0x9100 <mk.std.crypto.Helper>
    4015:      	addq	$0x1, %rcx
    4019:      	cmpq	%rdx, %rcx
    401c:      	jb	0x4010 <mk.std.crypto.SecretLoop+0x10>
    4020:      	movq	(%rsi), %rbx
    4023:      	addq	$0x1, %rbx
    4027:      	cmpq	%rdx, %rbx
    402a:      	jb	0x4020 <mk.std.crypto.SecretLoop+0x20>
    4030:      	decq	-0x8(%rsp)
    4035:      	jne	0x4030 <mk.std.crypto.SecretLoop+0x30>
    4037:      	retq
`
	_, back4, counts4 := ctClassifyBack(ctParse(dis4))
	if c := counts4["mk.std.crypto.SecretLoop"]; c != [3]int{0, 4, 4} || len(back4["mk.std.crypto.SecretLoop"]) != 4 {
		t.Fatalf("got counts %v, back %q; want [0 4 4] and 4 non-induction back edges", c, back4["mk.std.crypto.SecretLoop"])
	}
	// Trailing objdump comments on writes, mismatched subregister widths
	// between step and compare, prefixed atomic writes, and implicit
	// register writes must disqualify a counter and classify in [2], while
	// trailing comments on a clean step stay in [1].
	const dis5 = `
0000000000005000 <mk.std.crypto.CounterWidths>:
    5000:      	addq	$0x1, %r8 # imm = 0x1
    5004:      	cmpq	%rdx, %r8
    5007:      	jb	0x5000 <mk.std.crypto.CounterWidths>
    5010:      	movq	0x10(%rip), %rax # 0x9200 <mk.main.Main#report>
    5017:      	addq	$0x1, %rax
    501b:      	cmpq	%rdx, %rax
    501e:      	jb	0x5010 <mk.std.crypto.CounterWidths+0x10>
    5020:      	andq	$0x7f, %rbx # imm = 0x7F
    5024:      	addq	$0x1, %rbx
    5028:      	cmpq	%rdx, %rbx
    502b:      	jb	0x5020 <mk.std.crypto.CounterWidths+0x20>
    5030:      	addb	$0x1, %al
    5032:      	cmpq	%rdx, %rax
    5035:      	jb	0x5030 <mk.std.crypto.CounterWidths+0x30>
    5040:      	addb	$0x1, %al
    5042:      	cmpb	%dl, %ah
    5044:      	jne	0x5040 <mk.std.crypto.CounterWidths+0x40>
    5050:      	addq	$0x1, %rax
    5054:      	cmpb	%al, %dl
    5056:      	jne	0x5050 <mk.std.crypto.CounterWidths+0x50>
    5060:      	addl	$0x1, %eax
    5063:      	lock	xaddl	%eax, (%rdi)
    5067:      	testl	%eax, %eax
    5069:      	jne	0x5060 <mk.std.crypto.CounterWidths+0x60>
    5070:      	addq	$0x1, %rcx
    5074:      	rdtscp
    5077:      	cmpq	%rdx, %rcx
    507a:      	jb	0x5070 <mk.std.crypto.CounterWidths+0x70>
    5080:      	addq	$0x1, %rdx
    5084:      	cmpxchg8b	(%rdi)
    5087:      	cmpq	%rbx, %rdx
    508a:      	jb	0x5080 <mk.std.crypto.CounterWidths+0x80>
    5090:      	addq	$0x1, %rax
    5094:      	cbtw
    5096:      	cmpq	%rbx, %rax
    5099:      	jb	0x5090 <mk.std.crypto.CounterWidths+0x90>
    50a0:      	addq	$0x1, %rdi
    50a4:      	movsb
    50a5:      	cmpq	%rbx, %rdi
    50a8:      	jb	0x50a0 <mk.std.crypto.CounterWidths+0xa0>
    50aa:      	retq
`
	_, back5, counts5 := ctClassifyBack(ctParse(dis5))
	if c := counts5["mk.std.crypto.CounterWidths"]; c != [3]int{0, 11, 10} || len(back5["mk.std.crypto.CounterWidths"]) != 10 {
		t.Fatalf("got counts %v, back %q; want [0 11 10] and 10 non-induction back edges", c, back5["mk.std.crypto.CounterWidths"])
	}
}

// TestSTDCRYPTO_BackEdgeRule verifies ctBackProblems: an exact match passes,
// a function above its count (or above the default 0) fails, a function below
// its count fails, and an entry for a build with no read body fails.
func TestSTDCRYPTO_BackEdgeRule(t *testing.T) {
	tb := ctBackTable{
		"mk.std.crypto.Exact":   {"b": 2},
		"mk.std.crypto.TooHigh": {"b": 3},
		"mk.std.crypto.Missing": {"b": 1},
		"mk.std.crypto.Other":   {"other": 1},
	}
	back := map[string][]string{
		"mk.std.crypto.Exact":   {"cmpq %rax, %rbx  |  jb 0x10", "testb %al, %al  |  jne 0x20"},
		"mk.std.crypto.TooHigh": {"cmpq %rax, %rbx  |  jb 0x30"},
		"mk.std.crypto.Unread":  {"cmpb $0x0, (%rsi)  |  jne 0x40"},
	}
	read := []string{"mk.std.crypto.Exact", "mk.std.crypto.TooHigh", "mk.std.crypto.Unread"}
	problems := ctBackProblems("tb", tb, "b", back, read)
	if len(problems) != 3 {
		t.Fatalf("got %d problems, want 3:\n%s", len(problems), strings.Join(problems, "\n"))
	}
	if !strings.Contains(problems[0], "mk.std.crypto.Missing:") || !strings.Contains(problems[0], "drop the build") {
		t.Errorf("problem[0] = %q; want Missing drop-build error", problems[0])
	}
	if !strings.Contains(problems[1], "mk.std.crypto.TooHigh (b build) has 1 back edges") || !strings.Contains(problems[1], "allows 3") {
		t.Errorf("problem[1] = %q; want TooHigh lower-count error", problems[1])
	}
	if !strings.Contains(problems[2], "mk.std.crypto.Unread (b build) has 1 back edges") || !strings.Contains(problems[2], "0 allowed") {
		t.Errorf("problem[2] = %q; want Unread above-default error", problems[2])
	}
	okTb := ctBackTable{
		"mk.std.crypto.Exact":  {"b": 2},
		"mk.std.crypto.Unread": {"b": 1},
		"mk.std.crypto.Other":  {"other": 1},
	}
	if got := ctBackProblems("tb", okTb, "b", back, []string{"mk.std.crypto.Exact", "mk.std.crypto.Unread"}); len(okTb) == 0 || len(got) != 0 {
		t.Fatalf("exact table gave problems: %v", got)
	}
}

// TestSTDCRYPTO_PresenceRule plants each case of ctPresence: a listed
// function held out of line passes, and fails when the absent or the
// unrecorded list names its build; an absent one passes when a function the
// test reads holds it inlined and fails when only an unread one does; an
// unrecorded one fails when a read function holds it; and a listed function
// missing from the build, which no list names, fails.
func TestSTDCRYPTO_PresenceRule(t *testing.T) {
	allow := map[string]int{"held": 0, "heldAbsent": 0, "heldUnrecorded": 0, "inlined": 0, "inlinedUnread": 0, "unrecorded": 0, "unrecordedRead": 0, "gone": 0}
	seen := map[string]bool{"held": true, "heldAbsent": true, "heldUnrecorded": true, "reader": true, "public": true}
	absent := map[string][]string{"heldAbsent": {"b"}, "heldUnrecorded": {"*"}, "inlined": {"b"}, "inlinedUnread": {"*"}, "unrecorded": {"b"}, "unrecordedRead": {"b"}}
	unrecorded := map[string][]string{"heldUnrecorded": {"b"}, "unrecorded": {"b"}, "unrecordedRead": {"*"}}
	into := ctInlinedInto{"inlined": {"reader": true}, "inlinedUnread": {"public": true}, "unrecordedRead": {"reader": true}}
	read := func(fn string) bool { return fn != "public" }
	problems, notes := ctPresence("allow", "b", allow, seen, absent, unrecorded, into, read)
	var got []string
	for _, p := range problems {
		got = append(got, strings.TrimSuffix(strings.Fields(p)[0], ":"))
	}
	if want := []string{"gone", "heldAbsent", "heldUnrecorded", "inlinedUnread", "unrecordedRead"}; !slices.Equal(got, want) {
		t.Errorf("problems for %v, want %v:\n%s", got, want, strings.Join(problems, "\n"))
	}
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "inlined: ") {
		t.Errorf("notes %q, want the one for inlined", notes)
	}
}

// TestSTDCRYPTO_ProbeFunctionsRead: each constant-time test reads every
// function of its probe's module but Main's error reporter, so a probe
// function the optimizer keeps out of line, as the EC probe's ParseSign
// with the parsed secret, is read and not skipped.
func TestSTDCRYPTO_ProbeFunctionsRead(t *testing.T) {
	tests := []struct {
		name    string
		checked func(string) bool
	}{{"ctChecked", ctChecked}, {"cr2CtChecked", cr2CtChecked}, {"cr2EcCtChecked", cr2EcCtChecked}, {"h2CtChecked", h2CtChecked}}
	for _, tc := range tests {
		for _, fn := range []string{"mk.main.Main", "mk.main.ParseSign", "mk.main.Pick.0123456789abcdef"} {
			if !tc.checked(fn) {
				t.Errorf("%s(%q) is false: the test skips a function of its probe", tc.name, fn)
			}
		}
		if tc.checked("mk.main.Main#report") {
			t.Errorf("%s reads mk.main.Main#report, which only formats Main's error", tc.name)
		}
	}
}

// ctUnrecorded are the helpers inlined only into public functions:
// PtFromBytes decodes the public key, and PtDoubleMult uses only the
// public key and scalars from verification. The test checks for those
// copies and requires a new reading if either scope changes.
var ctUnrecorded = map[string][]string{
	"mk.std.crypto.FePow22523":  {"*"},
	"mk.std.crypto.FeSqrtRatio": {"*"},
	"mk.std.crypto.P1Sub":       {"*"},
	"mk.std.crypto.P1SubAffine": {"*"},
}
