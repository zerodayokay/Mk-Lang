// Why: the process streams' lock word and the final flush of proc.Exit
// must hold up under real contention on several carriers.
// What: TestStreamLockContention (io_stream.mk StreamLock, StreamPut,
// SealStream, TrySealStream).
// How: spawned writers and Main write records at once on several carriers
// while one task calls proc.Exit or panics; the output is judged by record.
//
// A user program cannot hand a Writer to a task (Writer is not Send, and
// System is not Share), so the spawned writers get their stream through a
// test fixture in std.core that builds System.Stdout's writer from the
// task's own Io. Main writes through sys.Stdout() and sys.Stderr() at the
// same time, which is the path a program reaches: one task writing while
// another task calls proc.Exit or panics.

package std

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// mnCore and mnProc are the fixtures core/io_zmn.mk and proc/zmn.mk.
const mnCore = `// Test fixture (std/std-io_mn_test.go): process-stream writers a spawned
// task can make for itself, so several tasks contend for one stream lock.

import std.raw

// MnProc is the Process's address as an integer, which crosses to a task.
export MnProc(sys: &System): uint {
    unsafe "only the address is taken" {
        return raw.Addr(sys.proc)
    }
}

// MnStream is System.Stdout (i 0) or Stderr (i 1) for the task owning io.
export MnStream(io: Io, p: uint, i: int): Writer {
    unsafe "p is the live Process's address (MnProc)" {
        return WriterOver(io, &StreamWriteTable, ProcCtx(raw.FromAddr[Process](p), i))
    }
}
`

const mnProc = `import std.core

export MnProc(sys: &System): uint {
    return core.MnProc(sys)
}

export MnStream(io: Io, p: uint, i: int): Writer {
    return core.MnStream(io, p, i)
}
`

// mnProgram: args tasks, rounds, exit_at, mode. tasks spawned writers
// (ids 0 to tasks-1) and Main (id tasks) each write rounds records
// "id:seq:len:payload\n", every third (seq % 3 == 2) to stderr, each with
// one Writer.Bytes call, so a record is one StreamPut. With exit_at >= 0,
// task 0 calls proc.Exit(3) (mode "exit") or fails an Assert (mode
// "panic") before its record exit_at, while the others keep writing. Mode
// "solo" panics the same way, but task 0 runs alone, after every other
// writer and Main have finished, so no stream lock is held at the panic.
const mnProgram = `import std.proc

Len(tid: int, seq: int): int {
    k := (tid * 5 + seq * 3) % 11
    if k == 0 {
        return 0
    }
    if k == 1 {
        return 1
    }
    if k == 2 {
        return 8_179
    }
    if k == 3 {
        return 8_192
    }
    if k == 4 {
        return 8_193
    }
    if k == 5 {
        return 20_000
    }
    if k == 6 {
        return 4_000 + seq % 97
    }
    return 10 + (seq * 37 + tid) % 300
}

PutNum(b: mut [20100]u8, at: int, v: int): int {
    var d: [20]u8 = [0; 20]
    var n := 0
    var x := v
    for {
        d[n] = Trunc[u8](48 + x % 10)
        n += 1
        x = x / 10
        if x == 0 {
            break
        }
    }
    for i in 0..n {
        b[at + i] = d[n - 1 - i]
    }
    return at + n
}

Record(b: mut [20100]u8, tid: int, seq: int): int {
    n := Len(tid, seq)
    var at := PutNum(b, 0, tid)
    b[at] = 0x3A
    at = PutNum(b, at + 1, seq)
    b[at] = 0x3A
    at = PutNum(b, at + 1, n)
    b[at] = 0x3A
    at += 1
    for i in 0..n {
        b[at + i] = Trunc[u8](0x61 + (tid * 7 + seq + i) % 26)
    }
    at += n
    b[at] = 0x0A
    return at + 1
}

Records(out: mut Writer, err: mut Writer, tid: int, rounds: int, exit_at: int, panic: bool) {
    var b: [20100]u8 = [0; 20100]
    for seq in 0..rounds {
        if tid == 0 && seq == exit_at {
            if panic {
                Assert(seq < 0)
            }
            proc.Exit(3)
        }
        n := Record(mut b, tid, seq)
        if seq % 3 == 2 {
            err.Bytes(b[0..n]) else {
                return
            }
        } else {
            out.Bytes(b[0..n]) else {
                return
            }
        }
    }
}

Worker(io: Io, p: uint, tid: int, rounds: int, exit_at: int, panic: bool) {
    var out := proc.MnStream(io, p, 0)
    var err := proc.MnStream(io, p, 1)
    Records(mut out, mut err, tid, rounds, exit_at, panic)
}

export Main(sys: &System): ParseError!void {
    tasks := int(try sys.args[1].ParseInt())
    rounds := int(try sys.args[2].ParseInt())
    exit_at := int(try sys.args[3].ParseInt())
    solo := sys.args[4] == "solo"
    panic := sys.args[4] == "panic" || solo
    p := proc.MnProc(sys)
    var out := sys.Stdout()
    var err := sys.Stderr()
    scope s := sys.io.Scope() {
        for tid in 0..tasks {
            if !solo || tid > 0 {
                s.go Worker(sys.io, p, tid, rounds, exit_at, panic)
            }
        }
        Records(mut out, mut err, tasks, rounds, -1, false)
    }
    if solo {
        scope s2 := sys.io.Scope() {
            s2.go Worker(sys.io, p, 0, rounds, exit_at, true)
        }
    }
}
`

