// Why: std.tls's QUIC handshake API (QuicClient, QuicServer) must interoperate
// with an independent TLS 1.3 QUIC stack (Go's crypto/tls QUICConn) in both
// roles and enforce RFC 9001 certificate and ALPN failures.
//
// What: TestStdTls_QuicGoServerHandshakes, TestStdTls_QuicGoClientHandshakes,
// TestStdTls_QuicClientRejectsUntrustedServerCertificate, and
// TestStdTls_QuicAlpnFailureIs0x0178.
//
// How: builds a Mk QUIC driver once via tlsBuildDriver and bridges it to
// crypto/tls.QUICConn over a line-framed stdin/stdout pipe that delivers
// CRYPTO bytes per encryption level, records installed secrets, peer
// transport parameters, and alerts, and checks secret equality on both sides.

package std

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const tlsQuicDriver = `import std.crypto
import std.encoding
import std.fs
import std.net
import std.tls
import std.x509

Nib(c: u8): u8 {
    if c >= b'a' {
        return c - 87
    }
    return c - 48
}

UnhexInto(s: str, out: mut []u8): int {
    b := s.AsBytes()
    n := b.Len() / 2
    for i in 0..n {
        out[i] = (Nib(b[2 * i]) << 4) | Nib(b[2 * i + 1])
    }
    return n
}

const Digits: str = "0123456789abcdef"

Hex(a: &Arena, b: []u8): OutOfMemory!str from a {
    out := try a.Slice[u8](2 * b.Len())
    d := Digits.AsBytes()
    for i in 0..b.Len() {
        out[2 * i] = d[int(b[i] >> 4)]
        out[2 * i + 1] = d[int(b[i] & 15)]
    }
    return str.From(out[..]).Must()
}

LevelCode(l: tls.QuicLevel): int {
    return match l {
        .Initial { 0 }
        .Handshake { 1 }
        .Application { 2 }
    }
}

SuiteCode(s: tls.QuicSuite): int {
    return match s {
        .Aes128Gcm { 0x1301 }
        .Aes256Gcm { 0x1302 }
        .Chacha20Poly1305 { 0x1303 }
    }
}

struct PipeTransport {
    a: &Arena
    r: Reader
    w: Writer
    in_buf: mut []u8
    in_at: [3]int
    in_len: [3]int
}

PipeTransport.New(a: &Arena, r: Reader, w: Writer): OutOfMemory!PipeTransport from a, r, w {
    in_buf := try a.Slice[u8](3 * 32768)
    return PipeTransport(
        a: a,
        r: r,
        w: w,
        in_buf: in_buf,
        in_at: [0, 0, 0],
        in_len: [0, 0, 0],
    )
}

ReadLine(r: mut Reader, dst: mut []u8): ReadError!?str from dst {
    var n := 0
    for n < dst.Len() {
        k := try r.Until(0x0A, mut dst[n..])
        if k == 0 {
            break
        }
        n += k
        if dst[n - 1] == 0x0A {
            break
        }
    }
    if n == 0 {
        return none
    }
    if dst[n - 1] == 0x0A {
        n -= 1
    }
    if n > 0 && dst[n - 1] == 0x0D {
        n -= 1
    }
    s := str.From(dst[0..n]) else {
        fail ReadError
    }
    return some(s)
}

impl tls.QuicTransport for PipeTransport {
    Recv(mut self, io: Io, level: tls.QuicLevel, buf: mut []u8): tls.Error!int {
        idx := LevelCode(level)
        var lbuf: [65536]u8 = [0; 65536]
        for {
            avail := self.in_len[idx] - self.in_at[idx]
            if avail > 0 {
                k := if buf.Len() < avail { buf.Len() } else { avail }
                off := idx * 32768 + self.in_at[idx]
                (mut buf[0..k]).CopyFrom(self.in_buf[off..off + k])
                self.in_at[idx] += k
                if self.in_at[idx] == self.in_len[idx] {
                    self.in_at[idx] = 0
                    self.in_len[idx] = 0
                }
                return k
            }
            line_opt := ReadLine(mut self.r, mut lbuf[..]) else {
                fail net.Reset
            }
            line := line_opt else {
                fail net.Reset
            }
            if line == "E" {
                fail net.Reset
            }
            if line.StartsWith("X ") {
                code := Trunc[u8](line[2..].ParseInt() else { 0 })
                fail tls.Alert(code: code)
            }
            if line.StartsWith("D ") {
                var p1 := ""
                var p2 := ""
                var pi := 0
                for part in line.Split(" ") {
                    if pi == 1 {
                        p1 = part
                    } else if pi == 2 {
                        p2 = part
                    }
                    pi += 1
                }
                l := Cast[int](p1.ParseInt() else { 0 })
                n := p2.Len() / 2
                if l >= 0 && l < 3 && self.in_len[l] + n <= 32768 {
                    dst := l * 32768 + self.in_len[l]
                    _ = UnhexInto(p2, mut self.in_buf[dst..dst + n])
                    self.in_len[l] += n
                }
            }
        }
    }

    Send(mut self, io: Io, level: tls.QuicLevel, data: []u8): tls.Error!void {
        h := try Hex(self.a, data)
        line := try self.a.Fmt("S {l} {h}\n", (l: LevelCode(level), h))
        self.w.Str(line) else {
            fail net.Reset
        }
        self.w.Flush() else {
            fail net.Reset
        }
    }

    Secret(mut self, level: tls.QuicLevel, write: bool, suite: tls.QuicSuite, secret: []u8): tls.Error!void {
        h := try Hex(self.a, secret)
        wr := if write { 1 } else { 0 }
        line := try self.a.Fmt("K {l} {wr} {s} {h}\n", (l: LevelCode(level), wr, s: SuiteCode(suite), h))
        self.w.Str(line) else {
            fail net.Reset
        }
        self.w.Flush() else {
            fail net.Reset
        }
    }

    PeerParams(mut self, params: []u8): tls.Error!void {
        h := try Hex(self.a, params)
        line := try self.a.Fmt("P {h}\n", (h))
        self.w.Str(line) else {
            fail net.Reset
        }
        self.w.Flush() else {
            fail net.Reset
        }
    }

    Alert(mut self, io: Io, code: u8) {
        line := self.a.Fmt("A {c}\n", (c: int(code))) else {
            return
        }
        self.w.Str(line) else {}
        self.w.Flush() else {}
    }
}

Names(a: &Arena, s: str into a): OutOfMemory![]str from a, s {
    var out := List[str].New(a)
    if s != "-" {
        for p in s.Split(",") {
            try out.Push(p)
        }
    }
    return out.Done()[..]
}

Run(sys: &System, a: &Arena into a): tls.Error|fs.Error|encoding.Error|crypto.Error|WriteError|OutOfMemory!str from a {
    io := sys.io
    role := sys.args[1]
    host := sys.args[2]
    roots_path := sys.args[3]
    cert_path := sys.args[4]
    key_path := sys.args[5]
    alpn := try Names(a, try a.Dup(sys.args[6]))
    var params := b""
    if sys.args[7] != "-" {
        pn := sys.args[7].Len() / 2
        pbuf := try a.Slice[u8](pn)
        _ = UnhexInto(sys.args[7], mut pbuf[..])
        params = pbuf[..]
    }

    var cfg := tls.DefaultConfig
    nochain: [0]x509.Certificate = []
    var roots_pool := x509.Pool.New(nochain[..])
    if roots_path != "-" {
        rtext := try fs.ReadText(a, io, roots_path)
        rcerts := (try x509.ParsePem(a, rtext)).Done()
        roots_pool = x509.Pool.New(rcerts[..])
        cfg.roots = some(&roots_pool)
    }
    var pt := try PipeTransport.New(a, sys.Stdin(), sys.Stdout())
    if role == "client" {
        done := try tls.QuicClient(sys.heap, io, mut pt, &cfg, host, alpn, params)
        return try a.Fmt("OK {p}", (p: done.Alpn()))
    }
    ctext := try fs.ReadText(a, io, cert_path)
    certs := (try x509.ParsePem(a, ctext)).Done()
    ktext := try fs.ReadText(a, io, key_path)
    blocks := try encoding.ParsePem(a, ktext)
    key_val := try crypto.ParsePrivateKey(blocks[0].der)
    chain_buf: [1]tls.Identity = [tls.Identity(chain: certs[..], key: &key_val)]
    cfg.identities = chain_buf[..]
    done := try tls.QuicServer(sys.heap, io, mut pt, &cfg, alpn, params)
    return try a.Fmt("OK {p}", (p: done.Alpn()))
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var arena := try Arena.New(sys.heap)
    defer arena.Free()
    res := Run(sys, &arena) else e {
        out := sys.Stdout()
        var w := out
        w.Str(try arena.Fmt("ERR {e}\n", (e))) else {}
        w.Flush() else {}
        return
    }
    var w := sys.Stdout()
    try w.Str(try arena.Fmt("{res}\n", (res)))
    try w.Flush()
}
`

