// Why: std.simd.kernel and std.http.fields are std-only and must not be
// imported by the browse file.
// What: the test of browsing a std.simd.kernel or std.http.fields file.
// How: checks the module and the pulls of each such file, while std.simd and
// std.http stay public.

package lsp

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestLSPSimdKernelBrowse(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "std")
	for _, tc := range []struct {
		file, module string
		pulls        []string
	}{
		{"simd/kernel/aes.mk", "", []string{"std.crypto", "std.hash"}},
		{"simd/simd.mk", "std.simd", nil},
		{"task/scope.mk", "", stdPulls},
		{"time/time.mk", "std.time", nil},
		{"http/fields/table.mk", "", []string{"std.http"}},
		{"http/client.mk", "std.http", nil},
	} {
		path := filepath.Join(tree, filepath.FromSlash(tc.file))
		if got := stdModuleOf(tree, path); got != tc.module {
			t.Errorf("stdModuleOf(%s) = %q, want %q", tc.file, got, tc.module)
		}
		if tc.module == "" && !slices.Equal(stdPullsOf(tree, path), tc.pulls) {
			t.Errorf("stdPullsOf(%s) = %v, want %v", tc.file, stdPullsOf(tree, path), tc.pulls)
		}
	}
}
