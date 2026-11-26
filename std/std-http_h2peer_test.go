// Why: Mk's HTTP/2 server (ServeOn over TLS ALPN and h2c) must interoperate
// with Go's net/http HTTP/2 peer (bin\h2peer.exe) across GET, POST echo, 64 MiB
// flow-controlled download, 1 MiB upload, 100 parallel streams on one connection,
// 8 MiB early response, Flush streaming, and lower-cased mixed-case headers.
//
// What: Peer kit helpers (httpPeerKitDir, launchMkH2Server, mkH2PeerServerProg)
// and TestStdHttp_H2ServerAgainstGoPeer + TestStdHttp_H2MixedCaseHandlerHeaderReachesGoClient.
//
// How: Locates MK_PEERS (or MK_EVIDENCE/peers, skipping cleanly when absent),
// launches the compiled Mk server on 127.0.0.1:0, and runs h2peer.exe client plus
// a Go net/http HTTP/2 client check.

package std

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const mkH2PeerServerProg = `import std.crypto
import std.encoding
import std.fs
import std.hash
import std.http
import std.net
import std.time
import std.tls
import std.x509

struct PeerApp {
    n: int
}

BytesEq(a: []u8, b: []u8): bool {
    if a.Len() != b.Len() {
        return false
    }
    for i in 0..a.Len() {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}

QueryInt(q: str, key: str, def: int): int {
    qb := q.AsBytes()
    kb := key.AsBytes()
    var i := 0
    for i < qb.Len() {
        var j := i
        for j < qb.Len() && qb[j] != b'&' {
            j += 1
        }
        var eq := -1
        for k in i..j {
            if qb[k] == b'=' {
                eq = k
                break
            }
        }
        if eq > i && BytesEq(qb[i..eq], kb) {
            val_s := str.From(qb[eq + 1..j]) else { return def }
            v := val_s.ParseInt() else { return def }
            return int(v)
        }
        i = j + 1
    }
    return def
}

HexDigest(a: &Arena, d: [32]u8): OutOfMemory!str from a {
    hex := "0123456789abcdef".AsBytes()
    buf := try a.Slice[u8](64)
    for i in 0..32 {
        b := int(d[i])
        buf[i * 2] = hex[(b >> 4) & 0xf]
        buf[i * 2 + 1] = hex[b & 0xf]
    }
    s := str.From(buf[..]) else {
        return ""
    }
    return s
}

PeerHandle(app: &PeerApp, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    _ = app
    try res.Header("X-Conn-Id", "1")
    try res.Header("X-Conn-Count", "1")
    try res.Header("X-Proto", "HTTP/2.0")

    if req.path == "/get" {
        res.Status(200)
        try res.Body("hello from the peer kit\n".AsBytes())
        return
    }
    if req.path == "/echo" {
        res.Status(200)
        try res.Body(req.body)
        return
    }
    if req.path == "/bytes" {
        n := QueryInt(req.query, "n", 1024)
        sz := if n < 0 { 0 } else { n }
        buf := try a.Slice[u8](sz)
        for i in 0..sz {
            buf[i] = Trunc[u8]((i * 7 + (i >> 8) + 13) & 0xff)
        }
        res.Status(200)
        try res.Body(buf[..])
        return
    }
    if req.path == "/sink" {
        sum := hash.Sha256(req.body)
        hhex := try HexDigest(a, sum)
        res.Status(200)
        var w := res.Writer()
        try Print(mut w, "{{\"bytes\":{n},\"sha256\":\"{h}\"}}", (n: req.body.Len(), h: hhex))
        return
    }
    if req.path == "/early" {
        res.Status(200)
        try res.Body("early\n".AsBytes())
        return
    }
    if req.path == "/flush" {
        chunks := QueryInt(req.query, "chunks", 5)
        delay := QueryInt(req.query, "delay", 50)
        res.Status(200)
        for i in 0..chunks {
            var w := res.Writer()
            try Print(mut w, "chunk {i}\n", (i))
            try res.Flush(io, link)
            if delay > 0 && i + 1 < chunks {
                io.Sleep(time.Millis(i64(delay))) else { fail Canceled }
            }
        }
        return
    }
    if req.path == "/conn" {
        res.Status(200)
        try res.Body("{\"conn\":1,\"conns\":1,\"reqs\":1}".AsBytes())
        return
    }
    if req.path == "/mixed" {
        res.Status(200)
        try res.Header("X-Mixed-Header", "MixedVal")
        try res.Body("mixed-ok".AsBytes())
        return
    }
    res.Status(404)
}

export Main(sys: &System): http.Error|tls.Error|x509.Error|encoding.Error|crypto.Error|fs.Error|WriteError!void {
    io := sys.io
    args := sys.args
    mode := args[1]
    var o := http.DefaultServerOptions
    o.connections = 256
    o.h2_streams = 200
    o.header_bytes = 65536
    o.body_bytes = 128 * 1024 * 1024
    o.request_header = time.Seconds(30)
    o.keep_alive = time.Seconds(15)

    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    app := PeerApp(n: 0)

    if mode == "tls" {
        var arena := try Arena.New(sys.heap)
        defer arena.Free()
        certs := try x509.ParsePem(&arena, try fs.ReadText(&arena, io, args[2]))
        blocks := try encoding.ParsePem(&arena, try fs.ReadText(&arena, io, args[3]))
        if blocks.Len() == 0 {
            fail WriteError
        }
        key := try crypto.ParsePrivateKey(blocks[0].der)
        all := certs.Done()
        ids: [1]tls.Identity = [tls.Identity(chain: all[..], key: &key)]
        var cfg := tls.DefaultConfig
        cfg.identities = ids[..]
        alpn: [2]str = ["h2", "http/1.1"]
        cfg.alpn = alpn[..]
        o.h2 = true
        o.secure = some(tls.Acceptor(&cfg))
        var out := sys.Stdout()
        try Print(mut out, "port {p}\n", (p: l.Port()))
        try out.Flush()
        try http.ServeOn(sys.heap, io, &l, &o, &app, PeerHandle)
        return
    }

    o.h2c = true
    var out := sys.Stdout()
    try Print(mut out, "port {p}\n", (p: l.Port()))
    try out.Flush()
    try http.ServeOn(sys.heap, io, &l, &o, &app, PeerHandle)
}
`

func httpPeerKitDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("MK_PEERS")
	if dir == "" {
		if ev := os.Getenv("MK_EVIDENCE"); ev != "" {
			dir = filepath.Join(ev, "peers")
		}
	}
	if dir == "" {
		t.Skip("peer kit absent: set MK_PEERS")
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	bin := filepath.Join(dir, "bin", "h2peer"+ext)
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("peer kit binary missing (%s): %v", bin, err)
	}
	return dir
}

func launchMkH2Server(t *testing.T, exe, mode, cert, key string) (int, func()) {
	t.Helper()
	cmd := exec.Command(exe, mode, cert, key)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	lineCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			lineCh <- sc.Text()
		}
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
		close(exited)
	}()

	var port int
	select {
	case line := <-lineCh:
		if !strings.HasPrefix(line, "port ") {
			cmd.Process.Kill()
			<-exited
			t.Fatalf("unexpected first line from server: %q (stderr: %s)", line, stderr.String())
		}
		p, err := strconv.Atoi(strings.TrimPrefix(line, "port "))
		if err != nil || p <= 0 {
			cmd.Process.Kill()
			<-exited
			t.Fatalf("bad port line %q: %v", line, err)
		}
		port = p
	case <-exited:
		t.Fatalf("server exited early (stderr: %s)", stderr.String())
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		<-exited
		t.Fatalf("server did not print port within 10s (stderr: %s)", stderr.String())
	}

	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-exited
		if stderr.Len() > 0 {
			t.Logf("server stderr:\n%s", stderr.String())
		}
	}
	t.Cleanup(stop)
	return port, stop
}