var tlsQuicBuilt struct {
	sync.Mutex
	exe string
}

func tlsQuicExe(t *testing.T) string {
	t.Helper()
	tlsQuicBuilt.Lock()
	defer tlsQuicBuilt.Unlock()
	if tlsQuicBuilt.exe != "" {
		return tlsQuicBuilt.exe
	}
	exe := tlsBuildDriver(t, "quic", "dev", tlsQuicDriver)
	tlsQuicBuilt.exe = exe
	return exe
}

type quicSecret struct {
	suite uint16
	data  []byte
}

type quicRunResult struct {
	mkAlpn       string
	mkErr        string
	mkAlert      int
	mkPeerParams []byte
	mkRead       map[int]quicSecret
	mkWrite      map[int]quicSecret

	goDone       bool
	goErr        error
	goAlpn       string
	goPeerParams []byte
	goRead       map[tls.QUICEncryptionLevel]quicSecret
	goWrite      map[tls.QUICEncryptionLevel]quicSecret
}

func goLevelFromCode(c int) tls.QUICEncryptionLevel {
	switch c {
	case 0:
		return tls.QUICEncryptionLevelInitial
	case 1:
		return tls.QUICEncryptionLevelHandshake
	case 2:
		return tls.QUICEncryptionLevelApplication
	default:
		return tls.QUICEncryptionLevelInitial
	}
}

