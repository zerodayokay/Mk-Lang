// Why: cached objects embed each error's code, types.ErrorCode of the key
// "std.<module>.<Name>", so a std error may never change name or module.
// What: stdErrorCodes, every std error's pinned code, and TestStdErrorCodesStable.
// How: checks the pins against SHA-256 in the domain mk.key.v1 (as
// internal/source.HashString), then reads the real codes from a native program.

package std

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// stdErrorCodes is every std error and its code, pinned. A new std error
// adds a row; a row never changes.
var stdErrorCodes = []struct {
	key  string
	code uint32
}{
	// Rows are in key order, as the comparison with std's sorted keys needs.
	{"std.big.TooLarge", 1686890960},
	{"std.cli.Invalid", 64638176},
	{"std.cli.Missing", 1715806407},
	{"std.cli.Unknown", 1389324631},
	{"std.compress.Corrupt", 1432811901},
	{"std.compress.TooLarge", 51772992},
	{"std.compress.Truncated", 289519659},
	{"std.core.Canceled", 462360641},
	{"std.core.Closed", 1098982786},
	{"std.core.OutOfMemory", 1112708252},
	{"std.core.ParseError", 405073081},
	{"std.core.ReadError", 1956435489},
	{"std.core.Utf8Error", 1613144957},
	{"std.core.WriteError", 738579917},
	{"std.crypto.BadCost", 716968875},
	{"std.crypto.BadEncoding", 324256297},
	{"std.crypto.BadKey", 509629254},
	{"std.crypto.BadLength", 898161908},
	{"std.crypto.BadTag", 2095905028},
	{"std.crypto.Encrypted", 1861956806},
	{"std.csv.Quote", 1800238900},
	{"std.csv.TooLarge", 1852369524},
	{"std.db.sqlite.Busy", 1286049393},
	{"std.db.sqlite.Misuse", 1381335837},
	{"std.db.sqlite.Sqlite", 1103757409},
	{"std.dl.BadName", 370562714},
	{"std.dl.InitFailed", 1934409706},
	{"std.dl.Invalid", 1553859510},
	{"std.dl.NotFound", 401391643},
	{"std.dl.Os", 1647908873},
	{"std.dl.Unsupported", 1723437980},
	{"std.encoding.BadInput", 1029405116},
	{"std.encoding.TooLarge", 1493494798},
	{"std.encoding.UnknownLabel", 986731042},
	{"std.encoding.Unsupported", 1489858312},
	{"std.fs.Denied", 471618892},
	{"std.fs.Exists", 1394612751},
	{"std.fs.InvalidPath", 1245597761},
	{"std.fs.IsDirectory", 1383388061},
	{"std.fs.NoSpace", 848703172},
	{"std.fs.NotDirectory", 1488527847},
	{"std.fs.NotFound", 363192304},
	{"std.fs.Os", 875537413},
	{"std.fs.Unsupported", 956715957},
	{"std.gpu.DeviceLost", 1961494741},
	{"std.gpu.Driver", 1059893016},
	{"std.gpu.Invalid", 1727707434},
	{"std.gpu.Missing", 1902189030},
	{"std.gpu.NoAdapter", 421350488},
	{"std.gpu.OutOfDeviceMemory", 1601703050},
	{"std.gpu.SurfaceLost", 451601330},
	{"std.gpu.Unsupported", 1437986487},
	{"std.gpu.dx.DxFail", 1072418869},
	{"std.gpu.dx.Unsupported", 2090660711},
	{"std.gpu.vk.DeviceLost", 209628066},
	{"std.gpu.vk.Driver", 12530534},
	{"std.gpu.vk.Missing", 1165374240},
	{"std.gpu.vk.NoAdapter", 1914984538},
	{"std.gpu.vk.OutOfDeviceMemory", 1447469870},
	{"std.gpu.vk.SurfaceLost", 2073396787},
	{"std.gpu.vk.Unsupported", 1705523985},
	{"std.http.BadRequest", 782613169},
	{"std.http.BadUrl", 2004359864},
	{"std.http.BodyNotReplayable", 870456459},
	{"std.http.NoSecureTransport", 536417299},
	{"std.http.Protocol", 747255426},
	{"std.http.ProxyRefused", 862385251},
	{"std.http.Status", 23996673},
	{"std.http.TooLarge", 474579670},
	{"std.http.TooManyRedirects", 352195647},
	{"std.http.fields.BadBase", 808398844},
	{"std.http.fields.BadHuffman", 188230450},
	{"std.http.fields.BadIndex", 561684964},
	{"std.http.fields.BadInstruction", 1611840938},
	{"std.http.fields.BadInteger", 380287713},
	{"std.http.fields.BadRequiredInsertCount", 224751550},
	{"std.http.fields.BadSizeUpdate", 861656982},
	{"std.http.fields.BlockedLimit", 1269906445},
	{"std.http.fields.CapacityExceeded", 1532491433},
	{"std.http.fields.MalformedField", 147075999},
	{"std.http.fields.MissingSizeUpdate", 28316873},
	{"std.http.fields.TooSmall", 667087857},
	{"std.http.fields.Truncated", 497700772},
	{"std.http.fields.Unusable", 828422955},
	{"std.http.h2.BadField", 1625016166},
	{"std.http.h2.TooLarge", 783803993},
	{"std.http.h2.Violation", 250376542},
	{"std.http.h3.SendTooLarge", 845373960},
	{"std.http.h3.TooLarge", 373640710},
	{"std.http.h3.Violation", 196786666},
	{"std.image.Invalid", 1812470315},
	{"std.image.TooLarge", 1774872296},
	{"std.image.Unsupported", 984748343},
	{"std.json.Depth", 1715825482},
	{"std.json.Mismatch", 490261723},
	{"std.json.Syntax", 567111721},
	{"std.net.AddrInUse", 1806112950},
	{"std.net.InvalidAddress", 1309140575},
	{"std.net.MuxClosed", 715208150},
	{"std.net.Os", 1628941661},
	{"std.net.Refused", 665067335},
	{"std.net.Reset", 1132664580},
	{"std.net.SecureFailed", 841924286},
	{"std.net.StreamReset", 2036820253},
	{"std.net.Unreachable", 133943711},
	{"std.net.Unsupported", 376793228},
	{"std.proc.Denied", 668687245},
	{"std.proc.NotFound", 769231435},
	{"std.proc.Os", 850047728},
	{"std.proc.TooLarge", 2112858787},
	{"std.proc.Unsupported", 783714364},
	{"std.regex.Syntax", 819143739},
	{"std.regex.TooLarge", 151229768},
	{"std.runtime.OsError", 1761627082},
	{"std.runtime.Unsupported", 2060526658},
	{"std.term.Device", 996712204},
	{"std.term.NotTerminal", 2013890631},
	{"std.term.Unsupported", 1649197191},
	{"std.time.BadData", 1055993327},
	{"std.time.BadName", 1380843026},
	{"std.time.NoDatabase", 1805895},
	{"std.time.NotFound", 1383880039},
	{"std.time.OutOfRange", 271279009},
	{"std.time.Repeated", 462189686},
	{"std.time.Skipped", 1211337415},
	{"std.time.TimedOut", 1818118312},
	{"std.tls.Alert", 985230147},
	{"std.tls.Protocol", 170805390},
	{"std.tls.Unsupported", 613412457},
	{"std.url.BadHost", 1595478784},
	{"std.url.BadUrl", 1392224660},
	{"std.url.IdnaUnsupported", 1532249654},
	{"std.vm.Misaligned", 1857100995},
	{"std.vm.Os", 1554062065},
	{"std.vm.Unsupported", 1188174119},
	{"std.websocket.Handshake", 309346312},
	{"std.websocket.PeerClosed", 1421078369},
	{"std.websocket.Protocol", 2089393314},
	{"std.websocket.TooLarge", 1217455577},
	{"std.window.Os", 463735808},
	{"std.window.Unsupported", 517637753},
	{"std.x509.BadSignature", 2015478602},
	{"std.x509.BadUsage", 1352315121},
	{"std.x509.Expired", 388630855},
	{"std.x509.Malformed", 994521507},
	{"std.x509.NameMismatch", 479531162},
	{"std.x509.TooComplex", 1126663323},
	{"std.x509.UnknownAuthority", 2047470188},
	{"std.x509.Unsupported", 1775950396},
}

