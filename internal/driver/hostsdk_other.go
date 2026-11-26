//go:build !windows

// Why: only a Windows host has a registry recording the Windows SDK.
//
// What: windowsKitsRoots, which finds none off Windows.
//
// How: a stub; hostsdk.go's other rules still apply.

package driver

// windowsKitsRoots are the Windows Kits roots the registry records: none
// on a host that is not Windows.
func windowsKitsRoots() []string { return nil }
