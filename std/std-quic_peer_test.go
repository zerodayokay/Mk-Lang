// Why: std.quic must interoperate on the wire over real OS UDP sockets with
// mature independent QUIC implementations (golang.org/x/net/quic and
// quic-go) and survive real UDP loss, reordering, duplication, and NAT rebinding.
//
// What: TestStdQuic_PeerClient_XNet, TestStdQuic_PeerClient_QuicGo,
// TestStdQuic_PeerClient_UdpProxy, TestStdQuic_PeerServer_XNet,
// TestStdQuic_PeerServer_QuicGo, and TestStdQuic_PeerServer_Retry.
//
// How: builds a Mk QUIC peer binary from a temp workspace (using std.quic's
// public Dial/Connector/Acceptor/Mux API) and drives it against quicpeer.exe
// and udpproxy.exe from $MK_PEERS (or $MK_EVIDENCE/peers). Skips cleanly when
// the peer kit is absent.

package std

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const qpeerMk = `// Generated fixture from std/std-quic_peer_test.go.
import std.crypto
import std.encoding
import std.fs
import std.hash
import std.net
import std.quic
import std.tls
import std.x509

FlagSet(f: &Mutex[u32], io: Io, v: u32) {
    var g := f.Lock(io.NoCancel()) else { return }
    *g.Mut() = v
    g.Unlock()
}

FlagGet(f: &Mutex[u32], io: Io): u32 {
    var g := f.Lock(io.NoCancel()) else { return 0 }
    v := *g.Mut()
    g.Unlock()
    return v
}

PatternByte(i: int): u8 {
    return Trunc[u8]((i * 7 + (i >> 8) + 13) & 0xff)
}

WritePattern(cm: &net.Mux, st: &net.MuxStream, io: Io, total: int, fin: bool, failed: &Mutex[u32]) {
    var buf: [16384]u8 = [0; 16384]
    var off := 0
    for off < total {
        var n := total - off
        if n > 16384 {
            n = 16384
        }
        for i in 0..n {
            buf[i] = PatternByte(off + i)
        }
        off += n
        last := fin && off == total
        cm.Write(io, st, buf[0..n], last) else {
            FlagSet(failed, io, 1)
            return
        }
    }
    if fin && total == 0 {
        cm.Write(io, st, b"", true) else {
            FlagSet(failed, io, 1)
        }
    }
}

ClientEchoWriter(cm: &net.Mux, st: &net.MuxStream, io: Io, total: int, failed: &Mutex[u32]) {
    cm.Write(io, st, b"echo\n", false) else {
        FlagSet(failed, io, 1)
        return
    }
    WritePattern(cm, st, io, total, true, failed)
}

RunEchoStream(cm: &net.Mux, io: Io, size: int): net.Error!bool {
    var mst := try cm.Open(io, false, true)
    match mst {
        none { return false }
        some(st) {
            onerr cm.Release(st)
            failed := Mutex[u32].New(0)
            var matched := true
            var off := 0
            scope sc := io.Scope() {
                sc.go ClientEchoWriter(cm, &st, io, size, &failed)
                var buf: [16384]u8 = [0; 16384]
                for {
                    r := try cm.Read(io, &st, mut buf[..], true)
                    match r {
                        none { break }
                        some(v) {
                            if v == 0 {
                                break
                            }
                            for i in 0..v {
                                if buf[i] != PatternByte(off + i) {
                                    matched = false
                                }
                            }
                            off += v
                        }
                    }
                }
            }
            cm.Release(st)
            return FlagGet(&failed, io) == 0 && matched && off == size
        }
    }
}

ParallelStreamTask(cm: &net.Mux, io: Io, size: int, failed: &Mutex[u32]) {
    ok := RunEchoStream(cm, io, size) else {
        FlagSet(failed, io, 1)
        return
    }
    if !ok {
        FlagSet(failed, io, 2)
    }
}

RunClientDownload(a: &Arena, cm: &net.Mux, io: Io, size: int): net.Error|OutOfMemory!bool {
    var mst := try cm.Open(io, false, true)
    match mst {
        none { return false }
        some(st) {
            onerr cm.Release(st)
            cmd := try a.Fmt("source {size}\n", (size))
            try cm.Write(io, &st, cmd.AsBytes(), true)
            var buf: [32768]u8 = [0; 32768]
            var off := 0
            var matched := true
            for {
                r := try cm.Read(io, &st, mut buf[..], true)
                match r {
                    none { break }
                    some(v) {
                        if v == 0 {
                            break
                        }
                        for i in 0..v {
                            if buf[i] != PatternByte(off + i) {
                                matched = false
                            }
                        }
                        off += v
                    }
                }
            }
            cm.Release(st)
            return matched && off == size
        }
    }
}

RunClientUpload(a: &Arena, cm: &net.Mux, io: Io, size: int): net.Error|OutOfMemory!bool {
    var mst := try cm.Open(io, false, true)
    match mst {
        none { return false }
        some(st) {
            onerr cm.Release(st)
            try cm.Write(io, &st, b"sink\n", false)
            var h := hash.Sha256State.New()
            var buf: [16384]u8 = [0; 16384]
            var off := 0
            for off < size {
                var n := size - off
                if n > 16384 {
                    n = 16384
                }
                for i in 0..n {
                    buf[i] = PatternByte(off + i)
                }
                h.Bytes(buf[0..n])
                off += n
                try cm.Write(io, &st, buf[0..n], off == size)
            }
            if size == 0 {
                try cm.Write(io, &st, b"", true)
            }
            digest := h.Sum()
            hex_s := try encoding.HexEncode(a, digest[..])
            want := try a.Fmt("{size} {hex_s}\n", (size, hex_s))
            var reply: [256]u8 = [0; 256]
            var rn := 0
            for rn < 256 {
                r := try cm.Read(io, &st, mut reply[rn..], true)
                match r {
                    none { break }
                    some(v) {
                        if v == 0 {
                            break
                        }
                        rn += v
                    }
                }
            }
            cm.Release(st)
            got := str.From(reply[0..rn]) else { return false }
            return got == want
        }
    }
}

RunClient(sys: &System): net.Error|x509.Error|encoding.Error|fs.Error|OutOfMemory|ParseError|WriteError|Utf8Error!void {
    a := sys.arena
    io := sys.io
    var out := sys.Stdout()
    addr := try net.ParseAddr(sys.args[2])
    ca_path := sys.args[3]
    alpn := sys.args[4]
    mode := sys.args[5]
    size := Cast[int](sys.args[6].ParseInt() else { 1024 })
    nstreams := Cast[int](sys.args[7].ParseInt() else { 10 })
    stream_size := Cast[int](sys.args[8].ParseInt() else { 4096 })
    close_code := Trunc[u64](sys.args[9].ParseInt() else { 7 })

    text := try fs.ReadText(a, io, ca_path)
    certs := (try x509.ParsePem(a, text)).Done()
    pool := x509.Pool.New(certs[..])
    var tcfg := tls.DefaultConfig
    tcfg.roots = some(&pool)
    var qcfg := quic.DefaultConfig
    qcfg.tls = some(&tcfg)

    if mode == "alpn-mismatch" {
        var cn := quic.Connector(&qcfg)
        var cm := cn.Dial(sys.heap, io, addr, "localhost", alpn) else {
            try out.Str("OK alpn-rejected\n")
            try out.Flush()
            return
        }
        cm.Abandon()
        try out.Str("ERR alpn-unexpected-success\n")
        try out.Flush()
        return
    }

    var cn := quic.Connector(&qcfg)
    var cm := try cn.Dial(sys.heap, io, addr, "localhost", alpn)
    onerr cm.Abandon()

    if mode == "all" {
        ok1 := try RunEchoStream(&cm, io, size)
        if !ok1 {
            cm.Abandon()
            try out.Str("ERR echo\n")
            try out.Flush()
            return
        }
        failed := Mutex[u32].New(0)
        scope ps := io.Scope() {
            for _ in 0..nstreams {
                ps.go ParallelStreamTask(&cm, io, stream_size, &failed)
            }
        }
        if FlagGet(&failed, io) != 0 {
            cm.Abandon()
            try out.Str("ERR parallel\n")
            try out.Flush()
            return
        }
        ok2 := try RunClientDownload(a, &cm, io, size * 16)
        if !ok2 {
            cm.Abandon()
            try out.Str("ERR download\n")
            try out.Flush()
            return
        }
        ok3 := try RunClientUpload(a, &cm, io, size)
        if !ok3 {
            cm.Abandon()
            try out.Str("ERR upload\n")
            try out.Flush()
            return
        }
        cm.Close(io, close_code) else e {
            cm.Abandon()
            fail e
        }
        try out.Str("OK all\n")
        try out.Flush()
        return
    }

    if mode == "echo-only" {
        ok1 := try RunEchoStream(&cm, io, size)
        if !ok1 {
            cm.Abandon()
            try out.Str("ERR echo\n")
            try out.Flush()
            return
        }
        cm.Close(io, close_code) else e {
            cm.Abandon()
            fail e
        }
        try out.Str("OK echo\n")
        try out.Flush()
        return
    }

    if mode == "wait-close" {
        var mst := try cm.Open(io, false, true)
        match mst {
            none {
                cm.Abandon()
                try out.Str("ERR open\n")
                try out.Flush()
                return
            }
            some(st) {
                onerr cm.Release(st)
                try cm.Write(io, &st, b"echo\nhi", true)
                var rb: [32]u8 = [0; 32]
                for {
                    r := cm.Read(io, &st, mut rb[..], true) else {
                        break
                    }
                    match r {
                        none { break }
                        some(v) {
                            if v == 0 {
                                break
                            }
                        }
                    }
                }
                // Wait for peer CONNECTION_CLOSE.
                var extra: [16]u8 = [0; 16]
                var got_closed := false
                for {
                    mst2 := cm.Accept(io, false, true) else {
                        got_closed = true
                        break
                    }
                    match mst2 {
                        none {
                            got_closed = true
                            break
                        }
                        some(s2) {
                            _ = cm.Read(io, &s2, mut extra[..], true) else {}
                            cm.Release(s2)
                        }
                    }
                }
                cm.Release(st)
                cm.Abandon()
                if got_closed {
                    try out.Str("OK wait-close\n")
                } else {
                    try out.Str("ERR wait-close\n")
                }
                try out.Flush()
                return
            }
        }
    }
    cm.Abandon()
    try out.Str("ERR unknown-mode\n")
    try out.Flush()
}

ServeStreamTask(cm: &net.Mux, st: net.MuxStream, io: Io) {
    var hdr: [128]u8 = [0; 128]
    var hlen := 0
    var saw_nl := false
    var eof := false
    for hlen < 128 && !saw_nl {
        var b1: [1]u8 = [0; 1]
        r := cm.Read(io, &st, mut b1[..], true) else {
            cm.Release(st)
            return
        }
        match r {
            none { break }
            some(v) {
                if v == 0 {
                    eof = true
                    break
                }
                if b1[0] == 0x0A {
                    saw_nl = true
                    break
                }
                hdr[hlen] = b1[0]
                hlen += 1
            }
        }
    }
    cmd := str.From(hdr[0..hlen]) else {
        cm.Release(st)
        return
    }
    if cmd == "echo" {
        var sent_total: i64 = 0
        cm.Credit(&st, 262144)
        var buf: [16384]u8 = [0; 16384]
        for !eof {
            r := cm.Read(io, &st, mut buf[..], true) else {
                cm.Release(st)
                return
            }
            match r {
                none { break }
                some(v) {
                    if v == 0 {
                        break
                    }
                    sent_total += i64(v)
                    cm.Credit(&st, sent_total + 262144)
                    cm.Write(io, &st, buf[0..v], false) else {
                        cm.Release(st)
                        return
                    }
                }
            }
        }
        cm.Write(io, &st, b"", true) else {}
        cm.Release(st)
        return
    }
    if cmd == "sink" {
        var h := hash.Sha256State.New()
        var total := 0
        cm.Credit(&st, 262144)
        var buf: [16384]u8 = [0; 16384]
        for !eof {
            r := cm.Read(io, &st, mut buf[..], true) else {
                cm.Release(st)
                return
            }
            match r {
                none { break }
                some(v) {
                    if v == 0 {
                        break
                    }
                    h.Bytes(buf[0..v])
                    total += v
                    cm.Credit(&st, i64(total) + 262144)
                }
            }
        }
        digest := h.Sum()
        var hex_chars := b"0123456789abcdef"
        var reply: [128]u8 = [0; 128]
        // Format "<total> <hex>\n" without an Arena.
        var digits: [24]u8 = [0; 24]
        var dn := 0
        if total == 0 {
            digits[0] = 0x30
            dn = 1
        } else {
            var tmp := total
            for tmp > 0 {
                digits[dn] = Trunc[u8](0x30 + (tmp % 10))
                dn += 1
                tmp /= 10
            }
        }
        var rpos := 0
        for i in 0..dn {
            reply[rpos] = digits[dn - 1 - i]
            rpos += 1
        }
        reply[rpos] = 0x20
        rpos += 1
        for i in 0..32 {
            b := int(digest[i])
            reply[rpos] = hex_chars[(b >> 4) & 0x0f]
            reply[rpos + 1] = hex_chars[b & 0x0f]
            rpos += 2
        }
        reply[rpos] = 0x0A
        rpos += 1
        cm.Write(io, &st, reply[0..rpos], true) else {}
        cm.Release(st)
        return
    }
    if cmd.StartsWith("source ") {
        n_s := cmd[7..cmd.Len()]
        n := Cast[int](n_s.ParseInt() else { 0 })
        unused := Mutex[u32].New(0)
        WritePattern(cm, &st, io, n, true, &unused)
        cm.Release(st)
        return
    }
    cm.Write(io, &st, b"error unknown command\n", true) else {}
    cm.Release(st)
}

ServeConnTask(sm: net.Mux, io: Io) {
    var conn := sm
    scope sc := io.Scope() {
        for {
            mst := conn.Accept(io, false, true) else { break }
            match mst {
                none { break }
                some(st) {
                    sc.go ServeStreamTask(&conn, st, io)
                }
            }
        }
    }
    conn.Abandon()
}

ServeAcceptLoop(l: &net.MuxListener, io: Io) {
    scope sc := io.Scope() {
        for {
            sm := l.Accept(io) else { break }
            sc.go ServeConnTask(sm, io)
        }
    }
}

ServeListenerRun(l: &net.MuxListener, io: Io) {
    _ = l.Run(io) else {}
}

RunServer(sys: &System): net.Error|x509.Error|encoding.Error|fs.Error|crypto.Error|OutOfMemory|WriteError|Utf8Error!void {
    a := sys.arena
    io := sys.io
    var out := sys.Stdout()
    var inp := sys.Stdin()
    cert_path := sys.args[2]
    key_path := sys.args[3]
    alpn := sys.args[4]
    retry_above := Cast[int](sys.args[5].ParseInt() else { 100000 })

    cert_text := try fs.ReadText(a, io, cert_path)
    key_text := try fs.ReadText(a, io, key_path)
    certs := (try x509.ParsePem(a, cert_text)).Done()
    blocks := try encoding.ParsePem(a, key_text)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    var k := key
    ids: [1]tls.Identity = [tls.Identity(chain: certs[..], key: &k)]
    var tcfg := tls.DefaultConfig
    tcfg.identities = ids[..]
    var qcfg := quic.DefaultConfig
    qcfg.tls = some(&tcfg)
    qcfg.retry_above = retry_above

    var ac := quic.Acceptor(&qcfg)
    var l := try ac.Listen(sys.heap, io, "127.0.0.1:0", alpn)
    defer l.Close()

    port := l.Port()
    msg := try a.Fmt("LISTEN {port}\n", (port))
    try out.Str(msg)
    try out.Flush()

    scope sc := io.Scope() {
        sc.go ServeListenerRun(&l, io)
        sc.go ServeAcceptLoop(&l, io)
        var quit_buf: [16]u8 = [0; 16]
        for {
            n := inp.Read(mut quit_buf[..]) else { break }
            if n == 0 {
                break
            }
        }
        sc.Cancel()
    }
}

export Main(sys: &System): net.Error|x509.Error|encoding.Error|fs.Error|crypto.Error|OutOfMemory|ParseError|ReadError|WriteError|Utf8Error!void {
    if sys.args[1] == "client" {
        try RunClient(sys)
    } else {
        try RunServer(sys)
    }
}
`

