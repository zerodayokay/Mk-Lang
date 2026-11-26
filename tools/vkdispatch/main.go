// Why: std.gpu.vk needs typed Vulkan instance and device dispatch tables
// whose fields match c.vulkan's PFN_vk* bindings and whose commands are
// classified and attributed directly from Khronos registry/vk.xml.
//
// What: command `vkdispatch` reading registry/vk.xml via encoding/xml and
// writing std/gpu/vk/dispatch.mk with GlobalDispatch, InstanceDispatch,
// DeviceDispatch, and Load*Dispatch helpers.
//
// How: parses <commands>, <feature>, and <extensions> from vk.xml, classifies
// each curated command by its first parameter's dispatchable handle type and
// core version or extension, and recases identifiers identically to cimport.

package main

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type cmdSpec struct {
	name     string
	optional bool
}

var wantedCommands = []cmdSpec{
	// Global / loader commands.
	{name: "vkEnumerateInstanceVersion"},
	{name: "vkEnumerateInstanceExtensionProperties"},
	{name: "vkEnumerateInstanceLayerProperties"},
	{name: "vkCreateInstance"},

	// Instance-level core 1.0 & 1.1 commands.
	{name: "vkDestroyInstance"},
	{name: "vkEnumeratePhysicalDevices"},
	{name: "vkGetPhysicalDeviceProperties"},
	{name: "vkGetPhysicalDeviceProperties2"},
	{name: "vkGetPhysicalDeviceFeatures"},
	{name: "vkGetPhysicalDeviceFeatures2"},
	{name: "vkGetPhysicalDeviceMemoryProperties"},
	{name: "vkGetPhysicalDeviceQueueFamilyProperties"},
	{name: "vkGetPhysicalDeviceFormatProperties"},
	{name: "vkEnumerateDeviceExtensionProperties"},
	{name: "vkCreateDevice"},

	// Instance-level surface & debug extensions.
	{name: "vkDestroySurfaceKHR", optional: true},
	{name: "vkGetPhysicalDeviceSurfaceSupportKHR", optional: true},
	{name: "vkGetPhysicalDeviceSurfaceCapabilitiesKHR", optional: true},
	{name: "vkGetPhysicalDeviceSurfaceFormatsKHR", optional: true},
	{name: "vkGetPhysicalDeviceSurfacePresentModesKHR", optional: true},
	{name: "vkCreateDebugUtilsMessengerEXT", optional: true},
	{name: "vkDestroyDebugUtilsMessengerEXT", optional: true},

	// Device-level core 1.0, 1.1, 1.2, 1.3 commands.
	{name: "vkGetDeviceProcAddr"},
	{name: "vkDestroyDevice"},
	{name: "vkGetDeviceQueue"},
	{name: "vkDeviceWaitIdle"},
	{name: "vkQueueWaitIdle"},
	{name: "vkQueueSubmit2"},
	{name: "vkAllocateMemory"},
	{name: "vkFreeMemory"},
	{name: "vkMapMemory"},
	{name: "vkUnmapMemory"},
	{name: "vkFlushMappedMemoryRanges"},
	{name: "vkInvalidateMappedMemoryRanges"},
	{name: "vkCreateBuffer"},
	{name: "vkDestroyBuffer"},
	{name: "vkGetBufferMemoryRequirements"},
	{name: "vkGetBufferMemoryRequirements2"},
	{name: "vkBindBufferMemory"},
	{name: "vkCreateImage"},
	{name: "vkDestroyImage"},
	{name: "vkGetImageMemoryRequirements"},
	{name: "vkGetImageMemoryRequirements2"},
	{name: "vkBindImageMemory"},
	{name: "vkCreateImageView"},
	{name: "vkDestroyImageView"},
	{name: "vkCreateSampler"},
	{name: "vkDestroySampler"},
	{name: "vkCreateSemaphore"},
	{name: "vkDestroySemaphore"},
	{name: "vkGetSemaphoreCounterValue"},
	{name: "vkWaitSemaphores"},
	{name: "vkSignalSemaphore"},
	{name: "vkCreateCommandPool"},
	{name: "vkDestroyCommandPool"},
	{name: "vkResetCommandPool"},
	{name: "vkAllocateCommandBuffers"},
	{name: "vkFreeCommandBuffers"},
	{name: "vkResetCommandBuffer"},
	{name: "vkBeginCommandBuffer"},
	{name: "vkEndCommandBuffer"},
	{name: "vkCreateShaderModule"},
	{name: "vkDestroyShaderModule"},
	{name: "vkCreateDescriptorSetLayout"},
	{name: "vkDestroyDescriptorSetLayout"},
	{name: "vkCreatePipelineLayout"},
	{name: "vkDestroyPipelineLayout"},
	{name: "vkCreateDescriptorPool"},
	{name: "vkDestroyDescriptorPool"},
	{name: "vkAllocateDescriptorSets"},
	{name: "vkFreeDescriptorSets"},
	{name: "vkUpdateDescriptorSets"},
	{name: "vkCreateGraphicsPipelines"},
	{name: "vkCreateComputePipelines"},
	{name: "vkDestroyPipeline"},
	{name: "vkCmdPipelineBarrier2"},
	{name: "vkCmdBeginRendering"},
	{name: "vkCmdEndRendering"},
	{name: "vkCmdBindPipeline"},
	{name: "vkCmdBindDescriptorSets"},
	{name: "vkCmdBindVertexBuffers"},
	{name: "vkCmdBindIndexBuffer"},
	{name: "vkCmdSetViewport"},
	{name: "vkCmdSetScissor"},
	{name: "vkCmdSetBlendConstants"},
	{name: "vkCmdSetStencilReference"},
	{name: "vkCmdPushConstants"},
	{name: "vkCmdDraw"},
	{name: "vkCmdDrawIndexed"},
	{name: "vkCmdDispatch"},
	{name: "vkCmdCopyBuffer"},
	{name: "vkCmdCopyBufferToImage"},
	{name: "vkCmdCopyImageToBuffer"},
	{name: "vkCmdCopyImage"},
	{name: "vkCmdFillBuffer"},
	{name: "vkCmdClearColorImage"},
	{name: "vkCmdClearDepthStencilImage"},

	// Device-level swapchain & debug extensions.
	{name: "vkCreateSwapchainKHR", optional: true},
	{name: "vkDestroySwapchainKHR", optional: true},
	{name: "vkGetSwapchainImagesKHR", optional: true},
	{name: "vkAcquireNextImageKHR", optional: true},
	{name: "vkQueuePresentKHR", optional: true},
	{name: "vkSetDebugUtilsObjectNameEXT", optional: true},
	{name: "vkCmdBeginDebugUtilsLabelEXT", optional: true},
	{name: "vkCmdEndDebugUtilsLabelEXT", optional: true},
}

