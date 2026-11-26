// Why: a conditional jump on a secret leaks it through timing; the source has
// no `if` on a secret, but LLVM can still turn a mask select into a branch.
// What: the classifier ctParse and ctClassify, the allowance tables with
// their presence rule (ctRequirePresent, ctPresence) and the probe-function
// rule (ctProbeFn), and TestSTDCRYPTO_ConstantTimeDisassembly over
// std.crypto's secret paths.
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
	builds []string
}{
	{"TestSTDCRYPTO_ConstantTimeDisassembly", []map[string]int{ctAllow}, ctAbsent, ctUnrecorded, nil, []string{"release"}},
	{"TestSTDCRYPTO_TlsConstantTimeDisassembly", []map[string]int{cr2CtAllow, ctAllow}, cr2CtAbsent, cr2CtUnrecorded, cr2CtByBuild, []string{"kernel", "kernel-pgo", "masked", "masked-pgo"}},
	{"TestSTDCRYPTO_EcConstantTimeDisassembly", []map[string]int{cr2EcCtAllow, cr2CtAllow, ctAllow}, cr2EcCtAbsent, cr2EcCtUnrecorded, cr2EcCtByBuild, []string{"release", "release-pgo"}},
	{"TestSTDHASH_ConstantTimeDisassembly", []map[string]int{h2CtAllow}, h2CtAbsent, h2CtUnrecorded, h2CtByBuild, []string{"kernel", "kernel-pgo", "masked", "masked-pgo"}},
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
// flag-setter is an induction compare when it is an add, sub, inc or dec,
// or a cmp or test of a register the loop body (target to the edge) adds
// to, subtracts from, increments, decrements or loads with lea; any other
// back edge may exit on data, and is counted in [2] for its reading.
func ctClassify(ins []ctInsn) (map[string][]string, map[string][3]int) {
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
		for _, p := range []string{"add", "sub", "inc", "dec"} {
			if strings.HasPrefix(set.op, p) {
				return true
			}
		}
		if !strings.HasPrefix(set.op, "cmp") && !strings.HasPrefix(set.op, "test") {
			return false
		}
		for _, r := range ctRegRe.FindAllString(set.args, -1) {
			for k := from; k < i-1; k++ {
				y := ins[k]
				fields := strings.Split(y.args, ",")
				dst := strings.TrimSpace(fields[len(fields)-1])
				if dst != r {
					continue
				}
				for _, p := range []string{"add", "sub", "inc", "dec", "lea"} {
					if strings.HasPrefix(y.op, p) {
						return true
					}
				}
			}
		}
		return false
	}
	other := map[string][]string{}
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
	return other, counts
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
	other, counts := ctClassify(ins)
	seen := map[string]bool{}
	for _, x := range ins {
		seen[x.fn] = true
	}
	var names []string
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
		t.Logf("%s: %d checks, %d loop edges (%d on a compare that is not an induction), %d other (%d allowed)", fn, counts[fn][0], counts[fn][1], counts[fn][2], len(other[fn]), allowed)
	}
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
