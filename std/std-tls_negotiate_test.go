// Why: std.http learns the ALPN result only through net.Secure.Negotiate, so
// the index it returns must agree with what a mature TLS stack negotiated,
// in both roles.
// What: TestStdTls_GoPeerNegotiate (tls.Connector's Negotiate against Go's
// crypto/tls server with NextProtos set four ways) and
// TestStdTls_GoClientNegotiate (Go's client against tls.Acceptor's
// Negotiate).
// How: the Mk side is a driver program per role; Go's side runs in the test
// process on loopback with the chains of tlsPeerPKI, and each case checks
// the Mk line (the chosen index and the bytes echoed) and Go's
// NegotiatedProtocol or its handshake error.

package std

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tlsNegoImports are the client driver's imports; the server's add
// crypto and encoding for its key.
const tlsNegoImports = `import std.fs
import std.net
import std.tls
import std.x509
`

// tlsNegoCommon is the drivers' shared part: the offer from a
// comma-separated list ("-" for none) and Main.
const tlsNegoCommon = `
Names(a: &Arena, s: str into a): OutOfMemory![]str from a, s {
    var out := List[str].New(a)
    if s != "-" {
        for p in s.Split(",") {
            try out.Push(p)
        }
    }
    return out.Done()[..]
}

export Main(sys: &System): WriteError|OutOfMemory!void {
    var arena := try Arena.New(sys.heap)
    defer arena.Free()
    line := Run(sys, &arena) else e {
        try sys.Print("err {e}\n", (e))
        return
    }
    try sys.Print("{line}\n", (line))
}
`

// tlsNegoClient dials a server and negotiates: arguments are the address,
// the root's PEM file, the offer and the lowest version ("12" turns on
// the TLS 1.2 client, "13" does not). It prints "ok chosen=<i> n=<bytes
// echoed>" or "err <the error>".
const tlsNegoClient = tlsNegoImports + tlsNegoCommon + `
Run(sys: &System, a: &Arena into a): x509.Error|tls.Error|fs.Error!str from a {
    io := sys.io
    certs := try x509.ParsePem(a, try fs.ReadText(a, io, sys.args[2]))
    all := certs.Done()
    pool := x509.Pool.New(all[..])
    var cfg := tls.DefaultConfig
    cfg.roots = some(&pool)
    if sys.args[4] == "12" {
        cfg.min_version = tls.Version.Tls12
    }
    offer := try Names(a, try a.Dup(sys.args[3]))
    sec := tls.Connector(&cfg)
    stream := try net.Dial(io, sys.args[1])
    conn := stream.Conn()
    (conn: c, chosen) := sec.Negotiate(sys.heap, io, conn, "localhost", offer) else e {
        conn.Abandon()
        return try a.Fmt("err {e}", (e))
    }
    onerr c.Abandon()
    try c.WriteAll(io, "hello".AsBytes())
    var back: [5]u8 = [0; 5]
    var got := 0
    for got < 5 {
        k := try c.Read(io, mut back[got..])
        if k == 0 {
            break
        }
        got += k
    }
    try c.Close(io)
    return try a.Fmt("ok chosen={chosen} n={got}", (chosen, got))
}
`

// tlsNegoServer serves one connection on 127.0.0.1:0 through
// tls.Acceptor's Negotiate: arguments are the chain's PEM file, the key's
// (PKCS #8) and the protocols accepted. It prints "port <n>", then
// "served chosen=<i> n=<bytes echoed>" or "err <the error>".
const tlsNegoServer = `import std.crypto
import std.encoding
` + tlsNegoImports + tlsNegoCommon + `
Run(sys: &System, a: &Arena into a): x509.Error|tls.Error|fs.Error|encoding.Error|crypto.Error|WriteError!str from a {
    io := sys.io
    certs := try x509.ParsePem(a, try fs.ReadText(a, io, sys.args[1]))
    blocks := try encoding.ParsePem(a, try fs.ReadText(a, io, sys.args[2]))
    key := try crypto.ParsePrivateKey(blocks[0].der)
    all := certs.Done()
    ids: [1]tls.Identity = [tls.Identity(chain: all[..], key: &key)]
    var cfg := tls.DefaultConfig
    cfg.identities = ids[..]
    accept := try Names(a, try a.Dup(sys.args[3]))
    sec := tls.Acceptor(&cfg)
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    var out := sys.Stdout()
    try Print(mut out, "port {p}\n", (p: l.Port()))
    try out.Flush()
    s := try l.Accept(io)
    conn := s.Conn()
    (conn: c, chosen) := sec.Negotiate(sys.heap, io, conn, "", accept) else e {
        conn.Abandon()
        return try a.Fmt("err {e}", (e))
    }
    onerr c.Abandon()
    var buf: [4096]u8 = [0; 4096]
    var total := 0
    for {
        k := try c.Read(io, mut buf[..])
        if k == 0 {
            break
        }
        try c.WriteAll(io, buf[0..k])
        total += k
    }
    try c.Close(io)
    return try a.Fmt("served chosen={chosen} n={total}", (chosen, total))
}
`

