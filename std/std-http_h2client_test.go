// Why: Mk's HTTP/2 client (http.Client with h2 or h2c on) must interoperate
// with an HTTP/2 implementation it did not write, through the public client
// path: Do, Fetch, Exchange bodies, the pool and Client.Drive.
//
// What: TestStdHttp_H2ClientAgainstGo, whose subtests each pin one behaviour
// against Go's net/http server: ALPN offer and fallback, SNI, :authority and
// :path, TLS 1.2, bodies both ways, 100 streams on one connection, cancel
// mid-body then reuse, an early answer to a large upload, GOAWAY, a stream
// limit of 1, h2c by prior knowledge, and Drive answering the server's PINGs.
//
// How: one Mk client program (h2ClientProg) is built once and run per case;
// it prints one line of key=value facts. Each subtest starts its own
// net/http server on 127.0.0.1:0 with the protocols and limits it needs, and
// that server counts connections, requests and concurrent streams and records
// what each TLS ClientHello offered.

package std

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const h2ClientProg = `import std.fs
import std.http
import std.time
import std.tls
import std.url
import std.x509

const NoHeaders: []http.Header = []

const NoCerts: []x509.Certificate = []

VerName(v: http.HttpVersion): int {
    match v {
        http.HttpVersion.Http10 { return 10 }
        http.HttpVersion.Http11 { return 11 }
        http.HttpVersion.Http2 { return 2 }
        _ { return 3 }
    }
}

Lower(b: u8): u8 {
    return if b >= b'A' && b <= b'Z' { b + 32 } else { b }
}

HeaderOf(hs: []http.Header, name: str): str from hs {
    nb := name.AsBytes()
    for h in hs {
        hb := h.name.AsBytes()
        if hb.Len() != nb.Len() {
            continue
        }
        var same := true
        for i in 0..hb.Len() {
            if Lower(hb[i]) != Lower(nb[i]) {
                same = false
                break
            }
        }
        if same {
            return h.value
        }
    }
    return ""
}

Pattern(i: int): u8 {
    return Trunc[u8]((i * 7 + (i >> 8) + 13) & 0xff)
}

PatternBody(a: &Arena, n: int): OutOfMemory![]u8 from a {
    b := try a.Slice[u8](n)
    for i in 0..n {
        b[i] = Pattern(i)
    }
    return b[..]
}

// Fetch1 makes one request and reads its body: the status (-1 when Do
// failed, -2 a body read, -3 Close), the X-Conn-Id value (0 when absent),
// the body's length and the version.
Fetch1(c: &http.Client, h: &Heap, io: Io, method: str, us: str, body: []u8): (status: int, conn: int, n: int, ver: int) {
    var ar := Arena.New(h) else {
        return (status: -1, conn: 0, n: 0, ver: 0)
    }
    defer ar.Free()
    u := url.Parse(&ar, us, none) else {
        return (status: -1, conn: 0, n: 0, ver: 0)
    }
    req := http.ClientRequest(method: method, url: &u, headers: NoHeaders, body: body)
    var ex := c.Do(&ar, io, &req) else {
        return (status: -1, conn: 0, n: 0, ver: 0)
    }
    status := ex.status
    ver := VerName(ex.Version())
    cs := HeaderOf(ex.headers, "x-conn-id")
    conn := if cs.Len() > 0 { int(cs.ParseInt() else { 0 }) } else { 0 }
    var rb: [0]u8 = []
    var r := ex.Body(mut rb[..])
    var piece: [16384]u8 = [0; 16384]
    var n := 0
    for {
        k := r.Read(mut piece[..]) else {
            ex.Abandon()
            return (status: -2, conn: conn, n: n, ver: ver)
        }
        if k == 0 {
            break
        }
        n += k
    }
    ex.Close(io) else {
        ex.Abandon()
        return (status: -3, conn: conn, n: n, ver: ver)
    }
    return (status: status, conn: conn, n: n, ver: ver)
}

struct Tally {
    ok: int
    conn_min: int
    conn_max: int
}

Worker(c: &http.Client, h: &Heap, io: Io, us: str, t: &Mutex[Tally]) {
    r := Fetch1(c, h, io, "GET", us, "".AsBytes())
    if r.status == 200 {
        var g := t.Lock(io.NoCancel()) else {
            return
        }
        m := mut *g.Mut()
        m.ok += 1
        m.conn_min = if r.conn < m.conn_min { r.conn } else { m.conn_min }
        m.conn_max = if r.conn > m.conn_max { r.conn } else { m.conn_max }
        g.Unlock()
    }
}

Parallel(c: &http.Client, h: &Heap, io: Io, us: str, n: int, out: mut Writer): WriteError!void {
    var t := Mutex[Tally].New(Tally(ok: 0, conn_min: 1 << 30, conn_max: 0))
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(60))
        for _ in 0..n {
            s.go Worker(c, h, io, us, &t)
        }
    }
    r := t.Into()
    try Print(out, "parallel ok={o} conn_min={a} conn_max={b}\n", (o: r.ok, a: r.conn_min, b: r.conn_max))
}

// Download reads a body of n bytes in 16 KiB pieces and checks the pattern.
Download(c: &http.Client, a: &Arena, io: Io, us: str into a, n: int, out: mut Writer): http.Error|url.Error|WriteError!void {
    u := try url.Parse(a, us, none)
    req := http.ClientRequest(method: "GET", url: &u, headers: NoHeaders, body: "".AsBytes())
    var ex := try c.Do(a, io, &req)
    onerr ex.Abandon()
    status := ex.status
    var rb: [0]u8 = []
    var r := ex.Body(mut rb[..])
    var piece: [16384]u8 = [0; 16384]
    var got := 0
    var bad := 0
    for {
        k := try r.Read(mut piece[..])
        if k == 0 {
            break
        }
        for i in 0..k {
            if piece[i] != Pattern(got + i) {
                bad += 1
            }
        }
        got += k
    }
    try ex.Close(io)
    try Print(out, "download status={s} bytes={g} bad={b} want={n}\n", (s: status, g: got, b: bad, n))
}

// CancelCycles opens a 1 MiB body, reads one piece and closes it, cycles
// times, then makes a whole request.
CancelCycles(c: &http.Client, h: &Heap, io: Io, us: str, ok_us: str, cycles: int, out: mut Writer): WriteError!void {
    var cmin := 1 << 30
    var cmax := 0
    var opened := 0
    for _ in 0..cycles {
        var ar := Arena.New(h) else { break }
        defer ar.Free()
        u := url.Parse(&ar, us, none) else { break }
        req := http.ClientRequest(method: "GET", url: &u, headers: NoHeaders, body: "".AsBytes())
        var ex := c.Do(&ar, io, &req) else e {
            try Print(out, "cancel do error {e}\n", (e))
            break
        }
        cs := HeaderOf(ex.headers, "x-conn-id")
        cn := if cs.Len() > 0 { int(cs.ParseInt() else { 0 }) } else { 0 }
        cmin = if cn < cmin { cn } else { cmin }
        cmax = if cn > cmax { cn } else { cmax }
        var rb: [0]u8 = []
        var r := ex.Body(mut rb[..])
        var piece: [16384]u8 = [0; 16384]
        _ = r.Read(mut piece[..]) else { 0 }
        ex.Close(io) else {
            ex.Abandon()
        }
        opened += 1
    }
    last := Fetch1(c, h, io, "GET", ok_us, "".AsBytes())
    try Print(out, "cancel cycles={o} conn_min={a} conn_max={b} last={l} last_conn={lc}\n", (o: opened, a: cmin, b: cmax, l: last.status, lc: last.conn))
}

// DriveTask runs the client's Drive as a task of a scope.
DriveTask(c: &http.Client, io: Io) {
    c.Drive(io) else {}
}

// Driven runs Drive beside two requests a pause apart, then stops it.
Driven(c: &http.Client, h: &Heap, io: Io, us: str, pause: int, out: mut Writer): WriteError!void {
    var first := (status: 0, conn: 0, n: 0, ver: 0)
    var second := (status: 0, conn: 0, n: 0, ver: 0)
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(60))
        s.go DriveTask(c, io)
        first = Fetch1(c, h, io, "GET", us, "".AsBytes())
        io.Sleep(time.Millis(i64(pause))) else {}
        second = Fetch1(c, h, io, "GET", us, "".AsBytes())
        c.Stop()
    }
    try Print(out, "drive first={a} c1={b} second={d} c2={e} stopped=1\n", (a: first.status, b: first.conn, d: second.status, e: second.conn))
}

export Main(sys: &System): http.Error|url.Error|x509.Error|fs.Error|WriteError|ParseError!void {
    var out := sys.Stdout()
    a := sys.arena
    io := sys.io
    args := sys.args
    what := args[1]
    base := args[2]
    ca := args[3]
    mode := args[4]
    var cfg := tls.DefaultConfig
    var pool := x509.Pool.New(NoCerts)
    if ca != "-" {
        text := try fs.ReadText(a, io, ca)
        certs := try x509.ParsePem(a, text)
        all := certs.Done()
        pool = x509.Pool.New(all[..])
        cfg.roots = some(&pool)
    }
    if mode == "h2tls12" {
        cfg.min_version = tls.Version.Tls12
    }
    var l := http.DefaultClientLimits
    l.body_bytes = 128 * 1024 * 1024
    l.secure = some(tls.Connector(&cfg))
    if mode == "h2" || mode == "h2tls12" {
        l.h2 = true
    }
    if mode == "h2c" {
        l.h2c = true
    }
    if what == "streams1" {
        l.h2_streams = 1
    }
    c := http.Client.New(sys.heap, l)
    defer c.Close()
    h := sys.heap
    if what == "get" || what == "streams1" {
        r := Fetch1(&c, h, io, "GET", try a.Fmt("{base}/get", (base)), "".AsBytes())
        try Print(mut out, "get status={s} ver={v} n={n} conn={c}\n", (s: r.status, v: r.ver, n: r.n, c: r.conn))
    } else if what == "root" {
        r := Fetch1(&c, h, io, "GET", base, "".AsBytes())
        try Print(mut out, "root status={s} ver={v}\n", (s: r.status, v: r.ver))
    } else if what == "seq" {
        n := int(try args[5].ParseInt())
        for i in 0..n {
            r := Fetch1(&c, h, io, "GET", try a.Fmt("{base}/conn", (base)), "".AsBytes())
            try Print(mut out, "seq {i} status={s} ver={v} conn={c}\n", (i, s: r.status, v: r.ver, c: r.conn))
        }
    } else if what == "echo" {
        body := try PatternBody(a, 1024 * 1024)
        u := try url.Parse(a, try a.Fmt("{base}/echo", (base)), none)
        req := http.ClientRequest(method: "POST", url: &u, headers: NoHeaders, body: body)
        r := try c.Fetch(a, io, &req)
        var same := r.body.Len() == body.Len()
        if same {
            for i in 0..body.Len() {
                if r.body[i] != body[i] {
                    same = false
                    break
                }
            }
        }
        try Print(mut out, "echo status={s} n={n} same={m}\n", (s: r.status, n: r.body.Len(), m: same))
    } else if what == "upload" {
        body := try PatternBody(a, 1024 * 1024)
        u := try url.Parse(a, try a.Fmt("{base}/sink", (base)), none)
        req := http.ClientRequest(method: "POST", url: &u, headers: NoHeaders, body: body)
        r := try c.Fetch(a, io, &req)
        try Print(mut out, "upload status={s} body=", (s: r.status))
        try out.Bytes(r.body)
        try out.Str("\n")
    } else if what == "early" {
        body := try PatternBody(a, 8 * 1024 * 1024)
        u := try url.Parse(a, try a.Fmt("{base}/early", (base)), none)
        req := http.ClientRequest(method: "POST", url: &u, headers: NoHeaders, body: body)
        r := c.Fetch(a, io, &req) else e {
            try Print(mut out, "early error {e}\n", (e))
            try out.Flush()
            return
        }
        again := Fetch1(&c, h, io, "GET", try a.Fmt("{base}/get", (base)), "".AsBytes())
        try Print(mut out, "early status={s} n={n} again={g} body=", (s: r.status, n: r.body.Len(), g: again.status))
        for b in r.body {
            if b >= 0x21 && b < 0x7f {
                try out.Bytes([b][..])
            }
        }
        try out.Str("\n")
    } else if what == "download" {
        n := int(try args[5].ParseInt())
        try Download(&c, a, io, try a.Fmt("{base}/bytes?n={n}", (base, n)), n, mut out)
    } else if what == "parallel" {
        n := int(try args[5].ParseInt())
        try Parallel(&c, h, io, try a.Fmt("{base}/conn", (base)), n, mut out)
    } else if what == "cancel" {
        try CancelCycles(&c, h, io, try a.Fmt("{base}/bytes?n=1048576", (base)), try a.Fmt("{base}/get", (base)), 50, mut out)
    } else if what == "drive" {
        pause := int(try args[5].ParseInt())
        try Driven(&c, h, io, try a.Fmt("{base}/get", (base)), pause, mut out)
    } else if what == "nodrive" {
        pause := int(try args[5].ParseInt())
        first := Fetch1(&c, h, io, "GET", try a.Fmt("{base}/get", (base)), "".AsBytes())
        io.Sleep(time.Millis(i64(pause))) else {}
        second := Fetch1(&c, h, io, "GET", try a.Fmt("{base}/get", (base)), "".AsBytes())
        try Print(mut out, "nodrive first={a} c1={b} second={d} c2={e}\n", (a: first.status, b: first.conn, d: second.status, e: second.conn))
    }
    try out.Flush()
}
`

