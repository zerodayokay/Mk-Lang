// Why: the fuzz watcher's rules (a hang, a late call, a normal stop) are
// about time, and a test through a real Mk child depends on how fast the
// machine starts one.
// What: tests of watchFuzzChild against a scripted child: a slow start
// followed by a hang, and a call in progress at the child's end that
// returns; and of fuzzWatch's rules poll by poll on made-up times.
// How: the test binary runs itself as the child (TestFuzzWatchChild under
// fuzzFakeEnv), writing the state file as std.testing's Loop does on a
// fixed script, with margins of seconds on each side of every rule.

package driver

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fuzzFakeEnv names the script TestFuzzWatchChild plays as a fake loop
// child; unset, the test does nothing.
const fuzzFakeEnv = "MK_DRIVER_FAKE_FUZZ_CHILD"

// TestFuzzWatchChild is the fake loop child: it waits the script's start
// delay, writes the state file's header with count 0, then counts calls
// as Loop does (the count written before each call), each call taking the
// script's time, and blocks in its last call.
func TestFuzzWatchChild(t *testing.T) {
	var startDelay, call time.Duration
	var calls int
	switch os.Getenv(fuzzFakeEnv) {
	case "":
		return
	case "slow-start-then-hang":
		// Starts long after the watcher's deadline, makes two short calls
		// and hangs in the third.
		startDelay, call, calls = 4*time.Second, 50*time.Millisecond, 3
	case "call-past-end":
		// Starts at once; its first call runs past its budget and returns,
		// and it goes on (as a child does between its budget checks).
		startDelay, call, calls = 0, 4*time.Second, 2
	default:
		os.Exit(3)
	}
	time.Sleep(startDelay)
	f, err := os.OpenFile(os.Getenv(TestingStateEnv), os.O_RDWR, 0)
	if err != nil {
		os.Exit(4)
	}
	var frame [16]byte
	f.WriteAt(frame[:], 0)
	for n := 1; n <= calls; n++ {
		binary.LittleEndian.PutUint64(frame[:], uint64(n))
		f.WriteAt(frame[:], 0)
		if n < calls {
			time.Sleep(call)
		}
	}
	time.Sleep(time.Hour) // the last call never returns
}

// watchFake runs the watcher over a fake child playing script, with the
// watcher's deadline budget from now and the hang limit hang.
func watchFake(t *testing.T, script string, budget, hang time.Duration) fuzzRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(state, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), fuzzFakeEnv+"="+script, TestingStateEnv+"="+state)
	argv := []string{exe, "-test.run=^TestFuzzWatchChild$"}
	r := watchFuzzChild(argv, env, 0, state, time.Now().Add(budget), budget, hang, time.Minute)
	if r.err != nil {
		t.Fatal(r.err)
	}
	return r
}

// TestFuzzWatchSlowStartHang: a child whose loop starts after the
// watcher's deadline has its budget from that start, so its count moving
// is not a late call, and a hang in its third call is saved as a hang
// (not ended as a normal stop that lets the run pass).
func TestFuzzWatchSlowStartHang(t *testing.T) {
	r := watchFake(t, "slow-start-then-hang", 2*time.Second, 2*time.Second)
	if !r.started || r.reason != "hang (no call finished in 2s)" || r.calls != 3 {
		t.Fatalf("a hang after a slow start: started %v, reason %q, %d calls; want a hang in call 3", r.started, r.reason, r.calls)
	}
}

// TestFuzzWatchCallPastEnd: a call in progress when the child's budget
// ends, which returns within the hang limit, ends the run as a normal
// stop when the count next moves.
func TestFuzzWatchCallPastEnd(t *testing.T) {
	r := watchFake(t, "call-past-end", time.Second, 10*time.Second)
	if !r.started || r.reason != "" || r.calls != 2 {
		t.Fatalf("a call past the end that returns: started %v, reason %q, %d calls; want a normal stop after call 1", r.started, r.reason, r.calls)
	}
}

// fuzzPollAt is one poll of a fuzzWatch: at the offset at from the start,
// it reads count c (ok: the header is there) and should judge want.
type fuzzPollAt struct {
	at   time.Duration
	c    uint64
	ok   bool
	want string
}

