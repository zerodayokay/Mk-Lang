// Why: mk test, mk test --fuzz and mk bench share one runner, and differ only
// in which tests they select and how each test's child runs.
//
// What: the std.testing environment variables, FuzzDirName, selectTest,
// testText, runTestChild, which picks the child's mode, and
// judgeExpectedPanic.
//
// How: a selected test runs as a plain child (testexec.go's runChild), given
// its fuzz corpus when testdata/fuzz/<test name>/ holds one, under the
// mutation loop's watcher (fuzz.go), or as a benchmark's repetitions
// (bench.go). std.testing (std/testing/testing.mk) is the child's half; the
// environment variables are the protocol between them, with the lines the
// child writes back: mkbench's, and ExpectPanic's, which carries the run's
// token so that no text a test prints passes for it.

package driver

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"mk/internal/toolchain"
)

// The environment of a test child that std.testing reads.
const (
	TestingFuzzEnv  = "MK_TESTING_FUZZ"
	TestingInputEnv = "MK_TESTING_INPUT"
	TestingStateEnv = "MK_TESTING_STATE"
	TestingSeedEnv  = "MK_TESTING_SEED"
	TestingItersEnv = "MK_TESTING_ITERS"
	TestingNanosEnv = "MK_TESTING_NANOS"
	TestingBenchEnv = "MK_TESTING_BENCH"
	// TestingExpectEnv is the run's token, which testing.ExpectPanic's line
	// carries; a plain child's alone (judgeExpectedPanic).
	TestingExpectEnv = "MK_TESTING_EXPECT"
)

// testingEnvNames are every variable above: a child gets only the ones its
// mode sets, never one inherited from mk's own environment.
var testingEnvNames = []string{TestingFuzzEnv, TestingInputEnv, TestingStateEnv, TestingSeedEnv, TestingItersEnv, TestingNanosEnv, TestingBenchEnv, TestingExpectEnv}

// FuzzDirName is a test name as the directory of its corpus under
// testdata/fuzz/: each byte that a Windows file name cannot hold (a
// control byte and < > : " / \ | ? *) becomes '_', and so does a trailing
// space or dot, so one name maps to one directory on both targets.
func FuzzDirName(test string) string {
	b := []byte(test)
	for i, c := range b {
		if c < 0x20 || c == 0x7f || strings.IndexByte(`<>:"/\|?*`, c) >= 0 {
			b[i] = '_'
		}
	}
	for i := len(b) - 1; i >= 0 && (b[i] == ' ' || b[i] == '.'); i-- {
		b[i] = '_'
	}
	if len(b) == 0 {
		return "_"
	}
	return string(b)
}

// benchCall finds a call of testing.Bench or testing.BenchSys in a test
// block's text: how mk bench tells a benchmark from another test without
// running it. The module must be imported under its own name.
var benchCall = regexp.MustCompile(`\btesting\.Bench(Sys)?\s*\(`)

// selectTest reports whether opt selects test tc: under --fuzz the test of
// exactly that name, under mk bench the tests whose block calls
// testing.Bench or testing.BenchSys, and in both and plain runs only those
// whose name contains opt.Run.
func (s *Session) selectTest(tc testCase, opt TestOptions) bool {
	if !strings.Contains(tc.name, opt.Run) {
		return false
	}
	switch {
	case opt.Fuzz != "":
		return tc.name == opt.Fuzz
	case opt.Bench:
		return benchCall.MatchString(codeOnly(s.testText(tc), false))
	}
	return true
}

// testText is the source text of test tc's block, from its keyword (the
// item's span) to the brace that closes its body, braces in string and
// byte literals and in line comments not counted.
func (s *Session) testText(tc testCase) string {
	sp := s.w.items.Get(tc.item).Span
	f := s.w.sm.File(sp.File)
	if f == nil || int(sp.Start) > len(f.Text) {
		return ""
	}
	return blockText(f.Text[sp.Start:])
}

// blockText is t up to the brace that closes its first brace, or all of t.
func blockText(t string) string {
	depth := 0
	for i := 0; i < len(t); i++ {
		switch c := t[i]; c {
		case '"', '\'':
			for i++; i < len(t) && t[i] != c && t[i] != '\n'; i++ {
				if t[i] == '\\' {
					i++
				}
			}
		case '/':
			if i+1 < len(t) && t[i+1] == '/' {
				for i < len(t) && t[i] != '\n' {
					i++
				}
			}
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return t[:i+1]
			}
		}
	}
	return t
}

