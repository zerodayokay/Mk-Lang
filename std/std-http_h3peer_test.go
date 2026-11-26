// Why: HTTP/3 needs a wire peer independent of Mk's HTTP and QUIC engines.
// What: native std.http client and server integration tests against quic-go.
// How: start the offline peer-kit binary on loopback with its test CA, and
// exercise both directions through the public std.http APIs.

package std

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func h3PeerFiles(t *testing.T) (peer, ca, cert, key string) {
	t.Helper()
	dir := os.Getenv("MK_PEERS")
	if dir == "" && os.Getenv("MK_EVIDENCE") != "" {
		dir = filepath.Join(os.Getenv("MK_EVIDENCE"), "peers")
	}
	if dir == "" {
		t.Skip("peer kit absent: set MK_PEERS")
	}
	exe := "h3peer"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	peer = filepath.Join(dir, "bin", exe)
	if _, err := os.Stat(peer); err != nil {
		t.Skipf("HTTP/3 peer unavailable: %v", err)
	}
	return peer, filepath.Join(dir, "certs", "ca.pem"),
		filepath.Join(dir, "certs", "server-p256-chain.pem"),
		filepath.Join(dir, "certs", "server-p256.key.pem")
}

func startH3Peer(t *testing.T, peer, cert, key string, extra ...string) int {
	t.Helper()
	args := append([]string{"server", "-cert", cert, "-key", key, "-stdin"}, extra...)
	return startH3PeerProcess(t, peer, args...)
}

func startH3PeerProcess(t *testing.T, exe string, args ...string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	listen := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(out)
		line, _ := r.ReadBytes('\n')
		var msg struct{ Port int }
		_ = json.Unmarshal(line, &msg)
		listen <- msg.Port
		io.Copy(io.Discard, r)
		_ = cmd.Wait()
	}()
	t.Cleanup(func() {
		_ = in.Close()
		<-done
	})
	select {
	case port := <-listen:
		if port == 0 {
			t.Fatal("peer failed to report its UDP port")
		}
		return port
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("HTTP/3 peer startup timed out")
	}
	return 0
}

func h3LossProxy(t *testing.T, peer string, port int) int {
	t.Helper()
	base := "udpproxy" + filepath.Ext(peer)
	exe := filepath.Join(filepath.Dir(peer), base)
	if _, err := os.Stat(exe); err != nil {
		t.Skipf("packet-loss proxy unavailable: %v", err)
	}
	return startH3PeerProcess(t, exe, "-stdin", "-server", fmt.Sprintf("127.0.0.1:%d", port), "-loss", "10", "-seed", "12")
}

func TestStdHttp_H3ServerAgainstPeer(t *testing.T) {
	peer, ca, cert, key := h3PeerFiles(t)
	src := strings.Replace(mkH2PeerServerProg, "import std.net", "import std.net\nimport std.quic", 1)
	src = strings.Replace(src, "    try res.Header(\"X-Proto\", \"HTTP/2.0\")", `    if res.Version() == http.HttpVersion.Http3 {
        Assert(link.Version() == http.HttpVersion.Http3 && res.Secure())
        if taken := link.Takeover() {
            taken.Abandon()
            fail http.Status(code: 500)
        }
        try res.Header("X-Proto", "HTTP/3.0")
    }`, 1)
	src = strings.Replace(src, "        o.h2 = true", `        o.h2 = true
        var q := quic.DefaultConfig
        q.tls = some(&cfg)
        q.streams = 200
        o.h3 = some(quic.Acceptor(&q))`, 1)
	exe := buildNative(t, nil, src)
	port, _ := launchMkH2Server(t, exe, "tls", cert, key)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	args := []string{"client", "-url", fmt.Sprintf("https://127.0.0.1:%d", port),
		"-ca", ca, "-servername", "localhost", "-case", "get,echo,download,upload,parallel,flush,altsvc",
		"-size", "16777216", "-n", "100"}
	cmd := exec.CommandContext(ctx, peer, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("HTTP/3 peer client: %v\n%s", err, out)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	count := 0
	for sc.Scan() {
		var r struct {
			Case    string
			OK      bool
			Proto   string
			Error   string
			Headers map[string]string
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("invalid peer output %q: %v", sc.Text(), err)
		}
		if !r.OK || r.Proto != "HTTP/3.0" {
			t.Errorf("peer case %s: protocol %q, error %q", r.Case, r.Proto, r.Error)
		}
		if r.Case == "get" && r.Headers["x-proto"] != "HTTP/3.0" {
			t.Error("handler did not see HTTP/3 connection metadata")
		}
		count++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Fatalf("expected seven peer cases, got %d\n%s", count, out)
	}
	t.Run("packet-loss", func(t *testing.T) {
		proxy := h3LossProxy(t, peer, port)
		cmd := exec.CommandContext(ctx, peer, "client", "-url", fmt.Sprintf("https://127.0.0.1:%d", proxy),
			"-ca", ca, "-servername", "localhost", "-case", "get,echo", "-size", "131072")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("HTTP/3 peer client through packet loss: %v\n%s", err, out)
		}
	})
}

