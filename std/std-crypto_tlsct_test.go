// Why: a conditional jump on a secret leaks it through timing, and LLVM can
// turn a mask select into a branch that the source never wrote.
// What: TestSTDCRYPTO_TlsConstantTimeDisassembly, over HMAC-384/512, HKDF,
// AES-GCM, the AES and GHASH kernels and crypto.Wipe, with allowance tables.
// How: builds a probe four ways for x86_64-linux (kernel, masked, each with
// PGO) and classifies every jump with ctParse and ctClassify.

package std

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// cr2CtProbe reaches every new secret path on lengths taken from the
// argument count: HMAC with a short and a long key, HKDF over each hash,
// AES-128-GCM and AES-256-GCM sealing and opening a message of a few
// blocks and a partial one, and the wipes.
const cr2CtProbe = `import std.crypto

export Main(sys: &System): WriteError|crypto.Error!void {
    var out := sys.Stdout()
    n := sys.args.Len()
    m := n % 7
    var key: [160]u8 = [7; 160]
    key[0] = Trunc[u8](n)
    var msg: [300]u8 = [3; 300]
    msg[1] = Trunc[u8](n)
    h3 := crypto.Hmac384(key[0..40 + m], msg[0..100 + m])
    h5 := crypto.Hmac512(key[0..150 + m], msg[..])
    var okm: [100]u8 = [0; 100]
    p2 := crypto.HkdfExtract(key[0..20 + m], msg[0..30 + m], crypto.HashKind.Sha256)
    try crypto.HkdfExpand(&p2, msg[0..10 + m], mut okm[0..90 + m])
    p3 := crypto.HkdfExtract(key[0..20 + m], msg[0..30 + m], crypto.HashKind.Sha384)
    try crypto.HkdfExpand(&p3, msg[0..10 + m], mut okm[0..80 + m])
    p5 := crypto.HkdfExtract(key[0..20 + m], msg[0..30 + m], crypto.HashKind.Sha512)
    try crypto.HkdfExpand(&p5, msg[0..10 + m], mut okm[0..70 + m])
    var nonce: [12]u8 = [1; 12]
    nonce[0] = Trunc[u8](n)
    var g := try crypto.AesGcm.New(key[0..16])
    var g2 := try crypto.AesGcm.New(key[16..48])
    var sealed: [320]u8 = [0; 320]
    s := try g.Seal(&nonce, msg[0..13 + m], msg[0..200 + m], mut sealed[..])
    var back: [300]u8 = [0; 300]
    b := try g.Open(&nonce, msg[0..13 + m], sealed[0..s], mut back[..])
    s2 := try g2.Seal(&nonce, msg[0..5], msg[0..70 + m], mut sealed[..])
    b2 := try g2.Open(&nonce, msg[0..5], sealed[0..s2], mut back[..])
    g.Wipe()
    g2.Wipe()
    crypto.Wipe(mut okm[0..50 + m])
    try Print(mut out, "{a} {c} {d} {e} {f} {i} {j}\n", (a: h3[0], c: h5[1], d: okm[60], e: back[3], f: b, i: b2, j: sealed[2]))
}
`