// mnLen and mnRecord are the Go model of the program's records.
func mnLen(tid, seq int) int {
	switch (tid*5 + seq*3) % 11 {
	case 0:
		return 0
	case 1:
		return 1
	case 2:
		return 8179
	case 3:
		return 8192
	case 4:
		return 8193
	case 5:
		return 20000
	case 6:
		return 4000 + seq%97
	}
	return 10 + (seq*37+tid)%300
}

func mnPayload(tid, seq int) string {
	n := mnLen(tid, seq)
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(0x61 + (tid*7+seq+i)%26)
	}
	return string(b)
}

// mnStream splits a stream into records and checks every one: well
// formed, its payload the model's, its stream the right one for its seq
// (stderr when seq % 3 == 2), and each writer's seqs on this stream in
// increasing order with none missing between two that arrived. It returns
// the seqs per writer. A stream that does not end in '\n' has a torn last
// record, which is an error unless torn is true.
func mnStream(t *testing.T, what, s string, writers int, isErr, torn bool) [][]int {
	t.Helper()
	if !strings.HasSuffix(s, "\n") && s != "" {
		i := strings.LastIndexByte(s, '\n')
		if torn {
			return mnStreamWhole(t, what, s[:i+1], writers, isErr)
		}
		tail := s[i+1:]
		if len(tail) > 60 {
			tail = tail[:60] + "..."
		}
		t.Errorf("%s: ends with a torn record (%d bytes after the last newline: %q)", what, len(s)-i-1, tail)
		s = s[:i+1]
	}
	return mnStreamWhole(t, what, s, writers, isErr)
}

func mnStreamWhole(t *testing.T, what, s string, writers int, isErr bool) [][]int {
	t.Helper()
	got := make([][]int, writers)
	if s == "" {
		return got
	}
	for n, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		f := strings.SplitN(line, ":", 4)
		bad := func(why string) {
			if len(line) > 80 {
				line = line[:80] + "..."
			}
			t.Fatalf("%s: record %d %s: %q", what, n, why, line)
		}
		if len(f) != 4 {
			bad("is not id:seq:len:payload (interleaved writes?)")
		}
		tid, e1 := strconv.Atoi(f[0])
		seq, e2 := strconv.Atoi(f[1])
		ln, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil || tid < 0 || tid >= writers || seq < 0 {
			bad("has a bad header")
		}
		if ln != mnLen(tid, seq) || f[3] != mnPayload(tid, seq) {
			bad("has a wrong payload")
		}
		if (seq%3 == 2) != isErr {
			bad("is on the wrong stream")
		}
		prev := -1
		if k := len(got[tid]); k > 0 {
			prev = got[tid][k-1]
		}
		next := prev + 1
		for next%3 == 2 != isErr {
			next++
		}
		if seq != next {
			bad(fmt.Sprintf("follows seq %d of its writer (want %d: lost or reordered)", prev, next))
		}
		got[tid] = append(got[tid], seq)
	}
	return got
}

// mnWant is the number of records writer tid writes on one stream when it
// writes seqs 0..upto-1.
func mnWant(upto int, isErr bool) int {
	n := 0
	for seq := 0; seq < upto; seq++ {
		if seq%3 == 2 == isErr {
			n++
		}
	}
	return n
}

// TestStreamLockContention: 8 spawned tasks and Main write 9 x 150
// records of 0 to 20,000 bytes to stdout and stderr through the shared
// process buffers, with MK_CARRIERS 1, 2, 4, 8 and 16: every record
// arrives whole and once, each writer's records in its order, on the
// right stream, and nothing else (a lost wake-up in StreamLock shows as
// the 60 s watchdog). Then three ways for task 0 to end the process
// before its record 60, 3 runs each per carrier count:
//
//   - "exit": proc.Exit(3) while the others write. Status 3, both streams
//     end with whole records (SealStream: the old FlushStd unlocked after
//     the final flush, so a writer on another carrier began a record the
//     exit cut, in 3 of 6 runs with 8 carriers), task 0's records before
//     the exit are all there, and every other writer's are a prefix of
//     its own.
//   - "solo": a failed Assert in task 0 while it is the only task left
//     (every other writer and Main have finished). No stream lock is held,
//     so the report's try-lock flush (TrySealStream) always takes both:
//     status 101, task 0's records before the panic all there, every other
//     writer's all there, no torn record, nothing after the report.
//   - "panic": the same Assert while the others write. This flush is best
//     effort (a held buffer is skipped,
//     and the report may land between another task's writes, even in the
//     middle of one of its records). So the one report line is cut out
//     wherever it is and the rest of stderr is judged as one stream: whole,
//     ordered records, only the last one may be torn, and every writer's
//     records (task 0's too, none at or after 60) a prefix of its own. The
//     runs where task 0 lost records to a skipped buffer are logged.
//
// History: the old "panic" judging cut stderr at the report and wanted all
// of task 0's records before it, so it failed when a holder's buffer write
// carrying task 0's last record landed after the report (4 of 200 panic
// runs, MK_CARRIERS 4 to 16, Windows, 2026-11-05); the exact check now
// lives in "solo", where the design guarantees it.
func TestStreamLockContention(t *testing.T) { ioStreamLockContention(t, hostIo) }

