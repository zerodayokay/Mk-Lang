// Why: std.gpu's portable test blocks must pass at dev and release and under
// both carrier counts, its device blocks on every adapter this host has, and
// its API must type-check on every target.
// What: TestGpu_Blocks, TestGpu_DeviceAdapters and TestGpu_CheckTargets.
// How: runs `mk test --std=std.gpu` over a copy of std in each mode (the
// device blocks then run on WARP), runs the device blocks again with
// MK_GPU_ADAPTER=d3d12 and =vulkan (each block skips when its adapter is
// absent), and runs `mk check --target=<t>` on a minimal caller.

package std

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var gpuSummary = regexp.MustCompile(`(?m)^(\d+) passed, (\d+) failed`)

func gpuBlocks(t *testing.T, carriers string, flags ...string) {
	t.Helper()
	gpuBlocksOn(t, "", carriers, flags...)
}

// gpuBlocksOn runs the std.gpu blocks with MK_GPU_ADAPTER set to adapter
// (unset when empty).
func gpuBlocksOn(t *testing.T, adapter, carriers string, flags ...string) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CARRIERS", carriers)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	t.Setenv("MK_GPU_ADAPTER", adapter)
	args := append([]string{"test", "--std=std.gpu"}, flags...)
	out, status := mkCmd(t, root, stdDir, args...)
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	m := gpuSummary.FindStringSubmatch(out)
	if status != 0 || m == nil || m[2] != "0" {
		t.Fatalf("mk %s (MK_CARRIERS=%s): exit %d\n%s", strings.Join(args, " "), carriers, status, out)
	}
	if m[1] == "0" {
		t.Fatalf("mk %s: no block ran\n%s", strings.Join(args, " "), out)
	}
	t.Logf("MK_GPU_ADAPTER=%q: %s", adapter, m[0])
}

// TestGpu_Blocks runs every std.gpu block on this host.
func TestGpu_Blocks(t *testing.T) {
	for _, prof := range []string{"dev", "release"} {
		for _, carriers := range []string{"1", "16"} {
			t.Run(prof+"/"+carriers, func(t *testing.T) {
				var flags []string
				if prof == "release" {
					flags = append(flags, "--release")
				}
				gpuBlocks(t, carriers, flags...)
			})
		}
	}
}

// TestGpu_DeviceAdapters runs the device blocks on the hardware adapters;
// TestGpu_Blocks already ran them on WARP.
func TestGpu_DeviceAdapters(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hardware adapter runs are x86_64-windows only")
	}
	for _, adapter := range []string{"d3d12", "vulkan"} {
		t.Run(adapter, func(t *testing.T) {
			gpuBlocksOn(t, adapter, "16", "--run=device")
		})
	}
}

// TestGpu_CheckTargets verifies std.gpu compiles on Windows and Linux targets.
func TestGpu_CheckTargets(t *testing.T) {
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	pkg := "const Package = pkg.Package(name: \"gpuchk\", mk: \"0.6\", bins: [pkg.Bin(name: \"app\", root: \"src\", subsystem: \"console\")])\n"
	main := "import std.gpu\nexport Main(sys: &System) {\n    _ = gpu.CheckShader(gpu.ShaderCode(spirv: \"\".AsBytes(), dxil: \"\".AsBytes()), gpu.Stage.Vertex, \"VsMain\") else {}\n    d := gpu.Open(sys.heap, sys.io, gpu.DefaultDeviceOptions) else { return }\n    _ = d.Close(sys.io)\n}\n"
	if err := os.WriteFile(filepath.Join(root, "pkg.mk"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.mk"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	for _, tgt := range []string{"x86_64-windows", "x86_64-linux"} {
		t.Run(tgt, func(t *testing.T) {
			out, status := mkCmd(t, root, stdDir, "check", "--target", tgt)
			if status != 0 {
				t.Fatalf("mk check --target %s failed (exit %d):\n%s", tgt, status, out)
			}
		})
	}
}