// cr2CtAllow is the reading (mk and std of the tree, LLVM 21.1.8, the four
// builds of TestSTDCRYPTO_TlsConstantTimeDisassembly): for each function, the
// most "other" jumps any build has, each read in llvm-objdump's listing as a
// test of a length, a loop bound, a public count or a CPU feature, never of
// a key, a message, H or the state. The release build's loop versioning
// (lower.VersionLoops) adds jumps to AesGcm:Ctr, HmacPad and CtrNi, and
// their comments say which.
var cr2CtAllow = map[string]int{
	// Main: the probe's try edges (nine, or eight in masked without PGO),
	// 13 write error tags, and seven inlined Writer.Bytes pairs of tests
	// on remaining capacity and whether the buffer is empty. Under PGO the
	// five u8 values' FmtU64 is inlined too: v >= 100 and v >= 10 on the
	// printed byte, and its Writer.Bytes pair (56). The buffer's fill moves
	// only with the text already printed, which the probe writes out.
	"mk.main.Main": 56,

	// aes_ct64 and the key schedule: the round count (nr, from the key's
	// length) and the schedule's word counters (j == 0, j == 4 for 256-bit
	// keys, the word index against 4 (nr + 1)); no jump in the S-box, the
	// linear layers or the round keys' expansion. AesEncrypt4 also tests
	// src's length before each block's words, (len - 16 i) >> 2 against 3,
	// twice as a jump table to the word's bounds panic (read in all four
	// builds: the classifier does not take the panic blocks after the
	// table's jmp for its target).
	"mk.std.crypto.AesAddRoundKey":   0,
	"mk.std.crypto.AesEncrypt4":      4,
	"mk.std.crypto.AesInterleaveIn":  0,
	"mk.std.crypto.AesInterleaveOut": 0,
	"mk.std.crypto.AesMixColumns":    0,
	"mk.std.crypto.AesOrtho":         0,
	"mk.std.crypto.AesSbox":          0,
	"mk.std.crypto.AesSchedule":      6,
	"mk.std.crypto.AesShiftRows":     0,
	"mk.std.crypto.AesSubWord":       0,
	// GHASH, portable: the block loop's entry on the length.
	"mk.std.crypto.GhashBmul":     0,
	"mk.std.crypto.GhashPortable": 1,
	"mk.std.crypto.GhashRev64":    0,
	// AesGcm: Seal's two length tests; Open's three and the verdict (one
	// ptest of the tag difference, after the whole tag is compared); Seal
	// and Open each test the round
	// count first (cmpq $0x0 of nr, the struct's first word: 0 only after
	// Wipe, public), read in all four builds; New's
	// key length (16, 32) and GhashKey's guard inlined (two simd.Cpu()
	// cache tests, the pclmul and aes bits); Block's kernel answer; Ctr's
	// kernel answer, the length zero, a whole 64-byte group left, the
	// loop and the tail's 16-byte step; Ghash's ghni flag and the guard of
	// GhashBlocks inlined twice (whole blocks, padded tail: 2 x 5) and the
	// tail's length; Tag's the same, partly inlined (masked-pgo: both Ghash
	// calls' whole-or-tail tests, the ghni flag and the kernel's answers).
	// Tag under kernel-pgo, 29: its three Ghash calls inlined, each with
	// the ghni flag, GhashBlocks' guard (two simd.Cpu() cache tests, the
	// pclmul and aes bits) for the whole blocks and for the tail, and the
	// whole-or-tail length test, then Block with the guard of the kernel's
	// AesEncryptBlocks (the cache test, the aes bit) and the round count's
	// dispatch (0xb, 0xd). Ctr with loop versioning: 11 in the kernel
	// build, 10 masked, 7 under each PGO build (the (n & 63) - 1 == 0 tail
	// test is read as an "other", not a check): kernel-pgo inlines the
	// kernel's AesCtr (its Cpu() cache test and aes bit, the round count's
	// 0xb and 0xd) beside the 64-byte step and two tail tests; masked-pgo
	// inlines AesEncrypt4, whose round loop tests nr, beside the loop's
	// entry on n, the 64-byte step and four tail tests.
	// Beyond the five above, every new jump is in the tail loop
	// (dst[i + t] = src[i + t] ^ ks[t]): the versioning guard, which tests n - 1
	// against dst's length and n & 63 against [1, 64] (one or of two setcc
	// in the non-PGO builds, two branches under PGO; in kernel-pgo the
	// second tests n - i - 1 against 63), and the fast copy's
	// vector width and epilogue tests (n & 63 against 3 and 16, n & 0x30,
	// n & 0xc, n & 0x3c; masked-pgo, n's low bit), all on n = src's length,
	// dst's length and i; ks and the counter are only XORed and stored.
	"mk.std.crypto.AesGcm:Block": 0, // inlined in every build (cr2CtAbsent): its kernel answer is read in its callers (read at 1 out of line)
	"mk.std.crypto.AesGcm:Ctr":   11,
	"mk.std.crypto.AesGcm:Ghash": 11, // no build reads more than 11
	"mk.std.crypto.AesGcm:New":   6,
	"mk.std.crypto.AesGcm:Open":  5,
	"mk.std.crypto.AesGcm:Seal":  3,
	"mk.std.crypto.AesGcm:Tag":   29,
	"mk.std.crypto.AesGcm:Wipe":  0,
	"mk.std.crypto.GcmJ0":        0,
	// HMAC and HKDF: the key length above the block (0x41, 0x81) and the
	// key length zero (CopyFrom's skip) in the keyed states; HmacPad's
	// copy and pad loops (PGO: out of line, the block size 64 or 128);
	// HKDF's output-length check and the HashKind dispatch, and
	// the Prk's live flag (cmpb $0x1 at offset 0x41, false only after
	// Wipe, public; all four builds); Expand's
	// output length zero, the last block's partial length and the loop
	// (PGO: the copy's length). HmacPad with loop versioning: 16 under PGO,
	// where it is out of line (both PGO builds have the same instructions),
	// from the versioning guard of the pad loop (b - 1 against 0x7f) and the
	// vectorised copy of that loop, unrolled: b against 3 and 0x20, b & 0xe0
	// against 0x20, 0x40 and 0x60, b's remainder (b & 0x1c, b & 0xfc) and the
	// scalar and 4-byte tails' exits (i | 1 to i | 3 against b, i | 4 to
	// i | 0x14 against b & 0xfc, the 4-byte loop's bound, and b against
	// b & 0xe0); b is the block size (64 or 128), and the key bytes and x
	// are only XORed and stored.
	"mk.std.crypto.HkdfExpand":        4,
	"mk.std.crypto.HkdfExtract":       2,
	"mk.std.crypto.Expand256":         2, // no build reads more than 2
	"mk.std.crypto.Expand384":         2, // no build reads more than 2
	"mk.std.crypto.Expand512":         2, // no build reads more than 2
	"mk.std.crypto.Hmac256Key:New":    1, // no build reads more than 1
	"mk.std.crypto.Hmac256Key:Finish": 0,
	"mk.std.crypto.Hmac384":           0,
	"mk.std.crypto.Hmac384Key:New":    1, // no build reads more than 1
	"mk.std.crypto.Hmac384Key:Finish": 0,
	"mk.std.crypto.Hmac512":           0,
	"mk.std.crypto.Hmac512Key:New":    1, // no build reads more than 1
	"mk.std.crypto.Hmac512Key:Finish": 0,
	"mk.std.crypto.HmacPad":           16,
	"mk.std.crypto.Be32":              0,
	"mk.std.crypto.Be64":              0,
	"mk.std.crypto.Le32":              0,
	"mk.std.crypto.Le64":              0,
	"mk.std.crypto.PutBe32":           0,
	"mk.std.crypto.PutBe64":           0,
	"mk.std.crypto.PutLe32":           0,
	"mk.std.crypto.PutLe64":           0,
	// Wipe (the kernel's loop inlined into crypto.Wipe, or out of line
	// under PGO): the length zero, the head's byte count to an 8-byte
	// boundary (from the buffer's address, unrolled to 6 compares), the
	// word loop and the tail; nothing reads the bytes.
	"mk.std.crypto.Wipe":        12,
	"mk.std.simd.kernel.Wipe":   8, // out of line under PGO
	"mk.std.simd.kernel.Rounds": 0,
	// The AES and GHASH kernels: each guard's simd.Cpu() cache test and
	// feature bit (two for GhashKey and GhashBlocks: pclmul and aes), the
	// round count's dispatch (rk's length: 0xb, 0xd); the bodies' block
	// loops and lengths (eight in flight, then one) and CtrNi's partial
	// last block, a byte loop unrolled to 16 compares of its public length,
	// twice under PGO, with its bounds checks laid out past the classifier's
	// five instructions. CtrNi with loop versioning: 40 for CtrNi[12] under
	// kernel-pgo (37 without loop versions), where 33 are those bounds
	// checks (i + j against src's and dst's lengths), and 24 in the kernel
	// build (19 without); the added jumps are the versioning guard of the partial
	// block's byte loop (i < 0, then one cmpb of the combined index and
	// length tests, or four branches in two of the PGO instances),
	// n - i <= 0, and the fast copy's width and tail tests (n - i against
	// 3 and 0x20, its low bit; 0x10 is the guard's n - i - 1 < 16 and the
	// unrolled exit's, older); the keystream block is only XORed and
	// stored.
	"mk.std.simd.kernel.AesCtr":           4,
	"mk.std.simd.kernel.AesEncryptBlocks": 4,
	"mk.std.simd.kernel.BlocksNi":         2,
	"mk.std.simd.kernel.CtrNi":            24, // no build reads more than 24
	"mk.std.simd.kernel.Encrypt":          0,
	"mk.std.simd.kernel.Encrypt8":         0,
	"mk.std.simd.kernel.GhashBlocks":      0, // no build reads more than 0
	"mk.std.simd.kernel.GhashKey":         4,
	"mk.std.simd.kernel.GhashNi":          3,
	"mk.std.simd.kernel.KeyNi":            0,
	"mk.std.simd.kernel.Keys":             0,
	"mk.std.simd.kernel.Mul":              0,
	"mk.std.simd.kernel.Powers":           0,
	"mk.std.simd.kernel.Product":          0,
	"mk.std.simd.kernel.Reduce":           0,
	"mk.std.simd.kernel.Load":             0,
	"mk.std.simd.kernel.Store":            0,
	"mk.std.simd.kernel.Xor":              0,
	"mk.std.simd.kernel.XorW":             0,
	"mk.std.simd.kernel.Xor4":             0,
	"mk.std.simd.kernel.Split":            0,
	"mk.std.simd.kernel.Join":             0,
	"mk.std.simd.kernel.Counter":          0,
}