// h2ConnInfo is what the Go server knows of one accepted connection.
type h2ConnInfo struct {
	id   int64
	reqs atomic.Int64
}

type h2ConnKey struct{}

// h2GoOpts selects the Go server's protocols and limits.
type h2GoOpts struct {
	tls         bool          // TLS with ALPN (else cleartext)
	h1Only      bool          // offer and serve only http/1.1
	tls12RSA    bool          // TLS 1.2 only, RSA leaf, ECDHE_RSA_WITH_AES_128_GCM_SHA256 on P-256
	maxStreams  int           // SETTINGS_MAX_CONCURRENT_STREAMS (0: Go's default)
	goawayAfter int64         // after this many requests on a connection, close it (GOAWAY)
	pingAfter   time.Duration // the server PINGs a connection silent this long, and closes it unanswered
}

// h2GoServer is a running Go server and what it observed.
type h2GoServer struct {
	base        string
	goawayAfter int64
	conns       atomic.Int64
	reqs        atomic.Int64
	inflight    atomic.Int64
	peak        atomic.Int64
	mu          sync.Mutex
	alpn        [][]string // the ALPN list of each ClientHello
	sni         []string   // the server_name of each ClientHello
	hosts       []string   // r.Host of each request
	paths       []string   // r.URL.Path of each request
	protos      []string   // r.Proto of each request
}