// runTestChild runs selected test i (tc) into out: the fuzz loop under
// --fuzz, the repetitions under mk bench, else one plain child, whose
// environment names the test's seed corpus when it has one.
func (s *Session) runTestChild(out *TestCase, tc testCase, argv, env []string, i int, opt TestOptions) {
	via := len(argv) > 1 // a runner prefix: the child is a Linux one through wsl.exe
	env = withoutNames(env, append(slices.Clip(testingEnvNames), CoverEnv), via)
	switch {
	case opt.Fuzz != "":
		s.fuzzLoop(out, tc, argv, env, i, opt)
		return
	case opt.Bench:
		s.benchRun(out, argv, env, i, opt)
		return
	}
	defer judgeExpectedPanic(out, opt.expectToken) // after runChild: testing.ExpectPanic's flip
	env = childEnv(env, via, TestingExpectEnv+"="+opt.expectToken)
	dir := s.fuzzCorpusDir(tc, opt)
	note := s.fuzzCorpusNote(tc, opt)
	defer func() { out.Note = note }() // after runChild, which sets the rest
	if seeds := corpusFiles(dir); len(seeds) > 0 {
		in, err := writeFuzzInput(dir, seeds, nil)
		if err != nil {
			out.Result, out.Status, out.Output = "fail", -1, "fuzz corpus: "+err.Error()
			return
		}
		defer os.RemoveAll(filepath.Dir(in))
		defer fuzzInputLines(out, opt.expectToken)
		env = childEnv(env, via, TestingFuzzEnv+"=run", TestingInputEnv+"="+hostPath(argv, in))
	}
	if opt.coverDir != "" { // cover.go: this child's counts file
		counts := filepath.Join(opt.coverDir, strconv.Itoa(i))
		env = childEnv(env, via, opt.cover.coverEnv(hostPath(argv, counts)))
		runChild(out, argv, env, i, s.cfg.TestTimeout, func() { out.coverAcked = coverAsk(counts) })
		return
	}
	runChild(out, argv, env, i, s.cfg.TestTimeout)
}

// fuzzInputLines displays tagged seed markers at the start of a line in
// the joined stdout and stderr stream. Only the child's private token
// marks a seed; ordinary output of "fuzz input" is left intact.
func fuzzInputLines(tc *TestCase, token string) {
	text := tc.Output
	mark := "mkfuzzinput " + token + " "
	at := strings.Index(text, mark)
	if token == "" || at < 0 {
		return
	}
	var out strings.Builder
	out.Grow(len(text))
	for at >= 0 {
		out.WriteString(text[:at])
		if at > 0 && text[at-1] != '\n' {
			out.WriteByte('\n')
		}
		out.WriteString("fuzz input ")
		text = text[at+len(mark):]
		at = strings.Index(text, mark)
	}
	out.WriteString(text)
	tc.Output = out.String()
}

// hostPath is p as the child sees it: through a runner prefix (wsl.exe)
// the Linux path of the Windows path p.
func hostPath(argv []string, p string) string {
	if len(argv) > 1 {
		return toolchain.HostPathFor(argv[:len(argv)-1], p)
	}
	return p
}

// withoutNames is env without any variable of names (and, through a
// runner, without them in WSLENV).
func withoutNames(env, names []string, viaRunner bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		n, v, _ := strings.Cut(kv, "=")
		if slicesContainsFold(names, n) {
			continue
		}
		if viaRunner && strings.EqualFold(n, "WSLENV") {
			for _, d := range names {
				v = dropWSLName(v, d)
			}
			kv = "WSLENV=" + v
		}
		out = append(out, kv)
	}
	return out
}

// expectPanicMark starts the line std.testing's ExpectPanic writes to
// standard error: "mkexpectpanic <token> <length of want> <want>".
const expectPanicMark = "mkexpectpanic "

// judgeExpectedPanic flips a plain child's result when its output holds
// ExpectPanic's line with the run's token (the last well-formed one
// counts), and takes those lines out of its output: a panic whose report
// contains the wanted text passes; a panic that does not match fails, and
// so does a body that returned (exit 0). Any other status, and a child
// with no such line, keeps what runChild set.
func judgeExpectedPanic(tc *TestCase, token string) {
	out, mark := tc.Output, expectPanicMark+token+" "
	if token == "" || !strings.Contains(out, mark) {
		return
	}
	var kept strings.Builder
	want, after, from, found := "", 0, 0, false
	for at := 0; ; {
		i := strings.Index(out[at:], mark)
		if i < 0 {
			break
		}
		start := at + i
		at = start + len(mark)
		sp := strings.IndexByte(out[at:], ' ')
		if sp < 0 {
			continue
		}
		n, err := strconv.Atoi(out[at : at+sp])
		if err != nil || n < 0 || n > len(out)-(at+sp+1) {
			continue
		}
		want, after, found = out[at+sp+1:at+sp+1+n], at+sp+1+n, true
		at = after
		kept.WriteString(out[from:start])
		from = after
		if strings.HasPrefix(out[from:], "\n") { // the line's own end
			from++
		}
	}
	if !found {
		return
	}
	tc.Output = kept.String() + out[from:]
	call := "testing.ExpectPanic(" + strconv.Quote(want) + ")"
	switch tc.Status {
	case 0:
		tc.Result, tc.Detail = "fail", "returned; "+call+" wanted a panic"
		return
	case 101:
	default:
		return
	}
	// std.rt flushes the streams, then writes the report ("panic: ", the
	// message, which may hold line breaks, and the site) and after it only
	// the backtrace, from a line that starts "backtrace:". So the text
	// after the last "panic: " before that line is the report's own,
	// whatever the test printed before it; a message that itself holds
	// "panic: " is compared after its last one.
	rep := out[after:]
	if b := strings.LastIndex(rep, "\nbacktrace:"); b >= 0 {
		rep = rep[:b]
	}
	p := strings.LastIndex(rep, "panic: ")
	if p < 0 {
		tc.Result, tc.Detail = "fail", "no panic report after "+call
		return
	}
	report := strings.TrimRight(rep[p:], "\r\n")
	if !strings.Contains(report[len("panic: "):], want) {
		first, _, _ := strings.Cut(report, "\n")
		tc.Result, tc.Detail = "fail", "panic does not contain "+strconv.Quote(want)+" (testing.ExpectPanic): "+first
		return
	}
	tc.Result = "pass"
}
