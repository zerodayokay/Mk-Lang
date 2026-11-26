// Why: the curve code's secret paths (key parsing, ECDH, ECDSA signing,
// Generate, Wipe) must not branch on a secret in the machine code.
// What: TestSTDCRYPTO_EcConstantTimeDisassembly and its allowance tables.
// How: std-crypto's classifier over a probe built for x86_64-linux with and
// without PGO; verification handles public data and is not read, but no
// secret path's build may hold a Vt function of verification.

package std

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// cr2VtSymbol matches a symbol of verification's variable-time arithmetic
// (ec_<curve>.mk's Vt functions).
var cr2VtSymbol = regexp.MustCompile(`\.(P256|P384)Vt[A-Z]`)

// cr2EcCtProbe uses a scalar and a digest built from the argument count.
const cr2EcCtProbe = `import std.crypto

// ParseSign parses der with ParsePrivateKey and signs digest under a
// P-256 secret, or reads a byte of an Ed25519 seed.
ParseSign(der: []u8, digest: []u8, sig: mut []u8): crypto.Error!u8 {
    k := try crypto.ParsePrivateKey(der)
    match k {
        .Ec(secret) {
            var t := secret
            _ = try crypto.EcdsaSign(&t, digest, crypto.SigFormat.Fixed, sig)
            t.Wipe()
            return sig[9]
        }
        .Ed25519(seed) {
            return seed[4]
        }
    }
}

export Main(sys: &System): WriteError|Canceled|crypto.Error!void {
    var out := sys.Stdout()
    n := sys.args.Len()
    var d: [32]u8 = [0x5a; 32]
    d[0] = Trunc[u8](n) & 0x7f
    d[31] = Trunc[u8](n * 13)
    var s := try crypto.EcSecret.FromBytes(crypto.Curve.P256, d[..])
    var pub: [65]u8 = [0; 65]
    _ = try s.Public(mut pub[..])
    var shared: [32]u8 = [0; 32]
    try crypto.EcdhShared(crypto.Curve.P256, &s, pub[..], mut shared[..])
    var digest: [64]u8 = [7; 64]
    digest[2] = Trunc[u8](n)
    var sig: [72]u8 = [0; 72]
    a := try crypto.EcdsaSign(&s, digest[0..32], crypto.SigFormat.Der, mut sig[..])
    b := try crypto.EcdsaSign(&s, digest[0..48], crypto.SigFormat.Fixed, mut sig[..])
    c := try crypto.EcdsaSign(&s, digest[..], crypto.SigFormat.Fixed, mut sig[..])
    s.Wipe()
    // The same scalar as a SEC 1 ECPrivateKey and as an Ed25519 PKCS#8
    // seed, through ParsePrivateKey.
    var k1: [51]u8 = [0; 51]
    sec1: [7]u8 = [0x30, 0x31, 0x02, 0x01, 0x01, 0x04, 0x20]
    (mut k1[0..7]).CopyFrom(sec1[..])
    (mut k1[7..39]).CopyFrom(d[..])
    named: [12]u8 = [0xa0, 0x0a, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07]
    (mut k1[39..51]).CopyFrom(named[..])
    var k2: [48]u8 = [0; 48]
    p8: [16]u8 = [0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20]
    (mut k2[0..16]).CopyFrom(p8[..])
    (mut k2[16..48]).CopyFrom(d[..])
    e := try ParseSign(k1[..], digest[0..32], mut sig[..]) ^ try ParseSign(k2[..], digest[0..32], mut sig[..])
    // A drawn secret, used as FromBytes' is.
    var g := try crypto.EcSecret.Generate(sys.io, crypto.Curve.P256)
    _ = try g.Public(mut pub[..])
    g.Wipe()
    try Print(mut out, "{a} {b} {c} {x} {y} {e} {z}\n", (a: a, b: b, c: c, x: shared[3], y: sig[5], e: e, z: pub[7]))
}
`