func (s *h2GoServer) note(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts = append(s.hosts, r.Host)
	s.paths = append(s.paths, r.URL.Path)
	s.protos = append(s.protos, r.Proto)
}

// h2Pattern is byte i of the kit pattern the Mk program checks.
func h2Pattern(i int) byte { return byte((i*7 + i>>8 + 13) & 0xff) }

func (s *h2GoServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.note(r)
		s.reqs.Add(1)
		info, _ := r.Context().Value(h2ConnKey{}).(*h2ConnInfo)
		var id, n int64
		if info != nil {
			id = info.id
			n = info.reqs.Add(1)
		}
		w.Header().Set("X-Conn-Id", strconv.FormatInt(id, 10))
		if s.goawayAfter > 0 && n >= s.goawayAfter {
			w.Header().Set("Connection", "close")
		}
		switch r.URL.Path {
		case "/get":
			io.WriteString(w, "hello from the peer kit\n")
		case "/echo":
			io.Copy(w, r.Body)
		case "/sink":
			h := sha256.New()
			nb, _ := io.Copy(h, r.Body)
			fmt.Fprintf(w, `{"bytes":%d,"sha256":"%s"}`, nb, hex.EncodeToString(h.Sum(nil)))
		case "/early":
			w.Header().Set("Content-Length", "6")
			io.WriteString(w, "early\n")
		case "/bytes":
			size, _ := strconv.Atoi(r.URL.Query().Get("n"))
			w.Header().Set("Content-Length", strconv.Itoa(size))
			buf := make([]byte, 32*1024)
			for off := 0; off < size; {
				k := min(len(buf), size-off)
				for i := 0; i < k; i++ {
					buf[i] = h2Pattern(off + i)
				}
				if _, err := w.Write(buf[:k]); err != nil {
					return
				}
				off += k
			}
		case "/conn":
			cur := s.inflight.Add(1)
			for {
				p := s.peak.Load()
				if cur <= p || s.peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			s.inflight.Add(-1)
			fmt.Fprintf(w, `{"conn":%d}`, id)
		default:
			io.WriteString(w, "root\n")
		}
	})
}

