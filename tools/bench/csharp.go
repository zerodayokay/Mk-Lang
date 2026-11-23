// Why: the benchmark contract adds C# as two columns — RyuJIT (cs) and
// NativeAOT (cs-aot) — built completely offline with pinned GC, globalization,
// safety and CPU-target settings so every row runs like-for-like.
//
// What: csCommand, csNugetDir, csCopySidecars, csRunEnv and dotnetVersionEnv.
//
// How: each C# row is one .cs source file; tools/bench writes a minimal
// bench.csproj and an offline NuGet.Config (with <clear /> pointing only at
// $MK_EVIDENCE/nuget) inside the work directory before invoking `dotnet
// publish`. All contract properties are passed on the command line so
// results.json records them verbatim and tests can check them directly.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dotnetVersionEnv keeps `dotnet --version` offline and quiet without reading
// user registry telemetry or writing first-time state.
var dotnetVersionEnv = []string{
	"DOTNET_CLI_TELEMETRY_OPTOUT=1",
	"DOTNET_NOLOGO=1",
	"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
}

// csRuntimeUnset are caller-inherited .NET runtime knobs that could alter
// JIT codegen, tiered compilation, PGO or GC unseen.
var csRuntimeUnset = []string{
	"DOTNET_Enable*",
	"DOTNET_Tiered*",
	"DOTNET_TC_*",
	"DOTNET_ReadyToRun*",
	"DOTNET_gc*",
	"DOTNET_GC*",
	"DOTNET_Jit*",
	"DOTNET_OSR*",
	"COMPlus_*",
}

// csRunEnv returns the extra environment variables a C# column needs at run
// time. RyuJIT (cs) detects host CPU features at run time, so its ISA is
// pinned to the row's CPU target to match the AOT columns (x86-64-v2: SSE4.2 +
// POPCNT only; x86-64-v3: AVX2/FMA/BMI/LZCNT, no AVX-512).
func csRunEnv(r *row, c column) []string {
	if c.ID != "cs" {
		return nil
	}
	target := "x86-64-v2"
	if r != nil {
		target = r.cpu()
	}
	switch target {
	case "x86-64-v3":
		return []string{"DOTNET_EnableAVX512=0"}
	default:
		return []string{
			"DOTNET_EnableAVX=0",
			"DOTNET_EnableBMI1=0",
			"DOTNET_EnableBMI2=0",
			"DOTNET_EnableLZCNT=0",
			"DOTNET_EnableAVX512=0",
		}
	}
}

// csNugetDir is the offline NuGet package directory under the evidence
// directory ($MK_EVIDENCE/nuget).
func csNugetDir(rowDir string) string {
	if root, err := repoRoot(rowDir); err == nil {
		return filepath.Join(evidenceDir(root), "nuget")
	}
	if e := os.Getenv("MK_EVIDENCE"); e != "" {
		return filepath.Join(e, "nuget")
	}
	return filepath.Join(filepath.Dir(rowDir), "Mk-evidence", "nuget")
}

