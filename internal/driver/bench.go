// Why: a benchmark number is worth publishing only when it is stable; mk
// bench must decide by itself how long to run each benchmark and say how
// sure the median is.
//
// What: the batch loop (benchRun), its statistics (medianCI, binomPMF,
// stableCI, BenchTolerance, BenchBatch, DefaultBenchLimit), and the record
// mk bench writes and compares (BenchRecord, BenchEntry, BenchRecordOf,
// ReadBenchRecord, BenchDiffers, WriteBenchText).
//
// How: mk bench runs each benchmark's child (std.testing's Bench or
// BenchSys) in batches. The first batch grows n from 1, doubling, until one
// call of f(n) takes at least 20 ms, then times BenchBatch repetitions of
// f(n); later batches time BenchBatch more at that n. After each batch the
// median time per call and its 95% confidence interval are recomputed;
// the runs stop when both ends of the interval are within BenchTolerance
// (2%) of the median, or when DefaultBenchLimit (20 s) has passed,
// whichever comes first.
//
// The interval is distribution-free, from order statistics (the binomial
// interval for the median: with m sorted times, [x_(k), x_(m+1-k)] for the
// largest k whose binomial(m, 1/2) tail P(X < k) is at most 2.5%). It
// exists from 6 repetitions on, so a first batch is 6.

package driver

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The bench statistics' numbers.
const (
	BenchBatch        = 6
	BenchTolerance    = 0.02
	DefaultBenchLimit = 20 * time.Second
	benchMaxReps      = 600
)

// BenchResult is one benchmark's statistics: nanoseconds per call of the
// work f(N) does N times.
type BenchResult struct {
	N      int64   `json:"n"`
	Reps   int     `json:"reps"`
	Median float64 `json:"median_ns"`
	Lo     float64 `json:"lo_ns"`
	Hi     float64 `json:"hi_ns"`
	// Stable is whether the interval is within BenchTolerance of the
	// median; false when the time limit ended the runs first.
	Stable bool      `json:"stable"`
	Times  []float64 `json:"times_ns"`
}

// medianCI is the median of xs and its distribution-free 95% interval;
// ok is false below 6 values.
func medianCI(xs []float64) (med, lo, hi float64, ok bool) {
	s := slices.Clone(xs)
	slices.Sort(s)
	m := len(s)
	if m == 0 {
		return 0, 0, 0, false
	}
	if m%2 == 1 {
		med = s[m/2]
	} else {
		med = (s[m/2-1] + s[m/2]) / 2
	}
	// P(X < k) for X ~ binomial(m, 1/2), k from 1 up.
	k := 0
	cdf := 0.0
	for j := 0; j < m; j++ {
		cdf += binomPMF(m, j)
		if cdf > 0.025 {
			break
		}
		k = j + 1 // P(X <= j) = P(X < j+1) <= 2.5%
	}
	if k == 0 {
		return med, s[0], s[m-1], false
	}
	return med, s[k-1], s[m-k], true
}

// binomPMF is P(X = j) for X ~ binomial(m, 1/2).
func binomPMF(m, j int) float64 {
	lg := func(x int) float64 { v, _ := math.Lgamma(float64(x + 1)); return v }
	return math.Exp(lg(m) - lg(j) - lg(m-j) - float64(m)*math.Ln2)
}

// stableCI reports whether the interval's ends are within tol of med. A
// zero median (a call below the clock's resolution, as a body that
// ignores n at the largest n) is stable: no repetition can refine it.
func stableCI(med, lo, hi, tol float64) bool {
	if med == 0 {
		return true
	}
	return med > 0 && med-lo <= tol*med && hi-med <= tol*med
}

// parseBenchLines reads the child's "mkbench <n> <ns>" lines: n and the
// nanoseconds per call of each.
func parseBenchLines(out string) (n int64, perCall []float64) {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(l))
		if len(f) != 3 || f[0] != "mkbench" {
			continue
		}
		nn, err1 := strconv.ParseInt(f[1], 10, 64)
		ns, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || nn <= 0 {
			continue
		}
		n = nn
		perCall = append(perCall, float64(ns)/float64(nn))
	}
	return n, perCall
}