// keyErrorCode is types.ErrorCode(key) written out: the first 8 bytes,
// big-endian, of SHA-256 over uvarint(len) and bytes of "mk.key.v1", then of
// the key; the low 31 bits plus 1.
func keyErrorCode(key string) uint32 {
	h := sha256.New()
	var b [binary.MaxVarintLen64]byte
	for _, s := range []string{"mk.key.v1", key} {
		h.Write(b[:binary.PutUvarint(b[:], uint64(len(s)))])
		h.Write([]byte(s))
	}
	return uint32(binary.BigEndian.Uint64(h.Sum(nil)[:8])&0x7fffffff) + 1
}

var errorDecl = regexp.MustCompile(`(?m)^export error ([A-Z][A-Za-z0-9]*)(?:\(([^)]*)\)|$)`)

// enumDecl finds an exported enum and its first member, the value a
// payload of that type takes in the native half.
var enumDecl = regexp.MustCompile(`(?m)^export (?:open )?enum ([A-Z][A-Za-z0-9]*) \{\n(?:\s*///[^\n]*\n)*\s*([A-Z][A-Za-z0-9]*)`)

// numericPayload: the field types a literal 5 fills.
var numericPayload = map[string]bool{"int": true, "i64": true, "i32": true, "i16": true, "i8": true, "uint": true, "u64": true, "u32": true, "u16": true, "u8": true}