func codeFromGoLevel(l tls.QUICEncryptionLevel) int {
	switch l {
	case tls.QUICEncryptionLevelInitial:
		return 0
	case tls.QUICEncryptionLevelHandshake:
		return 1
	case tls.QUICEncryptionLevelApplication:
		return 2
	default:
		return -1
	}
}

func runQuicPair(t *testing.T, mkRole, host, rootsPEM string, certPEM, keyPEM []byte, mkAlpn []string, mkParams []byte, goCfg *tls.Config, goParams []byte) quicRunResult {
	t.Helper()
	exe := tlsQuicExe(t)
	dir := t.TempDir()
	rootsPath := "-"
	if rootsPEM != "" {
		rootsPath = filepath.Join(dir, "roots.pem")
		if err := os.WriteFile(rootsPath, []byte(rootsPEM), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	certPath, keyPath := "-", "-"
	if len(certPEM) > 0 && len(keyPEM) > 0 {
		certPath = filepath.Join(dir, "cert.pem")
		keyPath = filepath.Join(dir, "key.pem")
		if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	alpnArg := "-"
	if len(mkAlpn) > 0 {
		alpnArg = strings.Join(mkAlpn, ",")
	}
	paramsArg := "-"
	if len(mkParams) > 0 {
		paramsArg = hex.EncodeToString(mkParams)
	}

	cmd := exec.Command(exe, mkRole, host, rootsPath, certPath, keyPath, alpnArg, paramsArg)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	qcfg := &tls.QUICConfig{TLSConfig: goCfg}
	var q *tls.QUICConn
	if mkRole == "client" {
		q = tls.QUICServer(qcfg)
	} else {
		q = tls.QUICClient(qcfg)
	}
	defer q.Close()
	q.SetTransportParameters(goParams)

	res := quicRunResult{
		mkAlert: -1,
		mkRead:  map[int]quicSecret{},
		mkWrite: map[int]quicSecret{},
		goRead:  map[tls.QUICEncryptionLevel]quicSecret{},
		goWrite: map[tls.QUICEncryptionLevel]quicSecret{},
	}

	drainGo := func() {
		for {
			ev := q.NextEvent()
			switch ev.Kind {
			case tls.QUICNoEvent:
				return
			case tls.QUICSetReadSecret:
				res.goRead[ev.Level] = quicSecret{suite: ev.Suite, data: append([]byte(nil), ev.Data...)}
			case tls.QUICSetWriteSecret:
				res.goWrite[ev.Level] = quicSecret{suite: ev.Suite, data: append([]byte(nil), ev.Data...)}
			case tls.QUICWriteData:
				lvl := codeFromGoLevel(ev.Level)
				fmt.Fprintf(stdin, "D %d %s\n", lvl, hex.EncodeToString(ev.Data))
			case tls.QUICTransportParameters:
				res.goPeerParams = append([]byte(nil), ev.Data...)
			case tls.QUICHandshakeDone:
				res.goDone = true
				res.goAlpn = q.ConnectionState().NegotiatedProtocol
			case tls.QUICErrorEvent:
				res.goErr = ev.Err
				var ae tls.AlertError
				if errors.As(ev.Err, &ae) {
					fmt.Fprintf(stdin, "X %d\n", uint8(ae))
				} else {
					fmt.Fprintln(stdin, "E")
				}
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("q.Start: %v", err)
	}
	drainGo()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "S "):
			parts := strings.SplitN(line, " ", 3)
			lvl, _ := strconv.Atoi(parts[1])
			data, _ := hex.DecodeString(parts[2])
			if err := q.HandleData(goLevelFromCode(lvl), data); err != nil {
				res.goErr = err
				var ae tls.AlertError
				if errors.As(err, &ae) {
					fmt.Fprintf(stdin, "X %d\n", uint8(ae))
				} else {
					fmt.Fprintln(stdin, "E")
				}
			}
			drainGo()
		case strings.HasPrefix(line, "K "):
			parts := strings.SplitN(line, " ", 5)
			lvl, _ := strconv.Atoi(parts[1])
			wr, _ := strconv.Atoi(parts[2])
			suite, _ := strconv.Atoi(parts[3])
			data, _ := hex.DecodeString(parts[4])
			sec := quicSecret{suite: uint16(suite), data: data}
			if wr == 1 {
				res.mkWrite[lvl] = sec
			} else {
				res.mkRead[lvl] = sec
			}
		case strings.HasPrefix(line, "P "):
			res.mkPeerParams, _ = hex.DecodeString(line[2:])
		case strings.HasPrefix(line, "A "):
			res.mkAlert, _ = strconv.Atoi(line[2:])
		case strings.HasPrefix(line, "OK "):
			res.mkAlpn = line[3:]
			stdin.Close()
		case line == "OK":
			stdin.Close()
		case strings.HasPrefix(line, "ERR "):
			res.mkErr = line[4:]
			stdin.Close()
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("scanner: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("mk quic driver failed: %v\nstderr: %s", err, stderr.String())
	}
	return res
}

func TestStdTls_QuicGoServerHandshakes(t *testing.T) {
	pki := tlsPeerPKI(t)
	cParams := []byte{0x01, 0x04, 0x80, 0x00, 0x75, 0x30}
	sParams := []byte{0x04, 0x02, 0x40, 0x64}

	cases := []struct {
		name   string
		leaf   string
		curves []tls.CurveID
	}{
		{"p256-x25519", "p256", []tls.CurveID{tls.X25519}},
		{"p384-x25519", "p384", []tls.CurveID{tls.X25519}},
		{"ed25519-x25519", "ed25519", []tls.CurveID{tls.X25519}},
		{"rsa-x25519", "rsa", []tls.CurveID{tls.X25519}},
		{"p256-hrr", "p256", []tls.CurveID{tls.CurveP256}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goCfg := &tls.Config{
				Certificates:     []tls.Certificate{pki.leaves[tc.leaf]},
				NextProtos:       []string{"h3"},
				CurvePreferences: tc.curves,
				MinVersion:       tls.VersionTLS13,
			}
			r := runQuicPair(t, "client", "localhost", pki.rootPEM, nil, nil, []string{"http/1.1", "h3"}, cParams, goCfg, sParams)
			if r.mkErr != "" || r.goErr != nil {
				t.Fatalf("mkErr=%q goErr=%v alert=%d", r.mkErr, r.goErr, r.mkAlert)
			}
			if !r.goDone || r.mkAlpn != "h3" || r.goAlpn != "h3" {
				t.Fatalf("done=%v mkAlpn=%q goAlpn=%q", r.goDone, r.mkAlpn, r.goAlpn)
			}
			if !bytes.Equal(r.mkPeerParams, sParams) || !bytes.Equal(r.goPeerParams, cParams) {
				t.Fatalf("params mismatch mk=%x go=%x", r.mkPeerParams, r.goPeerParams)
			}
			for _, lvl := range []int{1, 2} {
				gl := goLevelFromCode(lvl)
				if !bytes.Equal(r.mkRead[lvl].data, r.goWrite[gl].data) || r.mkRead[lvl].suite != r.goWrite[gl].suite {
					t.Fatalf("level %d client-read != server-write: mk=%+v go=%+v", lvl, r.mkRead[lvl], r.goWrite[gl])
				}
				if !bytes.Equal(r.mkWrite[lvl].data, r.goRead[gl].data) || r.mkWrite[lvl].suite != r.goRead[gl].suite {
					t.Fatalf("level %d client-write != server-read: mk=%+v go=%+v", lvl, r.mkWrite[lvl], r.goRead[gl])
				}
			}
		})
	}
}

func quicPoolFromPEM(t *testing.T, pemStr string) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pemStr)) {
		t.Fatal("AppendCertsFromPEM failed")
	}
	return pool
}

