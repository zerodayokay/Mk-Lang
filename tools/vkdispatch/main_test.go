// Why: std/gpu/vk/dispatch.mk must stay synchronized with registry/vk.xml
// and match c.vulkan's PFN_vk* bindings across machines.
//
// What: TestCommittedDispatchMatchesXml and TestDispatchTableStructure.
//
// How: verifies committed std/gpu/vk/dispatch.mk has expected structs/loaders
// and, when vk.xml is present, regenerates it and compares byte-for-byte.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func TestDispatchTableStructure(t *testing.T) {
	root := repoRoot(t)
	p := filepath.Join(root, "std", "gpu", "vk", "dispatch.mk")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(raw)
	for _, want := range []string{
		"struct GlobalDispatch",
		"struct InstanceDispatch",
		"struct DeviceDispatch",
		"LoadGlobalDispatch(",
		"LoadInstanceDispatch(",
		"LoadDeviceDispatch(",
		"vulkan.PfnVkQueueSubmit2",
		"vulkan.PfnVkCmdPipelineBarrier2",
		"vulkan.PfnVkCmdBeginRendering",
		"vulkan.PfnVkWaitSemaphores",
		"vulkan.PfnVkAcquireNextImageKhr",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("%s missing %q", p, want)
		}
	}
}

func TestCommittedDispatchMatchesXml(t *testing.T) {
	xmlPath := defaultXMLPath()
	if _, err := os.Stat(xmlPath); err != nil {
		t.Skipf("Vulkan-Headers vk.xml not present at %s", xmlPath)
	}
	got, err := generateDispatch(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	root := repoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, "std", "gpu", "vk", "dispatch.mk"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))) {
		t.Fatalf("std/gpu/vk/dispatch.mk differs from regenerated output (%d vs %d bytes)", len(want), len(got))
	}
}