// cr2CtNi are the instruction path's bodies: every unmasked build holds
// them, so the AES and carry-less multiply path was read, and no masked
// build does, so the mask took.

// cr2CtByBuild is this test's per-build counts (ctByBuild): each function
// whose four builds read different counts, at the count each build has (from
// the test's -v log; Ctr under masked-pgo includes the (n & 63) - 1 == 0
// tail test, read), every one at or below the reading above. CtrNi's three instances
// differ under PGO (12, 10 and 5); the name without its hash
// takes the most. A build not named keeps the table's count.
var cr2CtByBuild = ctByBuild{
	"mk.main.Main":                {"kernel": 36, "masked": 35},
	"mk.std.crypto.AesGcm:Ctr":    {"masked": 10, "kernel-pgo": 7, "masked-pgo": 7},
	"mk.std.crypto.AesGcm:New":    {"kernel-pgo": 2, "masked": 2, "masked-pgo": 2},
	"mk.std.crypto.AesGcm:Tag":    {"kernel": 6, "masked": 2, "masked-pgo": 9},
	"mk.std.crypto.Wipe":          {"kernel-pgo": 0, "masked-pgo": 0},
	"mk.std.simd.kernel.Wipe":     {"kernel": 0, "masked": 0},
	"mk.std.simd.kernel.CtrNi":    {"kernel-pgo": 12},
	"mk.std.simd.kernel.GhashKey": {"masked-pgo": 0},
	// kernel-pgo 2 with frame pointers on (the default; read on the
	// release disassembly): cmpq $0x40, %rcx (the block count
	// against 64) and cmpq %r8, %rdi (the index against the length), both
	// public lengths; 1 with them off, where one is a loop edge.
	"mk.std.simd.kernel.GhashNi": {"kernel-pgo": 2},
}
var cr2CtNi = []string{"mk.std.simd.kernel.CtrNi", "mk.std.simd.kernel.GhashNi"}

