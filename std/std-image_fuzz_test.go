// Why: image.DecodePng takes untrusted PNG bytes and must never panic, hang,
// or violate its DecodeLimits.
// What: FuzzStdImage_DecodePng (listed in tools/gate/safety.go).
// How: runs a long-lived release-built Mk oracle over stdin/stdout framing
// ([4-byte LE length][bytes] -> 1-line verdict), seeded with valid PNGs and
// edge cases; skips if LLVM is absent.

package std

import (
	"bufio"
	"bytes"
	"encoding/binary"
	goimg "image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const imageFuzzOracle = `import std.image

Word4(b: []u8): int {
    return int(b[0]) | (int(b[1]) << 8) | (int(b[2]) << 16) | (int(b[3]) << 24)
}

export Main(sys: &System): WriteError|ReadError|OutOfMemory!void {
    var inp := sys.Stdin()
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var lim := image.DefaultDecodeLimits
    lim.max_width = 256
    lim.max_height = 256
    lim.max_bytes = 262144
    var hdr: [4]u8 = [0; 4]
    for {
        got := try inp.ReadAll(mut hdr[..])
        if got < 4 {
            return
        }
        n := Word4(hdr[..])
        if n < 0 || n > 1048576 {
            try Print(mut out, "badlen\n")
            try out.Flush()
            return
        }
        buf := try ar.Slice[u8](n)
        _ = try inp.ReadAll(buf)
        _ = image.DecodePng(&ar, buf[..], lim) else e {
            match e {
                image.Invalid(why: _) { try Print(mut out, "invalid\n") }
                image.Unsupported { try Print(mut out, "unsupported\n") }
                image.TooLarge { try Print(mut out, "toolarge\n") }
                OutOfMemory { try Print(mut out, "oom\n") }
                _ { try Print(mut out, "other\n") }
            }
            try out.Flush()
            ar.Reset()
            continue
        }
        try Print(mut out, "ok\n")
        try out.Flush()
        ar.Reset()
    }
}
`

type imgOracle struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	exe string
}

var imgFuzzOracle imgOracle

func (o *imgOracle) ensureExe(t testing.TB) string {
	t.Helper()
	if o.exe != "" {
		return o.exe
	}
	root, err := os.MkdirTemp("", "mk-imgfuzz-*")
	if err != nil {
		t.Fatal(err)
	}
	stdDir := copyStd(t, root, nil)
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(imageFuzzOracle), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	exePath := filepath.Join(root, "imgoracle.exe")
	out, status := mkCmd(t, root, stdDir, "build", "--release", "-o", exePath, "main.mk")
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	if status != 0 {
		t.Fatalf("build image fuzz oracle (exit %d):\n%s", status, out)
	}
	o.exe = exePath
	return o.exe
}

func (o *imgOracle) check(t testing.TB, data []byte) {
	t.Helper()
	if len(data) > 1048576 {
		data = data[:1048576]
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cmd == nil {
		exe := o.ensureExe(t)
		cmd := exec.Command(exe)
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		o.cmd = cmd
		o.in = in
		o.out = bufio.NewReader(out)
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := o.in.Write(hdr[:]); err != nil {
		o.reset()
		t.Fatalf("write len: %v", err)
	}
	if _, err := o.in.Write(data); err != nil {
		o.reset()
		t.Fatalf("write data: %v", err)
	}
	line, err := o.out.ReadString('\n')
	if err != nil {
		o.reset()
		t.Fatalf("oracle crashed or exited (%v) on %d bytes", err, len(data))
	}
	switch strings.TrimSpace(line) {
	case "ok", "invalid", "unsupported", "toolarge", "oom":
	default:
		t.Fatalf("unexpected oracle verdict %q", line)
	}
}

func (o *imgOracle) reset() {
	if o.in != nil {
		_ = o.in.Close()
	}
	if o.cmd != nil {
		_ = o.cmd.Process.Kill()
		_ = o.cmd.Wait()
	}
	o.cmd = nil
	o.in = nil
	o.out = nil
}

func seedPng(t testing.TB, rgba bool) []byte {
	t.Helper()
	var img goimg.Image
	if rgba {
		m := goimg.NewNRGBA(goimg.Rect(0, 0, 4, 3))
		m.SetNRGBA(1, 1, color.NRGBA{R: 10, G: 20, B: 30, A: 200})
		img = m
	} else {
		m := goimg.NewGray(goimg.Rect(0, 0, 3, 2))
		m.SetGray(0, 0, color.Gray{Y: 77})
		img = m
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// FuzzStdImage_DecodePng checks DecodePng never panics or hangs on arbitrary bytes.
func FuzzStdImage_DecodePng(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	f.Add(seedPng(f, false))
	f.Add(seedPng(f, true))
	f.Fuzz(func(t *testing.T, data []byte) {
		imgFuzzOracle.check(t, data)
	})
}