type peerEnv struct {
	dir      string
	quicpeer string
	udpproxy string
	ca       string
	cert     string
	key      string
}

func loadPeerEnv(t *testing.T) peerEnv {
	t.Helper()
	dir := os.Getenv("MK_PEERS")
	if dir == "" {
		if ev := os.Getenv("MK_EVIDENCE"); ev != "" {
			cand := filepath.Join(ev, "peers")
			if _, err := os.Stat(cand); err == nil {
				dir = cand
			}
		}
	}
	if dir == "" {
		t.Skip("peer kit absent: set MK_PEERS")
	}
	exeSuffix := ""
	if runtime.GOOS == "windows" {
		exeSuffix = ".exe"
	}
	qp := filepath.Join(dir, "bin", "quicpeer"+exeSuffix)
	up := filepath.Join(dir, "bin", "udpproxy"+exeSuffix)
	if _, err := os.Stat(qp); err != nil {
		t.Skipf("peer binary missing (%s): %v", qp, err)
	}
	if _, err := os.Stat(up); err != nil {
		t.Skipf("udpproxy missing (%s): %v", up, err)
	}
	return peerEnv{
		dir:      dir,
		quicpeer: qp,
		udpproxy: up,
		ca:       filepath.Join(dir, "certs", "ca.pem"),
		cert:     filepath.Join(dir, "certs", "server-p256-chain.pem"),
		key:      filepath.Join(dir, "certs", "server-p256.key.pem"),
	}
}

