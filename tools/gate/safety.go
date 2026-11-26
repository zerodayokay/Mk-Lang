// Why: every parser of untrusted input must have a fuzz target that runs and
// passes, and every cancel-safety test must pass; a target or test that is
// renamed or skipped must fail the gate, not vanish from it.
//
// What: two lists as data, untrusted (entry points, each with the fuzz
// targets that stand for it) and cancelSafety (cancel-safety tests), with
// untrustedStep, cancelStep and their judgements, plus mathTests and
// mathStep for the exhaustive f32 checksums.
//
// How: the fuzz step runs every target for the tier's time (5 minutes quick,
// 10 full, an hour soak); untrustedStep then checks that each listed target
// exists and passed, and cancelStep that each listed test passed in the test
// step or, for a Linux one, in the Linux pass. A removed unit's rows leave
// with it.

package main

import (
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
)

// untrustedEntry is one untrusted-input entry point and the fuzz targets
// (package ./std unless untrustedTargetPkg names another; they run on
// Windows only) that cover it, and the std unit that owns it; no targets
// means a target is still owed.
type untrustedEntry struct {
	entry   string
	owner   string
	targets []string
}

// untrusted is the list of untrusted-input entry points.
var untrusted = []untrustedEntry{
	{"url.Parse and the URL setters", "std.url", []string{"FuzzStdUrl_Parse", "FuzzStdUrl_Set", "FuzzStdUrl_ParseQuery", "FuzzStdUrl_DomainToAscii"}},
	{"the HTTP request parser", "std.http", []string{"FuzzHttp_ReadRequest", "FuzzStdHttp_ServeWith"}},
	{"the HTTP response parser", "std.http", []string{"FuzzStdHttp_ClientReply"}},
	{"WebSocket frames", "std.websocket", []string{"FuzzStdWs_Frames", "FuzzStdWs_Request", "FuzzStdWs_Reply"}},
	{"Unzlib and the zlib stream", "std.compress", []string{"FuzzCompress_Unzlib", "FuzzCompress_ZlibStream"}},
	{"the zstd decoder", "std.compress", []string{"FuzzCompress_Zstd", "FuzzCompress_ZstdWrap"}},
	{"the deflate and gzip Inflater stream", "std.compress", []string{"FuzzSTDCOMPRESS_InflaterStream"}},
	{"Encoding.Decode", "std.encoding", []string{"FuzzStdEncoding_Decode", "FuzzStdEncodingCjk_RoundTrip"}},
	{"base64 and hex decoding", "std.encoding", []string{"FuzzStdEncoding_Base64", "FuzzStdEncoding_Hex"}},
	{"the DER reader", "std.encoding", []string{"FuzzStdEncoding_Der"}},
	{"encoding.ParsePem", "std.encoding", []string{"FuzzStdEncoding_Pem"}},
	{"the TZif reader", "std.time", []string{"FuzzStdTime_Tzif"}},
	{"time.ParseRfc3339", "std.time", []string{"FuzzStdTime_Rfc3339"}},
	{"ParseHttpDate", "std.time", []string{"FuzzStdTime_HttpDate"}},
	{"ParseDuration", "std.time", []string{"FuzzStdTime_Duration"}},
	{"the terminal input decoder", "std.term", []string{"FuzzTerm_Parse", "FuzzTerm_Read"}},
	{"the CSV reader", "std.csv", []string{"FuzzSTDCSV_Records"}},
	{"big.Parse", "std.big", []string{"FuzzBig_Parse"}},
	{"x509.Parse and x509.ParsePem", "std.x509", []string{"FuzzX509_Parse", "FuzzX509_ParsePem", "FuzzX509_Verify"}},
	{"the TLS record and handshake parsers", "std.tls", []string{"FuzzStdTls_Messages", "FuzzStdTls_Records", "FuzzStdTls_Logic", "FuzzStdTls_Messages12", "FuzzStdTls_Records12", "FuzzStdTls_QuicHandshake"}},
	{"the QUIC packet, frame and parameter parsers", "std.quic", []string{"FuzzStdQuic_Packets", "FuzzStdQuic_Frames", "FuzzStdQuic_Params"}},
	{"key parsing", "std.crypto", []string{"FuzzSTDCRYPTO_Keys"}},
	{"the HPACK decoder and Huffman decoding", "std.http.fields", []string{"FuzzStdHttpFields_Hpack", "FuzzStdHttpFields_Huffman"}},
	{"the QPACK decoder and encoder-stream instructions", "std.http.fields", []string{"FuzzStdHttpFields_Qpack"}},
	{"HTTP field validation", "std.http.fields", []string{"FuzzStdHttpFields_Check"}},
	{"the HTTP/2 frame and SETTINGS parsers", "std.http.h2", []string{"FuzzStdHttpH2_Frames"}},
	{"QUIC ACK range and recovery state", "std.quic", []string{"FuzzStdQuic_Ack"}},
	{"QUIC endpoint demux, Retry token and stateless reset", "std.quic", []string{"FuzzStdQuic_Endpoint"}},
	// Further entry points held to the same bar (their fuzz files say so).
	{"net.ParseAddr", "std.net", []string{"FuzzStdNet_ParseAddr"}},
	{"AesGcm.Open, HKDF and HMAC on peer bytes", "std.crypto", []string{"FuzzSTDCRYPTO_TlsPrimitives"}},
	{"encoding.PercentDecode", "std.encoding", []string{"FuzzStdEncoding_Percent"}},
	{"text segmentation's bounds", "std.text", []string{"FuzzSTDTEXT_Bounds"}},
	{"term.Sanitize of untrusted text", "std.term", []string{"FuzzTerm_Sanitize"}},
	// The git client's network input, whose targets live in ./internal/git
	// (untrustedTargetPkg).
	{"the git pkt-line reader and protocol replies", "internal/git", []string{"FuzzPktLine"}},
	{"the git pack reader", "internal/git", []string{"FuzzPack", "FuzzPackRecords"}},
	{"git delta application", "internal/git", []string{"FuzzDelta"}},
	{"the git tree parser and walk", "internal/git", []string{"FuzzTree"}},
}

