// Why: a C import that asks for the host's SDK (sdk: true) must find the
// Windows SDK where its installer recorded it, not where a guess puts it.
//
// What: windowsKitsRoots, the Windows Kits 10 roots the registry records.
//
// How: the value KitsRoot10 under HKLM\SOFTWARE\Microsoft\Windows
// Kits\Installed Roots, read through Go's syscall package in the 64-bit
// view and then the 32-bit (WOW6432Node) one; the UTF-16 bytes are decoded
// by hand, so no unsafe is needed.

package driver

import (
	"syscall"
	"unicode/utf16"
)

// keyWow64_32 opens the 32-bit registry view (KEY_WOW64_32KEY).
const keyWow64_32 = 0x0200

// windowsKitsRoots are the KitsRoot10 values of the registry's 64-bit
// and 32-bit views, in that order, each once.
func windowsKitsRoots() []string {
	var out []string
	for _, view := range []uint32{0, keyWow64_32} {
		if v := regString(`SOFTWARE\Microsoft\Windows Kits\Installed Roots`, "KitsRoot10", view); v != "" {
			dup := false
			for _, o := range out {
				dup = dup || o == v
			}
			if !dup {
				out = append(out, v)
			}
		}
	}
	return out
}

// regString reads the string value name of HKLM\key in registry view
// view; "" when it is absent or no string.
func regString(key, name string, view uint32) string {
	k16, err := syscall.UTF16PtrFromString(key)
	if err != nil {
		return ""
	}
	n16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}
	var h syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, k16, 0, syscall.KEY_READ|view, &h) != nil {
		return ""
	}
	defer syscall.RegCloseKey(h)
	var typ, size uint32
	if syscall.RegQueryValueEx(h, n16, nil, &typ, nil, &size) != nil || size == 0 || size > 1<<16 ||
		typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ {
		return ""
	}
	buf := make([]byte, size)
	if syscall.RegQueryValueEx(h, n16, nil, &typ, &buf[0], &size) != nil {
		return ""
	}
	u := make([]uint16, 0, size/2)
	for i := 0; i+1 < int(size); i += 2 {
		c := uint16(buf[i]) | uint16(buf[i+1])<<8
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}