type xmlRegistry struct {
	Commands   xmlCommands     `xml:"commands"`
	Features   []xmlFeature    `xml:"feature"`
	Extensions []xmlExtensions `xml:"extensions"`
}

type xmlCommands struct {
	List []xmlCommand `xml:"command"`
}

type xmlCommand struct {
	API   string     `xml:"api,attr"`
	Name  string     `xml:"name,attr"`
	Alias string     `xml:"alias,attr"`
	Proto xmlProto   `xml:"proto"`
	Param []xmlParam `xml:"param"`
}

type xmlProto struct {
	Name string `xml:"name"`
}

type xmlParam struct {
	API  string `xml:"api,attr"`
	Type string `xml:"type"`
}

type xmlFeature struct {
	API     string       `xml:"api,attr"`
	Name    string       `xml:"name,attr"`
	Number  string       `xml:"number,attr"`
	Require []xmlRequire `xml:"require"`
}

type xmlExtensions struct {
	List []xmlExtension `xml:"extension"`
}

type xmlExtension struct {
	Name      string       `xml:"name,attr"`
	Supported string       `xml:"supported,attr"`
	Require   []xmlRequire `xml:"require"`
}

type xmlRequire struct {
	API      string       `xml:"api,attr"`
	Commands []xmlReqName `xml:"command"`
}

type xmlReqName struct {
	Name string `xml:"name,attr"`
}

func hasVulkanAPI(api string) bool {
	if api == "" {
		return true
	}
	return slices.Contains(strings.Split(api, ","), "vulkan")
}

// splitWords mirrors internal/cimport/names.go splitWords so generated Mk
// type and field names match c.vulkan identically.
func splitWords(name string) []string {
	var words []string
	cur := []byte{}
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = []byte{}
		}
	}
	isUpper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	isLower := func(c byte) bool { return c >= 'a' && c <= 'z' }
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' {
			flush()
			continue
		}
		if isUpper(c) && len(cur) > 0 {
			prev := cur[len(cur)-1]
			nextLower := i+1 < len(name) && isLower(name[i+1])
			if isLower(prev) || isUpper(prev) && nextLower {
				flush()
			}
		}
		cur = append(cur, c)
	}
	flush()
	return words
}

