// Why: a server is not a program hyperfine can time, yet the app table
// (tests/bench/README.md "Applications", "Server rows") compares real
// servers in each language; they need their own runner and section.
//
// What: serverSpec and loadServerSpec, serverTable and serverRun (build,
// start and load each server column), renderServer and serverDefect.
//
// How: a real server program in each language runs under one of
// tools/bench's load generators, in the http row's shape (httpbench.go):
// keep-alive connections, one request outstanding on each, warm-up, a measured
// window, req/s, p50 and p99, the server on one CPU set and the generator
// on another. Each connection's session (httpbench.go) checks every reply
// against its own model of the server; a row may check the server's
// files afterwards too (kvserve's write-ahead log, kvbench.go).
//
//	tools/bench kv -check             build the servers, a fixed number of checked requests, nothing timed
//	tools/bench kv                    a short unpinned window: provisional numbers
//	tools/bench kv -publish           the row, under the machine lock
//	tools/bench http -row httpapi ... the same for app/httpapi (apibench.go)
//
// A server row's spec is tests/bench/app/<row>/<row>.json, never a
// bench.json: a server is not a program hyperfine can time, so discover
// does not list it, and verify and publish never run it.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// serverSpec is tests/bench/app/<row>/<row>.json.
type serverSpec struct {
	Name string `json:"name"`
	Task string `json:"task"`
	// Package names the Mk side as a package elsewhere in the repository
	// (kvserve's Dogfoods/kvd, unchanged); else sources.mk names it.
	Package *pkgSpec `json:"package,omitempty"`
	// Args is every column's command line: {port} is the port the server
	// listens on at 127.0.0.1, {data} a fresh, empty directory of its own.
	// MkArgs follow it in the Mk column only, for the dogfood's own flags
	// (README.md says why each is there).
	Args    []string          `json:"args"`
	MkArgs  []string          `json:"mk_args,omitempty"`
	Sources map[string]string `json:"sources"`
	Kinds   map[string]string `json:"kinds"`
	Hand    map[string]string `json:"hand,omitempty"`
	Skip    map[string]string `json:"skip"`
	Models  map[string]string `json:"models"`
	// UnsafeModules is a labelled exception: the
	// unsafe modules the Mk package declares, exactly, which a twinned row
	// may keep only with UnsafeWhy saying why the measured work does not
	// reach them. The contract test (TestServerRowsMeetTheContract) passes
	// the row only when the package declares exactly these.
	UnsafeModules []string `json:"unsafe_modules,omitempty"`
	UnsafeWhy     string   `json:"unsafe_why,omitempty"`

	Connections int `json:"connections"`
	// CheckRequests is the requests per connection of -check, which times
	// nothing.
	CheckRequests int    `json:"check_requests"`
	WarmupS       int    `json:"warmup_s"`
	MeasureS      int    `json:"measure_s"`
	ServerMask    string `json:"server_mask"`
	ClientMask    string `json:"client_mask"`
	Threads       int    `json:"threads"` // GOMAXPROCS and MK_CARRIERS of the servers, GOMAXPROCS of the generator
	Sweeps        int    `json:"sweeps"`
	Seed          uint64 `json:"seed"`
	// The row's load: exactly one of them.
	KV  *kvLoad  `json:"kv,omitempty"`
	API *apiLoad `json:"api,omitempty"`

	Notes []string `json:"notes"`

	Dir string `json:"-"`
}

// serverRows are the app table's server rows, by the subcommand that runs
// each (RESULTS.md prints them in this order).
var serverRows = []string{"kvserve", "httpapi"}