func quicCertKeyPEM(t *testing.T, cert tls.Certificate) ([]byte, []byte) {
	t.Helper()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	der, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return certPEM, keyPEM
}

func TestStdTls_QuicGoClientHandshakes(t *testing.T) {
	pki := tlsPeerPKI(t)
	pool := quicPoolFromPEM(t, pki.rootPEM)
	cParams := []byte{0x01, 0x04, 0x80, 0x00, 0x75, 0x30}
	sParams := []byte{0x04, 0x02, 0x40, 0x64}

	cases := []struct {
		name   string
		leaf   string
		curves []tls.CurveID
	}{
		{"p256-x25519", "p256", []tls.CurveID{tls.X25519}},
		{"ed25519-x25519", "ed25519", []tls.CurveID{tls.X25519}},
		{"p256-hrr", "p256", []tls.CurveID{tls.CurveP256}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			certPEM, keyPEM := quicCertKeyPEM(t, pki.leaves[tc.leaf])
			goCfg := &tls.Config{
				RootCAs:          pool,
				ServerName:       "localhost",
				NextProtos:       []string{"http/1.1", "h3"},
				CurvePreferences: tc.curves,
				MinVersion:       tls.VersionTLS13,
			}
			r := runQuicPair(t, "server", "", "", certPEM, keyPEM, []string{"h3"}, sParams, goCfg, cParams)
			if r.mkErr != "" || r.goErr != nil {
				t.Fatalf("mkErr=%q goErr=%v alert=%d", r.mkErr, r.goErr, r.mkAlert)
			}
			if !r.goDone || r.mkAlpn != "h3" || r.goAlpn != "h3" {
				t.Fatalf("done=%v mkAlpn=%q goAlpn=%q", r.goDone, r.mkAlpn, r.goAlpn)
			}
			if !bytes.Equal(r.mkPeerParams, cParams) || !bytes.Equal(r.goPeerParams, sParams) {
				t.Fatalf("params mismatch mk=%x go=%x", r.mkPeerParams, r.goPeerParams)
			}
			for _, lvl := range []int{1, 2} {
				gl := goLevelFromCode(lvl)
				if !bytes.Equal(r.mkRead[lvl].data, r.goWrite[gl].data) || r.mkRead[lvl].suite != r.goWrite[gl].suite {
					t.Fatalf("level %d server-read != client-write: mk=%+v go=%+v", lvl, r.mkRead[lvl], r.goWrite[gl])
				}
				if !bytes.Equal(r.mkWrite[lvl].data, r.goRead[gl].data) || r.mkWrite[lvl].suite != r.goRead[gl].suite {
					t.Fatalf("level %d server-write != client-read: mk=%+v go=%+v", lvl, r.mkWrite[lvl], r.goRead[gl])
				}
			}
		})
	}
}