// xmlEscape escapes text placed inside an XML attribute value.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// csCommand constructs the offline `dotnet publish` command for column c
// ("cs" or "cs-aot") of row r.
func (t *toolchain) csCommand(r *row, c column, out string) (buildCmd, error) {
	if t.Dotnet == "" {
		return buildCmd{}, fmt.Errorf("dotnet not found")
	}
	src := filepath.Join(r.Dir, r.Sources[c.Lang])
	absSrc, err := filepath.Abs(src)
	if err != nil {
		absSrc = src
	}
	cpu := cpus[r.cpu()]
	// Separate project directories by build tag (publish, verify, count, etc.)
	// so counting copies and timed builds never share obj/ or out/.
	tag := filepath.Base(filepath.Dir(filepath.Dir(out)))
	if tag == "" || tag == "." || tag == string(filepath.Separator) {
		tag = "run"
	}
	projDir := filepath.Join(t.Work, "cs-proj", tag, strings.ReplaceAll(r.id(), "/", "_"), c.ID)
	projFile := filepath.Join(projDir, "bench.csproj")
	outDir := filepath.Join(projDir, "out")
	nugetDir := csNugetDir(r.Dir)

	args := []string{
		t.Dotnet, "publish", projFile,
		"-c", "Release",
		"-r", "win-x64",
		"-p:AssemblyName=" + c.ID,
		"-p:ServerGarbageCollector=false",
		"-p:InvariantGlobalization=true",
		"-p:AllowUnsafeBlocks=false",
		"-p:NuGetAudit=false",
		"-p:DebugType=none",
		"-p:DebugSymbols=false",
	}
	var sidecars []string
	if c.ID == "cs-aot" {
		args = append(args,
			"-p:PublishAot=true",
			"-p:OptimizationPreference=Speed",
			"-p:IlcInstructionSet="+cpu.Cs,
			"-p:StripSymbols=true",
		)
	} else {
		args = append(args, "--self-contained", "false")
		sidecars = []string{c.ID + ".dll", c.ID + ".deps.json", c.ID + ".runtimeconfig.json"}
	}
	args = append(args, "-o", outDir)

	env := append(append([]string(nil), dotnetVersionEnv...),
		"MSBUILDDISABLENODEREUSE=1",
		"DOTNET_CLI_HOME="+filepath.Join(t.Work, "dotnet-home"),
		"NUGET_PACKAGES="+filepath.Join(t.Work, "nuget-cache"),
	)
	unset := append(append([]string(nil), csRuntimeUnset...), "NUGET_PACKAGES", "DOTNET_CLI_HOME")

	isAOT := c.ID == "cs-aot"
	prep := func() error {
		if err := os.RemoveAll(outDir); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(projDir, "obj")); err != nil {
			return err
		}
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			return err
		}
		hasNuget := false
		if ents, err := os.ReadDir(nugetDir); err == nil {
			for _, e := range ents {
				if strings.HasSuffix(strings.ToLower(e.Name()), ".nupkg") {
					hasNuget = true
					break
				}
			}
		}
		if isAOT && !hasNuget {
			return fmt.Errorf("offline NativeAOT NuGet packages absent from %s (the evidence directory is outside the repository; set MK_EVIDENCE)", nugetDir)
		}
		var cfg strings.Builder
		cfg.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n    <clear />\n")
		if hasNuget {
			fmt.Fprintf(&cfg, "    <add key=\"evidence\" value=\"%s\" />\n", xmlEscape(nugetDir))
		}
		cfg.WriteString("  </packageSources>\n</configuration>\n")
		if err := writeIfChanged(filepath.Join(projDir, "NuGet.Config"), cfg.String()); err != nil {
			return err
		}
		csproj := fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net11.0</TargetFramework>
    <RuntimeIdentifier>win-x64</RuntimeIdentifier>
    <ImplicitUsings>disable</ImplicitUsings>
    <Nullable>enable</Nullable>
    <EnableDefaultCompileItems>false</EnableDefaultCompileItems>
    <RestoreConfigFile>$(MSBuildProjectDirectory)\NuGet.Config</RestoreConfigFile>
    <BaseIntermediateOutputPath>$(MSBuildProjectDirectory)\obj\</BaseIntermediateOutputPath>
    <BaseOutputPath>$(MSBuildProjectDirectory)\bin\</BaseOutputPath>
  </PropertyGroup>
  <ItemGroup>
    <Compile Include="%s" />
  </ItemGroup>
</Project>
`, xmlEscape(absSrc))
		return writeIfChanged(projFile, csproj)
	}

	return buildCmd{
		Dir:      projDir,
		Env:      env,
		Unset:    unset,
		Args:     args,
		Deps:     t.version("cs", t.Dotnet),
		Product:  filepath.Join(outDir, c.ID+".exe"),
		Sidecars: sidecars,
		Prep:     prep,
	}, nil
}

// copySidecars copies the managed runtime files a framework-dependent apphost
// needs beside its executable (cs.dll, cs.deps.json, cs.runtimeconfig.json).
func copySidecars(product, exe string, sidecars []string) error {
	srcDir := filepath.Dir(product)
	dstDir := filepath.Dir(exe)
	for _, name := range sidecars {
		if err := copyVendorFile(filepath.Join(srcDir, name), filepath.Join(dstDir, name)); err != nil {
			return fmt.Errorf("copying %s: %w", name, err)
		}
	}
	return nil
}