// untrustedFuzzExempt are the Fuzz functions of std's untrusted-input fuzz
// files (a file whose text holds the marker TestFuzzTargetsListed
// scans for) that stand for no entry point of the
// list, each with the reason; TestFuzzTargetsListed holds every
// other one to a row of untrusted.
var untrustedFuzzExempt = map[string]string{}

// untrustedPkg is the package of every untrusted-input target not named in
// untrustedTargetPkg.
const untrustedPkg = "./std"

// untrustedTargetPkg names the package of each listed target outside
// ./std (the git client's targets in ./internal/git).
var untrustedTargetPkg = map[string]string{
	"FuzzPktLine":     "./internal/git",
	"FuzzPack":        "./internal/git",
	"FuzzPackRecords": "./internal/git",
	"FuzzDelta":       "./internal/git",
	"FuzzTree":        "./internal/git",
}

// untrustedPkgOf is the package of the listed target t.
func untrustedPkgOf(t string) string {
	if p, ok := untrustedTargetPkg[t]; ok {
		return p
	}
	return untrustedPkg
}

// untrustedPkgs are the packages that hold listed targets.
func untrustedPkgs() []string {
	out := []string{untrustedPkg}
	for _, p := range untrustedTargetPkg {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// cancelTest is one cancel-safety test: a top-level Go test, the unit that
// owns its rows of the table, and whether it is a Linux leaf
// (MK_LINUX=1, run by the Linux pass under the WSL lock).
type cancelTest struct {
	pkg, test, owner string
	linux            bool
}

// cancelSafety is the list of cancel-safety tests.
var cancelSafety = []cancelTest{
	{"./std", "TestCore_CancelClasses", "std.core", false},
	{"./std", "TestCore_CancelIsLoadBearing", "std.core", false},
	{"./std", "TestCore_LinuxBlocks", "std.core", true},
	{"./std", "TestStdUrl_FormatCancel", "std.url", false},
	{"./std", "TestStdUrl_FormatCancelIsLoadBearing", "std.url", false},
	{"./std", "TestStdEncoding_PemCancel", "std.encoding", false},
	{"./std", "TestStdEncoding_PemCancelIsLoadBearing", "std.encoding", false},
	{"./std", "TestFs_SimCancel", "std.fs", false},
	{"./std", "TestStdNet_SimCancel", "std.net", false},
	{"./std", "TestStdNet_SimCancelIsLoadBearing", "std.net", false},
	{"./std", "TestStdTime_CancelRepeatable", "std.time", false},
	{"./std", "TestStdTime_Linux", "std.time", true},
	{"./std", "TestStdLog_ReportWhileCanceled", "std.log", false},
	{"./std", "TestStdLog_CancelWhileParked", "std.log", false},
	{"./std", "TestRt_NotifyWaitSim", "std.rt", false},
	{"./std", "TestStdNet_SimSweep", "std.net", false},
	{"./std", "TestStdNet_SimSweepIsLoadBearing", "std.net", false},
	{"./std", "TestStdNet_Blocks", "std.net", false},
	{"./std", "TestStdNet_LinuxBlocks", "std.net", true},
	{"./std", "TestStdNet_LinuxSim", "std.net", true},
	{"./std", "TestCompress_MkTestBlocks", "std.compress", false},
	{"./std", "TestCompress_ZstdMkTestBlocks", "std.compress", false},
	{"./std", "TestSTDCRYPTO_EcGenerateRepeatable", "std.crypto", false},
	{"./std", "TestProc_SpawnBlocks", "std.proc", false},
	{"./std", "TestProc_SpawnLinuxBlocks", "std.proc", true},
	{"./std", "TestX509_CancelClasses", "std.x509", false},
	{"./std", "TestX509_CancelEachAwait", "std.x509", false},
	{"./std", "TestX509_SystemPoolBlocks", "std.x509", false},
	{"./std", "TestStdTls_Tls12SimCancel", "std.tls", false},
	{"./std", "TestStdWs_LinuxBlocks", "std.websocket", true},
	{"./std", "TestStdTls_SimCancelLinux", "std.tls", true},
	{"./std", "TestStdTls_SimCancel", "std.tls", false},
	{"./std", "TestStdTls_GoPeerReadRepeatable", "std.tls", false},
	{"./std", "TestStdTls_PairCases", "std.tls", false},
	{"./std", "TestX509_SystemRootsLinksLinux", "std.x509", true},
	{"./std", "TestStdWs_Blocks", "std.websocket", false},
	{"./std", "TestX509_SystemRootsLinux", "std.x509", true},
	{"./std", "TestStdSyncLead_PctCancel", "std.sync.lead", false},
	{"./std", "TestStdQuic_SimCancel", "std.quic", false},
}

// cancelExempt are the top-level tests of std that the cancel-safety scan
// finds (their doc comment, or their file's header, holds the marker
// TestCancelSafetyTestsListed scans for) but that test no row, each with the
// reason; TestCancelSafetyTestsListed holds every
// other one to a row of cancelSafety.
var cancelExempt = map[string]string{
	"TestCore_ErasureBlocks":       "std.core's cancel-safety blocks are in core_cancel_test.go; this file's header predates the move",
	"TestCore_ErasureRejects":      "erasure's compile-time rejections, no cancel",
	"TestCore_LoanRejects":         "erasure's loan rejections, no cancel",
	"TestCore_TypeMismatchPanics":  "erasure's type check, no cancel",
	"TestCore_CappedAgainstModel":  "the capped constructors against a model, no cancel",
	"TestCore_StressSeeds":         "its file's header sends the cancel-safety tests to core_cancel_test.go",
	"TestCore_ErasureUnderSim":     "its file's header sends the cancel-safety tests to core_cancel_test.go",
	"TestCore_ErasedCallAlignment": "its file's header sends the cancel-safety tests to core_cancel_test.go",
	"TestCore_SendShareBounds":     "its file's header sends the cancel-safety tests to core_cancel_test.go",
	"TestCore_BenchProbeBuilds":    "its file's header sends the cancel-safety tests to core_cancel_test.go",
	"TestProc_SpawnBlockNames":     "checks the block names of std.proc's runner, runs no block",
	"TestCore_FillFullBoxPanics":   "ErasedBox.Fill's panic on a full box, no cancel; core_erasure_test.go's header sends the cancel-safety tests to core_cancel_test.go",
	"TestCore_FillRejects":         "ErasedBox.Fill's compile-time rejections, no cancel; core_erasure_test.go's header sends the cancel-safety tests to core_cancel_test.go",
}

// untrustedStep judges the Untrusted input row after the fuzz step: a
// listed target the fuzz step did not find fails every tier (renamed or
// removed); an entry with no target yet, and a target the quick tier did
// not select, are notes in the quick tier and failures in the others,
// where every target runs.
func (g *gate) untrustedStep() {
	const name = "untrusted input"
	switch {
	case g.skip["fuzz"]:
		g.skipStep(name, "-skip fuzz")
		return
	case !slices.ContainsFunc(untrustedPkgs(), g.selected):
		g.skipStep(name, "-only leaves out "+strings.Join(untrustedPkgs(), " and "))
		return
	case g.fuzzFound == nil:
		g.skipStep(name, "the fuzz step found no targets (see its selection step)")
		return
	}
	s := g.newStep(name)
	judgeUntrusted(s, g.tier, g.fuzzFound, g.fuzzPassed)
}

// judgeUntrusted is untrustedStep's judgement over found (the targets the
// fuzz step listed, "pkg name") and passed (the targets it ran: whether
// each passed).
func judgeUntrusted(s *Step, tier string, found []string, passed map[string]bool) {
	strict := tier != "quick"
	owed, ran := 0, 0
	for _, e := range untrusted {
		if len(e.targets) == 0 {
			owed++
			msg := fmt.Sprintf("%s: no fuzz target yet (owed by %s, or the unit is cut and this row goes with it)", e.entry, e.owner)
			if strict {
				fail(s, msg)
				s.Failed = append(s.Failed, e.entry)
			}
			s.Notes = append(s.Notes, msg)
			continue
		}
		for _, t := range e.targets {
			key := untrustedPkgOf(t) + " " + t
			ok, didRun := passed[key]
			switch {
			case !slices.Contains(found, key):
				fail(s, fmt.Sprintf("%s: %s is not a fuzz target of %s (renamed or removed; %s's row here names it)", e.entry, t, untrustedPkgOf(t), e.owner))
				s.Failed = append(s.Failed, key)
			case !didRun && strict:
				fail(s, fmt.Sprintf("%s: %s did not run", e.entry, t))
				s.Failed = append(s.Failed, key)
			case !didRun:
				s.Notes = append(s.Notes, fmt.Sprintf("%s: %s not run in this tier (its inputs did not change)", e.entry, t))
			case !ok:
				fail(s, fmt.Sprintf("%s: %s failed (its fuzz step has the crasher)", e.entry, t))
				s.Failed = append(s.Failed, key)
			default:
				ran++
				s.Notes = append(s.Notes, fmt.Sprintf("%s: %s passed", e.entry, t))
			}
		}
	}
	if s.Status != "fail" {
		s.Reason = fmt.Sprintf("%d targets passed, %d entry points owe one", ran, owed)
	}
}

// cancelStep judges the cancel-safety tests from the test step's and the Linux
// pass's results: each must have passed. A Linux test that only skipped
// (the Linux pass did not run here) is a note in the quick tier and a
// failure in the others.
func (g *gate) cancelStep() {
	const name = "deadlines: cancel-safety"
	if g.skip["test"] || !g.testRan {
		g.skipStep(name, "the test step did not run")
		return
	}
	s := g.newStep(name)
	strict := g.tier != "quick" && !g.skip["linux"]
	judgeCancel(s, strict, g.selected, g.testResults, g.ended)
}

// judgeCancel is cancelStep's judgement over results ("pkg test" to pass,
// skip or fail, the last step's outcome winning). A Linux leaf that only
// skipped fails when strictLinux (a full or soak tier that did not
// -skip linux) and is a note otherwise; a Windows test that skipped
// always fails. A test with no result whose package ended mid-run (ended:
// package to why, "timed out after 4h0m0s in the test step") is not run,
// not missing, and so is a Linux leaf that kept the test step's skip
// because its package ended mid-run in the Linux step, in every tier: the
// step fails with one reason per package that names its end, before any
// other.
func judgeCancel(s *Step, strictLinux bool, selected func(string) bool, results map[string]string, ended map[string]pkgEnd) {
	passed := 0
	notRun := map[string]int{}
	for _, c := range cancelSafety {
		key := c.pkg + " " + c.test
		if !selected(c.pkg) {
			s.Notes = append(s.Notes, key+": -only leaves out "+c.pkg)
			continue
		}
		switch r := results[key]; {
		case r == "pass":
			passed++
		case r == "skip" && c.linux && ended[c.pkg].linux:
			notRun[c.pkg]++
			s.Failed = append(s.Failed, key)
		case r == "skip" && c.linux && !strictLinux:
			s.Notes = append(s.Notes, key+": a Linux leaf, not verified (the Linux pass did not run it)")
		case r == "skip":
			fail(s, key+" skipped")
			s.Failed = append(s.Failed, key)
		case r == "fail":
			fail(s, key+" failed (the test step has its output)")
			s.Failed = append(s.Failed, key)
		case ended[c.pkg].why != "":
			notRun[c.pkg]++
			s.Failed = append(s.Failed, key)
		default:
			fail(s, fmt.Sprintf("%s did not run: no such test (renamed or removed; %s's row here names it)", key, c.owner))
			s.Failed = append(s.Failed, key)
		}
	}
	if len(notRun) > 0 {
		var msgs []string
		for pkg, n := range notRun {
			msgs = append(msgs, fmt.Sprintf("%d of %d listed tests not run because %s %s", n, len(cancelSafety), pkg, ended[pkg].why))
		}
		sort.Strings(msgs)
		msg := strings.Join(msgs, "; ")
		s.Notes = append(s.Notes, msgs...)
		if s.Status == "fail" && s.Reason != "" {
			msg += "; " + s.Reason
		}
		s.Status, s.Reason = "fail", msg
	}
	if s.Status != "fail" {
		s.Reason = fmt.Sprintf("%d of %d tests passed", passed, len(cancelSafety))
	}
}

// mathTests are the exhaustive f32 checksum tests of std.math: the Mk side,
// over every f32 input, checked against recorded sums, on Windows and, as a
// Linux leaf, on Linux.
var mathTests = []struct {
	test  string
	linux bool
}{
	{"TestSTDMATH_Exhaustive", false},
	{"TestSTDMATH_ExhaustiveLinux", true},
}

// mathStep runs mathTests with MK_MATH_EXHAUSTIVE=verify in the full and
// soak tiers (minutes per target, so the quick tier skips them),
// the Linux one under the WSL lock with -p 1; each must pass.
func (g *gate) mathStep() {
	const name = "math checksums (MK_MATH_EXHAUSTIVE=verify)"
	switch {
	case g.tier == "quick":
		g.skipStep(name, "the full and soak tiers run it")
		return
	case g.skip["test"]:
		g.skipStep(name, "-skip test")
		return
	case !g.selected("./std"):
		g.skipStep(name, "-only leaves out ./std")
		return
	}
	s := g.newStep(name)
	for _, m := range mathTests {
		set := []string{"MK_GATE=1", "MK_MATH_EXHAUSTIVE=verify"}
		flags := g.testFlags(pkgTimeout) // one test each: the hour, not -stdtimeout
		lock := false
		if m.linux {
			if g.skip["linux"] {
				s.Notes = append(s.Notes, m.test+": not run (-skip linux)")
				continue
			}
			if runtime.GOOS != "windows" || g.pwsh == "" || !g.wslAnswers() {
				fail(s, m.test+": not verified: the Linux pass cannot run here")
				continue
			}
			set = append(set, "MK_LINUX=1")
			flags = append(flags, "-p", "1")
			lock = true
		}
		argv := append(append([]string{g.goExe}, flags...), "-run", "^"+m.test+"$", "./std")
		g.goTest(s, spec{argv: argv, set: set, lock: lock, tripwire: !lock})
		if r := g.testResults["./std "+m.test]; r != "pass" {
			fail(s, fmt.Sprintf("%s: %s, want pass", m.test, orNone(r)))
		}
	}
}

func orNone(r string) string {
	if r == "" {
		return "no result"
	}
	return r
}
