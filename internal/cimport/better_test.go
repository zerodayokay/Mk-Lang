// Why: P5 better bindings (constant globals, static consts, pointer macros,
// wide-string macros, COM methods, DECLARE_HANDLE naming, --why/--skipped)
// must produce deterministic, well-typed Mk output.
//
// What: TestCImport_StaticConstAndGuid, TestCImport_PointerAndWideMacros,
// TestCImport_ComMethods, TestCImport_HandleName, TestCImport_ExplainWhy.
//
// How: translates small C headers via clangFor/translate and verifies the
// generated Mk lines, Stats.SkippedDecls, and ExplainWhy answers.

package cimport

import (
	"slices"
	"strings"
	"testing"
)

func TestCImport_StaticConstAndGuid(t *testing.T) {
	hdr := `#include <stdint.h>
#define INITGUID 1
typedef struct _GUID {
    uint32_t Data1;
    uint16_t Data2;
    uint16_t Data3;
    uint8_t  Data4[8];
} GUID;
typedef GUID IID;
typedef GUID UUID;

enum VkFlagsEnum { VK_E0 = 1, VK_E1 = 4 };
static const enum VkFlagsEnum VK_ENUM_VAL = VK_E1;

typedef uint64_t VkFlags64;
typedef VkFlags64 VkPipelineStageFlagBits2;
static const VkPipelineStageFlagBits2 VK_PIPELINE_STAGE_2_NONE = 0ULL;
static const VkPipelineStageFlagBits2 VK_PIPELINE_STAGE_2_COPY_BIT = 0x100000000ULL;
static const VkPipelineStageFlagBits2 VK_PIPELINE_STAGE_2_COMBO = VK_PIPELINE_STAGE_2_COPY_BIT | 0x2ULL;

extern const IID IID_IDemo;
extern const IID IID_IDemo = { 0x189819f1, 0x1db6, 0x4b57, { 0xbe, 0x54, 0x18, 0x21, 0x33, 0x9b, 0x85, 0xf7 } };
static const UUID D3D12ExperimentalShaderModels = { 0x76f5573e, 0xf13a, 0x40f5, { 0xb2, 0x97, 0x81, 0xce, 0x9e, 0x18, 0x93, 0x3f } };
`
	res := translate(t, "x86_64-windows", map[string]string{"g.h": hdr}, Import{Name: "g", Headers: []string{"g.h"}})
	text := res.Modules[0]
	wantLines(t, text,
		"export struct Guid {",
		"export const VkEnumVal: VkFlagsEnum = 4",
		"export const VkPipelineStage2None: VkPipelineStageFlagBits2 = 0",
		"export const VkPipelineStage2CopyBit: VkPipelineStageFlagBits2 = 4294967296",
		"export const VkPipelineStage2Combo: VkPipelineStageFlagBits2 = 4294967298",
		"export const IidIDemo: Iid = Guid(data1: 412621297, data2: 7606, data3: 19287, data4: [190, 84, 24, 33, 51, 155, 133, 247])",
		"export const D3D12ExperimentalShaderModels: Uuid = Guid(data1: 1995790142, data2: 61754, data3: 16629, data4: [178, 151, 129, 206, 158, 24, 147, 63])",
	)
	if strings.Contains(text, "export extern var IID_IDemo") {
		t.Errorf("IID_IDemo should be bound as const, not extern var:\n%s", text)
	}
}

func TestCImport_PointerAndWideMacros(t *testing.T) {
	hdr := `typedef unsigned short wchar_t;
typedef void *HANDLE;
struct HWND__ { int unused; };
typedef struct HWND__ *HWND;
typedef const wchar_t *LPCWSTR;
#define INVALID_HANDLE_VALUE ((HANDLE)-1)
#define HWND_MESSAGE ((HWND)-3)
#define IDC_ARROW ((LPCWSTR)32512)
#define PTR_NULL ((void*)0)
#define PTR_STR ((const char*)"hello")
#define WIDE_HI L"hi"
#define WIDE_EMPTY L""
#define WIDE_SNOW L"a\u2603"
`
	res := translate(t, "x86_64-windows", map[string]string{"m.h": hdr}, Import{Name: "m", Headers: []string{"m.h"}})
	text := res.Modules[0]
	wantLines(t, text,
		"export struct HwndStruct {",
		"export type Hwnd = *HwndStruct",
		"export const InvalidHandleValue: uint = 18446744073709551615",
		"export const HwndMessage: uint = 18446744073709551613",
		"export const IdcArrow: uint = 32512",
		"export const PtrNull: uint = 0",
		"export const WideHi: []u16 = [104, 105, 0]",
		"export const WideEmpty: []u16 = [0]",
		"export const WideSnow: []u16 = [97, 9731, 0]",
	)
	if strings.Contains(text, "export const PtrStr:") {
		t.Errorf("PtrStr (relocatable string pointer) must not bind as uint const:\n%s", text)
	}
}

func TestCImport_ComMethods(t *testing.T) {
	hdr := `typedef struct IDemo IDemo;
typedef struct IDemoVtbl {
    int (*Query)(IDemo *This, int a, void **out);
    void (*Ping)(IDemo *This);
} IDemoVtbl;
struct IDemo {
    const struct IDemoVtbl *lpVtbl;
};
typedef struct INotCom {
    struct NotVtbl { int (*Fn)(int x); } *p;
} INotCom;
`
	res := translate(t, "x86_64-windows", map[string]string{"c.h": hdr}, Import{Name: "c", Headers: []string{"c.h"}})
	text := res.Modules[0]
	wantLines(t, text,
		"export struct IDemo {",
		"    export lp_vtbl: *IDemoVtbl",
		"export IDemo.Query(&self, p0: i32, p1: **void): i32 {",
		"return (*self.lp_vtbl).query.Must()(raw.Of(self), p0, p1)",
		"export IDemo.Ping(&self) {",
		"(*self.lp_vtbl).ping.Must()(raw.Of(self))",
	)
	if strings.Contains(text, "export InotCom.") {
		t.Errorf("INotCom is not a COM object and must not get COM methods:\n%s", text)
	}
}

func TestCImport_ExplainWhy(t *testing.T) {
	hdr := `typedef struct Box { int x; } Box;
static int hidden_fn(void) { return 1; }
#define BAD_M { int x; }
#define FN_M(x) ((x)+1)
#define GOOD_M 7
`
	res := translate(t, "x86_64-windows", map[string]string{"w.h": hdr}, Import{Name: "w", Headers: []string{"w.h"}})
	st := res.Stats[0]
	if !slices.ContainsFunc(st.SkippedDecls, func(d SkippedDecl) bool { return d.Name == "hidden_fn" && d.Reason == "static function" }) {
		t.Errorf("missing hidden_fn in SkippedDecls: %+v", st.SkippedDecls)
	}
	for _, tc := range []struct {
		cname, wantSub string
	}{
		{"hidden_fn", "was skipped: static function"},
		{"BAD_M", "was skipped: macro is no constant"},
		{"FN_M", "function-like macro with no prototype"},
		{"GOOD_M", "bound as `export const GoodM: i32 = 7`"},
		{"Box", "bound as `export struct Box`"},
	} {
		got, ok := ExplainWhy("w", res.Modules[0], st.SkippedDecls, res.FuncLike[0], res.Merged[0], tc.cname)
		if !ok || !strings.Contains(got, tc.wantSub) {
			t.Errorf("ExplainWhy(%q) = (%q, %v), want substring %q", tc.cname, got, ok, tc.wantSub)
		}
	}
}
