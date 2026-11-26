// Why: std.hash's secret paths (HMAC keys, messages) must not branch on
// secret data, on the portable or the instruction paths, with or without PGO.
// What: TestSTDHASH_ConstantTimeDisassembly and its tables h2CtAllow,
// h2CtByBuild, h2CtMust and h2CtAbsent; h2CtProfile, the PGO legs' training
// run in a fixed environment (h2CtTrain), which
// TestSTDHASH_PgoTrainingIgnoresEnvironment checks.
// How: four x86_64-linux builds of one probe, every conditional jump of
// every std.hash function classified by ctClassify against the tables.

package std

import (
	"encoding/binary"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// h2CtProbe reaches every SHA function and state on lengths taken from the
// argument count, so nothing folds to a constant: HMAC with a short and a
// long key (one hashed first), both states fed in two pieces, the one-shots.
const h2CtProbe = `import std.crypto
import std.hash

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    n := sys.args.Len()
    var key: [80]u8 = [7; 80]
    key[0] = Trunc[u8](n)
    var msg: [300]u8 = [3; 300]
    msg[1] = Trunc[u8](n)
    m := n % 7
    h := crypto.Hmac256(key[0..32 + m], msg[0..100 + m])
    hl := crypto.Hmac256(key[0..70 + m], msg[..])
    var s1 := hash.Sha1State.New()
    s1.Bytes(key[0..10 + m])
    s1.Bytes(msg[..])
    d1 := s1.Sum()
    o1 := hash.Sha1(msg[0..200 + m])
    var s3 := hash.Sha384State.New()
    s3.Bytes(key[0..10 + m])
    s3.Bytes(msg[..])
    d3 := s3.Sum()
    o3 := hash.Sha384(msg[0..200 + m])
    var s2 := hash.Sha256State.New()
    s2.Bytes(key[0..10 + m])
    s2.Bytes(msg[..])
    d2 := s2.Sum()
    o5 := hash.Sha512(msg[0..150 + m])
    try Print(mut out, "{a} {b} {c} {d} {e} {f} {g} {i}\n", (a: h[0], b: hl[1], c: d1[2], d: o1[3], e: d3[4], f: o3[5], g: d2[6], i: o5[7]))
}
`

// h2CtAllow is the reading (mk and std of the tree, LLVM 21.1.8, the four
// builds of TestSTDHASH_ConstantTimeDisassembly, PGO from h2CtTrain's
// profile): for each function, the most "other" jumps any build has, every
// one read in llvm-objdump's listing as a test of a length, a loop bound, a
// CPU feature, an error tag or a printed value, never of the message, the
// key or the state.
var h2CtAllow = map[string]int{
	// Main: the small inlined SHA state methods add no branches.
	// Without PGO, Print's writer path is inlined, with write error tags,
	// number formatting, remaining capacity and empty-buffer tests. With
	// PGO, the 15 write error tags and the eight separators' inlined
	// Writer.Bytes pairs (31); kernel-pgo also inlines Sha384State's Bytes
	// twice (Sha512State's), adding the buffered length's two tests, the
	// rest's length, the kernel's answer and CopyBytes' zero length (36).
	// The buffer's fill moves only with the text already printed.
	"mk.main.Main":          44,
	"mk.std.crypto.Hmac256": 1, // the key length above 64 (cmpq $0x41) and the data length zero; no build reads more than 1
	"mk.std.hash.Sha1":      0, // PGO builds only: the one-shot out of line
	// Sha1Blocks: the SHA-1 kernel's guard,
	// inlined once here: simd.Cpu()'s cache not yet filled, the sha bit;
	// then the portable loop's entry: fewer than 64 bytes (cmpq $0x40).
	"mk.std.hash.Sha1Blocks": 3,
	// Bytes: the length zero (jle / jg), the buffered length zero, the
	// buffer not yet full (cmpq $0x40), no whole block in the rest (andq
	// $-0x40), the rest's length (subq; jbe). The same five in each state.
	// Sha256State's under kernel-pgo, 7: the buffered length, the whole
	// blocks, the rest's length, CopyBytes' zero length, and the SHA-256
	// kernel's guard inlined (simd.Cpu()'s cache test and the sha bit, then
	// the second call's cache test).
	"mk.std.hash.Sha1State:Bytes":   5,
	"mk.std.hash.Sha1State:New":     0,
	"mk.std.hash.Sha1State:Sum":     1, // the buffered length zero (the copy's skip)
	"mk.std.hash.Sha256":            0,
	"mk.std.hash.Sha256State:Bytes": 7,
	"mk.std.hash.Sha256State:New":   0,
	"mk.std.hash.Sha256State:Sum":   4, // the buffered length zero; kernel-pgo: the SHA-256 kernel's guard inlined (the cache test and the sha bit, the cache test)
	// Sha256Compress (the portable loop, once std-crypto's Sha256Blocks):
	// the kernel's guard, inlined once here: simd.Cpu()'s cache not
	// yet filled (testq %rax; je to the detect call), the sha bit
	// (testl $0x100000; jne to ShaNi), the second Cpu() call's cache test
	// (for sha2) before the portable loop; then the loop's entry (cmpq
	// $0x40). Masked: the second cache test and the entry only. Under
	// kernel-pgo the guard is the kernel's Sha256Blocks, out of line, and
	// this tests its answer only.
	"mk.std.hash.Sha256Compress":    4,
	"mk.std.hash.Sha384":            0,
	"mk.std.hash.Sha384State:Bytes": 0, // a call of Sha512State:Bytes
	"mk.std.hash.Sha384State:New":   0,
	"mk.std.hash.Sha384State:Sum":   0, // a call of Sha512State:Sum and the 48-byte copy
	"mk.std.hash.Sha512":            0,
	// Sha512Blocks: the SHA-512 kernel's guard, inlined
	// once here: simd.Cpu()'s cache not yet filled (testq %rax; je to the
	// detect call), the avx2 and bmi2 bits (notl; testl $0x163f; jne to the
	// portable loop), then the loop's entry: fewer than 128 bytes (cmpq
	// $0x80). Under kernel-pgo the guard
	// is the kernel's Sha512Blocks, out of line, and this tests its answer
	// only.
	"mk.std.hash.Sha512Blocks":      3,
	"mk.std.hash.Sha512State:Bytes": 5, // as Sha256State's, on 128-byte blocks; kernel-pgo: the inlined Sha512Blocks' kernel answer in place of the buffer-full test
	"mk.std.hash.Sha512State:Sum":   4, // the buffered length zero; PGO: the buffered length above 111 (the second padding block); kernel-pgo: the SHA-512 kernel's guard inlined (the cache test, the avx2 and bmi2 bits)
	"mk.std.simd.kernel.ShaNi":      1, // the block count zero (andq of the length with -64; je)
	"mk.std.simd.kernel.Sha1Ni":     1, // SHA-1's: the block count zero, as ShaNi's
	// Sha512Avx2 (SHA-512's kernel): the block count zero (andq
	// of the length with -128; je) and the round group counter (cmpq $0x8;
	// jae: the last two groups of eight rounds compute no schedule words).
	"mk.std.simd.kernel.Sha512Avx2": 2,
	// The kernels' guards, out of line under kernel-pgo: simd.Cpu()'s
	// cache test and the sha bit, then the cache test before sha2; and
	// the cache test and the avx2 and bmi2 bits.
	"mk.std.simd.kernel.Sha256Blocks": 3,
	"mk.std.simd.kernel.Sha512Blocks": 2,
}

// h2CtByBuild is this test's per-build counts (ctByBuild): each function
// whose four builds read different counts, at the count each build has
// (the test's -v log; SHA-1's and SHA-512's Bytes under PGO gain their
// whole-block count test, len & -64 or -128 <= 0, read), every one at or
// below the reading above.
var h2CtByBuild = ctByBuild{
	"mk.main.Main":                  {"kernel-pgo": 36, "masked-pgo": 31},
	"mk.std.hash.Sha1State:Bytes":   {"kernel-pgo": 4, "masked-pgo": 4},
	"mk.std.hash.Sha256Compress":    {"kernel-pgo": 1, "masked": 2, "masked-pgo": 2},
	"mk.std.hash.Sha256State:Bytes": {"kernel": 5, "masked": 5, "masked-pgo": 4},
	"mk.std.hash.Sha256State:Sum":   {"kernel": 1, "masked": 1, "masked-pgo": 1},
	"mk.std.hash.Sha512Blocks":      {"kernel-pgo": 1},
	"mk.std.hash.Sha512State:Bytes": {"masked-pgo": 4},
	"mk.std.hash.Sha512State:Sum":   {"kernel": 1, "masked": 1, "masked-pgo": 2},
}

// h2CtBack is this test's ctBackTable: exact per-build counts of
// non-induction back edges (ctClassifyBack [2]), each read as public (lengths,
// fixed round counts, CPU feature flags, or Writer.Bytes buffer capacity).
var h2CtBack = ctBackTable{
	"mk.main.Main":                  {"kernel": 2, "kernel-pgo": 5, "masked": 2},                  // Writer.Bytes buffer capacity; kernel-pgo adds inlined Sha512State:Bytes/Sha512Blocks length/round loops
	"mk.std.hash.Sha1Blocks":        {"kernel": 1, "masked": 1},                                   // simd.Cpu().Has("sha") feature test
	"mk.std.hash.Sha1State:Bytes":   {"kernel": 1, "kernel-pgo": 1, "masked": 1, "masked-pgo": 1}, // self.n < 64 / i < n on public lengths
	"mk.std.hash.Sha256Compress":    {"kernel": 2, "kernel-pgo": 3, "masked": 1, "masked-pgo": 1}, // block loop (n - i >= 64), fixed 8 round groups, and Sha256Blocks CPU feature test
	"mk.std.hash.Sha256State:Bytes": {"kernel": 1, "kernel-pgo": 4, "masked": 1, "masked-pgo": 1}, // public lengths; kernel-pgo adds inlined Sha256Compress length and round loops
	"mk.std.hash.Sha256State:Sum":   {"kernel-pgo": 2},                                            // inlined Sha256Compress length and round loops
	"mk.std.hash.Sha512Blocks":      {"kernel": 2, "kernel-pgo": 3, "masked": 2, "masked-pgo": 1}, // Sha512Blocks CPU feature test, 128-byte block loop, and fixed 10 round groups
	"mk.std.hash.Sha512State:Bytes": {"kernel": 1, "kernel-pgo": 4, "masked": 1, "masked-pgo": 1}, // public lengths; kernel-pgo adds inlined Sha512Blocks length and round loops
	"mk.std.hash.Sha512State:Sum":   {"kernel-pgo": 2},                                            // inlined Sha512Blocks length and round loops
	"mk.std.simd.kernel.Sha1Ni":     {"kernel": 1, "kernel-pgo": 1, "masked": 1, "masked-pgo": 1}, // block loop i < n on public length
	"mk.std.simd.kernel.Sha512Avx2": {"kernel": 1, "kernel-pgo": 1, "masked": 1, "masked-pgo": 1}, // fixed 10 round groups or block loop i < n on public length
	"mk.std.simd.kernel.ShaNi":      {"kernel": 1, "kernel-pgo": 1},                               // block loop i < n on public length
}

// h2CtMust are the functions every build must hold out of line, so the
// check cannot pass by reading nothing: the three block loops, the SHA-512
// kernel's body (s8ShaOff masks only SHA-256's kernel, so the masked
// builds hold it too), the SHA-1 kernel's body (not masked either), and, in the builds with the SHA-256 kernel, its SHA-NI
// body.
var h2CtMust = []string{"mk.std.hash.Sha1Blocks", "mk.std.hash.Sha256Compress", "mk.std.hash.Sha512Blocks", "mk.std.simd.kernel.Sha512Avx2", "mk.std.simd.kernel.Sha1Ni"}

// h2CtAbsent are the h2CtAllow functions a build of this probe holds no
// body of, by build, each inlined into a checked caller (ctRequirePresent;
// the PGO builds inline more of the small one-shot and state functions),
// or, in the
// masked builds, ShaNi, the SHA-256 instruction path s8ShaOff leaves out.
var h2CtAbsent = map[string][]string{
	"mk.std.hash.Sha1":              {"*"},
	"mk.std.hash.Sha1State:New":     {"*"},
	"mk.std.hash.Sha256":            {"kernel", "masked"},
	"mk.std.hash.Sha256State:New":   {"kernel", "masked"},
	"mk.std.hash.Sha384":            {"*"},
	"mk.std.hash.Sha384State:Bytes": {"*"},
	"mk.std.hash.Sha384State:New":   {"*"},
	"mk.std.hash.Sha384State:Sum":   {"*"},
	"mk.std.hash.Sha512":            {"*"},
	"mk.std.simd.kernel.ShaNi":      {"masked", "masked-pgo"},

	"mk.std.simd.kernel.Sha256Blocks": {"kernel", "masked", "masked-pgo"},
	"mk.std.simd.kernel.Sha512Blocks": {"kernel", "masked", "masked-pgo"},
}

// h2CtChecked reports whether fn is on the probe's secret path: the
// probe's functions (ctProbeFn), Hmac256, std.hash's and the kernel's.
func h2CtChecked(fn string) bool {
	return ctProbeFn(fn) || fn == "mk.std.crypto.Hmac256" ||
		strings.HasPrefix(fn, "mk.std.hash.") || strings.HasPrefix(fn, "mk.std.simd.kernel.")
}

// h2CtProfile builds the probe natively with --pgo-gen over stdDir, runs
// it once (h2CtTrain), and merges its profile.
func h2CtProfile(t *testing.T, root, stdDir string) string {
	t.Helper()
	gen := h2CtTrainExe(t)
	if out, st := mkCmd(t, root, stdDir, "build", "--release", "--pgo-gen", "-o", gen, "main.mk"); st != 0 {
		t.Fatalf("mk build --pgo-gen: exit %d\n%s", st, out)
	}
	dir := filepath.Join(root, "prof")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := h2CtTrain(gen, root).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", gen, err, out)
	}
	profs, _ := filepath.Glob(filepath.Join(dir, "*.mkprof"))
	if len(profs) != 1 {
		t.Fatalf("profile directory holds %v, want one .mkprof", profs)
	}
	merged := filepath.Join(root, "merged.mkprof")
	if out, st := mkCmd(t, root, stdDir, "profile", "merge", "-o", merged, profs[0]); st != 0 {
		t.Fatalf("mk profile merge: exit %d\n%s", st, out)
	}
	return merged
}