var qpeerBuild struct {
	sync.Mutex
	exe string
	err string
}

func buildMkQuicPeer(t *testing.T) string {
	t.Helper()
	qpeerBuild.Lock()
	defer qpeerBuild.Unlock()
	if qpeerBuild.err != "" {
		t.Fatal(qpeerBuild.err)
	}
	if qpeerBuild.exe != "" {
		return qpeerBuild.exe
	}
	dir, err := packageTempDir("mkquicpeer")
	if err != nil {
		t.Fatal(err)
	}
	stdDir := copyStd(t, dir, nil)
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(qpeerMk), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "qpeer")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	t.Setenv("MK_CACHE", filepath.Join(dir, "cache"))
	out, status := mkCmd(t, dir, stdDir, "build", "--release", "-o", exe, "main.mk")
	if status != 0 {
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		qpeerBuild.err = fmt.Sprintf("mk build qpeer failed: status %d\n%s", status, out)
		t.Fatal(qpeerBuild.err)
	}
	qpeerBuild.exe = exe
	return exe
}

type startedProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr strings.Builder
	lines  []string
}

func startPeerProc(t *testing.T, exe string, args ...string) (*startedProc, int) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	sp := &startedProc{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}
	cmd.Stderr = &sp.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		l, e := sp.stdout.ReadString('\n')
		if e != nil {
			errCh <- e
			return
		}
		lineCh <- l
	}()
	var first string
	select {
	case first = <-lineCh:
	case e := <-errCh:
		_ = stdin.Close()
		_ = cmd.Wait()
		t.Fatalf("%s exited early (%v): stderr=%s", exe, e, sp.stderr.String())
	case <-time.After(15 * time.Second):
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("%s timed out waiting for listen line: stderr=%s", exe, sp.stderr.String())
	}
	sp.lines = append(sp.lines, strings.TrimSpace(first))
	var port int
	if strings.HasPrefix(sp.lines[0], "LISTEN ") {
		p, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(sp.lines[0], "LISTEN ")))
		if err != nil || p <= 0 {
			t.Fatalf("bad LISTEN line %q: %v", sp.lines[0], err)
		}
		port = p
	} else {
		var hdr struct {
			Port int `json:"port"`
		}
		if err := json.Unmarshal([]byte(sp.lines[0]), &hdr); err != nil || hdr.Port <= 0 {
			t.Fatalf("bad peer listen JSON %q: %v", sp.lines[0], err)
		}
		port = hdr.Port
	}
	return sp, port
}

