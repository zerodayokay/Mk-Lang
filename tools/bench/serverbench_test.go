// Why: a server row is fair only if its spec meets the contract and its
// sessions catch every wrong reply and every wrong log.
// What: tests of the server specs, the kv and api sessions against their Go
// twins and mutants, renderServer, serverDefect and the load window.
// How: runs the Go twins on free ports with short checked loads; nothing
// is timed.

package main

// Tests of the app table's server rows (serverbench.go, kvbench.go,
// apibench.go). None of them times anything.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestServerRowsMeetTheContract: each server row of the tree loads, is a
// valid app row (checkApp: kinds, hand, the Mk package), and meets the
// app table's contract (appRowViolations, with its labelled unsafe
// modules), every twin opening with the row's header; each
// column with a source names its model, and C's absence is stated.
func TestServerRowsMeetTheContract(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, name := range serverRows {
		s, err := loadServerSpec(root, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r, err := s.row()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, v := range appRowViolationsLabelled(r, s.UnsafeModules) {
			t.Errorf("%s: %s", name, v)
		}
		for lang := range r.Sources {
			if s.Models[lang] == "" {
				t.Errorf("%s: %s has a source and no model", name, lang)
			}
		}
		if s.Skip["c"] == "" {
			t.Errorf("%s: C has no source and no skip", name)
		}
		if len(s.UnsafeModules) > 0 && !declaresExactly(filepath.Join(r.Dir, r.Sources["mk"]), s.UnsafeModules) {
			t.Errorf("%s: unsafe_modules %q are not exactly the package's", name, s.UnsafeModules)
		}
		if _, err := os.Stat(filepath.Join(s.Dir, "bench.json")); err == nil {
			t.Errorf("%s: a server row has no bench.json (discover would hand it to verify and publish)", name)
		}
	}
}

// TestServerSpecRefusals: a spec without its label's reason, with two
// loads, or whose args never name the port, is refused.
func TestServerSpecRefusals(t *testing.T) {
	good, err := os.ReadFile(filepath.Join("..", "..", "tests", "bench", "app", "kvserve", "kvserve.json"))
	if err != nil {
		t.Fatal(err)
	}
	for what, edit := range map[string]func(m map[string]any){
		"unsafe_why":   func(m map[string]any) { delete(m, "unsafe_why") },
		"one load":     func(m map[string]any) { m["api"] = map[string]any{"items": 8} },
		"{port}":       func(m map[string]any) { m["args"] = []string{"serve"} },
		"sockets":      func(m map[string]any) { m["connections"] = 6000 },
		"no source":    func(m map[string]any) { m["models"].(map[string]any)["c"] = "x" },
		"percentages":  func(m map[string]any) { m["kv"].(map[string]any)["get"] = 51 },
		"keys must be": func(m map[string]any) { m["kv"].(map[string]any)["keys"] = 3 },
	} {
		var m map[string]any
		if err := json.Unmarshal(good, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		b, _ := json.Marshal(m)
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "tests", "bench", "app", "kvserve", "kvserve.json"), string(b))
		if _, err := loadServerSpec(root, "kvserve"); err == nil || !strings.Contains(err.Error(), what) {
			t.Errorf("%s: %v", what, err)
		}
	}
}

// TestKVSessionDeterministic: a connection's requests depend on the seed
// and the connection only, each connection's keys are its own, and the
// mix reaches every command.
func TestKVSessionDeterministic(t *testing.T) {
	s, err := loadServerSpec(filepath.Join("..", ".."), "kvserve")
	if err != nil {
		t.Fatal(err)
	}
	run := func(i int) []string {
		k := newKVSession(s, i).(*kvSession)
		var out []string
		for n := 0; n < 2000; n++ {
			out = append(out, string(k.request()))
			// pretend each was answered as the model says
			if k.set {
				k.model[k.key] = k.val
			} else if k.del {
				delete(k.model, k.key)
			}
		}
		return out
	}
	a, b, c := run(3), run(3), run(4)
	if !slices.Equal(a, b) {
		t.Fatal("one connection drew two different streams")
	}
	seen := map[string]bool{}
	for _, l := range a {
		f := strings.Fields(l)
		seen[f[0]] = true
		if !strings.HasPrefix(f[1], "c3k") {
			t.Fatalf("connection 3 used key %s", f[1])
		}
		if f[0] == "SET" && (strings.HasSuffix(l, " \n") || strings.HasPrefix(l[len("SET ")+len(f[1])+1:], " ")) {
			t.Fatalf("a value with an outer space: %q", l)
		}
	}
	for _, w := range []string{"GET", "SET", "INCR", "DEL"} {
		if !seen[w] {
			t.Errorf("no %s in 2,000 requests", w)
		}
	}
	if slices.Equal(a, c) {
		t.Error("connections 3 and 4 drew the same stream")
	}
}

