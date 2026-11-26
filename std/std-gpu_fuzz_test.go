// Why: gpu.CheckShader parses untrusted SPIR-V and DXIL bytecode and must never
// panic, hang, or overflow bounded scratch tables.
// What: FuzzStdGpu_Spirv and FuzzStdGpu_Dxil (untrusted-input fuzz targets
// listed in tools/gate/safety.go).
// How: runs a release-built Mk oracle over stdin/stdout framing
// ([1-byte mode][4-byte LE length][bytes] -> 1-line verdict), seeded with valid
// and malformed headers; skips if LLVM is absent.

package std

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const gpuFuzzOracle = `import std.gpu

Word4(b: []u8): int {
    return int(b[0]) | (int(b[1]) << 8) | (int(b[2]) << 16) | (int(b[3]) << 24)
}

const MinSpirv: [40]u8 = [
    0x03, 0x02, 0x23, 0x07,
    0x00, 0x00, 0x01, 0x00,
    0x00, 0x00, 0x00, 0x00,
    0x02, 0x00, 0x00, 0x00,
    0x00, 0x00, 0x00, 0x00,
    0x0F, 0x00, 0x05, 0x00,
    0x00, 0x00, 0x00, 0x00,
    0x01, 0x00, 0x00, 0x00,
    0x56, 0x73, 0x4D, 0x61,
    0x69, 0x6E, 0x00, 0x00,
]

export Main(sys: &System): WriteError|ReadError|OutOfMemory!void {
    var inp := sys.Stdin()
    var out := sys.Stdout()
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var hdr: [5]u8 = [0; 5]
    for {
        got := try inp.ReadAll(mut hdr[..])
        if got < 5 {
            return
        }
        mode := hdr[0]
        n := Word4(hdr[1..5])
        if n < 0 || n > 1048576 {
            try Print(mut out, "badlen\n")
            try out.Flush()
            return
        }
        buf := try ar.Slice[u8](n)
        _ = try inp.ReadAll(buf)
        if mode == 68 {
            _ = gpu.CheckShader(gpu.ShaderCode(spirv: MinSpirv[..], dxil: buf[..]), gpu.Stage.Vertex, "VsMain") else e {
                match e {
                    gpu.Invalid(misuse: _, at: _) { try Print(mut out, "invalid\n") }
                    _ { try Print(mut out, "other\n") }
                }
                try out.Flush()
                ar.Reset()
                continue
            }
        } else {
            _ = gpu.CheckShader(gpu.ShaderCode(spirv: buf[..], dxil: "".AsBytes()), gpu.Stage.Vertex, "VsMain") else e {
                match e {
                    gpu.Invalid(misuse: _, at: _) { try Print(mut out, "invalid\n") }
                    _ { try Print(mut out, "other\n") }
                }
                try out.Flush()
                ar.Reset()
                continue
            }
        }
        try Print(mut out, "ok\n")
        try out.Flush()
        ar.Reset()
    }
}
`

type gpuOracle struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	exe string
}

var gpuFuzzOracleProc gpuOracle

func (o *gpuOracle) ensureExe(t testing.TB) string {
	t.Helper()
	if o.exe != "" {
		return o.exe
	}
	root, err := os.MkdirTemp("", "mk-gpufuzz-*")
	if err != nil {
		t.Fatal(err)
	}
	stdDir := copyStd(t, root, nil)
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(gpuFuzzOracle), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	exePath := filepath.Join(root, "gpuoracle.exe")
	out, status := mkCmd(t, root, stdDir, "build", "--release", "-o", exePath, "main.mk")
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	if status != 0 {
		t.Fatalf("build gpu fuzz oracle (exit %d):\n%s", status, out)
	}
	o.exe = exePath
	return o.exe
}

func (o *gpuOracle) check(t testing.TB, mode byte, data []byte) {
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
	var hdr [5]byte
	hdr[0] = mode
	binary.LittleEndian.PutUint32(hdr[1:5], uint32(len(data)))
	if _, err := o.in.Write(hdr[:]); err != nil {
		o.reset()
		t.Fatalf("write hdr: %v", err)
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
	case "ok", "invalid":
	default:
		t.Fatalf("unexpected oracle verdict %q", line)
	}
}

func (o *gpuOracle) reset() {
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

func seedMinSpirv() []byte {
	b := make([]byte, 40)
	binary.LittleEndian.PutUint32(b[0:4], 0x07230203)
	binary.LittleEndian.PutUint32(b[4:8], 0x00010600)
	binary.LittleEndian.PutUint32(b[12:16], 2)
	binary.LittleEndian.PutUint32(b[20:24], (5<<16)|15)
	binary.LittleEndian.PutUint32(b[28:32], 1)
	copy(b[32:40], "VsMain\x00\x00")
	return b
}

func seedMinDxbc() []byte {
	b := make([]byte, 64)
	copy(b[0:4], "DXBC")
	b[4] = 1
	binary.LittleEndian.PutUint32(b[20:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], 64)
	binary.LittleEndian.PutUint32(b[28:32], 1)
	binary.LittleEndian.PutUint32(b[32:36], 36)
	copy(b[36:40], "DXIL")
	binary.LittleEndian.PutUint32(b[40:44], 20)
	binary.LittleEndian.PutUint32(b[44:48], 0x00010066)
	binary.LittleEndian.PutUint32(b[48:52], 5)
	copy(b[52:56], "DXIL")
	return b
}

// FuzzStdGpu_Spirv checks SPIR-V CheckShader never panics or hangs on arbitrary bytes.
func FuzzStdGpu_Spirv(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x03, 0x02, 0x23, 0x07})
	f.Add(seedMinSpirv())
	f.Fuzz(func(t *testing.T, data []byte) {
		gpuFuzzOracleProc.check(t, 'S', data)
	})
}

// FuzzStdGpu_Dxil checks DXIL CheckDxil (via CheckShader) never panics or hangs on arbitrary bytes.
func FuzzStdGpu_Dxil(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{'D', 'X', 'B', 'C'})
	f.Add(seedMinDxbc())
	f.Fuzz(func(t *testing.T, data []byte) {
		gpuFuzzOracleProc.check(t, 'D', data)
	})
}