// cr2EcCtAllow is the reading (mk and std of the tree, LLVM 21.1.8, both
// builds) of the curve unit's functions: for each,
// the most "other" jumps any build has, each read in llvm-objdump's
// listing. The arithmetic (fiat's Mul, Square, Add, Sub, the Montgomery
// conversions, Limbs, Encode, Reduce, Invert, PtAdd, PtDouble, PtTable,
// PtLookup) has none. A Wipe inlined into a function adds its tests of a
// stack buffer's address alignment and constant length (the unrolled
// head: testb of a constant, andl $7, cmpl $1..$6, cmpq of the length),
// none of the bytes. The rejection of a nonce outside [1, n - 1] and of a
// zero r or s (probability below 2^-32, as in Go) are loop edges.
// The interface format's hash domains decide which pack an instance lands
// in, and so what ThinLTO inlines; the comments of KeyAlgorithm, KeyEc,
// P256nDigest and Wipe say which jumps that adds.
var cr2EcCtAllow = map[string]int{
	// The scalar's range check (Below's borrow, js), the answer public.
	"mk.std.crypto.P256Decode":  1,
	"mk.std.crypto.P256nDecode": 0, // inlined in every build (cr2EcCtAbsent), read in its callers (read at 1 out of line)
	"mk.std.crypto.EcP256Valid": 1,
	// The curve, b's length, the range check (js on Below's borrow and a
	// separate ptest for zero): its answer is
	// public (BadKey).
	"mk.std.crypto.EcSecret:FromBytes": 4,
	// The output length and the inlined identity test in P256PtBytes.
	// The identity branch cannot be taken for a valid secret.
	"mk.std.crypto.EcSecret:Public": 2,
	// Main: the error tags of the probe's tries (FromBytes's too under PGO;
	// in release FromBytes is inlined, and Main tests its two public range
	// answers, EcP256Valid's and P256nDecode's: BadKey), Print's write
	// error tags (13, 7 in release) and seven inlined Writer.Bytes pairs:
	// the remaining capacity and the empty buffer, which move only with
	// the text already printed.
	"mk.main.Main": 37,
	// ParseSign, the probe's: ParsePrivateKey's error tag, the match on the
	// parsed key's kind (the DER's, public) and EcdsaSign's error tag; in
	// release the inlined Wipe of t (EcSecret:Wipe): the head's count from
	// the stack address and the tail's tests of i against the length 32.
	"mk.main.ParseSign": 7,
	// Generate: the curve, Random's try edge, and the inlined Wipe of b
	// (the unrolled head's tests of constants, andl $7, cmpq of the
	// length). The draw's range check (js, ptest) is the rejection loop's
	// back edge: it shows only whether a discarded draw was out of range.
	"mk.std.crypto.EcSecret:Generate": 13,
	"mk.std.crypto.Random":            0,
	// The DER structure, all public: tags, classes, the constructed bit,
	// lengths, the version INTEGER's bytes, the OIDs' bytes (KeySame and
	// the unrolled compares of KeyAlgorithm), the [0]/[1] order, the
	// error-union and option tags of DerRead and DerIter.Next, the scalar's
	// OCTET STRING length (over 32 refused, shorter padded) and the inlined
	// copy's length dispatch, and FromBytes' range check when inlined into
	// KeyEc (its answer public). No jump reads the scalar or the seed: they
	// are copied whole, and the seed is never tested. KeyAlgorithm:
	// In the release build, the
	// inlined KeySame compares are laid out without main's three-way
	// switch on the OID's length, so three length tests repeat after a
	// failed compare (oid.content.Len() against 3 twice more and
	// p.content.Len() against 5 once more; main's single 9 moved).
	// KeyAlgorithm and KeyEc inline KeySame in both builds. KeyEc also
	// inlines KeyBits. The PGO layout of KeyEc has more branches for
	// public OID lengths and bytes, DER tags and public-key encoding.
	// The scalar is copied after checking its encoded length and padding.
	// ParsePrivateKey under PGO: 38, among them a test of a parsed
	// element's constructed byte (cmpb $0x0 after its tag against 0x10),
	// public, which the classifier does not take for a check.
	"mk.std.crypto.ParsePrivateKey": 38,
	"mk.std.crypto.KeyAlgorithm":    51,
	"mk.std.crypto.KeyEc":           55,
	"mk.std.crypto.KeyUint":         11, // DER INTEGER type, length, sign and leading zero encoding
	"mk.std.crypto.KeySame":         0,
	"mk.std.crypto.KeyIs":           0, // inlined in every build (cr2EcCtAbsent), read in KeyEc and its other callers (read at 2 out of line)
	"mk.std.crypto.KeyBits":         0,
	// The inlined Wipe of d's 32 bytes.
	"mk.std.crypto.EcSecret:Wipe": 12,
	// Crypto's Wipe wrapper is inlined in both builds. The kernel's
	// out-of-line body tests the buffer length, alignment of the address,
	// and the word and byte loop counters.
	"mk.std.crypto.Wipe":      0,
	"mk.std.simd.kernel.Wipe": 13,
	// The identity test of Z (IsZero, its answer public); PGO adds an
	// out-of-line split of the same test.
	"mk.std.crypto.P256PtBytes":  0, // the inlined identity test is counted in EcSecret:Public
	"mk.std.crypto.P256PtXBytes": 1,
	// The loop's entry test on k.Len() (public).
	"mk.std.crypto.P256PtMul": 1,
	// The peer's point (public): its length and form, the coordinates'
	// range and the curve equation.
	"mk.std.crypto.P256PtFromBytes": 8, // the release build splits the public curve-equation test
	// Both curves' tests, out's length, the peer point's answer, the
	// secret's range (js, ptest), the identity test, the Wipe of x.
	"mk.std.crypto.EcdhShared": 18,
	// The curve, the digest's length (empty, at least 32), the secret's
	// range (js, ptest), the format, and the inlined Wipes of the nonce,
	// K and V. P256nDigest is out of line in both builds and its
	// length check is counted there. Each of the
	// three inlined Wipes (the nonce's 32 bytes, K's and V's 64) ends in
	// its byte tail, laid out in line and unrolled by two, with forward
	// exits and an unconditional jmp back, so its index tests are not
	// back edges: cmpl $0x1f; ja, cmpq $0x1f; je, cmpq $0x1e; je for the
	// nonce, and cmpl $0x3f; ja, cmpq $0x3f; je, cmpq $0x3e; je for K and
	// V. The index comes from constants (the nonce) or from the buffer's
	// stack address (Wipe's alignment head: leaq; notl; andl $7) plus a
	// constant; the tails only store zero, and no compare reads a byte of
	// the buffer, the key or the digest.
	"mk.std.crypto.EcdsaSign": 39,
	// The hash kind and the inlined HMAC constructors' tests of the
	// public key length against each hash's block size.
	"mk.std.crypto.DrbgMac":   5,
	"mk.std.crypto.Drbg:New":  12,
	"mk.std.crypto.Drbg:Next": 12, // no build reads more than 12
	// The signature's encoding: format, out's length and the leading zero
	// bytes of r and s, which are the public output.
	"mk.std.crypto.SigEncode": 7, // release body only; PGO reads it in EcdsaSign
	"mk.std.crypto.DerIntLen": 2,
	"mk.std.crypto.PutDerInt": 4,
	// No other jump: the field and group arithmetic and its helpers.
	"mk.std.crypto.P256Add":             0,
	"mk.std.crypto.P256Sub":             0,
	"mk.std.crypto.P256Opp":             0,
	"mk.std.crypto.P256Mul":             0,
	"mk.std.crypto.P256Square":          0,
	"mk.std.crypto.P256FromMontgomery":  0,
	"mk.std.crypto.P256ToMontgomery":    0,
	"mk.std.crypto.P256Nonzero":         0,
	"mk.std.crypto.P256Selectznz":       0,
	"mk.std.crypto.P256Cmovznz":         0,
	"mk.std.crypto.P256SetOne":          0,
	"mk.std.crypto.P256Encode":          0,
	"mk.std.crypto.P256Limbs":           0,
	"mk.std.crypto.P256Invert":          0,
	"mk.std.crypto.P256Reduce":          0,
	"mk.std.crypto.P256IsZero":          0,
	"mk.std.crypto.P256Equal":           0,
	"mk.std.crypto.P256Below":           0,
	"mk.std.crypto.P256nAdd":            0,
	"mk.std.crypto.P256nSub":            0,
	"mk.std.crypto.P256nOpp":            0,
	"mk.std.crypto.P256nMul":            0,
	"mk.std.crypto.P256nSquare":         0,
	"mk.std.crypto.P256nFromMontgomery": 0,
	"mk.std.crypto.P256nToMontgomery":   0,
	"mk.std.crypto.P256nNonzero":        0,
	"mk.std.crypto.P256nSelectznz":      0,
	"mk.std.crypto.P256nCmovznz":        0,
	"mk.std.crypto.P256nSetOne":         0,
	"mk.std.crypto.P256nEncode":         0,
	"mk.std.crypto.P256nLimbs":          0,
	"mk.std.crypto.P256nInvert":         0,
	"mk.std.crypto.P256nReduce":         0,
	"mk.std.crypto.P256nIsZero":         0,
	"mk.std.crypto.P256nEqual":          0,
	"mk.std.crypto.P256nBelow":          0,
	"mk.std.crypto.P256PtAdd":           0,
	"mk.std.crypto.P256PtDouble":        0,
	"mk.std.crypto.P256PtLookup":        0,
	"mk.std.crypto.P256PtTable":         0,
	"mk.std.crypto.P256PtSelect":        0,
	// The comb (k G for Public and EcdsaSign), read in both builds, all
	// three out of line: no other jump (PtBaseMul's skip
	// of the first doubling became a loop edge; the checks are bounds
	// checks on public indices).
	"mk.std.crypto.P256PtBaseMul":  0,
	"mk.std.crypto.P256CombLookup": 0,
	"mk.std.crypto.P256CombBits":   0,
	// P256nDigest: the digest's length against 32, selecting the copy.
	"mk.std.crypto.P256nDigest":   1,
	"mk.std.crypto.FiatAdd64":     0,
	"mk.std.crypto.FiatSub64":     0,
	"mk.std.crypto.FiatSubBorrow": 0,
	"mk.std.crypto.NonceHash":     0,
	"mk.std.crypto.Be64":          0,
	"mk.std.crypto.PutBe64":       0,
}