func TestStdTls_QuicClientRejectsUntrustedServerCertificate(t *testing.T) {
	pki := tlsPeerPKI(t)
	expiredLeaf, err := tlsMakeLeaf(pki.root, pki.rootKey, "p256", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		host      string
		rootsPEM  string
		leaf      tls.Certificate
		wantErr   string
		wantAlert int
	}{
		{"unknown-ca", "localhost", pki.otherPEM, pki.leaves["p256"], "UnknownAuthority", 48},
		{"name-mismatch", "wrong.example", pki.rootPEM, pki.leaves["p256"], "NameMismatch", 42},
		{"expired", "localhost", pki.rootPEM, expiredLeaf, "Expired", 45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goCfg := &tls.Config{
				Certificates: []tls.Certificate{tc.leaf},
				NextProtos:   []string{"h3"},
				MinVersion:   tls.VersionTLS13,
			}
			r := runQuicPair(t, "client", tc.host, tc.rootsPEM, nil, nil, []string{"h3"}, []byte{1}, goCfg, []byte{2})
			if !strings.Contains(r.mkErr, tc.wantErr) {
				t.Fatalf("got mkErr=%q, want %q", r.mkErr, tc.wantErr)
			}
			if r.mkAlert != tc.wantAlert {
				t.Fatalf("got mkAlert=%d, want %d", r.mkAlert, tc.wantAlert)
			}
		})
	}
}

