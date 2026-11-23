// Why: Serve has its own reader (HeadScan, the large-head path, the in-buffer
// chunked loop, the body copy, the 400 and 413 decisions), separate from ReadRequest.
// What: TestHttp_ServeDiff, dev and release.
// How: each ParseDiff stream is written to a running Mk server in random 1 to 64
// byte pieces; the echoed requests must match the Go model, and refusals get 400.
//
// Every request the model accepts must come back exactly, in order; where the
// model refuses, the server must answer 400 (413 or 431 for "large") and
// close, or, when the refusal is the stream ending inside a request, close
// without a reply. Differences on purpose: a reply that closes the
// connection (HTTP/1.0, Connection: close) ends the comparison, and a
// refusal that arrives as a reset is accepted, since the server closes
// with the client's later bytes unread (RFC 9112 9.6 lets it).

package std

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const htEchoMain = `import std.http

struct App {
    n: int
}

Hex(w: mut Writer, b: []u8): WriteError!void {
    digits := "0123456789abcdef".AsBytes()
    for i in 0..b.Len() {
        hi := int(b[i] >> 4)
        lo := int(b[i] & 15)
        try w.Bytes(digits[hi..hi + 1])
        try w.Bytes(digits[lo..lo + 1])
    }
}

// Handle writes the request as ParseDiff's driver prints it, hex-encoded
// in X-Echo, so a reply to HEAD carries it too.
Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    var t := Text.New(a)
    var w := t.Writer()
    try Print(mut w, "req {m} {p} q={q}\n", (m: req.method, p: req.path, q: req.query))
    for i in 0..req.headers.Len() {
        h := req.headers[i]
        try Print(mut w, "h {n}:{v}\n", (n: h.name, v: h.value))
    }
    try w.Str("b ")
    try Hex(mut w, req.body)
    try w.Str("\n")
    try w.Flush()
    var x := Text.New(a)
    var xw := x.Writer()
    try Hex(mut xw, t.Str().AsBytes())
    try xw.Flush()
    try res.Header("X-Echo", x.Str())
}

export Main(sys: &System): http.Error!void {
    app := App(n: 0)
    try http.Serve(sys, sys.args[1], 1048576, &app, Handle)
}
`

// htModelVerdicts is the model's reading of one stream: the requests it
// accepts, as driver lines, whether each closes its connection, and how
// it ends: "eof", "cut" (the stream ends inside a request), "bad" or
// "large".
func htModelVerdicts(data []byte, limit int) (reqs [][]string, methods []string, closes []bool, end string) {
	pos := 0
	for {
		start := pos
		req, bad := htModelOne(data, &pos, limit)
		if bad != nil {
			switch {
			case bad.out == "eof":
				return reqs, methods, closes, "eof"
			case bad.out == "large":
				return reqs, methods, closes, "large"
			case bad.out == fmt.Sprintf("bad %d", len(data)-start):
				return reqs, methods, closes, "cut"
			}
			return reqs, methods, closes, "bad"
		}
		// RFC 9112 9.3: HTTP/1.0 closes unless it asks to keep alive.
		line, _, _ := bytes.Cut(bytes.TrimLeft(data[start:pos], "\r\n"), []byte("\r\n"))
		close, keep := false, false
		for _, h := range req.headers {
			if strings.EqualFold(h[0], "connection") {
				for _, tok := range strings.FieldsFunc(h[1], func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
					close = close || strings.EqualFold(tok, "close")
					keep = keep || strings.EqualFold(tok, "keep-alive")
				}
			}
		}
		closes = append(closes, close || bytes.HasSuffix(line, []byte(" HTTP/1.0")) && !keep)
		lines := []string{fmt.Sprintf("req %s %s q=%s", req.method, req.path, req.query)}
		for _, h := range req.headers {
			lines = append(lines, "h "+h[0]+":"+h[1])
		}
		lines = append(lines, "b "+fmt.Sprintf("%x", req.body))
		reqs = append(reqs, lines)
		methods = append(methods, req.method)
	}
}