// TestKVSessionRejectsWrongReply: a server that answers every request +OK
// fails the first GET, which expects "_".
func TestKVSessionRejectsWrongReply(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					if _, err := br.ReadString('\n'); err != nil {
						return
					}
					c.Write([]byte("+OK\n"))
				}
			}(c)
		}
	}()
	s, err := loadServerSpec(filepath.Join("..", ".."), "kvserve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkLoad(l.Addr().String(), s.sessions(2), 100, 10*time.Second); err == nil || !strings.Contains(err.Error(), "answered") {
		t.Fatalf("a server answering +OK to everything passed: %v", err)
	}
}

// TestKVGoTwinAndLogCheck builds the row's Go twin and checks it as kv
// -check does (a fixed number of requests, nothing timed), then shows
// that kvWalCheck refuses a log with a flipped byte, a lost last record,
// a log that holds only its header, and models the log does not give.
func TestKVGoTwinAndLogCheck(t *testing.T) {
	root := filepath.Join("..", "..")
	s, err := loadServerSpec(root, "kvserve")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "kvserve-go.exe")
	build := exec.Command("go", "build", "-o", exe, filepath.Join(s.Dir, s.Sources["go"]))
	build.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	data := filepath.Join(dir, "data")
	col, _ := columnByID("go")
	cmd, addr, err := startServerArgs(exe, func(port int) []string { return s.args(col, port, data) }, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	ss := s.sessions(8)
	n, err := checkLoad(addr, ss, 300, 60*time.Second)
	cmd.Process.Kill()
	cmd.Wait()
	if err != nil || n != 8*300 {
		t.Fatalf("checkLoad: %d requests, %v", n, err)
	}
	note, err := kvWalCheck(data, ss)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(note)
	wal := filepath.Join(data, "kvd.wal")
	good, err := os.ReadFile(wal)
	if err != nil {
		t.Fatal(err)
	}
	try := func(what string, b []byte, ss []session, want string) {
		t.Helper()
		if err := os.WriteFile(wal, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := kvWalCheck(data, ss); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error naming %q", what, err, want)
		}
	}
	flip := bytes.Clone(good)
	flip[len(flip)-1] ^= 0x20
	try("a flipped byte", flip, ss, "CRC-32")
	// the last record, cut off whole: one change short
	last := kvWalHeader
	for pos := kvWalHeader; pos < len(good); {
		last = pos
		pos += 8 + int(uint32(good[pos])|uint32(good[pos+1])<<8|uint32(good[pos+2])<<16|uint32(good[pos+3])<<24)
	}
	try("the last record lost", good[:last], ss, "records")
	try("a torn last record", good[:len(good)-2], ss, "length")
	try("only the header", good[:kvWalHeader], ss, "records")
	try("not a log", []byte("hello"), ss, "header")
	// a model the log does not give: one session told otherwise
	if err := os.WriteFile(wal, good, 0o644); err != nil {
		t.Fatal(err)
	}
	k := ss[0].(*kvSession)
	var told, was string
	for key := range k.model {
		told, was = key, k.model[key]
		k.model[key] += "x"
		break
	}
	if _, err := kvWalCheck(data, ss); err == nil || !strings.Contains(err.Error(), "replays") {
		t.Errorf("a model the log does not give: %v", err)
	}
	k.model[told] = was
	if _, err := kvWalCheck(data, ss); err != nil {
		t.Fatalf("the good log again: %v", err)
	}
	// Two logs that replay to the same store
	// with the same record count, which only the per-connection order
	// check refuses. The records, whole, and the keys connection 0 owns:
	type rec struct {
		b   []byte
		key string
	}
	var recs []rec
	for pos := kvWalHeader; pos < len(good); {
		n := int(binary.LittleEndian.Uint32(good[pos:]))
		p := good[pos+8 : pos+8+n]
		kl := int(binary.LittleEndian.Uint16(p[1:]))
		recs = append(recs, rec{good[pos : pos+8+n], string(p[3 : 3+kl])})
		pos += 8 + n
	}
	mine := map[string]bool{}
	for _, key := range k.keys {
		mine[key] = true
	}
	join := func(rs []rec) []byte {
		out := bytes.Clone(good[:kvWalHeader])
		for _, r := range rs {
			out = append(out, r.b...)
		}
		return out
	}
	// (1) two consecutive changes of connection 0, of different keys,
	// swapped: each key's own records keep their order.
	swapped := false
	for a := 0; a < len(recs) && !swapped; a++ {
		if !mine[recs[a].key] {
			continue
		}
		for b := a + 1; b < len(recs); b++ {
			if !mine[recs[b].key] {
				continue
			}
			if recs[b].key != recs[a].key {
				rs := slices.Clone(recs)
				rs[a], rs[b] = rs[b], rs[a]
				try("connection 0's changes out of order", join(rs), ss, "in order")
				swapped = true
			}
			break
		}
	}
	if !swapped {
		t.Error("no two consecutive changes of connection 0 of different keys to swap")
	}
	// (2) a set of connection 0 that a later set of the same key
	// overwrites, given another value of the same length with its CRC-32
	// recomputed: the final store is unchanged.
	wrong := false
	for a := 0; a < len(recs) && !wrong; a++ {
		r := recs[a]
		if !mine[r.key] || r.b[8] != kvOpSet {
			continue
		}
		for b := a + 1; b < len(recs); b++ {
			if recs[b].key == r.key && recs[b].b[8] == kvOpSet {
				p := bytes.Clone(r.b[8:])
				p[len(p)-1] ^= 1 // a digit or letter stays printable
				nb := bytes.Clone(r.b[:8])
				binary.LittleEndian.PutUint32(nb[4:], crc32.ChecksumIEEE(p))
				rs := slices.Clone(recs)
				rs[a] = rec{append(nb, p...), r.key}
				try("a wrong value later overwritten", join(rs), ss, "in order")
				wrong = true
				break
			}
		}
	}
	if !wrong {
		t.Error("no set of connection 0 overwritten by a later set to change")
	}
}

