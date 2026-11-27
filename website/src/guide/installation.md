---
description: Install the mk compiler from a signed release bundle on Windows, Linux or macOS, or build it from source with Go and LLVM.
---

# Installation

A release bundle holds `mk`, the `clang` and `lld` it was tested with, and the documentation, so it needs neither Go nor LLVM.

## Install

::: code-group

```sh [Linux and macOS]
curl -fsSL https://github.com/zerodayokay/Mk-Lang/releases/latest/download/install.sh | sh
```

```powershell [Windows]
irm https://github.com/zerodayokay/Mk-Lang/releases/latest/download/install.ps1 | iex
```

:::

The script:

1. picks the archive for your machine (Linux on x86-64 or AArch64, Apple silicon Macs, Windows on x86-64);
2. checks the release's signature against the release key written into the script, and the archive against its SHA-256, before it installs anything;
3. unpacks the release under `~/.local/share/mk` (`%LOCALAPPDATA%\mk` on Windows) and puts `mk` on your `PATH`: in `~/.profile` on Linux, `~/.zprofile` and `~/.profile` on macOS, and the user `PATH` on Windows.

Open a new terminal and check the install:

```sh
mk version
```

It prints mk's version and build, and the LLVM it will use.

**Requirements.** On Linux the script needs `curl` or `wget`, and OpenSSL 3 or later or Perl for the signature check. The macOS bundle needs macOS 14 Sonoma or later and is tested on macOS 15. There is no binary release for Intel Macs yet: on one, the script installs nothing and points you to [building from source](#building-from-source).

::: tip Read the script first
To read a script before you run it, download it from the same address, or take it from [`tools/install/`](https://github.com/zerodayokay/Mk-Lang/tree/main/tools/install) at the release's tag.
:::

### A specific version

To install a given release, set `MK_VERSION` before the Windows command, or pass `--version` to the shell script:

::: code-group

```sh [Linux and macOS]
curl -fsSL https://github.com/zerodayokay/Mk-Lang/releases/latest/download/install.sh | sh -s -- --version 0.6.0
```

```powershell [Windows]
$env:MK_VERSION = '0.6.0'
irm https://github.com/zerodayokay/Mk-Lang/releases/latest/download/install.ps1 | iex
```

:::

Later, `mk upgrade` installs a newer signed release and makes it current, and `mk toolchain list` shows the versions you have ([mk upgrade and mk toolchain](/tools/#mk-version-mk-upgrade-and-mk-toolchain)).

### macOS notes

On macOS nothing needs signing or approving: the Apple silicon programs carry the ad hoc signature that hardware requires, and Gatekeeper checks only files marked as downloaded, which `curl` does not mark. A browser does mark them, so install with the script rather than from an archive saved by a browser.

## Building from source

You need Go 1.27 or newer and LLVM 21 or newer (`clang` and `lld`).

```sh
git clone https://github.com/zerodayokay/Mk-Lang
cd Mk-Lang
go build -o mk ./cmd/mk        # on Windows: go build -o mk.exe ./cmd/mk
./mk version
```

Put `mk` on your `PATH`. `mk version` shows the LLVM it found. `--llvm DIR` or the `MK_LLVM` environment variable names the directory; otherwise mk looks in the default install location (`C:\Program Files\LLVM\bin` on Windows, `/usr/lib/llvm-21/bin` on Linux) and on `PATH`, in the order [LLVM and toolchains](/tools/#11-llvm-and-toolchains) gives.

- **Windows**: use the installer from the LLVM releases page.
- **Ubuntu and Debian**: install `clang-21` and `lld-21` from apt.llvm.org.
- **macOS** (Intel, or Apple silicon without the bundle): Homebrew's LLVM 21 works. Homebrew keeps `clang` and `lld` in two directories and mk takes both from one, so link them into a directory of your own and name it with `MK_LLVM`:

```sh
brew install llvm@21 lld@21
mkdir -p ~/.local/llvm-21/bin
ln -sf "$(brew --prefix llvm@21)/bin/clang" "$(brew --prefix lld@21)/bin/ld64.lld" ~/.local/llvm-21/bin/
export MK_LLVM=~/.local/llvm-21/bin      # add this line to ~/.zprofile too
```

To work on the compiler itself (running its tests, changing it) see [building and testing](/development/).

## Platforms

Mk builds programs for:

| Target | Notes |
|---|---|
| Windows on x86-64 | Release bundle available |
| Linux on x86-64 | Release bundle available |
| Linux on AArch64 | Release bundle available; tested natively on an ARM runner |
| macOS 13 or later on Apple silicon | Release bundle available (the bundle itself needs macOS 14) |
| macOS 13 or later on Intel | Build `mk` from source |
| Freestanding x86-64 | No operating system: kernels and boot code |

[Targets](/reference/language/modules-and-build#targets) lists every target and what `mk check` and `mk build` do on each.

## Editor support

`mk lsp` is the language server built into `mk`: diagnostics, hover, completion, go to definition, references, rename, formatting and code actions in any editor that speaks LSP. The VS Code extension adds syntax highlighting and snippets on top. [Editors](/tools/editors) sets it up in VS Code, Neovim, Helix, Emacs, Sublime Text and others.

## Next

Run your first program in the [quick start](./quick-start).