// h2Leaf is a leaf for localhost and 127.0.0.1 signed by the test root.
func h2Leaf(t *testing.T, pki *tlsPKI, useRSA bool) tls.Certificate {
	t.Helper()
	var key crypto.Signer
	var err error
	if useRSA {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	} else {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, pki.root, key.Public(), pki.rootKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startH2Go starts a Go server with o on 127.0.0.1:0; it stops at the
// test's end.
func startH2Go(t *testing.T, pki *tlsPKI, o h2GoOpts) *h2GoServer {
	t.Helper()
	s := &h2GoServer{goawayAfter: o.goawayAfter}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var protos http.Protocols
	switch {
	case o.h1Only:
		protos.SetHTTP1(true)
	case o.tls:
		protos.SetHTTP2(true)
	default:
		protos.SetUnencryptedHTTP2(true)
	}
	srv := &http.Server{
		Handler:   s.handler(),
		Protocols: &protos,
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams: o.maxStreams,
			SendPingTimeout:      o.pingAfter,
			PingTimeout:          o.pingAfter,
		},
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, h2ConnKey{}, &h2ConnInfo{id: s.conns.Add(1)})
		},
		ErrorLog: nil,
	}
	scheme := "http"
	if o.tls {
		scheme = "https"
		cfg := &tls.Config{
			Certificates: []tls.Certificate{h2Leaf(t, pki, o.tls12RSA)},
			GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				s.mu.Lock()
				s.alpn = append(s.alpn, append([]string(nil), h.SupportedProtos...))
				s.sni = append(s.sni, h.ServerName)
				s.mu.Unlock()
				return nil, nil
			},
		}
		if o.tls12RSA {
			cfg.MinVersion = tls.VersionTLS12
			cfg.MaxVersion = tls.VersionTLS12
			cfg.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
			cfg.CurvePreferences = []tls.CurveID{tls.CurveP256}
		}
		srv.TLSConfig = cfg
		go srv.ServeTLS(ln, "", "")
	} else {
		go srv.Serve(ln)
	}
	t.Cleanup(func() { srv.Close() })
	s.base = fmt.Sprintf("%s://%s", scheme, ln.Addr().String())
	return s
}