func loadServerSpec(root, name string) (*serverSpec, error) {
	dir := filepath.Join(root, "tests", "bench", "app", name)
	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return nil, err
	}
	var s serverSpec
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s.json: %w", name, err)
	}
	s.Dir = dir
	if s.Name != name {
		return nil, fmt.Errorf("%s.json: name %q", name, s.Name)
	}
	if 2*s.Connections+64 > socketCap || s.Connections < 1 {
		return nil, fmt.Errorf("%s.json: %d connections: the generator and the server hold two sockets each, and the cap is %d", name, s.Connections, socketCap)
	}
	if s.Task == "" || s.MeasureS < 1 || s.Threads < 1 || s.Sweeps < 1 || s.CheckRequests < 1 {
		return nil, fmt.Errorf("%s.json: task, measure_s, threads, sweeps and check_requests are required", name)
	}
	for _, m := range []string{s.ServerMask, s.ClientMask} {
		if _, err := parseMask(m); err != nil {
			return nil, fmt.Errorf("%s.json: %w", name, err)
		}
	}
	if (s.KV == nil) == (s.API == nil) {
		return nil, fmt.Errorf("%s.json: a server row has one load, kv or api", name)
	}
	if !slices.ContainsFunc(s.Args, func(a string) bool { return strings.Contains(a, "{port}") }) {
		return nil, fmt.Errorf("%s.json: args never name {port}", name)
	}
	if (len(s.UnsafeModules) > 0) != (s.UnsafeWhy != "") {
		return nil, fmt.Errorf("%s.json: unsafe_modules and unsafe_why go together (the exception is labelled)", name)
	}
	for lang := range s.Models {
		if _, ok := s.Sources[lang]; !ok && !(lang == "mk" && s.Package != nil) {
			return nil, fmt.Errorf("%s.json: models names %s, which has no source", name, lang)
		}
	}
	if s.KV != nil {
		if err := s.KV.check(); err != nil {
			return nil, fmt.Errorf("%s.json: kv: %w", name, err)
		}
	}
	if s.API != nil {
		if err := s.API.check(); err != nil {
			return nil, fmt.Errorf("%s.json: api: %w", name, err)
		}
	}
	return &s, nil
}

// row is the spec as an app row, checked by checkApp (kinds, hand, the Mk
// package), so the servers build with the columns' commands.
func (s *serverSpec) row() (*row, error) {
	r := &row{Name: s.Name, Table: "app", Dir: s.Dir, Sources: maps.Clone(s.Sources), Skip: s.Skip,
		Kinds: s.Kinds, Hand: s.Hand, Package: s.Package,
		Concurrency: &concSpec{Mask: s.ServerMask, Threads: s.Threads, Models: s.Models}}
	if r.Sources == nil {
		r.Sources = map[string]string{}
	}
	if err := checkApp(r); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Name, err)
	}
	return r, nil
}

// args is column col's command line for port and data.
func (s *serverSpec) args(col column, port int, data string) []string {
	all := slices.Clone(s.Args)
	if isMkLang(col.Lang) {
		all = append(all, s.MkArgs...)
	}
	for i, a := range all {
		all[i] = strings.ReplaceAll(strings.ReplaceAll(a, "{port}", strconv.Itoa(port)), "{data}", data)
	}
	return all
}

// sessions is one sweep's sessions, one per connection.
func (s *serverSpec) sessions(n int) []session {
	out := make([]session, n)
	for i := range out {
		if s.KV != nil {
			out[i] = newKVSession(s, i)
		} else {
			out[i] = newAPISession(s, i)
		}
	}
	return out
}

// after checks what the server left behind once it is stopped: kvserve's
// log (kvWalCheck); for httpapi it counts what the responses held. It
// returns a line for the log.
func (s *serverSpec) after(data string, ss []session) (string, error) {
	if s.KV != nil {
		return kvWalCheck(data, ss)
	}
	return apiNote(ss), nil
}

type serverOptions struct {
	Root, Work, Mk string
	Name           string
	Check          bool // the fixed requests of check_requests, nothing timed
	Publish        bool
	LockPath       string
	Date           string
	Log            func(string)
}