// benchRun is mk bench for test i: batches of the child until the interval
// is stable or the limit passes. out.Bench holds the statistics; a child
// that fails fails the benchmark, with its output.
func (s *Session) benchRun(out *TestCase, argv, env []string, i int, opt TestOptions) {
	limit := opt.BenchLimit
	if limit <= 0 {
		limit = DefaultBenchLimit
	}
	start := time.Now()
	res := &BenchResult{}
	via := len(argv) > 1
	for {
		spec := fmt.Sprintf("%d %d", res.N, BenchBatch)
		var tc TestCase
		runChild(&tc, argv, childEnv(env, via, TestingBenchEnv+"="+spec), i, s.cfg.TestTimeout)
		if tc.Result != "pass" {
			*out = TestCase{Module: out.Module, Name: out.Name, Result: tc.Result, Status: tc.Status, Output: tc.Output, Duration: time.Since(start)}
			return
		}
		n, times := parseBenchLines(tc.Output)
		if len(times) == 0 {
			out.Result, out.Status, out.Detail = "fail", 0, "not a benchmark: the test did not reach testing.Bench or testing.BenchSys"
			out.Output, out.Duration = tc.Output, time.Since(start)
			return
		}
		res.N = n
		res.Times = append(res.Times, times...)
		res.Reps = len(res.Times)
		var ok bool
		res.Median, res.Lo, res.Hi, ok = medianCI(res.Times)
		res.Stable = ok && stableCI(res.Median, res.Lo, res.Hi, BenchTolerance)
		if res.Stable || time.Since(start) >= limit || res.Reps >= benchMaxReps {
			break
		}
	}
	out.Result, out.Status, out.Bench, out.Duration = "pass", 0, res, time.Since(start)
}

// BenchRecord is a saved mk bench run: who measured, how and when, and
// each benchmark's statistics (the --out file, which --baseline reads).
type BenchRecord struct {
	Machine string `json:"machine"`
	Command string `json:"command"`
	Time    string `json:"time"`
	// Timing is whether MK_TIMING=1 was set: a record with it false (any
	// --json output or text taken without it) is provisional and never a
	// published number, whatever file it lands in; mk bench --out writes
	// only records with it true.
	Timing  bool         `json:"mk_timing"`
	Results []BenchEntry `json:"results"`
}

// BenchEntry is one benchmark of a record.
type BenchEntry struct {
	Module string `json:"module"`
	Name   string `json:"name"`
	BenchResult
}

// BenchMachine names this machine for a record: its host name, OS,
// architecture and processor count.
func BenchMachine() string {
	h, _ := os.Hostname()
	return fmt.Sprintf("%s %s/%s %d CPUs", h, runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
}

// BenchRecordOf is the record of a mk bench report.
func BenchRecordOf(rep *TestReport, command string, now time.Time, timing bool) BenchRecord {
	r := BenchRecord{Machine: BenchMachine(), Command: command, Time: now.UTC().Format(time.RFC3339), Timing: timing}
	for _, t := range rep.Tests {
		if t.Bench != nil {
			r.Results = append(r.Results, BenchEntry{Module: t.Module, Name: t.Name, BenchResult: *t.Bench})
		}
	}
	return r
}

// ReadBenchRecord reads a --out file.
func ReadBenchRecord(path string) (BenchRecord, error) {
	var r BenchRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %v", path, err)
	}
	return r, nil
}

// BenchDiffers reports whether two results differ: only when each median
// lies outside the other's interval.
func BenchDiffers(base, now BenchResult) bool {
	out := func(x float64, r BenchResult) bool { return x < r.Lo || x > r.Hi }
	return out(now.Median, base) && out(base.Median, now)
}

// WriteBenchText prints rec, and against base when it is not nil: the
// header (machine, command, time, and whether MK_TIMING was set), then per
// benchmark the median, the interval and the repetitions, with the
// baseline's median and a mark when the two differ (BenchDiffers).
func WriteBenchText(w io.Writer, rec BenchRecord, base *BenchRecord) error {
	var b strings.Builder
	fmt.Fprintf(&b, "machine: %s\ncommand: %s\ntime: %s\n", rec.Machine, rec.Command, rec.Time)
	if !rec.Timing {
		b.WriteString("MK_TIMING unset: provisional, not for publication\n")
	}
	if base != nil {
		fmt.Fprintf(&b, "baseline: %s, %s, %s\n", base.Machine, base.Command, base.Time)
	}
	for _, e := range rec.Results {
		fmt.Fprintf(&b, "%s %s: %s/call, 95%% CI [%s, %s], n=%d, %d reps", e.Module, strconv.Quote(e.Name),
			nsText(e.Median), nsText(e.Lo), nsText(e.Hi), e.N, e.Reps)
		if !e.Stable {
			fmt.Fprintf(&b, ", not within %g%% (time limit)", BenchTolerance*100)
		}
		if base != nil {
			for _, o := range base.Results {
				if o.Module == e.Module && o.Name == e.Name {
					d := (e.Median - o.Median) / o.Median * 100
					fmt.Fprintf(&b, "; baseline %s (%+.1f%%)", nsText(o.Median), d)
					if BenchDiffers(o.BenchResult, e.BenchResult) {
						b.WriteString(" DIFFERS")
					}
				}
			}
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// nsText is a time in nanoseconds with 4 significant digits and its unit.
func nsText(ns float64) string {
	switch {
	case ns >= 1e9:
		return strconv.FormatFloat(ns/1e9, 'g', 4, 64) + "s"
	case ns >= 1e6:
		return strconv.FormatFloat(ns/1e6, 'g', 4, 64) + "ms"
	case ns >= 1e3:
		return strconv.FormatFloat(ns/1e3, 'g', 4, 64) + "µs"
	}
	return strconv.FormatFloat(ns, 'g', 4, 64) + "ns"
}