// playFuzzWatch plays polls on w from a made-up start.
func playFuzzWatch(t *testing.T, name string, w fuzzWatch, t0 time.Time, polls []fuzzPollAt) {
	t.Helper()
	for i, p := range polls {
		if got := w.step(t0.Add(p.at), p.c, p.ok); got != p.want {
			t.Errorf("%s: poll %d at %s (count %d, header %v) = %q, want %q", name, i, p.at, p.c, p.ok, got, p.want)
			return
		}
	}
}

// TestFuzzWatchMoveSeenPastEnd: a call that began before the child's end
// but was first seen by a poll after it (the next poll, or a watcher held
// up by load) is the call in progress at the end, waited on as a possible
// hang; only a call begun after a sight past the end stops the run.
func TestFuzzWatchMoveSeenPastEnd(t *testing.T) {
	t0 := time.Unix(1000, 0)
	w := fuzzWatch{deadline: t0.Add(time.Second), budget: time.Second, hang: 10 * time.Second}
	// The end is the first sight plus the budget: 1.02 s.
	playFuzzWatch(t, "a hang begun just before the end", w, t0, []fuzzPollAt{
		{20 * time.Millisecond, 1, true, ""},
		{time.Second, 1, true, ""},
		{1040 * time.Millisecond, 2, true, ""}, // call 2 began before 1.02 s, for all this poll knows
		{3 * time.Second, 2, true, ""},
		{11050 * time.Millisecond, 2, true, "hang"},
	})
	playFuzzWatch(t, "a watcher held up across the end", w, t0, []fuzzPollAt{
		{20 * time.Millisecond, 1, true, ""},
		{500 * time.Millisecond, 7, true, ""},
		{4 * time.Second, 9, true, ""}, // no poll ran from 0.5 s to 4 s
		{15 * time.Second, 9, true, "hang"},
	})
	playFuzzWatch(t, "a call begun after a sight past the end", w, t0, []fuzzPollAt{
		{20 * time.Millisecond, 1, true, ""},
		{1040 * time.Millisecond, 1, true, ""},
		{1060 * time.Millisecond, 2, true, "deadline"},
	})
}

// TestFuzzWatchStartIsAPlainTest: before its loop the child is held to the
// test timeout, as any test process is, not to the fuzz time: a start
// slower than the fuzz time plus seconds is not a stop, and with no test
// timeout nothing stops it.
func TestFuzzWatchStartIsAPlainTest(t *testing.T) {
	t0 := time.Unix(1000, 0)
	w := fuzzWatch{deadline: t0.Add(time.Second), budget: time.Second, hang: 10 * time.Second, startBy: t0.Add(time.Minute)}
	playFuzzWatch(t, "a slow start", w, t0, []fuzzPollAt{
		{6500 * time.Millisecond, 0, false, ""},
		{30 * time.Second, 0, true, ""},
		{40500 * time.Millisecond, 1, true, ""}, // its end is 31 s: this call may have begun before it
		{40600 * time.Millisecond, 1, true, ""},
		{51 * time.Second, 1, true, "hang"},
	})
	playFuzzWatch(t, "the test timeout", w, t0, []fuzzPollAt{
		{59 * time.Second, 0, false, ""},
		{61 * time.Second, 0, false, "timeout"},
	})
	w.startBy = time.Time{}
	playFuzzWatch(t, "no test timeout", w, t0, []fuzzPollAt{
		{time.Hour, 0, false, ""},
	})
}

// TestMkTestFuzzStuckBeforeLoop: a fuzz test that never reaches
// testing.Fuzz is stopped at the test timeout and reported as a timeout
// before the loop, not as a test that failed with no reason.
func TestMkTestFuzzStuckBeforeLoop(t *testing.T) {
	const src = `import std.testing

Never(b: []u8) {
    _ = b
}

test "stuck" {
    var n := 1
    for n > 0 {
        n = n % 1000 + 1
    }
    testing.Fuzz(Never)
}
`
	root := testPkg(t, map[string]string{"main.mk": src})
	rep := mkTest(t, Config{Root: root, TestTimeout: 3 * time.Second}, TestOptions{Fuzz: "stuck", FuzzTime: time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "timeout" || rep.Tests[0].Detail != "timeout after 3s before testing.Fuzz or testing.FuzzSys" {
		t.Fatalf("--fuzz stuck --timeout 3s: %+v", rep.Tests)
	}
}