// declaredStdErrors scans std for exported errors, outside test files, and
// returns their keys sorted, and the payload of those that take one: each
// field as "name: value", a number 5 or its enum type's first member,
// joined by ", "; "-" when a field has another type (the native half then
// leaves that error to the formula check).
func declaredStdErrors(t *testing.T) ([]string, map[string]string) {
	t.Helper()
	var keys []string
	payload := map[string]string{}
	enums := map[string]string{}
	err := fs.WalkDir(os.DirFS("."), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") || !strings.Contains(p, "/") || strings.HasPrefix(p[strings.LastIndex(p, "/")+1:], "tests_") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mod := "std." + strings.ReplaceAll(p[:strings.LastIndex(p, "/")], "/", ".")
		text := strings.ReplaceAll(string(b), "\r\n", "\n")
		for _, m := range errorDecl.FindAllStringSubmatch(text, -1) {
			k := mod + "." + m[1]
			keys = append(keys, k)
			if m[2] != "" {
				payload[k] = m[2]
			}
		}
		for _, m := range enumDecl.FindAllStringSubmatch(text, -1) {
			enums[mod+"."+m[1]] = strings.TrimPrefix(mod, "std.") + "." + m[1] + "." + m[2]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, fields := range payload {
		mod := k[:strings.LastIndex(k, ".")]
		var vals []string
		for _, f := range strings.Split(fields, ",") {
			name, ty, _ := strings.Cut(strings.TrimSpace(f), ":")
			ty = strings.TrimSpace(ty)
			v := ""
			if numericPayload[ty] {
				v = "5"
			} else if e, ok := enums[mod+"."+ty]; ok {
				v = e
			}
			if v == "" {
				vals = []string{"-"}
				break
			}
			vals = append(vals, name+": "+v)
		}
		payload[k] = strings.Join(vals, ", ")
	}
	slices.Sort(keys)
	return keys, payload
}

// TestStdErrorCodesStable: the pinned table is exactly std's errors, each
// pin is the formula's value, no two collide, and a native program reads
// each code from the error value the compiler built.
func TestStdErrorCodesStable(t *testing.T) {
	t.Parallel()
	if c := keyErrorCode("std.fs.NotFound"); c != 363192304 {
		t.Fatalf("formula gives %d for std.fs.NotFound; internal/types TestStableKeys pins 363192304", c)
	}
	var pinned []string
	seen := map[uint32]string{}
	for _, r := range stdErrorCodes {
		pinned = append(pinned, r.key)
		if c := keyErrorCode(r.key); c != r.code {
			t.Errorf("%s: pinned %d, formula %d", r.key, r.code, c)
		}
		if o, ok := seen[r.code]; ok {
			t.Errorf("%s and %s share code %d", r.key, o, r.code)
		}
		seen[r.code] = r.key
	}
	keys, payload := declaredStdErrors(t)
	if !slices.Equal(keys, pinned) {
		t.Fatalf("std declares the errors\n%v\nthe pinned table holds\n%v\nan error moved, was renamed or is new: a new one adds a row, a pinned one never changes", keys, pinned)
	}

	// The native half: one function per error fails with it, and CodeOf
	// (a fixture in std.sync, which may use unsafe) reads the error value's
	// first u32 (an error value is laid out as {u32 code, payload}).
	fixture := "import std.raw\n\n" +
		"// CodeOf reads the code word of an error value (test fixture).\n" +
		"export CodeOf[T](r: &T): u32 {\n" +
		"    unsafe \"an error value starts with its u32 code; r is live\" {\n" +
		"        return raw.Read(raw.PtrCast[T, u32](raw.Of(r)))\n" +
		"    }\n}\n"
	var b strings.Builder
	// std.db.sqlite is left out of the native half: a program importing it
	// builds the sqlite amalgamation, which is kept outside the
	// repository. Internal modules are also outside a program's imports.
	// Their codes are still pinned and checked against the
	// formula above, as is an error whose payload no literal fills ("-").
	var native []string
	imports := map[string]bool{"std.sync": true}
	for _, k := range keys {
		mod := k[:strings.LastIndex(k, ".")]
		if mod == "std.db.sqlite" || payload[k] == "-" || mod != PreludeModule && slices.Contains(Internal[:], mod) {
			continue
		}
		native = append(native, k)
		if mod != "std.core" {
			imports[mod] = true
		}
	}
	mods := make([]string, 0, len(imports))
	for m := range imports {
		mods = append(mods, m)
	}
	slices.Sort(mods)
	for _, m := range mods {
		b.WriteString("import " + m + "\n")
	}
	b.WriteString("\n")
	for i, k := range native {
		mod, name := k[:strings.LastIndex(k, ".")], k[strings.LastIndex(k, ".")+1:]
		q := name
		if mod != "std.core" {
			q = strings.TrimPrefix(mod, "std.") + "." + name
		}
		val := q
		if f := payload[k]; f != "" {
			val = q + "(" + f + ")"
		}
		// The error alone is the set: not every error is a member of its
		// module's Error (time.TimedOut is net.Error's).
		fmt.Fprintf(&b, "F%d(): %s!void {\n    fail %s\n}\n\n", i, q, val)
		fmt.Fprintf(&b, "C%d(): u32 {\n    match F%d() {\n        ok(_) {\n            return 0\n        }\n        err(e) {\n            return sync.CodeOf(&e)\n        }\n    }\n}\n\n", i, i)
	}
	b.WriteString("export Main(sys: &System): WriteError!void {\n    var out := sys.Stdout()\n")
	for i := range native {
		fmt.Fprintf(&b, "    c%d := C%d()\n    try Print(mut out, \"{c}\\n\", (c: c%d))\n", i, i, i)
	}
	b.WriteString("}\n")
	exe := buildNative(t, map[string]string{"sync/zz_codefix.mk": fixture}, b.String())
	r := runNative(t, exe, "")
	var want strings.Builder
	for _, k := range native {
		fmt.Fprintf(&want, "%d\n", keyErrorCode(k))
	}
	if r.code != 0 || r.stdout != want.String() {
		got := strings.Split(r.stdout, "\n")
		for i, k := range native {
			if i < len(got) && got[i] != fmt.Sprint(keyErrorCode(k)) {
				t.Errorf("%s: the compiler's code is %s, pinned %d", k, got[i], keyErrorCode(k))
			}
		}
		t.Fatalf("exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", r.code, r.stdout, want.String(), r.stderr)
	}
}