// TestAPISessionDeterministic: a connection's requests depend on the seed
// and the connection only, and the mix reaches every route.
func TestAPISessionDeterministic(t *testing.T) {
	s, err := loadServerSpec(filepath.Join("..", ".."), "httpapi")
	if err != nil {
		t.Fatal(err)
	}
	run := func(i int) []string {
		a := newAPISession(s, i).(*apiSession)
		var out []string
		for n := 0; n < 3000; n++ {
			out = append(out, string(a.request()))
			// pretend each was answered as the model says
			switch {
			case a.kind == apiPost:
				a.last++
				a.next.id = a.last
				a.live = append(a.live, a.next)
			case a.kind == apiPut && a.at >= 0:
				a.live[a.at] = a.next
			case a.kind == apiDel && a.at >= 0:
				a.gone = a.live[a.at].id
				a.live = append(a.live[:a.at], a.live[a.at+1:]...)
			}
			if len(a.live) > s.API.Live {
				t.Fatalf("connection %d keeps %d items", i, len(a.live))
			}
		}
		return out
	}
	a, b, c := run(5), run(5), run(6)
	if !slices.Equal(a, b) {
		t.Fatal("one connection drew two different streams")
	}
	if slices.Equal(a, c) {
		t.Error("connections 5 and 6 drew the same stream")
	}
	seen := map[string]bool{}
	for _, r := range a {
		line := strings.SplitN(r, "\r\n", 2)[0]
		switch {
		case strings.HasPrefix(line, "GET /items?tag=c5-"):
			seen["list"] = true
		case strings.HasPrefix(line, "GET /items/"):
			seen["get"] = true
		case strings.Contains(r, `"qty":-1}`):
			seen["bad"] = true
		default:
			seen[strings.Fields(line)[0]] = true
		}
		if strings.Contains(r, "Accept-Encoding: gzip") {
			seen["gzip"] = true
		}
	}
	for _, w := range []string{"POST", "get", "list", "PUT", "DELETE", "bad", "gzip"} {
		if !seen[w] {
			t.Errorf("no %s in 3,000 requests", w)
		}
	}
}