// cr2EcCtByBuild is this test's per-build counts (ctByBuild): each function
// whose two builds read different counts, at the count each build has (from
// the test's -v log), every one at or below the reading above.
var cr2EcCtByBuild = ctByBuild{
	"mk.main.Main":                    {"release": 32},
	"mk.main.ParseSign":               {"release-pgo": 3},
	"mk.std.crypto.Drbg:New":          {"release-pgo": 4},
	"mk.std.crypto.Drbg:Next":         {"release-pgo": 4},
	"mk.std.crypto.EcSecret:Generate": {"release-pgo": 2},
	"mk.std.crypto.EcSecret:Wipe":     {"release-pgo": 4},
	"mk.std.crypto.EcdhShared":        {"release-pgo": 6},
	"mk.std.crypto.EcdsaSign":         {"release-pgo": 21},
	"mk.std.crypto.KeyAlgorithm":      {"release-pgo": 23},
	"mk.std.crypto.KeyEc":             {"release": 20},
	"mk.std.crypto.P256PtFromBytes":   {"release-pgo": 5},
	"mk.std.crypto.P256PtXBytes":      {"release": 0},
	"mk.std.crypto.KeyUint":           {"release-pgo": 8},
	"mk.std.crypto.ParsePrivateKey":   {"release": 12},
	"mk.std.crypto.SigEncode":         {"release-pgo": 0},
	"mk.std.simd.kernel.Wipe":         {"release": 12},
}