// htServeStream writes data to addr in pieces from r and reads every
// reply until the end of the stream: each reply as "status close echo",
// then "reset" when the stream ended in a reset, else "end".
func htServeStream(t *testing.T, addr string, data []byte, r *htRng, methods []string) []string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	go func() {
		for rest := data; len(rest) > 0; {
			k := min(len(rest), 1+r.below(64))
			if _, err := c.Write(rest[:k]); err != nil {
				return
			}
			rest = rest[k:]
		}
		c.(*net.TCPConn).CloseWrite()
	}()
	br := bufio.NewReader(c)
	var out []string
	for i := 0; ; {
		if _, err := br.Peek(1); err != nil {
			if errors.Is(err, io.EOF) {
				return append(out, "end")
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatalf("no end of stream after %v", out)
			}
			return append(out, "reset")
		}
		method := "GET"
		if i < len(methods) {
			method = methods[i]
		}
		res, err := http.ReadResponse(br, &http.Request{Method: method})
		if err != nil {
			return append(out, "unreadable: "+err.Error())
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode == 100 {
			continue
		}
		echo, _ := hex.DecodeString(res.Header.Get("X-Echo"))
		out = append(out, fmt.Sprintf("%d %v %s", res.StatusCode, res.Close, echo))
		i++
	}
}

func TestHttp_ServeDiff(t *testing.T) {
	const limit = 1 << 20
	var recs []htRecord
	for _, rec := range htInputs() {
		if rec.limit == limit {
			recs = append(recs, rec)
		}
	}
	// Every table case, and an even spread of the seeded streams.
	if len(recs) > 1500 {
		keep := recs[:len(htTable)]
		for i := len(htTable); i < len(recs); i += 4 {
			keep = append(keep, recs[i])
		}
		recs = keep
	}
	root := t.TempDir()
	for _, prof := range []string{"dev", "release"} {
		t.Run(prof, func(t *testing.T) {
			p := htStartServerProcArgs(t, root, htEchoMain, prof)
			r := &htRng{s: 0x73727664}
			fails, checked, resets := 0, 0, 0
			for _, rec := range recs {
				reqs, methods, closes, end := htModelVerdicts(rec.data, limit)
				got := htServeStream(t, p.addr, rec.data, r, methods)
				bad := func(why string) {
					fails++
					if fails <= 10 {
						t.Errorf("%q: %s\nserver: %q\nmodel: %d requests then %s", rec.data, why, got, len(reqs), end)
					}
				}
				checked++
				if len(got) > 0 && got[len(got)-1] == "reset" {
					resets++
				}
				if why := htJudge(reqs, closes, end, got); why != "" {
					bad(why)
				}
			}
			t.Logf("%d streams, %d differ, %d ended in a reset (accepted only where the server is due to close)", checked, fails, resets)
		})
	}
}

// htJudge compares the server's replies with the model's verdicts: "" when
// they agree, else what differs. closes[k] is true when request k ends its
// connection (HTTP/1.0 without keep-alive, or Connection: close). A server
// that closes with the client's later bytes unread resets the connection,
// and on Windows the reset drops replies the client had not read yet; so
// a "reset" may cut the replies short wherever the server is due to close
// at or after that point: a closing request, a refusal.
func htJudge(reqs [][]string, closes []bool, end string, got []string) string {
	mayReset := func(k int) bool {
		if end == "bad" || end == "large" {
			return true
		}
		for j := k; j < len(closes); j++ {
			if closes[j] {
				return true
			}
		}
		return false
	}
	k := 0
	for ; k < len(reqs) && k < len(got); k++ {
		body := strings.Join(reqs[k], "\n") + "\n"
		switch got[k] {
		case "200 false " + body:
			if closes[k] {
				return fmt.Sprintf("reply %d keeps a connection its request closes", k)
			}
		case "200 true " + body:
			if !closes[k] {
				return fmt.Sprintf("reply %d closes a connection its request keeps", k)
			}
			if k+1 >= len(got) || got[k+1] != "end" && got[k+1] != "reset" {
				return "bytes after a reply that closed"
			}
			return ""
		case "reset":
			if mayReset(k) {
				return ""
			}
			return fmt.Sprintf("reset at reply %d", k)
		default:
			return fmt.Sprintf("reply %d differs", k)
		}
	}
	if k < len(reqs) {
		return fmt.Sprintf("only %d of %d requests answered", k, len(reqs))
	}
	tail := got[k:]
	switch end {
	case "eof", "cut":
		if len(tail) != 1 || tail[0] != "end" {
			return "want the end of the stream with no reply"
		}
	case "bad":
		if !(len(tail) == 1 && tail[0] == "reset") && !(len(tail) == 2 && strings.HasPrefix(tail[0], "400 true ")) {
			return "want 400 and the end"
		}
	case "large":
		if !(len(tail) == 1 && tail[0] == "reset") && !(len(tail) == 2 && (strings.HasPrefix(tail[0], "413 true ") || strings.HasPrefix(tail[0], "431 true "))) {
			return "want 413 or 431 and the end"
		}
	}
	return ""
}
