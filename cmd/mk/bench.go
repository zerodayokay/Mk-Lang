// Why: users need to time their own code the way Mk's benchmarks are timed,
// and a number saved from a busy machine must never pass for a real one.
//
// What: runBench, the `mk bench [flags] [name] [path]` command: it runs the
// test blocks that call testing.Bench or testing.BenchSys, in a release
// build unless --dev, and prints each one's median time per call, its 95%
// interval and the repetitions, under the machine, the command and the time
// (driver/bench.go).
//
// How: with one argument, an existing file or directory is the path and
// anything else the name; with two, the name comes first. --baseline
// compares with a saved run and marks only the differences outside both
// intervals. --out saves the run, and is refused unless MK_TIMING=1, as
// tools/bench refuses: a number published from a busy machine would say
// nothing. Printing is never refused: the text says "MK_TIMING unset:
// provisional", and a --json record carries "mk_timing": false, which marks
// it as not publishable wherever it is redirected; --baseline reads either
// kind.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"mk/internal/driver"
)

func init() {
	register(command{"bench", "bench   [flags] [name] [path]  run the benchmarks (test blocks calling testing.Bench or BenchSys) and print their statistics", func(args []string, stdout, stderr io.Writer) int {
		return runBench(args, stdout, stderr, os.Getenv)
	}})
}

// runBench is mk bench; getenv reads MK_TIMING (a test passes its own).
func runBench(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	var cf compileFlags
	fs := newFlagSet("bench", stderr, &cf)
	dev := fs.Bool("dev", false, "build the benchmarks in the dev profile instead of release")
	baseline := fs.String("baseline", "", "compare with this saved run (an --out file)")
	out := fs.String("out", "", "save the run as JSON to this file (needs MK_TIMING=1)")
	limit := fs.Duration("limit", driver.DefaultBenchLimit, "time limit per benchmark")
	timeout := fs.Duration("timeout", 120*time.Second, "per benchmark child process")
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return usageErr(stderr, fs, err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	name, path := "", ""
	switch len(pos) {
	case 0:
	case 1:
		if _, err := os.Stat(pos[0]); err == nil {
			path = pos[0]
		} else {
			name = pos[0]
		}
	case 2:
		name, path = pos[0], pos[1]
	default:
		return usageErr(stderr, fs, fmt.Errorf("want at most a name and a path, got %d arguments", len(pos)))
	}
	timing := getenv("MK_TIMING") == "1"
	if *out != "" && !timing {
		fmt.Fprintln(stderr, "mk bench: --out publishes a result, which needs MK_TIMING=1 on a quiet machine; without --out the run prints as provisional")
		return 2
	}
	var base *driver.BenchRecord
	if *baseline != "" {
		b, err := driver.ReadBenchRecord(*baseline)
		if err != nil {
			fmt.Fprintf(stderr, "mk bench: --baseline: %v\n", err)
			return 2
		}
		base = &b
	}
	cfg, ok := cf.config(path, stderr)
	if !ok {
		return 2
	}
	cfg.TestTimeout = *timeout
	cfg.Profile = driver.ProfileRelease
	if *dev {
		cfg.Profile = driver.ProfileTest
	}
	s, err := driver.NewSession(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mk bench: %v\n", err)
		return 2
	}
	// One child at a time: benchmarks timed side by side slow each other.
	res, rep, err := s.Test(driver.TestOptions{Run: name, Bench: true, BenchLimit: *limit})
	if err != nil {
		fmt.Fprintf(stderr, "mk bench: %v\n", err)
		return 2
	}
	if len(res.Report.Diags) > 0 || rep == nil {
		_ = res.WriteReport(stderr, cf.json)
		return driver.ExitStatus(res.Report)
	}
	rec := driver.BenchRecordOf(rep, "mk bench "+strings.Join(args, " "), time.Now(), timing)
	if cf.json {
		b, _ := json.Marshal(rec)
		fmt.Fprintln(stdout, string(b))
	} else if err := driver.WriteBenchText(stdout, rec, base); err != nil {
		fmt.Fprintf(stderr, "mk bench: %v\n", err)
		return 2
	}
	status := 0
	for _, t := range rep.Tests {
		if t.Result != "pass" {
			fmt.Fprintf(stderr, "FAIL %s %q: %s\n%s", t.Module, t.Name, t.Result+" "+t.Detail, t.Output)
			status = 1
		}
	}
	if *out != "" {
		b, _ := json.MarshalIndent(rec, "", "  ")
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "mk bench: %v\n", err)
			return 2
		}
	}
	return status
}