// TestAPIGoTwinAndMutants builds the row's Go twin and checks it as http
// -row httpapi -check does (nothing timed), with lists, gzip bodies, 404s
// and 422s among the responses; then puts a proxy between the generator
// and the twin that changes one thing, and each change fails the check.
func TestAPIGoTwinAndMutants(t *testing.T) {
	root := filepath.Join("..", "..")
	s, err := loadServerSpec(root, "httpapi")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "httpapi-go.exe")
	build := exec.Command("go", "build", "-o", exe, filepath.Join(s.Dir, s.Sources["go"]))
	build.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	col, _ := columnByID("go")
	// each run gets a fresh server: the connections' models start empty
	start := func() string {
		cmd, addr, err := startServerArgs(exe, func(port int) []string { return s.args(col, port, dir) }, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		return addr
	}
	addr := start()
	ss := s.sessions(8)
	n, err := checkLoad(addr, ss, 400, 60*time.Second)
	if err != nil || n != 8*400 {
		t.Fatalf("checkLoad: %d requests, %v", n, err)
	}
	note := apiNote(ss)
	t.Log(note)
	var gz, nf, rf, ls int64
	for _, x := range ss {
		a := x.(*apiSession)
		gz, nf, rf, ls = gz+a.gzipped, nf+a.notFound, rf+a.refused, ls+a.lists
	}
	if gz == 0 || nf == 0 || rf == 0 || ls == 0 {
		t.Fatalf("the mix missed a kind of response: %s", note)
	}
	for what, c := range map[string]struct {
		edit func(r *http.Response, body []byte) []byte
		want string
	}{
		"gzip dropped": {func(r *http.Response, b []byte) []byte {
			if r.Header.Get("Content-Encoding") != "gzip" {
				return b
			}
			zr, _ := gzip.NewReader(bytes.NewReader(b))
			out, _ := io.ReadAll(zr)
			r.Header.Del("Content-Encoding")
			return out
		}, "Content-Encoding"},
		"a body byte": {func(r *http.Response, b []byte) []byte {
			if r.StatusCode != 200 || r.Header.Get("Content-Encoding") != "" {
				return b
			}
			return bytes.Replace(b, []byte(`"qty":`), []byte(`"qty": `), 1)
		}, "body"},
		"no Location": {func(r *http.Response, b []byte) []byte {
			r.Header.Del("Location")
			return b
		}, "Location"},
		"404 as 200": {func(r *http.Response, b []byte) []byte {
			if r.StatusCode == 404 {
				r.StatusCode = 200
			}
			return b
		}, "status"},
	} {
		target, _ := url.Parse("http://" + start())
		p := httputil.NewSingleHostReverseProxy(target)
		p.Transport = &http.Transport{DisableCompression: true}
		p.ModifyResponse = func(r *http.Response) error {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return err
			}
			r.Body.Close()
			b = c.edit(r, b)
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
			r.Header.Set("Content-Length", strconv.Itoa(len(b)))
			return nil
		}
		front := serveFor(t, p.ServeHTTP)
		if _, err := checkLoad(front, s.sessions(4), 200, 60*time.Second); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", what, err, c.want)
		}
	}
}

