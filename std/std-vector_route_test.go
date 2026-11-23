// Why: http.Dispatch follows Go's ServeMux routing rules, and a hand-written
// router drifts from its reference on overlapping patterns.
// What: TestSTDVECTOR_DispatchMatchesServeMux and its route generator.
// How: random route tables Go registers without panicking; each request's
// slot, params, 404 or 405 Allow must match Go's (redirects excluded).

package std

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// svRouteHandlers is how many routes a table may hold: one Mk handler per
// slot, each answering its slot number.
const svRouteHandlers = 8

// svRouteMain is the Mk side: lines of stdin "R method pattern" add a route
// (slot k is the table's k-th route), "C" starts a new table, and "Q method
// path" dispatches a request and writes the reply as Send writes it, a
// Status(code) answered as Serve does. Each handler sets X-R to its slot and
// the request's params, ";name=value" each, a field HEAD keeps.
func svRouteMain() string {
	var b strings.Builder
	b.WriteString(`import std.http

struct App {
    n: int
}

Answer(k: int, a: &Arena, req: &http.Request, res: mut http.Response): http.Error!void {
    var t := Text.New(a)
    var w := t.Writer()
    try Print(mut w, "{k}", (k))
    for i in 0..req.params.Len() {
        p := req.params[i]
        try t.Push(";")
        try t.Push(p.name)
        try t.Push("=")
        try t.Push(p.value)
    }
    try res.Header("X-R", t.Str())
}
`)
	for k := range svRouteHandlers {
		fmt.Fprintf(&b, `
H%d(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try Answer(%d, a, req, res)
}
`, k, k)
	}
	b.WriteString(`
RouteOf(k: int, m: str, p: str): http.Route[App] from m, p {
`)
	for k := range svRouteHandlers - 1 {
		fmt.Fprintf(&b, "    if k == %d {\n        return http.Route[App](method: m, pattern: p, handler: H%d)\n    }\n", k, k)
	}
	fmt.Fprintf(&b, "    return http.Route[App](method: m, pattern: p, handler: H%d)\n}\n", svRouteHandlers-1)
	b.WriteString(`
// Space is the index of the first space of s at or after i, or s.Len().
Space(s: str, i: int): int {
    var j := i
    for j < s.Len() && s.Byte(j) != 0x20 {
        j += 1
    }
    return j
}

export Main(sys: &System): http.Error|ReadError|WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    var inp := sys.Stdin()
    a := sys.arena
    var routes := List[http.Route[App]].New(a)
    app := App(n: 0)
    empty := List[http.Header].New(a).Done()
    for {
        got := try inp.Line(a)
        line := got else {
            break
        }
        if line.Len() == 0 {
            continue
        }
        if line.Byte(0) == 0x43 {
            routes = List[http.Route[App]].New(a)
            continue
        }
        e1 := Space(line, 2)
        m := line[2..e1]
        p := line[e1 + 1..]
        if line.Byte(0) == 0x52 {
            try routes.Push(RouteOf(routes.Items().Len(), m, p))
            continue
        }
        req := http.Request(method: m, path: p, query: "", headers: empty[..], params: empty[..], body: "".AsBytes())
        var res := http.Response.New(a)
        http.Dispatch(a, sys.io, routes.Items(), &app, &req, mut res) else err {
            match err {
                http.Status(code) {
                    res.Status(code)
                }
                _ {
                    fail err
                }
            }
        }
        var t := Text.New(a)
        var w := t.Writer()
        try res.Send(sys.io, mut w)
        try out.Str(t.Str())
    }
    try out.Flush()
}
`)
	return b.String()
}

type svRoute struct{ method, pattern string }

// svRouteTable is one generated table and the requests asked of it.
type svRouteTable struct {
	routes  []svRoute
	queries []svRoute // method, raw path
}

