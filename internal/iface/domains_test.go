// Why: two encodings sharing a hash domain could collide in the cache.
// What: TestDomainsUnique over iface's domain table.
// How: checks that every domain is distinct, is an "mk." name and ends in the
// FormatVersion suffix.

package iface

import (
	"strings"
	"testing"

	"mk/internal/types"
)

// TestDomainsUnique: every domain of the table is distinct, is an
// "mk." name, and ends in the FormatVersion suffix; pgo's profile domain is
// among them.
func TestDomainsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Domains {
		if seen[d] {
			t.Errorf("domain %q is listed twice", d)
		}
		seen[d] = true
		if d == DomPGO || d == DomData || d == DomDataFail {
			continue // pgo's own domain and the data file domains: not iface encodings
		}
		if !strings.HasPrefix(d, "mk.") || !strings.HasSuffix(d, ".v3") {
			t.Errorf("domain %q is not mk.<name>.v3 (FormatVersion 3)", d)
		}
	}
	if !seen[DomPGO] || DomPGO != "mk.pgo.v1" {
		t.Errorf("DomPGO = %q, registered %v", DomPGO, seen[DomPGO])
	}
	// types' ReadData domain is registered.
	if !seen[DomData] || DomData != types.DataDomain {
		t.Errorf("DomData = %q (types.DataDomain %q), registered %v", DomData, types.DataDomain, seen[DomData])
	}
	if !seen[DomDataFail] || DomDataFail != types.DataFailDomain {
		t.Errorf("DomDataFail = %q (types.DataFailDomain %q), registered %v", DomDataFail, types.DataFailDomain, seen[DomDataFail])
	}
}