// TestRenderServer: a server row's section names its kinds, models,
// sweeps, the labelled unsafe module and the skipped languages, and
// RESULTS.md carries it once the row has a results file.
func TestRenderServer(t *testing.T) {
	res := &serverResults{ClockStep: 0.0005,
		Spec: serverSpec{Name: "kvserve", Task: "Serve keys.", Connections: 256, WarmupS: 5, MeasureS: 30, Sweeps: 2, ServerMask: "0xff", ClientMask: "0xff00", Threads: 8,
			Args: []string{"serve", "{port}"}, MkArgs: []string{"--compact-after", "1000000000"},
			Hand:          map[string]string{"rust": "a CRC-32, 25 lines"},
			Notes:         []string{"A note of the row."},
			UnsafeModules: []string{"src/snap"}, UnsafeWhy: "Never reached.", Skip: map[string]string{"c": "no std sockets"}},
		Package: &pkgFact{Path: "Dogfoods/kvd", Commit: "abc123"},
		Cells: []serverCell{
			{Col: "mk", Kind: "std", Class: "checked", Model: "tasks", RPS: 128000, P50: 0.002, P99: 0.004, Sweeps: []loadResult{{RPS: 127000}, {RPS: 129000}}},
			{Col: "rust", Kind: "hand", Class: "bounds", Model: "OS threads", RPS: 512000, P50: 0.001, P99: 0.003, Sweeps: []loadResult{{RPS: 512000}}},
			{Col: "go", Kind: "std", Class: "bounds", Model: "goroutines", RPS: 192000, P50: 0.001, P99: 0.003, Sweeps: []loadResult{{RPS: 190000}, {RPS: 194000}}},
			// An excluded cell is never the reference, however fast its
			// sweeps were before it failed.
			{Col: "zig-safe", Kind: "std", Class: "checked", Model: "OS threads", RPS: 900000, Sweeps: []loadResult{{RPS: 900000}}, Excluded: "did not accept"},
		}}
	md := renderServer(res, "2026-11-16")
	for _, w := range []string{"## Table \"app\", row kvserve",
		// kind, class, ratio to the best (rust) and to the best of its kind (go for std)
		"| mk | std | checked | tasks | 128000 (127000, 129000) | 2.0 ms | 2.0 ms | 4.0 ms | 0.25 | 0.67 |",
		"| rust | hand | bounds | OS threads | 512000 (512000) |", "| 1.00 | 1.00 |",
		"| go | std | bounds | goroutines | 192000 (190000, 194000) |", "| 0.38 | 1.00 |",
		"| zig-safe | std | checked | OS threads | excluded: did not accept |",
		"Every column runs `serve {port}`; the Mk column adds its own flags, `--compact-after 1000000000`.",
		"- `rust` is `hand`: a CRC-32, 25 lines.", "- A note of the row.", "Peak memory is not measured",
		"the request is built before the clock starts",
		"Defect line: `mk` against the best std cell, `go`: best std req/s over Mk req/s 1.50 (sweep range 1.47 to 1.53, 2 by 2 sweeps; no 95% interval, so a provisional rule), within 2.0 in every pair of sweeps.",
		"unsafe modules src/snap. Never reached.",
		"`Dogfoods/kvd` at abc123", "| c | | | | no std sockets |", "(CPUs 0-7)", "(CPUs 8-15)", "500 us"} {
		if !strings.Contains(md, w) {
			t.Errorf("render lacks %q:\n%s", w, md)
		}
	}
	if strings.Contains(md, "gunzip") {
		t.Errorf("a kv row's section names the api load's gunzip:\n%s", md)
	}
	if strings.Contains(md, "`zig-safe`: best") {
		t.Errorf("an excluded cell was the defect line's reference:\n%s", md)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "tests", "bench", "results", "2026-11-16")
	b, _ := json.Marshal(res)
	writeFile(t, filepath.Join(dir, "kvserve", "results.json"), string(b))
	got, err := loadServerIfAny(root, "2026-11-16", "kvserve")
	if err != nil || got == nil || got.Cells[0].RPS != 128000 {
		t.Fatalf("loadServerIfAny: %v %v", got, err)
	}
	if none, err := loadServerIfAny(root, "2026-11-16", "httpapi"); none != nil || err != nil {
		t.Fatalf("a row with no results: %v %v", none, err)
	}
}