var tlsNegoBuilt struct {
	sync.Mutex
	exe map[string]string
}

// tlsNegoExe builds the driver of role ("client" or "server") for mode
// once per test binary.
func tlsNegoExe(t *testing.T, role, mode string) string {
	t.Helper()
	tlsNegoBuilt.Lock()
	defer tlsNegoBuilt.Unlock()
	if tlsNegoBuilt.exe == nil {
		tlsNegoBuilt.exe = map[string]string{}
	}
	if exe, ok := tlsNegoBuilt.exe[role+"/"+mode]; ok {
		return exe
	}
	src := tlsNegoClient
	if role == "server" {
		src = tlsNegoServer
	}
	exe := tlsBuildDriver(t, "nego"+role, mode, src)
	tlsNegoBuilt.exe[role+"/"+mode] = exe
	return exe
}

// TestStdTls_GoPeerNegotiate runs Negotiate offering h2 then http/1.1
// against Go's server with each NextProtos, over TLS 1.3, through a
// HelloRetryRequest, and over TLS 1.2 (client12.mk's path): the chosen
// index must name the protocol Go negotiated at the version it
// negotiated, and none in common is Go's no_application_protocol alert.
func TestStdTls_GoPeerNegotiate(t *testing.T) {
	pki := tlsPeerPKI(t)
	cases := []struct {
		name   string
		offer  string
		protos []string
		want   string // the driver's line
		alpn   string // Go's NegotiatedProtocol
		goErr  string // a substring of Go's handshake error
		tls12  bool   // Go's server at TLS 1.2 only, the client allowing it
		curves []tls.CurveID
	}{
		{name: "server takes h2", offer: "h2,http/1.1", protos: []string{"h2"}, want: "ok chosen=0 n=5", alpn: "h2"},
		{name: "server takes http/1.1", offer: "h2,http/1.1", protos: []string{"http/1.1"}, want: "ok chosen=1 n=5", alpn: "http/1.1"},
		{name: "server prefers http/1.1", offer: "h2,http/1.1", protos: []string{"http/1.1", "h2"}, want: "ok chosen=1 n=5", alpn: "http/1.1"},
		{name: "server without ALPN", offer: "h2,http/1.1", want: "ok chosen=-1 n=5"},
		{name: "no protocol in common", offer: "h2,http/1.1", protos: []string{"x-other"}, want: "err SecureFailed(why: Handshake)", goErr: "unsupported application protocols"},
		{name: "empty offer", offer: "-", protos: []string{"h2"}, want: "ok chosen=-1 n=5"},
		{name: "after a HelloRetryRequest", offer: "h2,http/1.1", protos: []string{"http/1.1"}, curves: []tls.CurveID{tls.CurveP256}, want: "ok chosen=1 n=5", alpn: "http/1.1"},
		{name: "tls 1.2 server takes h2", offer: "h2,http/1.1", protos: []string{"h2"}, tls12: true, want: "ok chosen=0 n=5", alpn: "h2"},
		{name: "tls 1.2 server prefers http/1.1", offer: "h2,http/1.1", protos: []string{"http/1.1", "h2"}, tls12: true, want: "ok chosen=1 n=5", alpn: "http/1.1"},
		{name: "tls 1.2 server without ALPN", offer: "h2,http/1.1", tls12: true, want: "ok chosen=-1 n=5"},
		{name: "tls 1.2 no protocol in common", offer: "h2,http/1.1", protos: []string{"x-other"}, tls12: true, want: "err SecureFailed(why: Handshake)", goErr: "unsupported application protocols"},
	}
	for _, mode := range tlsModes {
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				cfg := &tls.Config{Certificates: []tls.Certificate{pki.leaves["p256"]}, MinVersion: tls.VersionTLS13, NextProtos: c.protos, CurvePreferences: c.curves}
				version, min := uint16(tls.VersionTLS13), "13"
				if c.tls12 {
					cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
					version, min = tls.VersionTLS12, "12"
				}
				addr, done := tlsGoServer(t, cfg)
				roots := filepath.Join(t.TempDir(), "root.pem")
				if err := os.WriteFile(roots, []byte(pki.rootPEM), 0o644); err != nil {
					t.Fatal(err)
				}
				r := runNative(t, tlsNegoExe(t, "client", mode), "", addr, roots, c.offer, min)
				if got := strings.TrimSpace(r.stdout); got != c.want {
					t.Errorf("got %q (%s), want %q", got, r.stderr, c.want)
				}
				select {
				case s := <-done:
					if c.goErr != "" {
						if s.err == nil || !strings.Contains(s.err.Error(), c.goErr) {
							t.Errorf("Go's server: %v, want an error with %q", s.err, c.goErr)
						}
						return
					}
					if s.err != nil {
						t.Fatalf("Go's server: %v", s.err)
					}
					if s.state.NegotiatedProtocol != c.alpn {
						t.Errorf("Go negotiated %q, want %q", s.state.NegotiatedProtocol, c.alpn)
					}
					if s.state.Version != version {
						t.Errorf("Go negotiated version %#x, want %#x", s.state.Version, version)
					}
					if c.curves != nil && !s.state.HelloRetryRequest {
						t.Errorf("Go sent no HelloRetryRequest")
					}
				case <-time.After(30 * time.Second):
					t.Errorf("Go's server did not finish")
				}
			})
		}
	}
}

