// Why: which system views exist, what each binds and which view owns a C
// declaration several of them include must be one fixed fact of mk, the
// same on every machine, or `windows.Hwnd` could be another type elsewhere.
//
// What: Row (one system view: name, targets, headers, defines, required
// views, the input it is generated from), Table (every view mk knows, in
// ownership order), Named, ForTarget and Input.
//
// How: the views of one target are generated as one translation unit, one
// import per row in table order; a declaration belongs to the first row
// whose headers reach it (the importer's rule), so windows comes first and
// HWND is always windows.Hwnd. A row lists the views its items may refer to;
// the generator checks the generated references against it.

package sysview

import "slices"

// Input is what a row is generated from.
type Input string

const (
	// InputWindowsSDK: the Windows SDK's um, shared and ucrt directories,
	// read with mk's toolset headers (internal/cimport msvch.go).
	InputWindowsSDK Input = "windows-sdk"
	// InputVulkanHeaders: KhronosGroup/Vulkan-Headers' include directory.
	InputVulkanHeaders Input = "vulkan-headers"
	// InputGlibc: a Linux target's glibc headers.
	InputGlibc Input = "glibc"
)

// Row is one system view.
type Row struct {
	Name    string
	Targets []string
	Headers []string
	// FallbackToFirst lets a target check against the first target's
	// declarations when its own archive has yet to be shipped.
	FallbackToFirst bool
	// Defines are #defines every target reads the headers with;
	// TargetDefines add a target's own (a platform's surface extension).
	Defines       []string
	TargetDefines map[string][]string
	// Requires are the views the row's items may refer to, earlier rows of
	// the same targets.
	Requires []string
	Input    Input
}

// windowsTargets are the targets the Windows SDK's views are generated for.
var windowsTargets = []string{"x86_64-windows"}

// Table is every system view, in ownership order.
var Table = []Row{
	{Name: "windows", Targets: windowsTargets, Headers: []string{"windows.h", "windowsx.h"}, Defines: []string{"INITGUID"}, Input: InputWindowsSDK},
	{Name: "dxgi", Targets: windowsTargets, Headers: []string{"dxgi1_6.h", "dxgidebug.h"}, Defines: []string{"INITGUID"}, Requires: []string{"windows"}, Input: InputWindowsSDK},
	{Name: "d3d12", Targets: windowsTargets, Headers: []string{"d3d12.h", "d3d12sdklayers.h"}, Defines: []string{"INITGUID"}, Requires: []string{"windows", "dxgi"}, Input: InputWindowsSDK},
	{Name: "d3dcompiler", Targets: windowsTargets, Headers: []string{"d3dcompiler.h"}, Defines: []string{"INITGUID"}, Requires: []string{"windows", "d3d12"}, Input: InputWindowsSDK},
	{Name: "posix", Targets: []string{"x86_64-linux", "aarch64-linux"},
		Headers: []string{"unistd.h", "fcntl.h", "sys/types.h", "sys/stat.h", "sys/mman.h", "sys/socket.h", "netinet/in.h",
			"arpa/inet.h", "poll.h", "pthread.h", "signal.h", "dirent.h", "errno.h", "time.h", "dlfcn.h", "stdio.h", "stdlib.h", "string.h"},
		Defines: []string{"_POSIX_C_SOURCE=200809L"}, Input: InputGlibc},
	{Name: "vulkan", Targets: []string{"x86_64-windows", "x86_64-linux", "aarch64-linux"}, Headers: []string{"vulkan/vulkan.h"},
		TargetDefines: map[string][]string{
			"x86_64-windows": {"VK_USE_PLATFORM_WIN32_KHR"},
			"x86_64-linux":   {"VK_USE_PLATFORM_XLIB_KHR", "VK_USE_PLATFORM_WAYLAND_KHR"},
			"aarch64-linux":  {"VK_USE_PLATFORM_XLIB_KHR", "VK_USE_PLATFORM_WAYLAND_KHR"},
		},
		Requires: []string{"windows", "posix"}, Input: InputVulkanHeaders, FallbackToFirst: true},
}

// Named is the row of view name, or false.
func Named(name string) (Row, bool) {
	for _, r := range Table {
		if r.Name == name {
			return r, true
		}
	}
	return Row{}, false
}

// ForTarget are the rows generated for target, in table order.
func ForTarget(target string) []Row {
	var out []Row
	for _, r := range Table {
		if slices.Contains(r.Targets, target) {
			out = append(out, r)
		}
	}
	return out
}

// DefinesFor are the row's defines for target.
func (r Row) DefinesFor(target string) []string {
	return append(slices.Clone(r.Defines), r.TargetDefines[target]...)
}

// RequiresFor are the row's required views that exist on target: on
// Linux, vulkan requires no windows view.
func (r Row) RequiresFor(target string) []string {
	var out []string
	for _, q := range r.Requires {
		if q, ok := Named(q); ok && slices.Contains(q.Targets, target) {
			out = append(out, q.Name)
		}
	}
	return out
}