func ioStreamLockContention(t *testing.T, h ioHost) {
	const tasks, rounds, exitAt = 8, 150, 60
	exe := h.build(t, map[string]string{"core/io_zmn.mk": mnCore, "proc/zmn.mk": mnProc}, mnProgram)
	var calls []ioCall
	for _, carriers := range []int{1, 2, 4, 8, 16} {
		// The record oracle handles one panic headline. Runtime backtrace
		// frames are unrelated to the program's stream writes.
		env := []string{fmt.Sprintf("MK_CARRIERS=%d", carriers), "MK_BACKTRACE=0"}
		calls = append(calls, ioCall{env: env, watch: 60, args: []string{strconv.Itoa(tasks), strconv.Itoa(rounds), "-1", "full"}})
		for rep := 0; rep < 3; rep++ {
			for _, mode := range []string{"exit", "solo", "panic"} {
				calls = append(calls, ioCall{env: env, watch: 60, args: []string{strconv.Itoa(tasks), strconv.Itoa(rounds), strconv.Itoa(exitAt), mode}})
			}
		}
	}
	short, panics := 0, 0
	for i, r := range h.runMany(t, exe, calls) {
		c := calls[i]
		mode := c.args[3]
		what := fmt.Sprintf("%s %s", c.env[0], mode)
		want := map[string]int{"full": 0, "exit": 3, "solo": 101, "panic": 101}[mode]
		if r.code == 124 {
			t.Fatalf("%s: no exit within %d s (deadlock or lost wake-up in the stream lock)", what, c.watch)
		}
		if r.code != want {
			t.Fatalf("%s: exit %d, want %d\nstderr tail: %q", what, r.code, want, stdioTail(r.stderr))
		}
		stderr := r.stderr
		if mode == "solo" || mode == "panic" {
			stderr = mnCutReport(t, what, stderr, mode == "solo")
		}
		lost := false
		for _, sd := range []struct {
			name  string
			s     string
			isErr bool
		}{{"stdout", r.stdout, false}, {"stderr", stderr, true}} {
			got := mnStream(t, what+" "+sd.name, sd.s, tasks+1, sd.isErr, mode == "panic")
			for tid := 0; tid <= tasks; tid++ {
				n, full, before := len(got[tid]), mnWant(rounds, sd.isErr), mnWant(exitAt, sd.isErr)
				switch {
				case tid == 0 && mode != "full" && n > before:
					t.Errorf("%s %s: task 0 has %d records, more than the %d it wrote before its exit", what, sd.name, n, before)
				case tid == 0 && (mode == "exit" || mode == "solo") && n != before:
					t.Errorf("%s %s: task 0 has %d records before its exit, want %d", what, sd.name, n, before)
				case tid == 0 && mode == "panic" && n < before:
					lost = true
				case tid > 0 && (mode == "full" || mode == "solo") && n != full:
					t.Errorf("%s %s: writer %d has %d records, want %d", what, sd.name, tid, n, full)
				case tid == 0 && mode == "full" && n != full:
					t.Errorf("%s %s: writer 0 has %d records, want %d", what, sd.name, n, full)
				}
			}
		}
		if mode == "panic" {
			panics++
		}
		if lost {
			short++
		}
	}
	t.Logf("panic while others write: task 0 lost records to a skipped buffer in %d of %d runs (the contract allows it)", short, panics)
}

// mnCutReport finds the one panic report line on stderr ("panic: assert at
// main.mk:..."), which may sit in the middle of another task's record, and
// returns stderr without it. With last, nothing may follow the report.
func mnCutReport(t *testing.T, what, s string, last bool) string {
	t.Helper()
	if n := strings.Count(s, "panic: "); n != 1 {
		t.Fatalf("%s: %d panic reports, want 1\nstderr tail: %q", what, n, stdioTail(s))
	}
	i := strings.Index(s, "panic: ")
	e := strings.IndexByte(s[i:], '\n')
	if e < 0 {
		t.Fatalf("%s: the panic report has no newline: %q", what, stdioTail(s[i:]))
	}
	e += i + 1
	if !strings.HasPrefix(s[i:e], "panic: assert at main.mk:") {
		t.Fatalf("%s: panic report %q, want an assert in main.mk", what, s[i:e])
	}
	if last && e != len(s) {
		t.Fatalf("%s: %d bytes after the panic report, want none (no other writer was left)", what, len(s)-e)
	}
	return s[:i] + s[e:]
}