func TestStdTls_QuicAlpnFailureIs0x0178(t *testing.T) {
	pki := tlsPeerPKI(t)
	t.Run("client", func(t *testing.T) {
		goCfg := &tls.Config{
			Certificates: []tls.Certificate{pki.leaves["p256"]},
			NextProtos:   []string{"http/1.1"},
			MinVersion:   tls.VersionTLS13,
		}
		r := runQuicPair(t, "client", "localhost", pki.rootPEM, nil, nil, []string{"h3"}, []byte{1}, goCfg, []byte{2})
		if !strings.Contains(r.mkErr, "Alert(code: 120)") {
			t.Fatalf("expected Alert(code: 120), got mkErr=%q goErr=%v", r.mkErr, r.goErr)
		}
	})
	t.Run("server", func(t *testing.T) {
		pool := quicPoolFromPEM(t, pki.rootPEM)
		certPEM, keyPEM := quicCertKeyPEM(t, pki.leaves["p256"])
		goCfg := &tls.Config{
			RootCAs:    pool,
			ServerName: "localhost",
			NextProtos: []string{"http/1.1"},
			MinVersion: tls.VersionTLS13,
		}
		r := runQuicPair(t, "server", "", "", certPEM, keyPEM, []string{"h3"}, []byte{2}, goCfg, []byte{1})
		if r.mkAlert != 120 {
			t.Fatalf("expected mkAlert=120 (0x0178), got %d (mkErr=%q)", r.mkAlert, r.mkErr)
		}
		if !strings.Contains(r.mkErr, "Unsupported") {
			t.Fatalf("expected Unsupported, got mkErr=%q", r.mkErr)
		}
	})
}