const mkH3PeerClientMain = `
export Main(sys: &System): http.Error|url.Error|x509.Error|fs.Error|WriteError!void {
    a := sys.arena
    io := sys.io
    base := sys.args[1]
    roots := try x509.ParsePem(a, try fs.ReadText(a, io, sys.args[2]))
    pool := x509.Pool.New(roots.Done()[..])
    var cfg := tls.DefaultConfig
    cfg.roots = some(&pool)
    var q := quic.DefaultConfig
    q.tls = some(&cfg)
    if sys.args[3] == "idle" {
        q.idle = time.Millis(750)
    }
    var limits := http.DefaultClientLimits
    limits.h3 = some(quic.Connector(&q))
    mode := sys.args[3]
    limits.h3_direct = mode == "direct" || mode == "fallback" || mode == "undriven" || mode == "loss" || mode == "idle"
    if mode != "direct" && mode != "loss" && mode != "idle" {
        limits.secure = some(tls.Connector(&cfg))
    }
    if mode == "fallback" {
        limits.h3_connect = time.Millis(200)
    }
    cli := http.Client.New(sys.heap, limits)
    defer cli.Close()
    var out := sys.Stdout()
    get_url := try a.Fmt("{base}/get", (base))
    if mode == "undriven" {
        first := Fetch1(&cli, sys.heap, io, "GET", get_url, "".AsBytes())
        try Print(mut out, "undriven status={s} ver={v}\n", (s: first.status, v: first.ver))
        return
    }
    scope requests := io.Scope() {
        requests.CancelAfter(time.Seconds(60))
        requests.go DriveTask(&cli, io)
        defer cli.Stop()
        if !(try cli.WaitDriving(io)) {
            fail Canceled
        }
        first := Fetch1(&cli, sys.heap, io, "GET", get_url, "".AsBytes())
        try Print(mut out, "first status={s} ver={v} conn={c} n={n}\n", (s: first.status, v: first.ver, c: first.conn, n: first.n))
        if mode == "idle" {
            try io.Sleep(time.Seconds(3))
        }
        second := Fetch1(&cli, sys.heap, io, "GET", get_url, "".AsBytes())
        try Print(mut out, "second status={s} ver={v} conn={c} n={n}\n", (s: second.status, v: second.ver, c: second.conn, n: second.n))
        if mode == "idle" && (first.conn != second.conn || first.conn == 0) {
            fail http.BadRequest(offset: 1)
        }
        if mode == "loss" {
            data := try a.Slice[u8](131072)
            echo := Fetch1(&cli, sys.heap, io, "POST", try a.Fmt("{base}/echo", (base)), data[..])
            try Print(mut out, "loss status={s} ver={v} n={n}\n", (s: echo.status, v: echo.ver, n: echo.n))
        }
        if mode == "direct" {
            if first.conn != second.conn || first.conn == 0 {
                fail http.BadRequest(offset: 0)
            }
            try Parallel(&cli, sys.heap, io, try a.Fmt("{base}/conn", (base)), 100, mut out)
            data := try a.Slice[u8](16 * 1024 * 1024)
            for i in 0..data.Len() {
                (mut data[..])[i] = Pattern(i)
            }
            echo := Fetch1(&cli, sys.heap, io, "POST", try a.Fmt("{base}/echo", (base)), data[0..1024])
            sink := try url.Parse(a, try a.Fmt("{base}/sink", (base)), none)
            req := http.ClientRequest(method: "POST", url: &sink, headers: NoHeaders, body: data[..])
            upload := try cli.Fetch(a, io, &req)
            download := Fetch1(&cli, sys.heap, io, "GET", try a.Fmt("{base}/bytes?n={n}", (base, n: data.Len())), "".AsBytes())
            try Print(mut out, "bulk echo={e} upload={u} download={d}\n", (e: echo.n, u: upload.status, d: download.n))
            early := Fetch1(&cli, sys.heap, io, "POST", try a.Fmt("{base}/early", (base)), data[0..1048576])
            try Print(mut out, "early status={s} ver={v} n={n}\n", (s: early.status, v: early.ver, n: early.n))
        }
    }
}
`

