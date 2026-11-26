// Why: tls.Server must interoperate with a mature TLS 1.3 client.
// What: TestStdTls_GoClientHandshakes: P-256 and Ed25519 server keys, a
// HelloRetryRequest, ALPN, and a TLS 1.2-only client refused with
// protocol_version.
// How: the Mk server echoes one connection on loopback; Go's crypto/tls client
// checks the version, suite, group and ALPN it got.

package std

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tlsServerDriver serves one connection on 127.0.0.1:0: it prints
// "port <n>", then "served alpn=<p> n=<bytes echoed>" or "err <the
// error>". Arguments: the chain's PEM file, the key's PEM file (PKCS#8),
// the protocols ("-" for none).
const tlsServerDriver = `import std.crypto
import std.encoding
import std.fs
import std.net
import std.tls
import std.x509

Names(a: &Arena, s: str into a): OutOfMemory![]str from a, s {
    var out := List[str].New(a)
    if s != "-" {
        for p in s.Split(",") {
            try out.Push(p)
        }
    }
    return out.Done()[..]
}

Echo(io: Io, c: &tls.Conn, buf: mut []u8): tls.Error!int {
    var total := 0
    for {
        k := try c.Read(io, buf)
        if k == 0 {
            return total
        }
        try c.WriteAll(io, buf[0..k])
        total += k
    }
}

Serve(sys: &System, a: &Arena into a): x509.Error|tls.Error|fs.Error|encoding.Error|crypto.Error|WriteError!str from a {
    io := sys.io
    certs := try x509.ParsePem(a, try fs.ReadText(a, io, sys.args[1]))
    blocks := try encoding.ParsePem(a, try fs.ReadText(a, io, sys.args[2]))
    if blocks.Len() == 0 {
        return "err no key"
    }
    key := try crypto.ParsePrivateKey(blocks[0].der)
    all := certs.Done()
    ids: [1]tls.Identity = [tls.Identity(chain: all[..], key: &key)]
    var cfg := tls.DefaultConfig
    cfg.identities = ids[..]
    cfg.alpn = try Names(a, try a.Dup(sys.args[3]))
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    var out := sys.Stdout()
    try Print(mut out, "port {p}\n", (p: l.Port()))
    try out.Flush()
    s := try l.Accept(io)
    conn := s.Conn()
    c := tls.Server(sys.heap, io, conn, &cfg) else e {
        conn.Abandon()
        return try a.Fmt("err {e}", (e))
    }
    onerr c.Abandon()
    p := try a.Dup(c.Alpn() else { "-" })
    buf := try a.Slice[u8](16384)
    total := try Echo(io, &c, buf)
    try c.Close(io)
    return try a.Fmt("served alpn={p} n={total}", (p, total))
}

export Main(sys: &System): WriteError|OutOfMemory!void {
    var arena := try Arena.New(sys.heap)
    defer arena.Free()
    line := Serve(sys, &arena) else e {
        try sys.Print("err {e}\n", (e))
        return
    }
    try sys.Print("{line}\n", (line))
}
`

var tlsServerBuilt struct {
	sync.Mutex
	exe map[string]string
}

// tlsServerExe builds the server driver for mode once per test binary.
func tlsServerExe(t *testing.T, mode string) string {
	t.Helper()
	tlsServerBuilt.Lock()
	defer tlsServerBuilt.Unlock()
	if tlsServerBuilt.exe == nil {
		tlsServerBuilt.exe = map[string]string{}
	}
	if exe, ok := tlsServerBuilt.exe[mode]; ok {
		return exe
	}
	exe := tlsBuildDriver(t, "server", mode, tlsServerDriver)
	tlsServerBuilt.exe[mode] = exe
	return exe
}

// tlsServeOnce starts the Mk server with the leaf's chain and key and the
// protocols, and returns its address and a function that waits for its
// last line.
func tlsServeOnce(t *testing.T, mode string, leaf tls.Certificate, protos string) (string, func() string) {
	t.Helper()
	return tlsServeExe(t, tlsServerExe(t, mode), leaf, protos)
}

// tlsServeExe is tlsServeOnce for any driver exe that takes the chain's
// PEM file, the key's PEM file and the protocols, prints "port <n>" first
// and its result last.
func tlsServeExe(t *testing.T, exe string, leaf tls.Certificate, protos string) (string, func() string) {
	t.Helper()
	dir := t.TempDir()
	chain := filepath.Join(dir, "chain.pem")
	keyf := filepath.Join(dir, "key.pem")
	os.WriteFile(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}), 0o644)
	der, err := x509.MarshalPKCS8PrivateKey(leaf.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(keyf, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	cmd := exec.CommandContext(ctx, exe, chain, keyf, protos)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	first, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "port ") {
		cancel()
		cmd.Wait()
		t.Fatalf("the server driver said %q (%v): %s", first, err, stderr.String())
	}
	addr := "127.0.0.1:" + strings.TrimSpace(strings.TrimPrefix(first, "port "))
	return addr, func() string {
		defer cancel()
		rest, _ := io.ReadAll(r)
		cmd.Wait()
		return strings.TrimSpace(string(rest)) + strings.TrimSpace(stderr.String())
	}
}