// cr2EcCtBack is this test's ctBackTable: exact per-build counts of
// non-induction back edges (ctClassifyBack [2]), each read as public or
// rejection-sampling outcome (never secret).
var cr2EcCtBack = ctBackTable{
	"mk.main.Main":                    {"release": 6},                     // try error-tag jumps to Main's shared error block
	"mk.std.crypto.DerIntLen":         {"release-pgo": 1},                 // leading zero-byte skip on public signature (r, s)
	"mk.std.crypto.Drbg:New":          {"release-pgo": 6},                 // inlined Wipe: 5 alignment-head tests (addr & 7), 1 tail length test
	"mk.std.crypto.Drbg:Next":         {"release-pgo": 6},                 // inlined Wipe: 5 alignment-head tests (addr & 7), 1 tail length test
	"mk.std.crypto.EcSecret:Generate": {"release": 3, "release-pgo": 1},   // Random error tag and rejection loop (b >= n, b == 0; public outcome)
	"mk.std.crypto.EcSecret:Wipe":     {"release-pgo": 6},                 // inlined Wipe: 5 alignment-head tests (addr & 7), 1 tail length test
	"mk.std.crypto.EcdsaSign":         {"release": 4, "release-pgo": 22},  // 4 rejection-loop checks (k, r, s; public outcome); PGO adds 3 inlined Wipes (18)
	"mk.std.crypto.KeyAlgorithm":      {"release": 4, "release-pgo": 30},  // public DER AlgorithmIdentifier OID bytes, tags, and parameter presence
	"mk.std.crypto.KeyEc":             {"release": 51, "release-pgo": 3},  // public DER structure, OIDs, curve, and single post-loop pad != 0 error test
	"mk.std.crypto.KeyUint":           {"release-pgo": 2},                 // public DER INTEGER minimal encoding and sign checks
	"mk.std.crypto.ParsePrivateKey":   {"release": 34, "release-pgo": 11}, // public DER TLV parsing, version, OIDs, and algorithm dispatch
	"mk.std.crypto.PutDerInt":         {"release-pgo": 2},                 // leading zero-byte skips on public signature (r, s)
	"mk.std.simd.kernel.Wipe":         {"release": 1, "release-pgo": 1},   // word loop i + 8 <= n on buffer length
}

