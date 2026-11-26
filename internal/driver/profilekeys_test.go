// Why: every key that hashes the build profile must keep a checked build
// apart from the others without moving the keys of existing profiles.
// What: profileKeys and TestOtherProfileKeysPinned.
// How: profileKeys prints everything a profile adds to the cache keys for
// fixed inputs; the test pins that line for dev, test, release and size.

package driver

// The checked profile (mk build --profile checked) adds a fourth
// types.Profile after Release, so every key that hashes the profile
// separates a checked build from the others, while the keys of the dev,
// test, release and size builds stay what they were.

import (
	"fmt"
	"testing"

	"mk/internal/codegen"
	"mk/internal/iface"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// profileKeys is everything a profile contributes to a key, for fixed
// other inputs, as one line: the number every key hashes (the front
// cache's context, PackFacts' prekey, iface.PackKey), its name in the link
// key, each Mk pack's and the support pack's -O level and -flto with the
// object keys of a fixed .ll under those jobs, whether a COFF and an ELF
// link stage their hits, a pack key, and the link options string.
func profileKeys(p types.Profile, size bool) string {
	w := &world{cfg: Config{Profile: p, Size: size}}
	cfg := codegen.Config{Target: types.X86_64Windows, Triple: types.X86_64Windows.LLVMTriple, CPU: "x86-64-v2"}
	tools := &toolchain.Tools{}
	ll := []byte("define i32 @f() { ret i32 0 }\n")
	pack := w.packJob(cfg, "p.ll", "p.o", 0, 2)
	sup := w.packJob(cfg, "s.ll", "s.o", 2, 2)
	return fmt.Sprintf("%d %s | pack O%d lto=%t %v | support O%d lto=%t %v | staged coff=%t elf=%t | packkey %v | %s",
		uint64(p), p, pack.Opt, pack.LTO, objKey(source.Hash{}, tools, pack, ll), sup.Opt, sup.LTO, objKey(source.Hash{}, tools, sup, ll),
		w.stagedHits(cfg.Triple), w.stagedHits(types.X86_64Linux.LLVMTriple),
		iface.PackKey(iface.PackKeyInput{Target: cfg.Target.Name, Profile: p, CPU: cfg.CPU}),
		nativeOptions(tools, source.Hash{}, cfg, p, w.linkJob(cfg.Triple, "a.exe")))
}

// TestOtherProfileKeysPinned pins what dev, test, release and size
// put into every key, as they were before the checked profile: a change
// that renumbers or renames a profile, or moves its compile level, link
// or pack key, goes red here.
func TestOtherProfileKeysPinned(t *testing.T) {
	if iface.FormatVersion != pinnedFormat {
		t.Fatalf("iface.FormatVersion is %d, the pinned pack keys are version %d's: every key hashes under its domains, so pin them again", iface.FormatVersion, pinnedFormat)
	}
	for _, c := range []struct {
		name string
		p    types.Profile
		size bool
		want string
	}{
		{"dev", types.Dev, false, mkh144Dev},
		{"test", types.TestProfile, false, mkh144Test},
		{"release", types.Release, false, mkh144Release},
		{"size", types.Release, true, mkh144Size},
	} {
		if got := profileKeys(c.p, c.size); got != c.want {
			t.Errorf("%s keys moved:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

// The keys before the checked profile. A pack key is a mk.cachekey hash, so
// it also moves with iface.FormatVersion, whose domains every key hashes
// under: these are version pinnedFormat's.
const (
	pinnedFormat = 3

	mkh144Dev     = `0 dev | pack O0 lto=false 1c70945b73f6eafead58655844a5c65ef80a69330c9cc431e941bb93c5d1d284 | support O1 lto=false 185be297fdf7222c488ca9630534b61c0f1196013cc9ef5667af025d69baa5dc | staged coff=false elf=false | packkey c26a3cbefd1244a018599140ac4a0e06a85c3b34c7e15ae8d325c7b0c6bed0ba | mk.link.v4 30303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030 30646435363562353136666230623038626265316535363338353666613238373331666130323932326133633063356235323862333938633138326531663865 x86_64-pc-windows-msvc x86-64-v2 dev 1 "" false false 8388608 false false `
	mkh144Test    = `1 test | pack O0 lto=false 1c70945b73f6eafead58655844a5c65ef80a69330c9cc431e941bb93c5d1d284 | support O1 lto=false 185be297fdf7222c488ca9630534b61c0f1196013cc9ef5667af025d69baa5dc | staged coff=false elf=false | packkey a4bdf303f3c58bc556611d8c02e308414dc44cbd431f79034e101102ab332a15 | mk.link.v4 30303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030 30646435363562353136666230623038626265316535363338353666613238373331666130323932326133633063356235323862333938633138326531663865 x86_64-pc-windows-msvc x86-64-v2 test 1 "" false false 8388608 false false `
	mkh144Release = `2 release | pack O1 lto=true ebc3393e69583e626af4d4297d8d9cbcf91521756e92f393bce6753576946035 | support O1 lto=false 185be297fdf7222c488ca9630534b61c0f1196013cc9ef5667af025d69baa5dc | staged coff=false elf=true | packkey b909768f0f6f273147a18e109b1d56b86fb0586db5dbbcd314bacad7fb726025 | mk.link.v4 30303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030 30646435363562353136666230623038626265316535363338353666613238373331666130323932326133633063356235323862333938633138326531663865 x86_64-pc-windows-msvc x86-64-v2 release 1 "" true true 8388608 false false `
	mkh144Size    = `2 release | pack O2 lto=true 14e8fe61ee4ad23c35358a4e9bc5b86e594e7850b2328fe6fbec614f1a7d9372 | support O2 lto=false f0006aa46bd98df520dfdc33e9f9bea99d3222cd56a8a68d31c0165ee89aa6c5 | staged coff=false elf=true | packkey b909768f0f6f273147a18e109b1d56b86fb0586db5dbbcd314bacad7fb726025 | mk.link.v4 30303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030 30646435363562353136666230623038626265316535363338353666613238373331666130323932326133633063356235323862333938633138326531663865 x86_64-pc-windows-msvc x86-64-v2 release 1 "" true true 8388608 false false `
)