// h2Facts runs the Mk client: what, the base URL, the roots and the mode
// (h2, h2c, h2tls12, h1), then extra; it returns the key=value facts of the
// line that starts with prefix.
func h2Facts(t *testing.T, exe, ca, what, base, mode, prefix string, extra ...string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	args := append([]string{what, base, ca, mode}, extra...)
	cmd := exec.CommandContext(ctx, exe, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mk client %v: %v\n%s", args, err, out)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, prefix+" ") {
			continue
		}
		facts := map[string]string{"line": line}
		for _, f := range strings.Fields(line)[1:] {
			if k, v, ok := strings.Cut(f, "="); ok {
				facts[k] = v
			}
		}
		return facts
	}
	t.Fatalf("mk client %v: no %q line in:\n%s", args, prefix, out)
	return nil
}

func h2cWant(t *testing.T, facts map[string]string, key, want string) {
	t.Helper()
	if facts[key] != want {
		t.Fatalf("%s = %q, want %q (line %q)", key, facts[key], want, facts["line"])
	}
}

func TestStdHttp_H2ClientAgainstGo(t *testing.T) {
	pki := tlsPeerPKI(t)
	exe := http2Build(t, h2ClientProg)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte(pki.rootPEM), 0o644); err != nil {
		t.Fatal(err)
	}

	// RFC 7301 3.1, RFC 9113 3.2-3.3, 8.3.1, 9.2: the ClientHello offers
	// exactly ["h2", "http/1.1"] (never "h2c"), no server_name for an IP
	// literal, and the request goes over h2 with :authority and :path "/".
	t.Run("AlpnClientOffersH2ThenHttp11", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		f := h2Facts(t, exe, ca, "root", s.base, "h2", "root")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "ver", "2")
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.alpn) != 1 || strings.Join(s.alpn[0], ",") != "h2,http/1.1" {
			t.Fatalf("ALPN offers %q, want one [h2 http/1.1]", s.alpn)
		}
		if s.sni[0] != "" {
			t.Fatalf("server_name %q sent for an IP literal", s.sni[0])
		}
		if s.paths[0] != "/" || s.protos[0] != "HTTP/2.0" || s.hosts[0] != strings.TrimPrefix(s.base, "https://") {
			t.Fatalf("path %q proto %q host %q", s.paths[0], s.protos[0], s.hosts[0])
		}
	})

	// RFC 9113 9.2: a DNS name is sent as server_name.
	t.Run("ClientSendsSni", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		base := strings.Replace(s.base, "127.0.0.1", "localhost", 1)
		f := h2Facts(t, exe, ca, "get", base, "h2", "get")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "ver", "2")
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.sni) == 0 || s.sni[0] != "localhost" {
			t.Fatalf("server_name %q, want localhost", s.sni)
		}
	})

	// RFC 9113 3.3, RFC 7301 3.2: a server that picks http/1.1 gets
	// HTTP/1.1, and the client reuses that connection from the h1 pool.
	t.Run("ClientFallsBackToH1WithoutAlpnH2", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true, h1Only: true})
		f := h2Facts(t, exe, ca, "seq", s.base, "h2", "seq 2", "3")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "ver", "11")
		if n := s.conns.Load(); n != 1 {
			t.Fatalf("%d connections for 3 sequential HTTP/1.1 requests, want 1", n)
		}
	})

	// RFC 9113 9.2, 9.2.2: h2 over TLS 1.2 with ECDHE_RSA_WITH_AES_128_GCM_SHA256 on P-256.
	t.Run("OverTls12GoServer", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true, tls12RSA: true})
		f := h2Facts(t, exe, ca, "get", s.base, "h2tls12", "get")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "ver", "2")
		h2cWant(t, f, "n", "24")
	})

	t.Run("EchoOneMiB", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		f := h2Facts(t, exe, ca, "echo", s.base, "h2", "echo")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "n", "1048576")
		h2cWant(t, f, "same", "true")
	})

	t.Run("UploadOneMiB", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		f := h2Facts(t, exe, ca, "upload", s.base, "h2", "upload")
		h2cWant(t, f, "status", "200")
		body := make([]byte, 1<<20)
		for i := range body {
			body[i] = h2Pattern(i)
		}
		sum := sha256.Sum256(body)
		want := fmt.Sprintf(`body={"bytes":1048576,"sha256":"%s"}`, hex.EncodeToString(sum[:]))
		if !strings.Contains(f["line"], want) {
			t.Fatalf("upload line %q, want %s", f["line"], want)
		}
	})

	// Flow control end to end: 64 MiB through a 1 MiB stream window.
	t.Run("Download64MiB", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		f := h2Facts(t, exe, ca, "download", s.base, "h2", "download", "67108864")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "bytes", "67108864")
		h2cWant(t, f, "bad", "0")
	})

	// 100 concurrent requests share one connection, and the handler sees
	// them in flight together.
	t.Run("HundredStreamsOneConnection", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		f := h2Facts(t, exe, ca, "parallel", s.base, "h2", "parallel", "100")
		h2cWant(t, f, "ok", "100")
		h2cWant(t, f, "conn_min", "1")
		h2cWant(t, f, "conn_max", "1")
		if n := s.conns.Load(); n != 1 {
			t.Fatalf("%d connections, want 1", n)
		}
		if p := s.peak.Load(); p < 2 {
			t.Fatalf("peak concurrent streams %d, want at least 2", p)
		}
	})

	// RFC 9113 5.1, 6.4: closing a body early resets its stream and keeps
	// the connection; 50 cycles, then a whole request, on one connection.
	t.Run("CancelMidBodyThenReuse", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		f := h2Facts(t, exe, ca, "cancel", s.base, "h2", "cancel")
		h2cWant(t, f, "cycles", "50")
		h2cWant(t, f, "last", "200")
		h2cWant(t, f, "conn_min", "1")
		h2cWant(t, f, "conn_max", "1")
		h2cWant(t, f, "last_conn", "1")
	})

	// RFC 9113 8.1: a server that answers before reading an 8 MiB upload
	// and resets the stream still delivers its complete response.
	t.Run("EarlyAnswerToLargeUpload", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true})
		f := h2Facts(t, exe, ca, "early", s.base, "h2", "early")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "n", "6")
		h2cWant(t, f, "body", "early")
		h2cWant(t, f, "again", "200")
	})

	// RFC 9113 6.8: after GOAWAY the client opens a new connection; 7
	// requests at 3 per connection use 3 connections, and all succeed.
	t.Run("ServerGoaway", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true, goawayAfter: 3})
		f := h2Facts(t, exe, ca, "seq", s.base, "h2", "seq 6", "7")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "ver", "2")
		if n := s.reqs.Load(); n != 7 {
			t.Fatalf("server saw %d requests, want 7", n)
		}
		if n := s.conns.Load(); n != 3 {
			t.Fatalf("%d connections, want 3", n)
		}
	})

	// RFC 9113 5.1.2: with MaxConcurrentStreams=1 every one of 10
	// concurrent requests still succeeds.
	t.Run("MaxConcurrentStreamsOne", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true, maxStreams: 1})
		f := h2Facts(t, exe, ca, "parallel", s.base, "h2", "parallel", "10")
		h2cWant(t, f, "ok", "10")
		if p := s.peak.Load(); p > s.conns.Load() {
			t.Fatalf("peak %d concurrent streams over %d connections with a limit of 1", p, s.conns.Load())
		}
	})

	// RFC 9113 3.3: h2c by prior knowledge, multiplexed.
	t.Run("H2cPriorKnowledge", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{})
		f := h2Facts(t, exe, "-", "get", s.base, "h2c", "get")
		h2cWant(t, f, "status", "200")
		h2cWant(t, f, "ver", "2")
		g := h2Facts(t, exe, "-", "parallel", s.base, "h2c", "parallel", "20")
		h2cWant(t, g, "ok", "20")
		if n := s.conns.Load(); n != 2 {
			t.Fatalf("%d connections for two client runs, want 2", n)
		}
	})

	// RFC 9113 5.2.2, 6.7: the Go server PINGs a connection silent for
	// 200 ms and closes it when the PING goes unanswered. With Drive the
	// pooled connection answers and survives a 1.5 s pause; without it the
	// connection dies and the next request is retried on a new one.
	t.Run("DriveAnswersPingWhileIdle", func(t *testing.T) {
		s := startH2Go(t, pki, h2GoOpts{tls: true, pingAfter: 200 * time.Millisecond})
		f := h2Facts(t, exe, ca, "drive", s.base, "h2", "drive", "1500")
		h2cWant(t, f, "first", "200")
		h2cWant(t, f, "second", "200")
		if f["c1"] != f["c2"] {
			t.Fatalf("driven connection replaced: %q", f["line"])
		}
		u := startH2Go(t, pki, h2GoOpts{tls: true, pingAfter: 200 * time.Millisecond})
		g := h2Facts(t, exe, ca, "nodrive", u.base, "h2", "nodrive", "1500")
		h2cWant(t, g, "first", "200")
		h2cWant(t, g, "second", "200")
		if g["c1"] == g["c2"] {
			t.Logf("undriven connection survived the pause (the server's PING was not sent): %q", g["line"])
		}
	})
}