// serverCell is one server column of a server row.
type serverCell struct {
	Col      string       `json:"column"`
	Kind     string       `json:"kind"`
	Class    string       `json:"class"` // appClass: the column's class, or eco's "not classed"
	Model    string       `json:"model"`
	Sweeps   []loadResult `json:"sweeps"`
	RPS      float64      `json:"rps"`
	P50      float64      `json:"p50"`
	P99      float64      `json:"p99"`
	Build    buildCmd     `json:"build"`
	Excluded string       `json:"excluded,omitempty"`
}

type serverResults struct {
	Run       facts        `json:"run"`
	ClockStep float64      `json:"clock_step"`
	Spec      serverSpec   `json:"spec"`
	Package   *pkgFact     `json:"package,omitempty"`
	Cells     []serverCell `json:"cells"`
}

// serverTable builds every server column of the row and runs its load
// against each: with Check, check_requests per connection and the after
// check, once, timing nothing; else a short unpinned window (provisional),
// or, with Publish, Sweeps sweeps in rotated order under the machine lock.
// Every server starts on a fresh port and a fresh data directory, which
// is deleted after it stops.
func serverTable(o serverOptions, w func(string)) error {
	if runtime.GOOS != "windows" {
		return errors.New("the server rows run on Windows")
	}
	if o.Check && o.Publish {
		return errors.New("-check times nothing and -publish measures: give one")
	}
	spec, err := loadServerSpec(o.Root, o.Name)
	if err != nil {
		return err
	}
	if o.Publish {
		lock, err := acquireLock(o.LockPath, o.Log)
		if err != nil {
			return err
		}
		defer lock.Release()
		o.Log("holding the machine lock " + lock.Path())
	}
	t, err := findToolchain(o.Work, o.Mk)
	if err != nil {
		return err
	}
	r, err := spec.row()
	if err != nil {
		return err
	}
	bs := buildAll(t, spec.Name, []*row{r}, nil, o.Log)
	lo := loadOptions{Connections: spec.Connections,
		Warmup: time.Duration(spec.WarmupS) * time.Second, Measure: time.Duration(spec.MeasureS) * time.Second}
	sweeps := spec.Sweeps
	if !o.Publish {
		lo.Connections, lo.Warmup, lo.Measure, sweeps = min(50, spec.Connections), time.Second, 2*time.Second, 1
	}
	if o.Check {
		lo.Connections = spec.Connections
	}
	res := serverResults{Spec: *spec}
	serverMask, _ := parseMask(spec.ServerMask)
	clientMask, _ := parseMask(spec.ClientMask)
	if o.Publish {
		res.ClockStep = clockStep()
		res.Run = gatherFacts(o.Root, t)
		res.Run.Date = time.Now().Format("2006-01-02 15:04")
		res.Run.MkBinary, res.Run.MkSHA256, res.Run.MkStd = o.Mk, fileSHA(o.Mk), t.MkStd
		res.Run.Affinity = fmt.Sprintf("server %s, generator %s (set on tools/bench before each start; the server inherits it)", spec.ServerMask, spec.ClientMask)
		res.Package = packageFact(o.Root, spec.Package)
		defer setAffinity(^uint64(0) >> (64 - runtime.NumCPU()))
	}
	env := rowEnv(r)
	cells := map[string]*serverCell{}
	var order []string
	exe := map[string]*built{}
	for _, b := range bs {
		c := &serverCell{Col: b.Col.ID, Kind: spec.Kinds[b.Col.Lang], Class: appClass(r, b.Col), Model: spec.Models[b.Col.Lang], Build: b.Cmd}
		if b.Err != nil {
			c.Excluded = firstLines(b.Err.Error(), 6)
		}
		cells[b.Col.ID], order, exe[b.Col.ID] = c, append(order, b.Col.ID), b
	}
	dataRoot := filepath.Join(o.Work, "server-data", spec.Name)
	for s := 0; s < sweeps; s++ {
		for k := range order {
			id := order[(k+s)%len(order)]
			c := cells[id]
			if c.Excluded != "" {
				continue
			}
			lr, note, err := serverRun(spec, exe[id], env, filepath.Join(dataRoot, id), lo, o, serverMask, clientMask)
			if err != nil {
				c.Excluded = firstLines(err.Error(), 6)
				w(fmt.Sprintf("FAIL %s %s: %s", spec.Name, id, c.Excluded))
				continue
			}
			if o.Check {
				w(fmt.Sprintf("ok %s %s (%s): %d connections, %d requests, every reply checked%s (nothing timed)", spec.Name, id, c.Kind, lo.Connections, lr.Requests, note))
				continue
			}
			c.Sweeps = append(c.Sweeps, lr)
			o.Log(fmt.Sprintf("%s %s sweep %d: %.0f req/s, p50 %s, p99 %s%s", spec.Name, id, s+1, lr.RPS, fmtSec(lr.P50), fmtSec(lr.P99), note))
		}
	}
	failed := 0
	tag := "PROVISIONAL (no machine lock, unpinned, short)"
	if o.Publish {
		tag = "published"
	}
	for _, id := range order {
		c := cells[id]
		if c.Excluded != "" {
			failed++
			c.Sweeps = nil
		} else if !o.Check {
			var rps, p50, p99 []float64
			for _, s := range c.Sweeps {
				rps, p50, p99 = append(rps, s.RPS), append(p50, s.P50), append(p99, s.P99)
			}
			c.RPS, c.P50, c.P99 = median(rps), median(p50), median(p99)
			w(fmt.Sprintf("ok %s %s (%s, %s): %.0f req/s, p50 %s, p99 %s, %s", spec.Name, id, c.Kind, c.Model, c.RPS, fmtSec(c.P50), fmtSec(c.P99), tag))
		}
		res.Cells = append(res.Cells, *c)
	}
	if o.Publish {
		dir := filepath.Join(o.Root, "tests", "bench", "results", o.Date, spec.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "results.json"), append(b, '\n'), 0o644); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(o.Root, "tests", "bench", "results", o.Date, "results.json")); err == nil {
			if err := writeResultsMD(o.Root, o.Date); err != nil {
				return err
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d %s cells failed", failed, spec.Name)
	}
	return nil
}

