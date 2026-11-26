// Why: the probe's Transport (core8Conn, core_erasure_test.go) must keep the
// shape of net.Transport in doc/reference.md, so ERASE-5 tests what std.net builds.
// What: TestCore_TransportMatchesReference.
// How: compares receivers in the reference and checks the networking guide's
// example: Close(&self, io) is fallible and in place, Abandon(self) the one release.

package std

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// core8InterfaceBody returns the lines of the first `interface Transport {`
// block in text, trimmed, without the braces.
func core8InterfaceBody(t *testing.T, text, where string) []string {
	t.Helper()
	start := strings.Index(text, "interface Transport {")
	if start < 0 {
		t.Fatalf("%s: no interface Transport block", where)
	}
	rest := text[start+len("interface Transport {"):]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("%s: interface Transport block not closed", where)
	}
	var out []string
	for _, l := range strings.Split(rest[:end], "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// core8Receiver maps each method name of an interface body to its receiver
// (`&self`, `self`, `mut self`).
func core8Receiver(lines []string) map[string]string {
	re := regexp.MustCompile(`^([A-Z][A-Za-z]*)\((&self|mut self|self)\b`)
	m := map[string]string{}
	for _, l := range lines {
		if s := re.FindStringSubmatch(l); s != nil {
			m[s[1]] = s[2]
		}
	}
	return m
}

func TestCore_TransportMatchesReference(t *testing.T) {
	ref, err := os.ReadFile("../doc/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	refRecv := core8Receiver(core8InterfaceBody(t, string(ref), "reference.md"))
	probeRecv := core8Receiver(core8InterfaceBody(t, core8Conn, "core8Conn"))

	// The peer-facing close is shared and fallible, Abandon releases.
	want := map[string]string{"Close": "&self", "Abandon": "self", "Read": "&self", "WriteAll": "&self"}
	for name, recv := range want {
		if got := refRecv[name]; got != recv {
			t.Errorf("reference net.Transport.%s takes %q, want %q", name, got, recv)
		}
		if got := probeRecv[name]; got != recv {
			t.Errorf("probe Transport.%s takes %q, want %q (the reference's)", name, got, recv)
		}
	}
	// Only Abandon consumes the transport: anything else by value would put a
	// move before a fallible call behind the erased box (E0411).
	for name, recv := range refRecv {
		if recv != "&self" && name != "Abandon" {
			t.Errorf("reference net.Transport.%s takes %q; only Abandon may consume", name, recv)
		}
	}
	// The networking guide's Lent block implements the same Close.
	guide := readText(t, "../doc/std/net.md")
	_, impl, _ := strings.Cut(guide, "impl net.Transport for Lent {\n")
	body, _, _ := strings.Cut(impl, "\n}")
	if !regexp.MustCompile(`(?m)^    Close\(&self, io: Io\): net\.Error!void \{`).MatchString(body) {
		t.Errorf("doc/std/net.md: the Lent block's impl does not have Close(&self, io: Io): net.Error!void")
	}
}