func TestStdHttp_H2ServerAgainstGoPeer(t *testing.T) {
	peers := httpPeerKitDir(t)
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	h2peer := filepath.Join(peers, "bin", "h2peer"+ext)
	cert := filepath.Join(peers, "certs", "server-p256-chain.pem")
	key := filepath.Join(peers, "certs", "server-p256.key.pem")
	ca := filepath.Join(peers, "certs", "ca.pem")
	exe := http2Build(t, mkH2PeerServerProg)

	modes := []struct {
		name   string
		scheme string
	}{
		{name: "tls", scheme: "https"},
		{name: "h2c", scheme: "http"},
	}

	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			port, stop := launchMkH2Server(t, exe, m.name, cert, key)
			defer stop()

			args := []string{
				"client",
				"-url", fmt.Sprintf("%s://127.0.0.1:%d", m.scheme, port),
				"-case", "get,echo,download,upload,parallel,early,flush",
			}
			if m.name == "tls" {
				args = append(args, "-ca", ca, "-servername", "localhost")
			}
			cmd := exec.Command(h2peer, args...)
			out, err := cmd.CombinedOutput()
			t.Logf("h2peer %s output:\n%s", m.name, string(out))
			if err != nil {
				t.Fatalf("h2peer client (%s) failed: %v", m.name, err)
			}
			sc := bufio.NewScanner(bytes.NewReader(out))
			count := 0
			for sc.Scan() {
				var res struct {
					Case  string `json:"case"`
					Ok    bool   `json:"ok"`
					Error string `json:"error"`
				}
				if err := json.Unmarshal(sc.Bytes(), &res); err != nil {
					t.Fatalf("bad JSON line %q: %v", sc.Text(), err)
				}
				if !res.Ok {
					t.Fatalf("case %s failed: %s", res.Case, res.Error)
				}
				count++
			}
			if count != 7 {
				t.Fatalf("expected 7 peer cases, got %d", count)
			}
		})
	}
}

func TestStdHttp_H2MixedCaseHandlerHeaderReachesGoClient(t *testing.T) {
	peers := httpPeerKitDir(t)
	cert := filepath.Join(peers, "certs", "server-p256-chain.pem")
	key := filepath.Join(peers, "certs", "server-p256.key.pem")
	caBytes, err := os.ReadFile(filepath.Join(peers, "certs", "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		t.Fatal("bad ca.pem")
	}

	exe := http2Build(t, mkH2PeerServerProg)
	port, stop := launchMkH2Server(t, exe, "tls", cert, key)
	defer stop()

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: "localhost",
			NextProtos: []string{"h2"},
		},
		ForceAttemptHTTP2: true,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	resp, err := cl.Get(fmt.Sprintf("https://127.0.0.1:%d/mixed", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.ProtoMajor != 2 || resp.StatusCode != 200 {
		t.Fatalf("got proto=%s status=%d", resp.Proto, resp.StatusCode)
	}
	if resp.Header.Get("X-Mixed-Header") != "MixedVal" || string(body) != "mixed-ok" {
		t.Fatalf("unexpected header=%q body=%q", resp.Header.Get("X-Mixed-Header"), string(body))
	}

	// Verify 64 MiB download read in 32 KiB chunks cleanly from Go client.
	dlResp, err := cl.Get(fmt.Sprintf("https://127.0.0.1:%d/bytes?n=67108864", port))
	if err != nil {
		t.Fatal(err)
	}
	defer dlResp.Body.Close()
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, rerr := dlResp.Body.Read(buf)
		total += int64(n)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			t.Fatal(rerr)
		}
	}
	if total != 67108864 {
		t.Fatalf("64 MiB download read %d bytes", total)
	}
	_ = net.IPv4(127, 0, 0, 1)
}