// serverRun is one server's run: a fresh data directory, the server
// started (pinned when publishing), the load or the check, the server
// killed, the after check, and the directory deleted.
func serverRun(spec *serverSpec, b *built, env []string, data string, lo loadOptions, o serverOptions, serverMask, clientMask uint64) (loadResult, string, error) {
	if err := os.RemoveAll(data); err != nil {
		return loadResult{}, "", err
	}
	if err := os.MkdirAll(data, 0o755); err != nil {
		return loadResult{}, "", err
	}
	defer os.RemoveAll(data)
	if o.Publish {
		if err := setAffinity(serverMask); err != nil {
			return loadResult{}, "", err
		}
	}
	cmd, addr, err := startServerArgs(b.Exe, func(port int) []string { return spec.args(b.Col, port, data) }, env)
	if err != nil {
		return loadResult{}, "", err
	}
	stop := func() {
		if cmd != nil {
			cmd.Process.Kill()
			cmd.Wait()
			cmd = nil
		}
	}
	defer stop()
	if o.Publish {
		if err := setAffinity(clientMask); err != nil {
			return loadResult{}, "", err
		}
	}
	ss := spec.sessions(lo.Connections)
	var lr loadResult
	prev := runtime.GOMAXPROCS(spec.Threads)
	if o.Check {
		lr.Requests, err = checkLoad(addr, ss, spec.CheckRequests, serverCheckLimit)
	} else {
		lo.Addr = addr
		lo.Session = func(i int) session { return ss[i] }
		lr, err = generate(lo)
	}
	runtime.GOMAXPROCS(prev)
	stop()
	if err != nil {
		return lr, "", err
	}
	note, err := spec.after(data, ss)
	return lr, note, err
}

// serverCheckLimit is how long -check may take on one server: the check is
// a few thousand requests per connection, done in seconds.
const serverCheckLimit = 120 * time.Second