// cr2EcCtAbsent are the cr2EcCtAllow functions a build of this probe
// holds no body of, by build, each inlined into a checked caller
// (ctRequirePresent). The release build's
// crypto.Wipe is among them: its callers call kernel.Wipe, which the test
// reads.
var cr2EcCtAbsent = map[string][]string{
	"mk.std.crypto.Be64":           {"*"},
	"mk.std.crypto.DerIntLen":      {"release"},
	"mk.std.crypto.EcP256Valid":    {"release"},
	"mk.std.crypto.FiatAdd64":      {"*"},
	"mk.std.crypto.FiatSub64":      {"*"},
	"mk.std.crypto.FiatSubBorrow":  {"*"},
	"mk.std.crypto.KeyBits":        {"*"},
	"mk.std.crypto.KeyIs":          {"*"},
	"mk.std.crypto.KeySame":        {"*"},
	"mk.std.crypto.NonceHash":      {"*"},
	"mk.std.crypto.P256Add":        {"release"},
	"mk.std.crypto.P256Below":      {"*"},
	"mk.std.crypto.P256Cmovznz":    {"*"},
	"mk.std.crypto.P256Equal":      {"*"},
	"mk.std.crypto.P256IsZero":     {"*"},
	"mk.std.crypto.P256Limbs":      {"*"},
	"mk.std.crypto.P256Nonzero":    {"*"},
	"mk.std.crypto.P256Opp":        {"*"},
	"mk.std.crypto.P256Reduce":     {"*"},
	"mk.std.crypto.P256Selectznz":  {"*"},
	"mk.std.crypto.P256SetOne":     {"*"},
	"mk.std.crypto.P256Sub":        {"release"},
	"mk.std.crypto.P256nAdd":       {"*"},
	"mk.std.crypto.P256nBelow":     {"*"},
	"mk.std.crypto.P256nCmovznz":   {"*"},
	"mk.std.crypto.P256nDecode":    {"*"},
	"mk.std.crypto.P256nEqual":     {"*"},
	"mk.std.crypto.P256nIsZero":    {"*"},
	"mk.std.crypto.P256nNonzero":   {"*"},
	"mk.std.crypto.P256nOpp":       {"*"},
	"mk.std.crypto.P256nReduce":    {"*"},
	"mk.std.crypto.P256nSelectznz": {"*"},
	"mk.std.crypto.P256nSetOne":    {"*"},
	"mk.std.crypto.P256nSub":       {"*"},
	"mk.std.crypto.PutBe64":        {"*"},
	"mk.std.crypto.Random":         {"*"},
	"mk.std.crypto.SigEncode":      {"release-pgo"}, // EcdsaSign
	"mk.std.crypto.Wipe":           {"*"},

	"mk.std.crypto.P256FromMontgomery":  {"*"},       // P256Encode
	"mk.std.crypto.P256PtBytes":         {"*"},       // EcSecret:Public
	"mk.std.crypto.P256PtSelect":        {"*"},       // P256PtLookup
	"mk.std.crypto.P256PtXBytes":        {"release"}, // EcdhShared and EcdsaSign
	"mk.std.crypto.P256ToMontgomery":    {"*"},       // P256Decode
	"mk.std.crypto.P256nFromMontgomery": {"*"},       // P256nEncode
}