// TestServerDefect: the defect line on a server row reads the sweeps' range:
// over only when every pair of sweeps is over 2.0, within only when every
// pair is at or below it, a rule the line calls provisional; "over" blames
// the row's program and std's server path together, never a profile's std
// function; the reference is the best live std cell of another language,
// never Mk's own or an excluded or hand cell.
func TestServerDefect(t *testing.T) {
	sw := func(r ...float64) []loadResult {
		var out []loadResult
		for _, x := range r {
			out = append(out, loadResult{RPS: x})
		}
		return out
	}
	cell := func(col, kind string, rps ...float64) serverCell {
		return serverCell{Col: col, Kind: kind, RPS: median(rps), Sweeps: sw(rps...)}
	}
	for _, tc := range []struct {
		name  string
		cells []serverCell
		want  string
	}{
		{"over", []serverCell{cell("mk", "std", 100, 110), cell("go", "std", 250, 260), cell("zig-fast", "std", 230)},
			"`mk` against the best std cell, `go`: best std req/s over Mk req/s 2.43 (sweep range 2.27 to 2.60, 2 by 2 sweeps; no 95% interval, so a provisional rule), **over 2.0 in every pair of sweeps: a defect line against the row's program and std's server path together, not one std function**."},
		{"inconclusive", []serverCell{cell("mk", "std", 100, 140), cell("go", "std", 210, 230)},
			"best std req/s over Mk req/s 1.83 (sweep range 1.50 to 2.30, 2 by 2 sweeps; no 95% interval, so a provisional rule), inconclusive at 2.0."},
		{"hand is no reference", []serverCell{cell("mk", "std", 100), cell("rust", "hand", 900), cell("go", "std", 150)},
			"against the best std cell, `go`: best std req/s over Mk req/s 1.50"},
		{"no std cell", []serverCell{cell("mk", "std", 100), cell("rust", "hand", 900), {Col: "go", Kind: "std", RPS: 500, Sweeps: sw(500), Excluded: "failed"}},
			"`mk` has no std cell of C, Rust, Zig, Go or C# to be judged against."},
	} {
		got := strings.Join(serverDefect(tc.cells), "\n")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to hold %q", tc.name, got, tc.want)
		}
		// A server spec names no profile, so no verdict may blame "the std
		// function the row's profile names".
		if strings.Contains(got, "profile") || strings.Contains(got, "a std defect") {
			t.Errorf("%s: a server row's defect line blames one std function: %q", tc.name, got)
		}
	}
	if got := serverDefect([]serverCell{cell("go", "std", 100), {Col: "mk", Kind: "std", Excluded: "failed"}}); len(got) != 0 {
		t.Errorf("an excluded Mk cell has no defect line: %q", got)
	}
}

// TestServerGenerateWindow: the measured-window path that kv and http
// -row httpapi take without -check (generate with the row's sessions,
// then the after check) works on each row's Go twin. It asserts only that
// requests completed and every reply and the log checked; no rate or
// latency is read or logged.
func TestServerGenerateWindow(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, name := range serverRows {
		s, err := loadServerSpec(root, name)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		exe := filepath.Join(dir, name+"-go.exe")
		build := exec.Command("go", "build", "-o", exe, filepath.Join(s.Dir, s.Sources["go"]))
		build.Env = append(os.Environ(), "GOFLAGS=")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%s: go build: %v\n%s", name, err, out)
		}
		data := filepath.Join(dir, "data")
		col, _ := columnByID("go")
		cmd, addr, err := startServerArgs(exe, func(port int) []string { return s.args(col, port, data) }, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		ss := s.sessions(8)
		lr, err := generate(loadOptions{Addr: addr, Connections: 8, Warmup: 100 * time.Millisecond, Measure: 200 * time.Millisecond,
			Session: func(i int) session { return ss[i] }})
		cmd.Process.Kill()
		cmd.Wait()
		if err != nil || lr.Requests == 0 {
			t.Fatalf("%s: generate: %d requests, %v", name, lr.Requests, err)
		}
		if _, err := s.after(data, ss); err != nil {
			t.Fatalf("%s: after the window: %v", name, err)
		}
	}
}
