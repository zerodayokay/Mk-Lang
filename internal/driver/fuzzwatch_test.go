// Why: the fuzz watcher's rules (a hang, a late call, a normal stop) are
// about time, and a test through a real Mk child depends on how fast the
// machine starts one.
// What: tests of watchFuzzChild against a scripted child: a slow start
// followed by a hang, and a call in progress at the child's end that
// returns.
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
	r := watchFuzzChild(argv, env, 0, state, time.Now().Add(budget), budget, hang)
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
