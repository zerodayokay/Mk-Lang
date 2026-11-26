// Why: the TLS handshake parsers and record paths read bytes from the network
// and must never panic or hang on any input.
// What: FuzzStdTls_Messages, FuzzStdTls_Records, FuzzStdTls_Logic and
// FuzzStdTls_QuicHandshake, seeded from RFC 8448's messages and records.
// How: each runs the release driver on the fuzzer's bytes; a panic, a hang or a
// parse-write-parse round trip that does not settle fails (go test -fuzz
// FuzzStdTls_<name> -parallel 2 explores).
// Its fuzz targets read untrusted input; tools/gate/safety.go lists each untrusted-input target.

package std

import (
	"strings"
	"testing"
)

// tlsFuzzOne runs one record through the release driver.
func tlsFuzzOne(t *testing.T, record string) string {
	t.Helper()
	r := runNative(t, tlsExe(t, "release"), record+"\n")
	if r.code != 0 {
		t.Fatalf("driver exit %d on %.80s\nstderr: %s", r.code, record, r.stderr)
	}
	return strings.TrimSpace(r.stdout)
}

// tlsFuzzSeeds is every handshake message and record of RFC 8448's two
// traces.
func tlsFuzzSeeds(f *testing.F) (msgs, recs [][]byte) {
	for _, sec := range tlsSections {
		steps := tlsTrace(f, sec.from, sec.to)
		for _, s := range steps {
			if strings.HasPrefix(s.action, "construct a") && len(s.fields) > 0 {
				msgs = append(msgs, s.fields[0].b)
			}
			if r := s.field("complete record"); r != nil {
				recs = append(recs, r)
			}
		}
	}
	return msgs, recs
}

func FuzzStdTls_Messages(f *testing.F) {
	msgs, _ := tlsFuzzSeeds(f)
	for _, m := range msgs {
		f.Add(m)
		f.Add(m[:len(m)/2])
	}
	f.Add([]byte{24, 0, 0, 1, 1})
	// The large-message bounds: a Certificate of many small
	// entries, one of a single 200 KiB entry, and a 60 KiB ClientHello.
	for _, m := range tlsBigSeeds() {
		f.Add(m)
	}
	f.Fuzz(func(t *testing.T, msg []byte) {
		// Up to the largest message std.tls reads: a Certificate, 4 +
		// MaxCertificate bytes.
		if len(msg) > 4+262144 {
			return
		}
		got := tlsFuzzOne(t, "z "+tlsHex(msg))
		switch got {
		case "err", "same", "lossy":
		default:
			t.Fatalf("message %x: %s", msg, got)
		}
	})
}

func FuzzStdTls_Records(f *testing.F) {
	_, recs := tlsFuzzSeeds(f)
	for _, r := range recs {
		f.Add(append([]byte{0}, r...))
	}
	f.Add([]byte{1, 1, 24, 0, 0, 1, 1})
	f.Add([]byte{1, 3, 4, 0, 0, 20, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 1, 7, 0, 0})
	// Inner plaintexts (path 2): data with padding, a lone type byte, all
	// zeros, and each RFC 8448 record's body as an inner plaintext.
	f.Add([]byte{2, 0, 'h', 'i', 23, 0, 0, 0})
	f.Add([]byte{2, 1, 22})
	f.Add([]byte{2, 2, 0, 0, 0})
	for _, r := range recs {
		f.Add(append([]byte{2, byte(len(r))}, r[5:]...))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 17000 {
			return
		}
		got := tlsFuzzOne(t, "y "+tlsHex(b))
		switch got {
		case "ok", "err", "short":
		default:
			t.Fatalf("bytes %x: %s", b, got)
		}
	})
}

// FuzzStdTls_Logic runs any bytes as a handshake message through the
// checks after its parser: ReadOffer for a ClientHello, CheckServerHello
// for a ServerHello, CheckEncryptedExtensions for EncryptedExtensions.
func FuzzStdTls_Logic(f *testing.F) {
	msgs, _ := tlsFuzzSeeds(f)
	for _, m := range msgs {
		f.Add(m)
	}
	f.Fuzz(func(t *testing.T, msg []byte) {
		if len(msg) > 16384 {
			return
		}
		got := tlsFuzzOne(t, "g "+tlsHex(msg))
		switch got {
		case "ok", "err", "skip":
		default:
			t.Fatalf("message %x: %s", msg, got)
		}
	})
}

// FuzzStdTls_QuicHandshake feeds untrusted bytes per encryption level into
// QuicServer, QuicClient and QuicPostMessage.
func FuzzStdTls_QuicHandshake(f *testing.F) {
	msgs, _ := tlsFuzzSeeds(f)
	for _, m := range msgs {
		f.Add(append([]byte{0}, m...))
		f.Add(append([]byte{1}, m...))
		f.Add(append([]byte{2}, m...))
	}
	f.Add([]byte{2, 24, 0, 0, 1, 1})
	f.Add([]byte{2, 13, 0, 0, 4, 0, 2, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 16384 {
			return
		}
		got := tlsFuzzOne(t, "q "+tlsHex(b))
		switch got {
		case "ok", "err", "short":
		default:
			t.Fatalf("bytes %x: %s", b, got)
		}
	})
}