// TestStdTls_GoClientNegotiate runs Go's client with each NextProtos
// against a server whose Negotiate accepts h2 then http/1.1: the server's
// chosen index must name what Go negotiated, the server's order winning,
// and none in common is the no_application_protocol alert Go reports.
func TestStdTls_GoClientNegotiate(t *testing.T) {
	pki := tlsPeerPKI(t)
	pool := x509.NewCertPool()
	pool.AddCert(pki.root)
	cases := []struct {
		name   string
		accept string
		client []string
		want   string // the server's line
		alpn   string
		goErr  string
	}{
		{name: "client prefers http/1.1, server h2", accept: "h2,http/1.1", client: []string{"http/1.1", "h2"}, want: "served chosen=0 n=5", alpn: "h2"},
		{name: "client offers http/1.1", accept: "h2,http/1.1", client: []string{"http/1.1"}, want: "served chosen=1 n=5", alpn: "http/1.1"},
		{name: "client without ALPN", accept: "h2,http/1.1", want: "served chosen=-1 n=5"},
		{name: "no protocol in common", accept: "h2,http/1.1", client: []string{"x-other"}, want: "err SecureFailed(why: Handshake)", goErr: "no application protocol"},
	}
	for _, mode := range tlsModes {
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				addr, wait := tlsServeExe(t, tlsNegoExe(t, "server", mode), pki.leaves["p256"], c.accept)
				cfg := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13, NextProtos: c.client}
				conn, err := tls.Dial("tcp", addr, cfg)
				if c.goErr != "" {
					if err == nil {
						_, err = conn.Read(make([]byte, 1))
						conn.Close()
					}
					if err == nil || !strings.Contains(err.Error(), c.goErr) {
						t.Errorf("Go's client: %v, want an error with %q", err, c.goErr)
					}
					if got := wait(); got != c.want {
						t.Errorf("server: %q, want %q", got, c.want)
					}
					return
				}
				if err != nil {
					t.Fatalf("Go's client: %v (server: %s)", err, wait())
				}
				if p := conn.ConnectionState().NegotiatedProtocol; p != c.alpn {
					t.Errorf("Go negotiated %q, want %q", p, c.alpn)
				}
				data := []byte("hello")
				if _, err := conn.Write(data); err != nil {
					t.Fatalf("write: %v", err)
				}
				back := make([]byte, len(data))
				if _, err := io.ReadFull(conn, back); err != nil || string(back) != string(data) {
					t.Fatalf("echo: %q %v", back, err)
				}
				conn.CloseWrite()
				conn.Close()
				if got := wait(); got != c.want {
					t.Errorf("server: %q, want %q", got, c.want)
				}
			})
		}
	}
}