// TestStdTls_GoClientHandshakes runs Go's client against the Mk server in
// dev and release.
func TestStdTls_GoClientHandshakes(t *testing.T) {
	pki := tlsPeerPKI(t)
	pool := x509.NewCertPool()
	pool.AddCert(pki.root)
	cases := []struct {
		name    string
		leaf    string
		curves  []tls.CurveID
		protos  string
		client  []string
		max     uint16
		want    string // the server's line
		curve   tls.CurveID
		alpn    string
		goErr   string
		payload int
	}{
		{name: "p256 key x25519", leaf: "p256", want: "served alpn=- n=5", curve: tls.X25519},
		{name: "ed25519 key", leaf: "ed25519", want: "served alpn=- n=5", curve: tls.X25519},
		{name: "p256 share", leaf: "p256", curves: []tls.CurveID{tls.CurveP256}, want: "served alpn=- n=5", curve: tls.CurveP256},
		{name: "HelloRetryRequest for p256", leaf: "p256", curves: []tls.CurveID{tls.CurveP384, tls.CurveP256}, want: "served alpn=- n=5", curve: tls.CurveP256},
		{name: "HelloRetryRequest for x25519", leaf: "ed25519", curves: []tls.CurveID{tls.CurveP384, tls.X25519}, want: "served alpn=- n=5", curve: tls.X25519},
		{name: "alpn server order", leaf: "p256", protos: "h2,http/1.1", client: []string{"http/1.1", "h2"}, want: "served alpn=h2 n=5", alpn: "h2"},
		{name: "alpn no overlap", leaf: "p256", protos: "h2", client: []string{"x"}, want: "err Unsupported", goErr: "no application protocol"},
		{name: "tls 1.2 client", leaf: "p256", max: tls.VersionTLS12, want: "err Unsupported", goErr: "protocol version"},
		{name: "one MiB", leaf: "p256", want: "served alpn=- n=1048576", payload: 1 << 20},
	}
	for _, mode := range tlsModes {
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				protos := c.protos
				if protos == "" {
					protos = "-"
				}
				addr, wait := tlsServeOnce(t, mode, pki.leaves[c.leaf], protos)
				addr, ccs := tlsCountingProxy(t, addr)
				cfg := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12, MaxVersion: c.max,
					CurvePreferences: c.curves, NextProtos: c.client}
				if c.max == 0 {
					cfg.MinVersion = tls.VersionTLS13
				}
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
				st := conn.ConnectionState()
				if st.Version != tls.VersionTLS13 || (c.curve != 0 && st.CurveID != c.curve) || st.NegotiatedProtocol != c.alpn {
					t.Errorf("version %x curve %v alpn %q", st.Version, st.CurveID, st.NegotiatedProtocol)
				}
				n := c.payload
				if n == 0 {
					n = 5
				}
				data := make([]byte, n)
				for i := range data {
					data[i] = byte(i*3 + 1)
				}
				done := make(chan error, 1)
				go func() {
					_, err := conn.Write(data)
					done <- err
				}()
				back := make([]byte, n)
				if _, err := io.ReadFull(conn, back); err != nil {
					t.Fatalf("echo: %v", err)
				}
				if err := <-done; err != nil {
					t.Fatalf("write: %v", err)
				}
				for i := range back {
					if back[i] != data[i] {
						t.Fatalf("byte %d differs", i)
					}
				}
				conn.CloseWrite()
				if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
					t.Errorf("after close_notify both ways Go read %v, want io.EOF", err)
				}
				conn.Close()
				if got := wait(); got != c.want {
					t.Errorf("server: %q, want %q", got, c.want)
				}
				// Go's client sends a session id, so the server sends one
				// ChangeCipherSpec (RFC 8446 D.4), with or without a
				// HelloRetryRequest.
				if n := <-ccs; n != 1 {
					t.Errorf("the server sent %d ChangeCipherSpec records, want 1", n)
				}
			})
		}
	}
}

// tlsCountingProxy forwards a client to target and reports how many
// ChangeCipherSpec records the server sent, read from the record headers
// of its bytes.
func tlsCountingProxy(t *testing.T, target string) (string, <-chan int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan int, 1)
	go func() {
		defer l.Close()
		c, err := l.Accept()
		if err != nil {
			out <- -1
			return
		}
		defer c.Close()
		u, err := net.Dial("tcp", target)
		if err != nil {
			out <- -1
			return
		}
		defer u.Close()
		go func() {
			io.Copy(u, c)
			u.(*net.TCPConn).CloseWrite()
		}()
		count := 0
		head := make([]byte, 5)
		for {
			if _, err := io.ReadFull(u, head); err != nil {
				break
			}
			body := make([]byte, int(head[3])<<8|int(head[4]))
			if _, err := io.ReadFull(u, body); err != nil {
				break
			}
			if head[0] == 20 {
				count++
			}
			c.Write(head)
			c.Write(body)
		}
		c.(*net.TCPConn).CloseWrite()
		out <- count
	}()
	return l.Addr().String(), out
}