// svRouteGen makes tables of 1 to svRouteHandlers routes: patterns of 1 to
// 3 segments, each a literal from a small set (so patterns overlap), a
// {name} or, last, a {name...}; methods GET, HEAD, POST, PUT and DELETE.
// Requests are each route's pattern filled in (wildcards by literals of
// the set, other words, percent escapes, an escaped slash, an empty rest)
// and random paths, with random methods.
func svRouteGen(seed uint64, tables int) []svRouteTable {
	rng := seed
	next := func(k int) int {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(k))
	}
	methods := []string{"GET", "HEAD", "POST", "PUT", "DELETE"}
	lits := []string{"a", "b", "ab", "x"}
	vals := []string{"a", "b", "ab", "x", "zz", "%61", "%62", "a%2Fb", "%7E", "q-1"}
	var out []svRouteTable
	for range tables {
		var t svRouteTable
		n := 1 + next(svRouteHandlers)
		for range n {
			segs := 1 + next(3)
			var p strings.Builder
			for s := range segs {
				p.WriteString("/")
				r := next(20)
				switch {
				case s == segs-1 && r < 3:
					fmt.Fprintf(&p, "{w%d...}", s)
				case r < 8:
					fmt.Fprintf(&p, "{w%d}", s)
				default:
					p.WriteString(lits[next(len(lits))])
				}
			}
			t.routes = append(t.routes, svRoute{methods[next(len(methods))], p.String()})
		}
		for _, r := range t.routes {
			for range 3 {
				var q strings.Builder
				for _, seg := range strings.Split(r.pattern, "/")[1:] {
					switch {
					case strings.HasSuffix(seg, "...}"):
						for range next(3) {
							q.WriteString("/" + vals[next(len(vals))])
						}
						if q.Len() == 0 || next(4) == 0 {
							q.WriteString("/")
						}
					case strings.HasPrefix(seg, "{"):
						q.WriteString("/" + vals[next(len(vals))])
					default:
						if next(8) == 0 {
							q.WriteString("/" + vals[next(len(vals))])
						} else {
							q.WriteString("/" + seg)
						}
					}
				}
				t.queries = append(t.queries, svRoute{methods[next(len(methods))], q.String()})
			}
		}
		for range 3 {
			var q strings.Builder
			for range 1 + next(3) {
				q.WriteString("/" + vals[next(len(vals))])
			}
			if next(5) == 0 {
				q.WriteString("/")
			}
			t.queries = append(t.queries, svRoute{methods[next(len(methods))], q.String()})
		}
		out = append(out, t)
	}
	return out
}

// svWildcards is a pattern's wildcard names in order.
func svWildcards(pattern string) []string {
	var ns []string
	for _, seg := range strings.Split(pattern, "/") {
		if strings.HasPrefix(seg, "{") {
			ns = append(ns, strings.TrimSuffix(strings.TrimSuffix(seg[1:], "}"), "..."))
		}
	}
	return ns
}

// svGoMux registers t's routes in a Go 1.22 ServeMux, each handler setting
// X-R as the Mk one does; false when Go refuses the table (two patterns
// that conflict, which a const Mk table cannot refuse).
func svGoMux(t svRouteTable) (mux *http.ServeMux, ok bool) {
	mux = http.NewServeMux()
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	for k, r := range t.routes {
		names := svWildcards(r.pattern)
		mux.HandleFunc(r.method+" "+r.pattern, func(w http.ResponseWriter, req *http.Request) {
			v := strconv.Itoa(k)
			for _, n := range names {
				v += ";" + n + "=" + req.PathValue(n)
			}
			w.Header().Set("X-R", v)
		})
	}
	return mux, true
}

