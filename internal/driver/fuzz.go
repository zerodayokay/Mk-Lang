// Why: mk test --fuzz needs a mutation loop that survives the crashes it is
// looking for; the loop runs in a test child, and something outside that
// child must notice a panic or a hang and keep the input that caused it.
//
// What: the seed corpus of plain mk test, and the watcher of mk test --fuzz
// <test name> [--fuzztime d] (the child's loop is std.testing's Loop), with
// fuzzDictionary, the constants of the test block's file.
//
// How: the child writes a call count, the input's length and the input to a
// state file before every call of the target; the watcher polls the count,
// and
//   - when the child exits with a failure (a panic, a leak, any status but
//     0), saves the input the state file holds into the test's corpus,
//     testdata/fuzz/<test name>/<first 16 hex digits of its SHA-256>, with
//     <that name>.report beside it (the reason, the seed and the child's
//     output), then restarts the child with the next seed;
//   - when the count stops moving for the hang limit, kills the child and
//     saves the input the same way, as a hang;
//   - when the fuzz time is spent, kills the child: a run that saved
//     nothing passes.
// The build has the coverage counters (Config.Cover) and each child gets
// MK_COVER with no counts file (cover.go), so std.testing's Guided reads
// them live and keeps an input that reaches a new block.

package driver

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mk/internal/source"
)

// The fuzz loop's defaults: the fuzz time when --fuzztime is absent, the
// hang limit, and how many failing inputs one run saves before it stops.
const (
	DefaultFuzzTime = 10 * time.Second
	DefaultFuzzHang = 10 * time.Second
	maxFuzzSaved    = 20
	maxFuzzTokens   = 256
	fuzzPoll        = 20 * time.Millisecond
	fuzzGrace       = 5 * time.Second
)

// corpusFiles are the seed files of dir in name order: its regular files,
// without the .report files the loop writes beside its findings. A
// missing directory has none.
func corpusFiles(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".report") {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

// writeFuzzInput writes std.testing's input file for the seeds of dir (by
// name) and the dictionary tokens into a new temporary directory, and
// returns its path. A record is a kind byte ('s' or 'd'), a u32
// little-endian name length, the name, a u32 data length and the data.
func writeFuzzInput(dir string, seeds []string, dict [][]byte) (string, error) {
	var b []byte
	rec := func(kind byte, name string, data []byte) {
		b = append(b, kind)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(name)))
		b = append(b, name...)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
		b = append(b, data...)
	}
	for _, n := range seeds {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		rec('s', n, data)
	}
	for _, d := range dict {
		rec('d', "", d)
	}
	tmp, err := os.MkdirTemp("", "mk-fuzz-")
	if err != nil {
		return "", err
	}
	p := filepath.Join(tmp, "input")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	return p, nil
}

// The constants fuzzDictionary reads: string literals, byte literals and
// integer literals.
var (
	fuzzStrLit  = regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`)
	fuzzByteLit = regexp.MustCompile(`b'((?:[^'\\\n]|\\.)+)'`)
	fuzzIntLit  = regexp.MustCompile(`\b(0x[0-9A-Fa-f_]+|[0-9][0-9_]*)\b`)
)

// fuzzDictionary is the loop's dictionary for a test: the constants of the
// source file holding its block, in order of first appearance and without
// repeats, at most maxFuzzTokens. A string literal gives its bytes; a byte
// literal and an integer below 256 one byte; a larger integer below 2^32
// its little-endian bytes, 2 or 4 of them. Comments are not skipped: a
// token from one costs a wasted mutation, nothing more.
func fuzzDictionary(text string) [][]byte {
	type at struct {
		pos int
		tok []byte
	}
	var found []at
	for _, m := range fuzzStrLit.FindAllStringSubmatchIndex(text, -1) {
		if t := unquoteMk(text[m[2]:m[3]]); len(t) > 0 {
			found = append(found, at{m[0], t})
		}
	}
	for _, m := range fuzzByteLit.FindAllStringSubmatchIndex(text, -1) {
		if t := unquoteMk(text[m[2]:m[3]]); len(t) == 1 {
			found = append(found, at{m[0], t})
		}
	}
	for _, m := range fuzzIntLit.FindAllStringSubmatchIndex(text, -1) {
		lit := strings.ReplaceAll(text[m[2]:m[3]], "_", "")
		v, err := strconv.ParseUint(lit, 0, 64)
		switch {
		case err != nil:
		case v < 256:
			found = append(found, at{m[0], []byte{byte(v)}})
		case v < 1<<16:
			found = append(found, at{m[0], binary.LittleEndian.AppendUint16(nil, uint16(v))})
		case v < 1<<32:
			found = append(found, at{m[0], binary.LittleEndian.AppendUint32(nil, uint32(v))})
		}
	}
	slices.SortStableFunc(found, func(a, b at) int { return a.pos - b.pos })
	var out [][]byte
	seen := map[string]bool{}
	for _, f := range found {
		if len(out) == maxFuzzTokens {
			break
		}
		if !seen[string(f.tok)] {
			seen[string(f.tok)] = true
			out = append(out, f.tok)
		}
	}
	return out
}