func loadServerIfAny(root, date, name string) (*serverResults, error) {
	b, err := os.ReadFile(filepath.Join(root, "tests", "bench", "results", date, name, "results.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r serverResults
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s results of %s: %w", name, date, err)
	}
	return &r, nil
}

// renderServer is a server row's section of RESULTS.md: what the app table
// prints of every row (README "Applications"): each cell's kind and class,
// its ratio to the best cell and to the best cell of its kind, the
// hand-written parts, the Mk column's own flags, the notes, and the defect
// line (serverDefect). A server row's peak memory is not read, and the
// section says so.
func renderServer(res *serverResults, date string) string {
	var w strings.Builder
	s := res.Spec
	fmt.Fprintf(&w, "## Table \"app\", row %s\n\nFrom `tests/bench/results/%s/%s/results.json`. %s Each server under tools/bench's load generator: %d keep-alive connections, one request outstanding on each, every reply checked against the connection's own model of the server, %d s of warm-up and %d s measured, %d sweeps in rotated order, each a fresh server on a fresh data directory; the server on affinity mask %s (CPUs %s), the generator on %s (CPUs %s), %d threads each (GOMAXPROCS, MK_CARRIERS). req/s is the median of the sweeps, p50 and p99 the medians of the sweeps' latencies, timed by Go's clock, whose step on this machine was %s; the mean latency is exact by Little's law (connections / req/s). A sample spans the request's write, the response's read and the session's check of it against the model%s, on the generator's CPUs; the request is built before the clock starts (httpbench.go, generate). Peak memory is not measured on a server row.\n\n",
		s.Name, date, s.Name, s.Task, s.Connections, s.WarmupS, s.MeasureS, s.Sweeps, s.ServerMask, maskCPUs(s.ServerMask), s.ClientMask, maskCPUs(s.ClientMask), s.Threads, fmtSec(res.ClockStep), gunzipNote(s))
	fmt.Fprintf(&w, "Every column runs `%s`", strings.Join(s.Args, " "))
	if len(s.MkArgs) > 0 {
		fmt.Fprintf(&w, "; the Mk column adds its own flags, `%s`", strings.Join(s.MkArgs, " "))
	}
	w.WriteString(".\n\n")
	if p := res.Package; p != nil {
		fmt.Fprintf(&w, "Mk package: `%s` at %s%s.\n\n", p.Path, orDash(p.Commit), map[bool]string{true: " (the tree differs there)", false: ""}[p.Dirty])
	}
	if s.UnsafeWhy != "" {
		fmt.Fprintf(&w, "Labelled: the Mk package declares the unsafe modules %s. %s\n\n", strings.Join(s.UnsafeModules, ", "), s.UnsafeWhy)
	}
	fmt.Fprintf(&w, "Machine: %s; %s; power scheme %s; affinity %s; measured %s.\n\n", orDash(res.Run.CPU), orDash(res.Run.OS), orDash(res.Run.Power), orDash(res.Run.Affinity), orDash(res.Run.Date))
	live := func(c serverCell) bool { return c.Excluded == "" && c.RPS > 0 }
	best := bestServer(res.Cells, live)
	w.WriteString("| Column | Kind | Class | Model | req/s (sweeps) | Mean latency | p50 | p99 | req/s / best | req/s / best of its kind |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range res.Cells {
		if !live(c) {
			why := c.Excluded
			if why == "" {
				why = "no request completed"
			}
			fmt.Fprintf(&w, "| %s | %s | %s | %s | excluded: %s | | | | | |\n", c.Col, orDash(c.Kind), orDash(c.Class), cellText(c.Model), cellText(why))
			continue
		}
		var sw []string
		for _, x := range c.Sweeps {
			sw = append(sw, strconv.FormatFloat(x.RPS, 'f', 0, 64))
		}
		kb := bestServer(res.Cells, func(x serverCell) bool { return live(x) && x.Kind == c.Kind })
		fmt.Fprintf(&w, "| %s | %s | %s | %s | %.0f (%s) | %s | %s | %s | %.2f | %.2f |\n", c.Col, orDash(c.Kind), orDash(c.Class), cellText(c.Model), c.RPS, strings.Join(sw, ", "), fmtSec(float64(s.Connections)/c.RPS), fmtSec(c.P50), fmtSec(c.P99), c.RPS/best.RPS, c.RPS/kb.RPS)
	}
	for _, l := range slices.Sorted(maps.Keys(s.Skip)) {
		fmt.Fprintf(&w, "| %s | | | | %s | | | | | |\n", l, cellText(s.Skip[l]))
	}
	for _, lang := range slices.Sorted(maps.Keys(s.Hand)) {
		fmt.Fprintf(&w, "\n- `%s` is `hand`: %s.", lang, s.Hand[lang])
	}
	for _, line := range serverDefect(res.Cells) {
		w.WriteString("\n- " + line)
	}
	for _, n := range s.Notes {
		w.WriteString("\n- " + n)
	}
	w.WriteString("\n\n")
	return w.String()
}

// gunzipNote is what a sample's check adds on a row whose responses may be
// compressed: the api load gunzips each compressed body.
func gunzipNote(s serverSpec) string {
	if s.API != nil {
		return " (and a gunzip of each compressed body)"
	}
	return ""
}

// bestServer is the cell of most req/s among those keep accepts, or nil.
func bestServer(cs []serverCell, keep func(serverCell) bool) *serverCell {
	var b *serverCell
	for i := range cs {
		if keep(cs[i]) && (b == nil || cs[i].RPS > b.RPS) {
			b = &cs[i]
		}
	}
	return b
}

// serverDefect is the defect line for each Mk cell of a server row (an Mk
// cell over 2 times the best std cell is a std defect): the best std cell of
// C, Rust, Zig or Go, by req/s, over the Mk cell's req/s, the ratio of
// medians. A row's few sweeps give no 95 percent interval, so the verdict
// reads the sweeps' range instead: over 2.0 only when the smallest ratio of
// any reference sweep to any Mk sweep is over it, within only when the
// largest is at or below it, and inconclusive otherwise. This sweep-range
// rule is provisional, and a server row's "over" blames the row's program and
// std's server path together, not one std function: a server spec names no
// profile, and the program's own work (kvd's log writes, client table and
// counters) is in the Mk cell. An excluded cell is never the reference.
func serverDefect(cs []serverCell) []string {
	live := func(c serverCell) bool { return c.Excluded == "" && c.RPS > 0 && len(c.Sweeps) > 0 }
	ref := bestServer(cs, func(c serverCell) bool { return live(c) && c.Kind == "std" && !isMkLang(langOf(c.Col)) })
	var out []string
	for _, c := range cs {
		if !isMkLang(langOf(c.Col)) || !live(c) {
			continue
		}
		if ref == nil {
			out = append(out, fmt.Sprintf("Defect line: `%s` has no std cell of C, Rust, Zig, Go or C# to be judged against.", c.Col))
			continue
		}
		lo, hi := math.Inf(1), 0.0
		for _, r := range ref.Sweeps {
			for _, m := range c.Sweeps {
				if m.RPS <= 0 {
					continue
				}
				lo, hi = min(lo, r.RPS/m.RPS), max(hi, r.RPS/m.RPS)
			}
		}
		verdict := "inconclusive at 2.0"
		switch {
		case lo > 2.0:
			verdict = "**over 2.0 in every pair of sweeps: a defect line against the row's program and std's server path together, not one std function**"
		case hi <= 2.0:
			verdict = "within 2.0 in every pair of sweeps"
		}
		out = append(out, fmt.Sprintf("Defect line: `%s` against the best std cell, `%s`: best std req/s over Mk req/s %.2f (sweep range %.2f to %.2f, %d by %d sweeps; no 95%% interval, so a provisional rule), %s.",
			c.Col, ref.Col, ref.RPS/c.RPS, lo, hi, len(ref.Sweeps), len(c.Sweeps), verdict))
	}
	return out
}
