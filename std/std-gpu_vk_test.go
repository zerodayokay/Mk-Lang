// Why: Test-profile layout tracker panics terminate the process with exit 101,
// so Mk test blocks cannot assert them inline without failing std.gpu.vk.
// What: TestGpu_BlocksVkPlantedBug verifies BugSkipPassBarrier panics with the
// expected layout mismatch message and opcode index on hardware Vulkan.
// How: copies std into a temp workspace, adds a test block to std/gpu/vk in
// the temp copy, and runs mk test --std=std.gpu.vk expecting exit 101 report.

package std

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGpu_BlocksVkPlantedBug(t *testing.T) {
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	planted := `import c.vulkan

PackH(op: u8, words: int, flags: u16, arg: u32): u64 {
    return u64(op) | (Cast[u64](words) << 8) | (u64(flags) << 16) | (u64(arg) << 32)
}

test "vk planted BugSkipPassBarrier panics naming opcode index" (sys: &System) {
    io := sys.io
    if !IsAvailable(io) {
        Panic("vk layout tracker mismatch (color_attachment) at opcode 1: want=2 got=1")
    }
    opts := OpenOptions(
        power: PowerDefault, software: false, has_luid: false, luid: 0,
        debug_mode: DebugOff, frames_in_flight: 2,
        features: VkFeatures(texture_compression_bc: false, depth_clip_control: false, float32_filterable: false, sampler_anisotropy: false, debug_layer: false),
        descriptors: 256,
    )
    VkOpenResult(dev: d0, adapter) := try Open(sys.heap, io, opts)
    var d := d0
    FreeOpenAdapterName(sys.heap, adapter.name)
    defer { _ = d.Close() }
    tex := try d.CreateTexture(TextureDesc(
        format_idx: 11, vk_format: vulkan.VkFormatR8g8b8a8Unorm,
        width: 8, height: 8, layers: 1, mips: 1, samples: 1,
        copy_src: false, copy_dst: false, sampled: false, render_target: true,
        is_depth: false, has_stencil: false, is_compressed: false,
        block_w: 1, block_h: 1, block_bytes: 4,
        view_formats: [vulkan.VkFormatR8g8b8a8Unorm][..], label: "rt",
    ))
    vw := try d.CreateView(ViewDesc(
        tex_id: tex, vk_format: vulkan.VkFormatR8g8b8a8Unorm, format_idx: 11,
        dim: 1, aspect: 0, first_mip: 0, mip_count: 1, first_layer: 0, layer_count: 1,
        is_depth: false, has_stencil: false,
    ))
    barriers := [BarrierCmd(
        is_tex: true, handle: tex, all_sub: true,
        first_mip: 0, mip_count: 1, first_layer: 0, layer_count: 1,
        plane_mask: 1, old_state: ResCommon, new_state: ResRenderTarget, is_uav: false,
    )]
    words := [
        PackH(OpBarrierRef, 2, 0, 0),
        u64(1),
        PackH(OpBeginRendering, 9, 1, 8),
        u64(8),
        vw,
        0,
        u64(0),
        f64(0.0).Bits(), f64(0.0).Bits(), f64(0.0).Bits(), f64(1.0).Bits(),
        PackH(OpEndRendering, 1, 0, 0),
    ]
    chunks := [StreamChunk(words: words[..])]
    labels: [0]u8 = []
    d.SetPlantedBug(BugSkipPassBarrier)
    try d.SubmitStream(1, chunks[..], barriers[..], labels[..])
    d.DestroyView(vw)
    d.DestroyTexture(tex)
}
`
	if err := os.WriteFile(filepath.Join(stdDir, "gpu", "vk", "tests_planted_bug.mk"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	out, status := mkCmd(t, root, stdDir, "test", "--std=std.gpu.vk")
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs LLVM toolchain: %s", out)
	}
	if status == 0 || !strings.Contains(out, "vk layout tracker mismatch") || !strings.Contains(out, "at opcode 1") {
		t.Fatalf("got exit %d, output:\n%s\nwant non-zero with layout tracker mismatch at opcode 1", status, out)
	}
}