// unquoteMk is the bytes of a literal's body: the escapes \n \r \t \0 \\
// \" \' and \xHH, any other escape kept as written.
func unquoteMk(s string) []byte {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b = append(b, c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case '0':
			b = append(b, 0)
		case '\\', '"', '\'':
			b = append(b, s[i])
		case 'x':
			if i+2 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					b = append(b, byte(v))
					i += 2
					continue
				}
			}
			b = append(b, '\\', 'x')
		default:
			b = append(b, '\\', s[i])
		}
	}
	return b
}

// fuzzLoop is mk test --fuzz for test i (tc): the child runs std.testing's
// loop under the watcher until the fuzz time is spent, restarted with the
// next seed after each failing input it saves. out is the test's outcome:
// pass when nothing was saved, fail with the saved files named otherwise.
func (s *Session) fuzzLoop(out *TestCase, tc testCase, argv, env []string, i int, opt TestOptions) {
	start := time.Now()
	fuzzTime, hang := opt.FuzzTime, opt.FuzzHang
	if fuzzTime <= 0 {
		fuzzTime = DefaultFuzzTime
	}
	if hang <= 0 {
		hang = DefaultFuzzHang
	}
	dir := s.fuzzCorpusDir(tc, opt)
	rel := "testdata/fuzz/" + s.fuzzDir(tc, opt)
	out.Note = s.fuzzCorpusNote(tc, opt)
	sigs := savedSignatures(dir)
	in, err := writeFuzzInput(dir, corpusFiles(dir), fuzzDictionary(s.testFileText(tc)))
	if err != nil {
		out.Result, out.Status, out.Output = "fail", -1, "fuzz corpus: "+err.Error()
		return
	}
	tmp := filepath.Dir(in)
	defer os.RemoveAll(tmp)
	state := filepath.Join(tmp, "state")
	via := len(argv) > 1
	var log strings.Builder
	var saved []string
	found := 0
	var calls uint64
	deadline := start.Add(fuzzTime)
	for seed := uint64(1); time.Now().Before(deadline) && found < maxFuzzSaved; seed++ {
		if err := os.WriteFile(state, nil, 0o644); err != nil {
			out.Result, out.Status, out.Output = "fail", -1, "fuzz state: "+err.Error()
			return
		}
		set := []string{}
		if opt.cover != nil { // cover.go: live counters for the loop's Guided
			set = append(set, opt.cover.coverEnv("-"))
		}
		budget := max(time.Until(deadline), time.Millisecond)
		cenv := childEnv(env, via, append(set, TestingFuzzEnv+"=loop", TestingInputEnv+"="+hostPath(argv, in),
			TestingStateEnv+"="+hostPath(argv, state), TestingSeedEnv+"="+strconv.FormatUint(seed, 10),
			TestingItersEnv+"="+strconv.Itoa(opt.FuzzIters), TestingNanosEnv+"="+strconv.FormatInt(int64(budget), 10))...)
		// The child stops itself when its budget (MK_TESTING_NANOS) is
		// spent on its own clock; past that the watcher waits only on a
		// call in progress (watchFuzzChild).
		r := watchFuzzChild(argv, cenv, i, state, deadline, budget, hang)
		calls += r.calls
		if r.err != nil {
			out.Result, out.Status, out.Output = "fail", -1, log.String()+r.err.Error()
			return
		}
		if !r.started {
			// The child never reached the loop: the test does not call
			// testing.Fuzz or testing.FuzzSys, or failed before it did.
			out.Status = r.status
			out.Result = "fail"
			out.Detail = "not a fuzz target: the test did not reach testing.Fuzz or testing.FuzzSys"
			if r.status != 0 {
				out.Detail = "the test failed before testing.Fuzz or testing.FuzzSys: " + r.reason
			}
			out.Output = log.String() + r.output
			out.Duration = time.Since(start)
			return
		}
		if r.reason == "" {
			break // the fuzz time is spent, or MK_TESTING_ITERS calls ran
		}
		found++
		sig := fuzzSignature(r)
		if prev, ok := sigs[sig]; ok {
			// One bug, many inputs: the first input of a failure is kept.
			fmt.Fprintf(&log, "seed %d: %s after %s: the failure of %s/%s (%s), not saved again\n", seed, r.reason, counted(r.calls, "call"), rel, prev, sig)
			continue
		}
		name, err := saveFuzzInput(dir, r, seed, sig)
		if err != nil {
			out.Result, out.Status, out.Output = "fail", -1, log.String()+"save: "+err.Error()
			return
		}
		sigs[sig] = name
		if !slices.Contains(saved, name) {
			saved = append(saved, name)
		}
		fmt.Fprintf(&log, "seed %d: %s after %s: saved %s/%s\n", seed, r.reason, counted(r.calls, "call"), rel, name)
	}
	out.Duration = time.Since(start)
	fmt.Fprintf(&log, "fuzz: %s in %s, %s, %s saved\n", counted(calls, "call"), out.Duration.Round(time.Millisecond), counted(found, "failing run"), counted(len(saved), "input"))
	out.Output = log.String()
	if found == 0 {
		out.Result, out.Status = "pass", 0
		return
	}
	out.Result, out.Status = "fail", 1
	out.Detail = fmt.Sprintf("fuzz: %s, %s saved to %s/", counted(found, "failing run"), counted(len(saved), "input"), rel)
}