// TestSTDCRYPTO_EcConstantTimeDisassembly runs std-crypto's classifier
// (ctParse, ctClassify) over a probe that reaches EcSecret.FromBytes,
// Public, EcdhShared, EcdsaSign (both formats, the three RFC 6979 hashes),
// Wipe, ParsePrivateKey (a SEC 1 key and an Ed25519 PKCS#8 seed) and
// EcSecret.Generate, with a secret taken from argv or drawn, built for
// x86_64-linux (release, symbols kept; built only, never run under WSL)
// without and with a profile collected natively. The curves have no
// instruction path, so the AES mask's builds add nothing here. Every
// conditional jump of every std.crypto function in the probe and of Main is
// classified; an "other" jump beyond the function's reading, or a function
// the table does not list, fails with the instructions to read. It skips
// without llvm-objdump or the LLVM toolchain.
func TestSTDCRYPTO_EcConstantTimeDisassembly(t *testing.T) {
	objdump := cryptoObjdump()
	if objdump == "" {
		t.Skip("needs llvm-objdump (LLVM 21)")
	}
	for _, pgo := range []bool{false, true} {
		name := "release"
		if pgo {
			name = "release-pgo"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ins, into := cr2CtBuildSrc(t, objdump, cr2EcCtProbe, nil, pgo)
			other, back, counts := ctClassifyBack(ins)
			seen := map[string]bool{}
			for _, x := range ins {
				seen[x.fn] = true
			}
			for _, fn := range []string{"mk.std.crypto.P256PtMul", "mk.std.crypto.P256PtAdd"} {
				if !seen[fn] {
					t.Errorf("%s is not in the %s build: the probe no longer reaches it out of line, so its jumps went unread", fn, name)
				}
			}
			ctRequirePresent(t, "cr2EcCtAllow", name, cr2EcCtAllow, seen, cr2EcCtAbsent, cr2EcCtUnrecorded, into, cr2EcCtChecked)
			// No secret path's build holds verification's variable-time
			// arithmetic (the Vt functions), out of line or inlined into
			// any function; TestSTDCRYPTO_VtOnlyFromVerify reads the
			// sources for the same rule.
			for fn := range seen {
				if cr2VtSymbol.MatchString(fn) {
					t.Errorf("%s is in the %s build of the secret paths' probe: a secret path reaches verification's variable-time arithmetic", fn, name)
				}
			}
			for fn, holders := range into {
				if cr2VtSymbol.MatchString(fn) {
					t.Errorf("%s is inlined into %v in the %s build of the secret paths' probe: a secret path reaches verification's variable-time arithmetic", fn, slices.Sorted(maps.Keys(holders)), name)
				}
			}
			var names, read []string
			for fn := range seen {
				// kernel.Wipe: the release build calls it where main
				// calls crypto.Wipe.
				if cr2EcCtChecked(fn) {
					names = append(names, fn)
				}
			}
			sort.Strings(names)
			for _, fn := range names {
				allowed, known := cr2EcCtAllow[cr2CtInstance.ReplaceAllString(fn, "")]
				if !known {
					allowed, known = cr2CtAllow[cr2CtInstance.ReplaceAllString(fn, "")]
				}
				if !known {
					allowed, known = ctAllow[fn]
				}
				if !known && ctPublic[fn] {
					continue
				}
				if !known {
					t.Errorf("%s (%s build) is on the secret path and unread: %d other jumps\n    %s", fn, name, len(other[fn]), strings.Join(other[fn], "\n    "))
					continue
				}
				allowed = ctAllowed(allowed, cr2EcCtByBuild, fn, name)
				if n := len(other[fn]); n > allowed {
					t.Errorf("%s (%s build) has %d conditional jumps that neither guard a panic nor close a loop, %d allowed; read each (is it a secret?) and record the reading:\n    %s",
						fn, name, n, allowed, strings.Join(other[fn], "\n    "))
				}
				read = append(read, fn)
				t.Logf("%s: %d checks, %d loop edges (%d on a compare that is not an induction, %d allowed), %d other (%d allowed)", fn, counts[fn][0], counts[fn][1], counts[fn][2], cr2EcCtBack[cr2CtInstance.ReplaceAllString(fn, "")][name], len(other[fn]), allowed)
			}
			ctBackCheck(t, "cr2EcCtBack", cr2EcCtBack, name, back, read)
		})
	}
}