// TestSTDVECTOR_DispatchMatchesServeMux: on every random table
// Go's ServeMux registers, Dispatch picks the route Go picks (slot and
// params), or answers 404 or 405 with Go's Allow field, for each request,
// dev and release. A request Go answers with a redirect (its trailing-slash
// and clean-path rules, which Dispatch does not adopt) is left out and
// counted, and where Go's 405 Allow names the methods of path + "/" for
// that redirect, Mk's must be Go's without them (svRouteAgree).
func TestSTDVECTOR_DispatchMatchesServeMux(t *testing.T) {
	t.Parallel()
	all := svRouteGen(0xA5A5F00DCAFE1234, 6000)
	var kept []svRouteTable
	var muxes []*http.ServeMux
	for _, tb := range all {
		if m, ok := svGoMux(tb); ok {
			kept = append(kept, tb)
			muxes = append(muxes, m)
		}
	}
	var in strings.Builder
	type want struct {
		method, path string
		code         int
		allow, xr    string
		table        int
		// slash is Go's Allow for path + "/" when Go answered 405 to a
		// path without a trailing slash: Go's matchingMethods adds the
		// methods of the patterns that match path + "/", for its
		// trailing-slash redirect, which Dispatch does not adopt.
		slash string
	}
	var ws []want
	codes := map[int]int{}
	redirects, slashed := 0, 0
	for i, tb := range kept {
		in.WriteString("C\n")
		for _, r := range tb.routes {
			fmt.Fprintf(&in, "R %s %s\n", r.method, r.pattern)
		}
		for _, q := range tb.queries {
			req := httptest.NewRequest(q.method, "http://h"+q.pattern, nil)
			rec := httptest.NewRecorder()
			muxes[i].ServeHTTP(rec, req)
			if rec.Code == 301 || rec.Code == 307 || rec.Code == 308 {
				redirects++
				continue
			}
			codes[rec.Code]++
			slash := ""
			if rec.Code == 405 && !strings.HasSuffix(q.pattern, "/") {
				rec2 := httptest.NewRecorder()
				muxes[i].ServeHTTP(rec2, httptest.NewRequest("OPTIONS", "http://h"+q.pattern+"/", nil))
				if rec2.Code == 405 {
					slash = rec2.Header().Get("Allow")
					slashed++
				}
			}
			fmt.Fprintf(&in, "Q %s %s\n", q.method, q.pattern)
			ws = append(ws, want{table: i, method: q.method, path: q.pattern, code: rec.Code, allow: rec.Header().Get("Allow"), xr: rec.Header().Get("X-R"), slash: slash})
		}
	}
	t.Logf("%d of %d tables registered by Go; %d requests compared (status %v), %d redirects left out, %d 405s whose Allow Go widened by path + \"/\"", len(kept), len(all), len(ws), codes, redirects, slashed)
	if len(kept) < 1000 || codes[200] < 3000 || codes[404] < 500 || codes[405] < 300 {
		t.Fatalf("too few cases reached: %d tables, status %v", len(kept), codes)
	}
	for _, flags := range c2Flags {
		t.Run(c2FlagName(flags), func(t *testing.T) {
			t.Parallel()
			r := runNative(t, buildNative(t, nil, svRouteMain(), flags...), in.String())
			if r.code != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", r.code, r.stderr)
			}
			br := bufio.NewReader(strings.NewReader(r.stdout))
			bad := 0
			for i, w := range ws {
				res, err := http.ReadResponse(br, &http.Request{Method: w.method})
				if err != nil {
					t.Fatalf("reply %d (%s %s): %v", i, w.method, w.path, err)
				}
				res.Body.Close()
				if !svRouteAgree(w.code, w.allow, w.xr, w.slash, res.StatusCode, res.Header.Get("Allow"), res.Header.Get("X-R")) {
					if bad < 15 {
						t.Errorf("table %v: %s %s: Mk %d allow %q route %q, Go %d allow %q route %q", kept[w.table].routes, w.method, w.path, res.StatusCode, res.Header.Get("Allow"), res.Header.Get("X-R"), w.code, w.allow, w.xr)
					}
					bad++
				}
			}
			if bad > 0 {
				t.Errorf("%d of %d requests differ", bad, len(ws))
			}
		})
	}
}

// svRouteAgree: Mk's reply (code, allow, xr) is Go's. Where Go's 405 Allow
// was widened by the methods of path + "/" (slash), Mk's methods must be a
// subset of Go's and, with slash's, all of them; Mk's answer is 405, or 404
// when every one of Go's came from path + "/".
func svRouteAgree(gcode int, gallow, gxr, slash string, code int, allow, xr string) bool {
	if slash == "" {
		return code == gcode && allow == gallow && xr == gxr
	}
	if xr != "" || gxr != "" {
		return false
	}
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, x := range strings.Split(s, ", ") {
			if x != "" {
				m[x] = true
			}
		}
		return m
	}
	g, m, sl := set(gallow), set(allow), set(slash)
	if code != 405 && !(code == 404 && len(m) == 0) {
		return false
	}
	if code == 405 && len(m) == 0 {
		return false
	}
	for x := range m {
		if !g[x] {
			return false
		}
	}
	for x := range g {
		if !m[x] && !sl[x] {
			return false
		}
	}
	return true
}