func stopPeerProc(t *testing.T, sp *startedProc) []string {
	t.Helper()
	_ = sp.stdin.Close()
	rest, _ := io.ReadAll(sp.stdout)
	if err := sp.cmd.Wait(); err != nil {
		t.Fatalf("peer process failed: %v\nfirst=%v\nrest=%s\nstderr=%s", err, sp.lines, string(rest), sp.stderr.String())
	}
	for _, ln := range strings.Split(string(rest), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			sp.lines = append(sp.lines, ln)
		}
	}
	return sp.lines
}

func runMkClient(t *testing.T, mkExe string, addr string, ca string, alpn string, mode string, size int, n int, streamSize int, closeCode int) string {
	t.Helper()
	cmd := exec.Command(mkExe, "client", addr, ca, alpn, mode, fmt.Sprint(size), fmt.Sprint(n), fmt.Sprint(streamSize), fmt.Sprint(closeCode))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("qpeer client (%s %s) failed: %v\n%s", mode, addr, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func runPeerClientSide(t *testing.T, pe peerEnv, impl string, mkExe string, retryAbove int) {
	t.Helper()
	srv, port := startPeerProc(t, mkExe, "server", pe.cert, pe.key, "mkq", fmt.Sprint(retryAbove))
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	qlog := filepath.Join(t.TempDir(), impl+".qlog")
	cmd := exec.Command(pe.quicpeer, "client", "-impl", impl, "-addr", addr, "-alpn", "mkq", "-ca", pe.ca,
		"-case", "echo,parallel,download,upload", "-size", "1048576", "-n", "50", "-stream-size", "4096", "-close-code", "7", "-timeout", "30s", "-events", qlog)
	out, err := cmd.CombinedOutput()
	lines := stopPeerProc(t, srv)
	if err != nil {
		qb, _ := os.ReadFile(qlog)
		if len(qb) > 16384 {
			qb = qb[len(qb)-16384:]
		}
		t.Fatalf("quicpeer client (%s) -> Mk server (retry=%d) failed: %v\n%s\nsrvLines=%v\nqlog:\n%s", impl, retryAbove, err, string(out), lines, string(qb))
	}
	if !strings.Contains(string(out), `"case":"download"`) || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("unexpected quicpeer client output:\n%s", string(out))
	}
}

func TestStdQuic_PeerClient_XNet(t *testing.T) {
	pe := loadPeerEnv(t)
	mkExe := buildMkQuicPeer(t)

	// Full suite: 1 MiB echo, 100 streams parallel, 16 MiB download, 1 MiB upload, close code 7.
	srv, port := startPeerProc(t, pe.quicpeer, "server", "-impl", "xnet", "-cert", pe.cert, "-key", pe.key, "-alpn", "mkq", "-stdin")
	got := runMkClient(t, mkExe, fmt.Sprintf("127.0.0.1:%d", port), pe.ca, "mkq", "all", 1048576, 100, 4096, 7)
	lines := stopPeerProc(t, srv)
	if got != "OK all" {
		t.Fatalf("got %q, lines=%v", got, lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"kind":"application"`) || !strings.Contains(joined, `"code":7`) {
		t.Fatalf("server did not observe close code 7:\n%s", joined)
	}

	// ALPN mismatch.
	srv2, port2 := startPeerProc(t, pe.quicpeer, "server", "-impl", "xnet", "-cert", pe.cert, "-key", pe.key, "-alpn", "mkq", "-stdin")
	got2 := runMkClient(t, mkExe, fmt.Sprintf("127.0.0.1:%d", port2), pe.ca, "wrong-alpn", "alpn-mismatch", 0, 0, 0, 0)
	_ = stopPeerProc(t, srv2)
	if got2 != "OK alpn-rejected" {
		t.Fatalf("alpn mismatch got %q", got2)
	}

	// Server-initiated close (-close-after 1 -close-code 9).
	srv3, port3 := startPeerProc(t, pe.quicpeer, "server", "-impl", "xnet", "-cert", pe.cert, "-key", pe.key, "-alpn", "mkq", "-close-after", "1", "-close-code", "9", "-stdin")
	got3 := runMkClient(t, mkExe, fmt.Sprintf("127.0.0.1:%d", port3), pe.ca, "mkq", "wait-close", 0, 0, 0, 0)
	_ = stopPeerProc(t, srv3)
	if got3 != "OK wait-close" {
		t.Fatalf("wait-close got %q", got3)
	}
}

func TestStdQuic_PeerClient_QuicGo(t *testing.T) {
	pe := loadPeerEnv(t)
	mkExe := buildMkQuicPeer(t)
	qlog := filepath.Join(t.TempDir(), "quicgo.qlog")

	srv, port := startPeerProc(t, pe.quicpeer, "server", "-impl", "quicgo", "-cert", pe.cert, "-key", pe.key, "-alpn", "mkq", "-events", qlog, "-stdin")
	got := runMkClient(t, mkExe, fmt.Sprintf("127.0.0.1:%d", port), pe.ca, "mkq", "all", 1048576, 100, 4096, 7)
	time.Sleep(100 * time.Millisecond)
	lines := stopPeerProc(t, srv)
	if got != "OK all" {
		t.Fatalf("got %q, lines=%v", got, lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"kind":"application"`) || !strings.Contains(joined, `"code":7`) {
		t.Fatalf("quicgo server did not observe close code 7:\n%s", joined)
	}
	qb, err := os.ReadFile(qlog)
	if err != nil || !strings.Contains(string(qb), "parameters_set") {
		t.Fatalf("missing quicgo parameters_set qlog (%v, len=%d)", err, len(qb))
	}
}

func TestStdQuic_PeerClient_UdpProxy(t *testing.T) {
	pe := loadPeerEnv(t)
	mkExe := buildMkQuicPeer(t)

	// 5% loss, 3% reorder, 1% dup against xnet server.
	srv, srvPort := startPeerProc(t, pe.quicpeer, "server", "-impl", "xnet", "-cert", pe.cert, "-key", pe.key, "-alpn", "mkq", "-stdin")
	proxy, proxyPort := startPeerProc(t, pe.udpproxy, "-server", fmt.Sprintf("127.0.0.1:%d", srvPort), "-seed", "42", "-loss", "5", "-reorder", "3", "-dup", "1", "-stdin")
	got := runMkClient(t, mkExe, fmt.Sprintf("127.0.0.1:%d", proxyPort), pe.ca, "mkq", "echo-only", 262144, 1, 0, 7)
	pLines := stopPeerProc(t, proxy)
	_ = stopPeerProc(t, srv)
	if got != "OK echo" {
		t.Fatalf("udpproxy loss/reorder/dup got %q, proxy=%v", got, pLines)
	}

	// NAT rebind (-rebind-after 25) against quicgo server.
	srv2, srvPort2 := startPeerProc(t, pe.quicpeer, "server", "-impl", "quicgo", "-cert", pe.cert, "-key", pe.key, "-alpn", "mkq", "-stdin")
	proxy2, proxyPort2 := startPeerProc(t, pe.udpproxy, "-server", fmt.Sprintf("127.0.0.1:%d", srvPort2), "-rebind-after", "25", "-stdin")
	got2 := runMkClient(t, mkExe, fmt.Sprintf("127.0.0.1:%d", proxyPort2), pe.ca, "mkq", "echo-only", 262144, 1, 0, 7)
	p2Lines := stopPeerProc(t, proxy2)
	_ = stopPeerProc(t, srv2)
	if got2 != "OK echo" || !strings.Contains(strings.Join(p2Lines, "\n"), `"event":"rebind"`) {
		t.Fatalf("udpproxy rebind got %q, proxy=%v", got2, p2Lines)
	}
	_ = sha256.Size
}

func TestStdQuic_PeerServer_XNet(t *testing.T) {
	pe := loadPeerEnv(t)
	mkExe := buildMkQuicPeer(t)
	runPeerClientSide(t, pe, "xnet", mkExe, 100000)
}

func TestStdQuic_PeerServer_QuicGo(t *testing.T) {
	pe := loadPeerEnv(t)
	mkExe := buildMkQuicPeer(t)
	runPeerClientSide(t, pe, "quicgo", mkExe, 100000)
}

func TestStdQuic_PeerServer_Retry(t *testing.T) {
	pe := loadPeerEnv(t)
	mkExe := buildMkQuicPeer(t)
	runPeerClientSide(t, pe, "quicgo", mkExe, 0)
}