// cr2CtBuildSrc builds src for x86_64-linux (release, symbols kept) over
// the crypto std copy with forces, with a natively collected profile when
// pgo, and returns its disassembly (cr2CtBuild's steps, for another
// probe).
func cr2CtBuildSrc(t *testing.T, objdump, src string, forces []sdForce, pgo bool) ([]ctInsn, ctInlinedInto) {
	t.Helper()
	root := t.TempDir()
	stdDir := cryptoStdDir(t, root, sdFixtures(t, forces))
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(src), 0o644); err != nil {
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

// cr2EcCtChecked reports whether TestSTDCRYPTO_EcConstantTimeDisassembly
// reads fn: the probe's functions (ctProbeFn: Main and ParseSign),
// std.crypto's functions and the kernel's Wipe (its callers call it where
// Main called crypto.Wipe).
func cr2EcCtChecked(fn string) bool {
	return ctProbeFn(fn) || strings.HasPrefix(fn, "mk.std.crypto.") || fn == "mk.std.simd.kernel.Wipe"
}

// cr2EcCtUnrecorded are the cr2EcCtAbsent functions whose build's DWARF
// records no copy, by build (ctRequirePresent):
// neither an out-of-line body nor an inlined copy in either build, so the
// probe does not reach them, or reaches an inlined copy whose debug
// locations the optimizer dropped; their allowances cover no body read
// here.
var cr2EcCtUnrecorded = map[string][]string{
	// Only the discarded out-of-line encoders retain the debug record.
	// This limb OR is part of P256IsZero; the encoders' live callers
	// cover the identity test in EcSecret:Public, EcdhShared and EcdsaSign.
	"mk.std.crypto.P256Nonzero":  {"release"},
	"mk.std.crypto.P256Opp":      {"*"},
	"mk.std.crypto.P256Reduce":   {"*"},
	"mk.std.crypto.P256SetOne":   {"*"},
	"mk.std.crypto.P256nBelow":   {"*"},
	"mk.std.crypto.P256nEqual":   {"*"},
	"mk.std.crypto.P256nNonzero": {"*"},
	"mk.std.crypto.P256nOpp":     {"*"},
	"mk.std.crypto.P256nSetOne":  {"*"},
	"mk.std.crypto.P256nSub":     {"*"},
}