// h2CtTrainLen is the length in bytes of the training executable's path.
const h2CtTrainLen = 200

// h2CtTrainExe is the path h2CtProfile builds the instrumented probe at: in
// a directory of its own under the temporary directory, whose name pads the
// path to h2CtTrainLen bytes. The runtime reads its own path at exit to name
// the profile, a few counters per byte, so without the pad a longer
// temporary directory moves the counts; the test's directory, which holds
// the test's name, would leave no room for the pad under a long one.
func h2CtTrainExe(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(os.Getenv("GOTMPDIR"), "mkct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	name := "gen"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	pad := h2CtTrainLen - len(root) - len(name) - 2
	if pad < 1 {
		t.Fatalf("%s is too long for a training path of %d bytes", root, h2CtTrainLen)
	}
	dir := filepath.Join(root, strings.Repeat("p", pad))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

// h2CtTrain is the training run of the instrumented probe gen: a fixed
// program name and the arguments "a b c", the working directory root, and
// MK_PROFILE_DIR=prof (relative to root) as the whole environment, to which
// exec adds SYSTEMROOT on Windows. The runtime decodes the environment block
// and the command line at start, so an inherited environment would be most
// of the probe's entry counts and would move the hot threshold, LLVM's
// profile summary and the PGO builds the tests read with the runner's
// shell. What still reaches the profile from the runner is the number of
// separators in gen's path and the digits of the process id.
func h2CtTrain(gen, root string) *exec.Cmd {
	cmd := exec.Command(gen, "a", "b", "c")
	cmd.Args[0] = "gen"
	cmd.Dir = root
	cmd.Env = []string{"MK_PROFILE_DIR=prof"}
	return cmd
}

// TestSTDHASH_PgoTrainingIgnoresEnvironment: two trainings of the probe,
// the second under a parent environment 4,000 characters larger, give every
// function the same entry count (Counts[0]), and so the same hot threshold
// for the PGO builds the constant-time tests read. Counters inside a
// function may still differ with the digits of the process id.
func TestSTDHASH_PgoTrainingIgnoresEnvironment(t *testing.T) {
	if cryptoObjdump() == "" {
		t.Skip("needs llvm-objdump (LLVM 21)")
	}
	entries := func() map[string]uint64 {
		root := t.TempDir()
		stdDir := copyStd(t, root, nil)
		if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(h2CtProbe), 0o644); err != nil {
			t.Fatal(err)
		}
		return h2CtEntries(t, h2CtProfile(t, root, stdDir))
	}
	first := entries()
	t.Setenv("MK_TEST_TRAINING_PADDING", strings.Repeat("x", 4000))
	second := entries()
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("%d records, then %d", len(first), len(second))
	}
	for _, k := range slices.Sorted(maps.Keys(first)) {
		if first[k] != second[k] {
			t.Errorf("%s: entry count %d, then %d under the larger environment", k, first[k], second[k])
		}
	}
}