// cr2CtInstance matches the hash suffix of a generic instance's symbol.
var cr2CtInstance = regexp.MustCompile(`\.[0-9a-f]{16}$`)

// cr2CtAbsent are the cr2CtAllow functions a build of this probe holds no
// body of, by build (ctRequirePresent): each
// is inlined into a checked caller (the kernel helpers at 0 when out of
// line, the byte-order and interleave helpers, the small HMAC and GHASH
// steps), or, in the masked builds, is on the AES instruction path that
// s8AesOff leaves out (CtrNi, GhashNi, KeyNi and their callers, which
// cr2CtNi requires absent there).
var cr2CtAbsent = map[string][]string{
	"mk.std.crypto.AesGcm:Block":          {"*"},
	"mk.std.crypto.AesAddRoundKey":        {"masked-pgo"}, // AesEncrypt4, AesGcm:Ctr
	"mk.std.crypto.AesGcm:Ghash":          {"kernel-pgo", "masked", "masked-pgo"},
	"mk.std.crypto.AesInterleaveIn":       {"*"},
	"mk.std.crypto.AesInterleaveOut":      {"*"},
	"mk.std.crypto.AesMixColumns":         {"*"},
	"mk.std.crypto.AesOrtho":              {"kernel-pgo", "masked-pgo"}, // AesSchedule, AesSubWord
	"mk.std.crypto.AesSbox":               {"masked-pgo"},               // AesSchedule, AesSubWord, AesGcm:Ctr
	"mk.std.crypto.AesShiftRows":          {"masked-pgo"},               // AesGcm:Ctr
	"mk.std.crypto.AesSubWord":            {"kernel", "masked"},
	"mk.std.crypto.Be32":                  {"*"},
	"mk.std.crypto.Be64":                  {"kernel", "masked", "masked-pgo"},
	"mk.std.crypto.GcmJ0":                 {"*"},
	"mk.std.crypto.GhashBmul":             {"kernel", "masked", "masked-pgo"},
	"mk.std.crypto.GhashRev64":            {"*"},
	"mk.std.crypto.HkdfExtract":           {"kernel", "masked"},
	"mk.std.crypto.Hmac256Key:Finish":     {"*"},
	"mk.std.crypto.Hmac384":               {"kernel", "masked"},
	"mk.std.crypto.Hmac384Key:Finish":     {"*"},
	"mk.std.crypto.Hmac512":               {"kernel", "masked"},
	"mk.std.crypto.Hmac512Key:Finish":     {"*"},
	"mk.std.crypto.HmacPad":               {"kernel", "masked"},
	"mk.std.crypto.Le32":                  {"*"},
	"mk.std.crypto.Le64":                  {"kernel", "masked", "masked-pgo"},
	"mk.std.crypto.PutBe32":               {"*"},
	"mk.std.crypto.PutBe64":               {"*"},
	"mk.std.crypto.PutLe32":               {"*"},
	"mk.std.crypto.PutLe64":               {"kernel", "masked", "masked-pgo"},
	"mk.std.simd.kernel.AesCtr":           {"kernel-pgo", "masked", "masked-pgo"},
	"mk.std.simd.kernel.AesEncryptBlocks": {"kernel-pgo", "masked", "masked-pgo"},
	"mk.std.simd.kernel.BlocksNi":         {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Counter":          {"*"},
	"mk.std.simd.kernel.CtrNi":            {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Encrypt":          {"kernel", "masked", "masked-pgo"},
	"mk.std.simd.kernel.Encrypt8":         {"kernel", "masked", "masked-pgo"},
	"mk.std.simd.kernel.GhashBlocks":      {"kernel", "kernel-pgo", "masked"},
	"mk.std.simd.kernel.GhashKey":         {"kernel", "masked"},
	"mk.std.simd.kernel.GhashNi":          {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Join":             {"*"},
	"mk.std.simd.kernel.KeyNi":            {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Keys":             {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Load":             {"*"},
	"mk.std.simd.kernel.Mul":              {"*"},
	"mk.std.simd.kernel.Powers":           {"*"},
	"mk.std.simd.kernel.Product":          {"*"},
	"mk.std.simd.kernel.Reduce":           {"*"},
	"mk.std.simd.kernel.Rounds":           {"*"},
	"mk.std.simd.kernel.Split":            {"*"},
	"mk.std.simd.kernel.Store":            {"*"},
	"mk.std.simd.kernel.Wipe":             {"kernel", "masked"},
	"mk.std.simd.kernel.Xor":              {"*"},
	"mk.std.simd.kernel.Xor4":             {"*"},
	"mk.std.simd.kernel.XorW":             {"*"},
}

// cr2CtMust are functions every build must hold out of line, so the check
// cannot pass by reading nothing: the portable AES rounds and GHASH. The
// S-box, inlined into the rounds' callers under masked-pgo, is held out of
// line in the other builds by ctRequirePresent.
var cr2CtMust = []string{"mk.std.crypto.AesEncrypt4", "mk.std.crypto.GhashPortable"}

// cr2CtChecked reports whether fn is on the probe's secret path and read
// here: std.crypto's functions (std.hash's are std-hash2's test), the
// kernel's AES, GHASH and Wipe, and Main.
func cr2CtChecked(fn string) bool {
	if fn == "mk.main.Main" || strings.HasPrefix(fn, "mk.std.crypto.") {
		return true
	}
	// Every kernel function but the SHA ones (std-hash_ct_test.go's): the
	// helpers (Load, Store, Xor, Split, Join, Counter) carry key and state
	// when a build leaves them out of line.
	return strings.HasPrefix(fn, "mk.std.simd.kernel.") && !strings.Contains(fn, "Sha")
}

// TestSTDCRYPTO_TlsConstantTimeDisassembly applies std-crypto's machine-code
// classifier (ctParse, ctClassify in std-crypto_ct_test.go) to every secret
// path std-crypto2 adds: Hmac384, Hmac512 and the keyed states, HKDF over
// the three hashes, AES-GCM's key schedule (the bitsliced S-box), aes_ct64,
// ghash_ctmul64, Seal and Open, the AES and GHASH kernels with their guards,
// and crypto.Wipe. The probe takes its secrets from argv, so nothing folds,
// and is built four times for x86_64-linux (release, symbols kept; built
// only, never run, so no WSL): as is (the AES and carry-less multiply
// instructions), with simd_aes_test.go's s8AesOff mask (the portable path only), and
// each again with a profile collected by running the probe natively. Every
// conditional jump of every std.crypto function, of the kernel's AES, GHASH
// and Wipe functions and of Main is classified: an "other" jump beyond the
// function's reading, or a function the table does not list, fails with the
// instructions to read. It skips without llvm-objdump or the LLVM toolchain.
func TestSTDCRYPTO_TlsConstantTimeDisassembly(t *testing.T) {
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
		{"masked", s8AesOff, false},
		{"masked-pgo", s8AesOff, true},
	}
	for _, b := range builds {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			ins, into := cr2CtBuild(t, objdump, b.forces, b.pgo)
			other, counts := ctClassify(ins)
			seen := map[string]bool{}
			for _, x := range ins {
				seen[x.fn] = true
			}
			for _, fn := range cr2CtMust {
				if !seen[fn] {
					t.Errorf("%s is not in the %s build: the probe no longer reaches it out of line, so its jumps went unread", fn, b.name)
				}
			}
			ctRequirePresent(t, "cr2CtAllow", b.name, cr2CtAllow, seen, cr2CtAbsent, cr2CtUnrecorded, into, cr2CtChecked)
			for _, ni := range cr2CtNi {
				held := false
				for fn := range seen {
					held = held || cr2CtInstance.ReplaceAllString(fn, "") == ni
				}
				if held != (b.forces == nil) {
					t.Errorf("%s held out of line: %v in the %s build; want %v", ni, held, b.name, b.forces == nil)
				}
			}
			var names []string
			for fn := range seen {
				if cr2CtChecked(fn) {
					names = append(names, fn)
				}
			}
			sort.Strings(names)
			for _, fn := range names {
				allowed, known := cr2CtAllow[cr2CtInstance.ReplaceAllString(fn, "")]
				if !known {
					allowed, known = ctAllow[fn]
				}
				if !known && ctPublic[fn] {
					continue
				}
				if !known {
					t.Errorf("%s (%s build) is on the secret path and unread: %d other jumps\n    %s", fn, b.name, len(other[fn]), strings.Join(other[fn], "\n    "))
					continue
				}
				allowed = ctAllowed(allowed, cr2CtByBuild, fn, b.name)
				if n := len(other[fn]); n > allowed {
					t.Errorf("%s (%s build) has %d conditional jumps that neither guard a panic nor close a loop, %d allowed; read each (is it a secret?) and record the reading:\n    %s",
						fn, b.name, n, allowed, strings.Join(other[fn], "\n    "))
				}
				t.Logf("%s: %d checks, %d loop edges (%d on a compare that is not an induction), %d other (%d allowed)", fn, counts[fn][0], counts[fn][1], counts[fn][2], len(other[fn]), allowed)
			}
		})
	}
}

// cr2CtBuild builds cr2CtProbe for x86_64-linux (release, symbols kept)
// over the crypto std copy with forces, with a natively collected profile
// when pgo, and returns its disassembly.
func cr2CtBuild(t *testing.T, objdump string, forces []sdForce, pgo bool) ([]ctInsn, ctInlinedInto) {
	t.Helper()
	root := t.TempDir()
	stdDir := cryptoStdDir(t, root, sdFixtures(t, forces))
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(cr2CtProbe), 0o644); err != nil {
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

// cr2CtUnrecorded are the cr2CtAbsent functions whose build's DWARF records
// no copy, by build (ctRequirePresent): in the masked builds the AES and GHASH instruction path
// s8AesOff leaves out (the kernel's Ni functions, their helpers and
// callers; GhashBlocks and GhashKey only without PGO, which keeps their
// guard). Under kernel-pgo, AesMixColumns has no out-of-line call or
// inline DIE. Its only source caller is AesEncrypt4, which applies the
// straight-line transform in every non-final round. cr2CtMust requires
// that caller's emitted body, and its jump allowance covers the loop;
// under masked-pgo the DWARF records its copy in AesGcm:Ctr.
var cr2CtUnrecorded = map[string][]string{
	"mk.std.crypto.AesMixColumns":    {"kernel-pgo"},
	"mk.std.simd.kernel.BlocksNi":    {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Counter":     {"masked", "masked-pgo"},
	"mk.std.simd.kernel.CtrNi":       {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Encrypt":     {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Encrypt8":    {"masked", "masked-pgo"},
	"mk.std.simd.kernel.GhashBlocks": {"masked"},
	"mk.std.simd.kernel.GhashKey":    {"masked"},
	"mk.std.simd.kernel.GhashNi":     {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Join":        {"masked", "masked-pgo"},
	"mk.std.simd.kernel.KeyNi":       {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Keys":        {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Load":        {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Mul":         {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Powers":      {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Product":     {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Reduce":      {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Split":       {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Store":       {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Xor":         {"masked", "masked-pgo"},
	"mk.std.simd.kernel.Xor4":        {"masked", "masked-pgo"},
	"mk.std.simd.kernel.XorW":        {"masked", "masked-pgo"},
}
