// Why: h2spec v2.6.0 exercises hundreds of RFC 9113 protocol, frame, header,
// flow-control, and state-machine rules from an independent peer against ServeOn.
//
// What: TestStdHttp_H2spec (h2c and h2c-strict), checking all RFC 9113 rules
// against ServeOn (h2spec v2.6.0 supports TLS 1.2 only while std.tls is TLS 1.3,
// so TLS ALPN peer checks run against Go 1.27's h2peer in std-http_h2peer_test.go).
//
// How: Compiles and launches the Mk H2 server over loopback (port 0), runs
// V:\Mk-wt\_run\peers\h2\run-h2spec.ps1, parses its JSON summary, and verifies
// all failures belong strictly to the documented dual-protocol h2c preface sniff
// (3.5) and obsolete RFC 7540 PRIORITY self-dependency (5.3.1).

package std

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStdHttp_H2spec(t *testing.T) {
	peers := httpPeerKitDir(t)
	exe := http2Build(t, mkH2PeerServerProg)
	cert := filepath.Join(peers, "certs", "server-p256-chain.pem")
	key := filepath.Join(peers, "certs", "server-p256.key.pem")
	script := filepath.Join(peers, "h2", "run-h2spec.ps1")

	allowed := map[string]bool{
		"http2/3.5: Sends invalid connection preface":             true,
		"http2/5.3.1: Sends HEADERS frame that depends on itself": true,
		"http2/5.3.1: Sends PRIORITY frame that depend on itself": true,
	}

	cases := []struct {
		name   string
		strict bool
	}{
		{name: "h2c", strict: false},
		{name: "h2c-strict", strict: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port, stop := launchMkH2Server(t, exe, "h2c", cert, key)
			defer stop()

			args := []string{
				"-NoProfile", "-File", script,
				"-Mode", "h2c",
				"-Port", fmt.Sprintf("%d", port),
				"-Report", filepath.Join(t.TempDir(), fmt.Sprintf("h2spec-%s.xml", tc.name)),
			}
			if tc.strict {
				args = append(args, "-Strict")
			}
			cmd := exec.Command("pwsh", args...)
			out, err := cmd.CombinedOutput()
			t.Logf("h2spec %s: %s", tc.name, string(out))
			if err != nil {
				t.Fatalf("h2spec runner (%s) failed: %v", tc.name, err)
			}
			var summary struct {
				Tests     int      `json:"tests"`
				Passed    int      `json:"passed"`
				Failed    int      `json:"failed"`
				FailedIDs []string `json:"failed_ids"`
			}
			var parsed bool
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "{") {
					if jerr := json.Unmarshal([]byte(line), &summary); jerr == nil {
						parsed = true
						break
					}
				}
			}
			if !parsed {
				t.Fatalf("could not parse h2spec JSON summary from %q", string(out))
			}
			if summary.Tests < 140 || summary.Passed < 143 {
				t.Fatalf("unexpected h2spec totals: %+v", summary)
			}
			for _, id := range summary.FailedIDs {
				if !allowed[id] {
					t.Errorf("unexpected h2spec failure in %s: %s", tc.name, id)
				}
			}
		})
	}
}