// h2CtEntries reads a version-1 .mkprof (internal/pgo's format: the magic,
// a u64 record count, then per record a u32 key length, the key, a 16-byte
// checksum, a u32 counter count and the u64 counters, little-endian) and
// returns each record's entry count by key and checksum.
func h2CtEntries(t *testing.T, path string) map[string]uint64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 16 || string(b[:8]) != "MKPROF\x00\x01" {
		t.Fatalf("%s: not a version-1 profile", path)
	}
	out := map[string]uint64{}
	at := 16
	take := func(n int) []byte {
		if at+n > len(b) {
			t.Fatalf("%s: truncated at byte %d", path, at)
		}
		at += n
		return b[at-n : at]
	}
	for range binary.LittleEndian.Uint64(b[8:16]) {
		key := string(take(int(binary.LittleEndian.Uint32(take(4)))))
		sum := take(16)
		n := int(binary.LittleEndian.Uint32(take(4)))
		counts := take(8 * n)
		var entry uint64
		if n > 0 {
			entry = binary.LittleEndian.Uint64(counts)
		}
		out[fmt.Sprintf("%s %x", key, sum)] = entry
	}
	return out
}

// TestSTDHASH_ConstantTimeDisassembly: the constant-time check of
// std.hash's secret paths.
// std-crypto's TestSTDCRYPTO_ConstantTimeDisassembly checks the SHA-2
// functions its own probe reaches and skips every std.hash function its
// table does not list; this test lists them all. The probe feeds secrets
// derived from argv through Hmac256 (Sha256State, so Sha256Compress and the
// kernel's ShaNi), Sha1State, Sha384State (Sha512State) and Sha512, and is
// built four times for x86_64-linux (built only, never run, so no WSL):
// as is, with the kernel masked (s8ShaOff: the portable loop only), and
// each again with a profile collected by running the same probe natively.
// Every conditional jump of every std.hash function, of the kernel's SHA
// functions and of the probe's Main (where the small state methods are
// inlined) is classified by ctClassify; an "other" jump beyond the
// function's reading fails, with the instructions to read.
// It skips without llvm-objdump or the LLVM toolchain.
func TestSTDHASH_ConstantTimeDisassembly(t *testing.T) {
	objdump := cryptoObjdump()
	if objdump == "" {
		t.Skip("needs llvm-objdump (LLVM 21)")
	}
	builds := []struct {
		name   string
		forces []sdForce
		pgo    bool
	}{
		{"kernel", nil, false},
		{"kernel-pgo", nil, true},
		{"masked", s8ShaOff, false},
		{"masked-pgo", s8ShaOff, true},
	}
	for _, b := range builds {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ins, into := h2CtBuild(t, objdump, b.forces, b.pgo)
			other, back, counts := ctClassifyBack(ins)
			seen := map[string]bool{}
			for _, x := range ins {
				seen[x.fn] = true
			}
			must := h2CtMust
			// The instruction path is the cuttable SHA-256 unit
			// (hash/sha256_kernel.mk): with it, the unmasked builds hold
			// ShaNi; cut, they need not.
			if _, err := os.Stat(filepath.Join("hash", "sha256_kernel.mk")); err == nil && b.forces == nil {
				must = append(must[:len(must):len(must)], "mk.std.simd.kernel.ShaNi")
			}
			for _, fn := range must {
				if !seen[fn] {
					t.Errorf("%s is not in the %s build: the probe no longer reaches it out of line, so its jumps went unread", fn, b.name)
				}
			}
			ctRequirePresent(t, "h2CtAllow", b.name, h2CtAllow, seen, h2CtAbsent, h2CtUnrecorded, into, h2CtChecked)
			var names, read []string
			for fn := range seen {
				if h2CtChecked(fn) {
					names = append(names, fn)
				}
			}
			sort.Strings(names)
			for _, fn := range names {
				allowed, known := h2CtAllow[fn]
				if !known {
					t.Errorf("%s (%s build) is on the secret path and unread: %d other jumps %q; read each and add it to h2CtAllow", fn, b.name, len(other[fn]), other[fn])
					continue
				}
				allowed = ctAllowed(allowed, h2CtByBuild, fn, b.name)
				if n := len(other[fn]); n > allowed {
					t.Errorf("%s (%s build) has %d conditional jumps that neither guard a panic nor close a loop, %d allowed; read each (is it a secret?) and record the reading:\n    %s",
						fn, b.name, n, allowed, strings.Join(other[fn], "\n    "))
				}
				read = append(read, fn)
				t.Logf("%s: %d checks, %d loop edges (%d on a compare that is not an induction, %d allowed), %d other (%d allowed)", fn, counts[fn][0], counts[fn][1], counts[fn][2], h2CtBack[cr2CtInstance.ReplaceAllString(fn, "")][b.name], len(other[fn]), allowed)
			}
			ctBackCheck(t, "h2CtBack", h2CtBack, b.name, back, read)
		})
	}
}