// counted is n and noun, the noun plural unless n is 1: "1 call",
// "2 calls".
func counted[N int | uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// testFileText is the text of the source file holding test tc's block.
func (s *Session) testFileText(tc testCase) string {
	if f := s.w.sm.File(s.w.items.Get(tc.item).Span.File); f != nil {
		return f.Text
	}
	return ""
}

// fuzzRun is one child's run under the watcher.
type fuzzRun struct {
	started bool   // the state file had its header: the loop ran
	calls   uint64 // the count the state file last held
	reason  string // "" when stopped at the fuzz time or after its calls; else the failure
	status  int
	output  string
	input   []byte // the input the state file held at the failure
	err     error  // the child could not run, or the state could not be read
}

// watchFuzzChild runs one loop child and polls its state file until it
// exits or its count stops moving for hang (a hang, whenever it starts).
// The child's budget runs on its own clock, from its loop's start, so the
// watcher judges it from the first poll that finds the state file's
// header, never earlier than that start: its end is the later of deadline
// and that poll plus budget, and a slow start uses none of the budget nor
// of the hang limit. Past its end the child should have stopped itself;
// while it has not, the watcher keeps it only as long as its count is
// unmoved, since that call may be a hang: a count that moves after the
// end ends the run as a normal stop, and the hang limit elapsing saves the
// input. A child that has not reached the loop by deadline + fuzzGrace is
// stopped.
func watchFuzzChild(argv, env []string, i int, state string, deadline time.Time, budget, hang time.Duration) fuzzRun {
	cmd := exec.Command(argv[0], append(argv[1:], strconv.Itoa(i))...)
	cmd.Env = env
	var buf bytes.Buffer // one writer for both: exec copies with one goroutine
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.WaitDelay = 5 * time.Second
	prepareChild(cmd)
	if err := cmd.Start(); err != nil {
		return fuzzRun{err: err}
	}
	done := make(chan error, 1)
	var waitErr error
	go func() {
		defer func() { done <- waitErr }()
		if ie := source.Guard("wait for a fuzz child", source.Span{}, func() { waitErr = cmd.Wait() }); ie != nil {
			waitErr = errors.New(ie.What)
		}
	}()
	var r fuzzRun
	var last uint64
	var moved, end time.Time
	tick := time.NewTicker(fuzzPoll)
	defer tick.Stop()
	var werr error
	killed := ""
loop:
	for {
		select {
		case werr = <-done:
			break loop
		case <-tick.C:
			now := time.Now()
			movedLate := false
			if c, ok := readFuzzCount(state); ok {
				if !r.started {
					// The first sight of the loop: the call in progress, if
					// any, began no earlier than the budget's clock did.
					r.started, last, moved = true, c, now
					end = deadline
					if e := now.Add(budget); e.After(end) {
						end = e
					}
				} else if c != last {
					last, moved = c, now
					movedLate = now.After(end)
				}
			}
			switch {
			case r.started && now.Sub(moved) > hang:
				killed = "hang"
			case r.started && movedLate:
				killed = "deadline" // the call in progress at its end returned
			case !r.started && now.After(deadline.Add(fuzzGrace)):
				killed = "deadline"
			}
			if killed != "" {
				// A child that exited since the last tick (a panic just
				// after the deadline) is judged by its exit, never killed
				// and called a normal stop.
				select {
				case werr = <-done:
					killed = ""
					break loop
				default:
				}
				killTree(cmd)
				werr = <-done
				// It may still have exited on its own between the check and
				// the kill: a panic's 101 or a leak's 102 is never the
				// kill's status (taskkill /F gives 1, a signal none).
				var ee *exec.ExitError
				if errors.As(werr, &ee) && (ee.ExitCode() == 101 || ee.ExitCode() == 102) {
					killed = ""
				}
				break loop
			}
		}
	}
	r.output = buf.String()
	st, err := readFuzzState(state)
	if err != nil {
		r.err = err
		return r
	}
	r.started = r.started || st.started
	r.calls, r.input = st.calls, st.input
	var ee *exec.ExitError
	switch {
	case killed == "hang":
		r.status, r.reason = -1, fmt.Sprintf("hang (no call finished in %s)", hang)
	case killed == "deadline":
		r.status = -1
	case werr == nil:
		r.status = 0
	case errors.As(werr, &ee) && ee.ExitCode() >= 0:
		r.status = ee.ExitCode()
		r.reason = resultOf(r.status)
		if r.reason == "fail" {
			r.reason = "exit " + strconv.Itoa(r.status)
		}
	default:
		r.err = werr
	}
	return r
}

// readFuzzCount is the call count of the state file's header.
func readFuzzCount(state string) (uint64, bool) {
	f, err := os.Open(state)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	var h [8]byte
	if n, _ := f.Read(h[:]); n < 8 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(h[:]), true
}

// fuzzState is the state file once its child has stopped.
type fuzzState struct {
	started bool
	calls   uint64
	input   []byte
}

func readFuzzState(state string) (fuzzState, error) {
	b, err := os.ReadFile(state)
	if err != nil {
		return fuzzState{}, err
	}
	if len(b) < 16 {
		return fuzzState{}, nil
	}
	st := fuzzState{started: true, calls: binary.LittleEndian.Uint64(b), input: b[16:]}
	if n := binary.LittleEndian.Uint64(b[8:]); n <= uint64(len(st.input)) {
		st.input = st.input[:n]
	}
	return st, nil
}

// saveFuzzInput writes a failing input and its report, headed by the
// failure's signature (fuzzSignature), into the corpus directory dir, made
// when missing, and returns the input's file name.
func saveFuzzInput(dir string, r fuzzRun, seed uint64, sig string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(r.input)
	name := hex.EncodeToString(sum[:8])
	if err := os.WriteFile(filepath.Join(dir, name), r.input, 0o644); err != nil {
		return "", err
	}
	var rep strings.Builder
	fmt.Fprintf(&rep, "signature: %s\n", sig)
	fmt.Fprintf(&rep, "result: %s\nstatus: %d\nseed: %d\ncall: %d\nlength: %d\n", r.reason, r.status, seed, r.calls, len(r.input))
	if utf8.Valid(r.input) && len(r.input) <= 256 {
		fmt.Fprintf(&rep, "input: %q\n", r.input)
	} else {
		fmt.Fprintf(&rep, "input (hex, first 256 bytes): %s\n", hex.EncodeToString(r.input[:min(len(r.input), 256)]))
	}
	rep.WriteString("output:\n")
	rep.WriteString(lastLines(r.output, 200))
	if err := os.WriteFile(filepath.Join(dir, name+".report"), []byte(rep.String()), 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "")
}