func pascal(name string) string {
	var b strings.Builder
	for _, w := range splitWords(name) {
		allCaps := true
		for i := 0; i < len(w); i++ {
			if w[i] >= 'a' && w[i] <= 'z' {
				allCaps = false
			}
		}
		for i := 0; i < len(w); i++ {
			c := w[i]
			switch {
			case i == 0 && c >= 'a' && c <= 'z':
				c -= 'a' - 'A'
			case i > 0 && allCaps && c >= 'A' && c <= 'Z':
				c += 'a' - 'A'
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

func snake(name string) string {
	var parts []string
	for _, w := range splitWords(name) {
		parts = append(parts, strings.ToLower(w))
	}
	return strings.Join(parts, "_")
}

type cmdInfo struct {
	name     string
	field    string
	pfnType  string
	level    string // "global", "instance", or "device"
	source   string // e.g. "Vulkan 1.0" or "VK_KHR_swapchain"
	optional bool
}

func classifyCommand(name, firstParam string) string {
	if name == "vkGetInstanceProcAddr" || name == "vkCreateInstance" || strings.HasPrefix(name, "vkEnumerateInstance") {
		return "global"
	}
	if name == "vkGetDeviceProcAddr" {
		return "device"
	}
	switch firstParam {
	case "VkInstance", "VkPhysicalDevice":
		return "instance"
	case "VkDevice", "VkQueue", "VkCommandBuffer":
		return "device"
	default:
		return "global"
	}
}

func versionRank(v string) int {
	switch v {
	case "1.0":
		return 10
	case "1.1":
		return 11
	case "1.2":
		return 12
	case "1.3":
		return 13
	case "1.4":
		return 14
	default:
		return 99
	}
}

func generateDispatch(xmlPath string) ([]byte, error) {
	raw, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, err
	}
	var reg xmlRegistry
	if err := xml.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", xmlPath, err)
	}

	firstParam := map[string]string{}
	aliases := map[string]string{}
	for _, c := range reg.Commands.List {
		if !hasVulkanAPI(c.API) {
			continue
		}
		if c.Alias != "" && c.Name != "" {
			aliases[c.Name] = c.Alias
			continue
		}
		if c.Proto.Name == "" {
			continue
		}
		fp := ""
		for _, p := range c.Param {
			if hasVulkanAPI(p.API) {
				fp = p.Type
				break
			}
		}
		firstParam[c.Proto.Name] = fp
	}
	for name, target := range aliases {
		if fp, ok := firstParam[target]; ok {
			firstParam[name] = fp
		}
	}

	coreVer := map[string]string{}
	for _, f := range reg.Features {
		if !hasVulkanAPI(f.API) {
			continue
		}
		for _, req := range f.Require {
			if !hasVulkanAPI(req.API) {
				continue
			}
			for _, c := range req.Commands {
				prev, ok := coreVer[c.Name]
				if !ok || versionRank(f.Number) < versionRank(prev) {
					coreVer[c.Name] = f.Number
				}
			}
		}
	}

	extName := map[string]string{}
	for _, extGroup := range reg.Extensions {
		for _, ext := range extGroup.List {
			if ext.Supported != "" && !hasVulkanAPI(ext.Supported) {
				continue
			}
			for _, req := range ext.Require {
				if !hasVulkanAPI(req.API) {
					continue
				}
				for _, c := range req.Commands {
					if _, isCore := coreVer[c.Name]; !isCore {
						if _, seen := extName[c.Name]; !seen {
							extName[c.Name] = ext.Name
						}
					}
				}
			}
		}
	}

	var globals, instances, devices []cmdInfo
	for _, spec := range wantedCommands {
		fp, ok := firstParam[spec.name]
		if !ok {
			return nil, fmt.Errorf("command %q not found in %s", spec.name, xmlPath)
		}
		src := ""
		if v, ok := coreVer[spec.name]; ok && versionRank(v) <= 13 {
			src = "Vulkan " + v
		} else if ext, ok := extName[spec.name]; ok {
			src = ext
		} else if v, ok := coreVer[spec.name]; ok {
			src = "Vulkan " + v
		} else {
			return nil, fmt.Errorf("command %q has no feature/extension in %s", spec.name, xmlPath)
		}
		info := cmdInfo{
			name:     spec.name,
			field:    snake(spec.name),
			pfnType:  "vulkan." + pascal("PFN_"+spec.name),
			level:    classifyCommand(spec.name, fp),
			source:   src,
			optional: spec.optional,
		}
		switch info.level {
		case "global":
			globals = append(globals, info)
		case "instance":
			instances = append(instances, info)
		case "device":
			devices = append(devices, info)
		}
	}

	var b bytes.Buffer
	b.WriteString("// Why: std.gpu.vk calls Vulkan 1.3 and WSI entry points through typed\n")
	b.WriteString("// instance and device dispatch tables without hardcoding signatures.\n")
	b.WriteString("//\n")
	b.WriteString("// What: GlobalDispatch, InstanceDispatch, DeviceDispatch, and their\n")
	b.WriteString("// vkGetInstanceProcAddr / vkGetDeviceProcAddr loaders. Generated by\n")
	b.WriteString("// tools/vkdispatch from Khronos registry/vk.xml; do not edit by hand.\n")
	b.WriteString("//\n")
	b.WriteString("// How: each field uses c.vulkan's PFN_vk* type; required core entries\n")
	b.WriteString("// fail loading when null, while extension entries depend on flags.\n\n")
	b.WriteString("import c.vulkan\n")
	b.WriteString("import std.mem\n")
	b.WriteString("import std.raw\n\n")

	b.WriteString("LoadInstanceProcAddr(get_addr: vulkan.PfnVkGetInstanceProcAddr, inst: vulkan.VkInstance, name: str): uint {\n")
	b.WriteString("    match get_addr {\n")
	b.WriteString("        some(g) {\n")
	b.WriteString("            unsafe \"vkGetInstanceProcAddr resolves a NUL-terminated Vulkan entry point\" {\n")
	b.WriteString("                p := g(inst, raw.StrData(name))\n")
	b.WriteString("                match p {\n")
	b.WriteString("                    some(f) {\n")
	b.WriteString("                        var slot := f\n")
	b.WriteString("                        return raw.Read(raw.PtrCast[fn(), uint](raw.Of(&slot)))\n")
	b.WriteString("                    }\n")
	b.WriteString("                    none { return 0 }\n")
	b.WriteString("                }\n")
	b.WriteString("            }\n")
	b.WriteString("        }\n")
	b.WriteString("        none { return 0 }\n")
	b.WriteString("    }\n")
	b.WriteString("}\n\n")

	b.WriteString("LoadDeviceProcAddr(get_addr: vulkan.PfnVkGetDeviceProcAddr, dev: vulkan.VkDevice, name: str): uint {\n")
	b.WriteString("    match get_addr {\n")
	b.WriteString("        some(g) {\n")
	b.WriteString("            unsafe \"vkGetDeviceProcAddr resolves a NUL-terminated Vulkan device entry point\" {\n")
	b.WriteString("                p := g(dev, raw.StrData(name))\n")
	b.WriteString("                match p {\n")
	b.WriteString("                    some(f) {\n")
	b.WriteString("                        var slot := f\n")
	b.WriteString("                        return raw.Read(raw.PtrCast[fn(), uint](raw.Of(&slot)))\n")
	b.WriteString("                    }\n")
	b.WriteString("                    none { return 0 }\n")
	b.WriteString("                }\n")
	b.WriteString("            }\n")
	b.WriteString("        }\n")
	b.WriteString("        none { return 0 }\n")
	b.WriteString("    }\n")
	b.WriteString("}\n\n")

	writeStruct := func(name string, cmds []cmdInfo) {
		fmt.Fprintf(&b, "struct %s {\n", name)
		for _, c := range cmds {
			fmt.Fprintf(&b, "    %s: %s // %s\n", c.field, c.pfnType, c.source)
		}
		b.WriteString("}\n\n")
	}

	writeStruct("GlobalDispatch", globals)
	writeStruct("InstanceDispatch", instances)
	writeStruct("DeviceDispatch", devices)

	b.WriteString("LoadGlobalDispatch(get_addr: vulkan.PfnVkGetInstanceProcAddr): ?GlobalDispatch {\n")
	b.WriteString("    var g := mem.Zeroed[GlobalDispatch]()\n")
	b.WriteString("    null_inst := raw.Null[vulkan.VkInstanceT]()\n")
	b.WriteString("    unsafe \"vkGetInstanceProcAddr addresses match c.vulkan PFN_vk* signatures\" {\n")
	for _, c := range globals {
		fmt.Fprintf(&b, "        g.%s = raw.FnFromAddr[%s](LoadInstanceProcAddr(get_addr, null_inst, \"%s\\x00\"))\n", c.field, c.pfnType, c.name)
		if c.name == "vkCreateInstance" {
			fmt.Fprintf(&b, "        if g.%s.IsNone() { return none }\n", c.field)
		}
	}
	b.WriteString("    }\n")
	b.WriteString("    return some(g)\n")
	b.WriteString("}\n\n")

	b.WriteString("LoadInstanceDispatch(get_addr: vulkan.PfnVkGetInstanceProcAddr, inst: vulkan.VkInstance, has_surface: bool, has_debug: bool): ?InstanceDispatch {\n")
	b.WriteString("    var d := mem.Zeroed[InstanceDispatch]()\n")
	b.WriteString("    unsafe \"vkGetInstanceProcAddr addresses match c.vulkan PFN_vk* signatures\" {\n")
	for _, c := range instances {
		cond := ""
		reqCheck := true
		switch c.source {
		case "VK_KHR_surface":
			cond = "if has_surface"
			reqCheck = true
		case "VK_EXT_debug_utils":
			cond = "if has_debug"
			reqCheck = false
		}
		if cond != "" {
			fmt.Fprintf(&b, "        %s {\n", cond)
			fmt.Fprintf(&b, "            d.%s = raw.FnFromAddr[%s](LoadInstanceProcAddr(get_addr, inst, \"%s\\x00\"))\n", c.field, c.pfnType, c.name)
			if reqCheck {
				fmt.Fprintf(&b, "            if d.%s.IsNone() { return none }\n", c.field)
			}
			b.WriteString("        }\n")
		} else {
			fmt.Fprintf(&b, "        d.%s = raw.FnFromAddr[%s](LoadInstanceProcAddr(get_addr, inst, \"%s\\x00\"))\n", c.field, c.pfnType, c.name)
			fmt.Fprintf(&b, "            if d.%s.IsNone() { return none }\n", c.field)
		}
	}
	b.WriteString("    }\n")
	b.WriteString("    return some(d)\n")
	b.WriteString("}\n\n")

	b.WriteString("LoadDeviceDispatch(get_addr: vulkan.PfnVkGetInstanceProcAddr, inst: vulkan.VkInstance, dev: vulkan.VkDevice, has_swapchain: bool, has_debug: bool): ?DeviceDispatch {\n")
	b.WriteString("    var d := mem.Zeroed[DeviceDispatch]()\n")
	b.WriteString("    unsafe \"vkGetDeviceProcAddr addresses match c.vulkan PFN_vk* signatures\" {\n")
	b.WriteString("        d.vk_get_device_proc_addr = raw.FnFromAddr[vulkan.PfnVkGetDeviceProcAddr](LoadInstanceProcAddr(get_addr, inst, \"vkGetDeviceProcAddr\\x00\"))\n")
	b.WriteString("        if d.vk_get_device_proc_addr.IsNone() { return none }\n")
	b.WriteString("        gd := d.vk_get_device_proc_addr\n")
	for _, c := range devices {
		if c.name == "vkGetDeviceProcAddr" {
			continue
		}
		cond := ""
		reqCheck := true
		switch c.source {
		case "VK_KHR_swapchain":
			cond = "if has_swapchain"
			reqCheck = true
		case "VK_EXT_debug_utils":
			cond = "if has_debug"
			reqCheck = false
		}
		if cond != "" {
			fmt.Fprintf(&b, "        %s {\n", cond)
			fmt.Fprintf(&b, "            d.%s = raw.FnFromAddr[%s](LoadDeviceProcAddr(gd, dev, \"%s\\x00\"))\n", c.field, c.pfnType, c.name)
			if reqCheck {
				fmt.Fprintf(&b, "            if d.%s.IsNone() { return none }\n", c.field)
			}
			b.WriteString("        }\n")
		} else {
			fmt.Fprintf(&b, "        d.%s = raw.FnFromAddr[%s](LoadDeviceProcAddr(gd, dev, \"%s\\x00\"))\n", c.field, c.pfnType, c.name)
			fmt.Fprintf(&b, "        if d.%s.IsNone() { return none }\n", c.field)
		}
	}
	b.WriteString("    }\n")
	b.WriteString("    return some(d)\n")
	b.WriteString("}\n")

	return b.Bytes(), nil
}

func defaultXMLPath() string {
	if dir := os.Getenv("MK_VULKAN_HEADERS"); dir != "" {
		return filepath.Join(dir, "registry", "vk.xml")
	}
	return `V:\Mk-wt\_run\tools\vulkan-headers\Vulkan-Headers-vulkan-sdk-1.4.363.0\registry\vk.xml`
}

func main() {
	xmlPath := flag.String("xml", defaultXMLPath(), "path to Khronos registry/vk.xml")
	outPath := flag.String("out", filepath.Join("std", "gpu", "vk", "dispatch.mk"), "output Mk file path")
	flag.Parse()

	src, err := generateDispatch(*xmlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vkdispatch: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "vkdispatch: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, src, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "vkdispatch: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("vkdispatch: wrote %s (%d bytes)\n", *outPath, len(src))
}
