// Why: a bug report needs the exact compiler that ran.
// What: TestVersionPrintsReleaseAndCommit.
// How: runs mk version and checks it prints the release and the commit.

package driver

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionPrintsReleaseAndCommit(t *testing.T) {
	first, _, _ := strings.Cut(VersionText(""), "\n")
	if first != "mk "+Release+" commit "+Commit() {
		t.Errorf("first line %q", first)
	}
	if Release != "0.7.0" {
		t.Errorf("Release %q, want 0.7.0", Release)
	}
	for _, c := range []struct {
		set  []debug.BuildSetting
		want string
	}{
		{nil, "unknown"},
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "5f79abe0123456789abcdef"}}, "5f79abe01234"},
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "5f79abe0123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}, "5f79abe01234+dirty"},
	} {
		if got := commitOf(c.set); got != c.want {
			t.Errorf("commitOf(%v) = %q, want %q", c.set, got, c.want)
		}
	}
}