func TestStdHttp_H3ClientAgainstPeer(t *testing.T) {
	peer, ca, cert, key := h3PeerFiles(t)
	helpers, _, _ := strings.Cut(h2ClientProg, "export Main")
	src := strings.Replace(helpers, "import std.http", "import std.http\nimport std.quic", 1) + mkH3PeerClientMain
	exe := buildNative(t, nil, src)
	port := startH3Peer(t, peer, cert, key, "-altsvc")
	base := fmt.Sprintf("https://localhost:%d", port)
	for _, mode := range []string{"direct", "alt", "undriven", "idle"} {
		t.Run(mode, func(t *testing.T) {
			r := runNative(t, exe, "", base, ca, mode)
			if r.code != 0 {
				t.Fatalf("exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			t.Log(r.stdout)
			if mode == "undriven" {
				if !strings.Contains(r.stdout, "undriven status=200 ver=11") {
					t.Fatalf("HTTP/3 ran without Drive:\n%s", r.stdout)
				}
				return
			}
			if mode == "direct" && (!strings.Contains(r.stdout, "parallel ok=100 conn_min=1 conn_max=1") ||
				!strings.Contains(r.stdout, "bulk echo=1024 upload=200 download=16777216") ||
				!strings.Contains(r.stdout, "early status=200 ver=3 n=6")) {
				t.Fatalf("parallel or bulk transfer failed:\n%s", r.stdout)
			}
			first := 3
			if mode == "alt" {
				first = 11
			}
			if !strings.Contains(r.stdout, fmt.Sprintf("first status=200 ver=%d", first)) ||
				!strings.Contains(r.stdout, "second status=200 ver=3") {
				t.Fatalf("unexpected versions:\n%s", r.stdout)
			}
		})
	}
	t.Run("packet-loss", func(t *testing.T) {
		proxy := h3LossProxy(t, peer, port)
		r := runNative(t, exe, "", fmt.Sprintf("https://localhost:%d", proxy), ca, "loss")
		if r.code != 0 || !strings.Contains(r.stdout, "loss status=200 ver=3 n=131072") {
			t.Fatalf("HTTP/3 through packet loss, exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
		}
	})
	t.Run("udp-unavailable", func(t *testing.T) {
		checkH3Fallback(t, exe, peer, ca, cert, key, false)
	})
	t.Run("origin-cert-mismatch", func(t *testing.T) {
		checkH3Fallback(t, exe, peer, ca, cert, key, true)
	})
}

func checkH3Fallback(t *testing.T, exe, peer, ca, cert, key string, badOrigin bool) {
	t.Helper()
	tcpCert, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	address := "localhost"
	if badOrigin {
		pki := tlsPeerPKI(t)
		cert, key, ca = tlsLeafFiles(t, t.TempDir(), pki, "p256")
		tcpCert = h2Leaf(t, pki, false)
		address = "127.0.0.1"
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "hello from the peer kit\n")
		}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{tcpCert}},
	}
	t.Cleanup(func() { _ = srv.Close() })
	go srv.ServeTLS(tcp, "", "")
	if badOrigin {
		// Both certs chain to the same CA. The UDP certificate names
		// localhost only; the TCP certificate additionally names 127.0.0.1.
		startH3Peer(t, peer, cert, key, "-addr", fmt.Sprintf("127.0.0.1:%d", port))
	}

	r := runNative(t, exe, "", fmt.Sprintf("https://%s:%d", address, port), ca, "fallback")
	if r.code != 0 || !strings.Contains(r.stdout, "first status=200 ver=11") ||
		!strings.Contains(r.stdout, "second status=200 ver=11") {
		t.Fatalf("expected both requests over TCP, exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
}