// h2CtBuild builds h2CtProbe for x86_64-linux (release, symbols kept) over
// a copy of std with forces, with a natively collected profile when pgo,
// and returns its disassembly.
func h2CtBuild(t *testing.T, objdump string, forces []sdForce, pgo bool) ([]ctInsn, ctInlinedInto) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, sdFixtures(t, forces))
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(h2CtProbe), 0o644); err != nil {
		t.Fatal(err)
	}
	flags := []string{"build", "--release", "--debug", "-target", "x86_64-linux"}
	if pgo {
		flags = append(flags, "--pgo="+h2CtProfile(t, root, stdDir))
	} else {
		flags = append(flags, "--pgo=off")
	}
	exe := filepath.Join(root, "probe.elf")
	out, st := mkCmd(t, root, stdDir, append(flags, "-o", exe, "main.mk")...)
	if st != 0 {
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		t.Fatalf("mk %s: exit %d\n%s", strings.Join(flags, " "), st, out)
	}
	dis, err := exec.Command(objdump, "-d", "--no-show-raw-insn", exe).Output()
	if err != nil {
		t.Fatalf("%s -d: %v", objdump, err)
	}
	ins := ctParse(string(dis))
	if len(ins) == 0 {
		t.Fatal("llvm-objdump printed no instruction")
	}
	return ins, ctInlined(t, exe)
}

// h2CtUnrecorded are the h2CtAbsent functions whose build's DWARF records
// no copy, by build (ctRequirePresent; read 2026-11-18): the SHA
// instruction path s8ShaOff leaves out.
var h2CtUnrecorded = map[string][]string{
	"mk.std.simd.kernel.ShaNi": {"masked", "masked-pgo"},
}
