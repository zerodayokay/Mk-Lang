# Regenerate the Linux `c.posix` views

Use Clang 21.1.8 and separate Debian bookworm sysroots for `amd64` and `arm64`.
Fetch the exact files below and verify their SHA-256 hashes before extraction.
The hashes come from the package download pages for `libc6`, `libc6-dev`, and
`linux-libc-dev`, under each architecture in the [Debian package index](https://packages.debian.org/bookworm/).

The Debian mirror paths are `https://deb.debian.org/debian/pool/main/g/glibc/`
for the glibc packages and
`https://deb.debian.org/debian-security/pool/updates/main/l/linux/` for the
Linux UAPI headers. If a mirror no longer carries the pinned revision, find
that revision in the [Debian snapshot archive](https://snapshot.debian.org/)
and verify the same checksum.

```text
ba4f88f73dbc3ae9055f3c20f4523bfdbaf1ad13ff95e258924f77d20b4fbedf  libc6_2.36-9+deb12u14_amd64.deb
01f4330719fd4f65580e16ea5a0527f372fca750e8f588d26deaf09f2d3b1cf4  libc6_2.36-9+deb12u14_arm64.deb
0218fc2befcd784c1b0c6292c0a137ce89fad054efaa579ad083bee0f2c01aae  libc6-dev_2.36-9+deb12u14_amd64.deb
8bc395bf2ee9939f1a6ae395f974cffdcf541d98f99adfe89ce7e4bef36d159e  libc6-dev_2.36-9+deb12u14_arm64.deb
e0632f8e55ec0d2d1c56f9c65a8bcb64ad5d79add297266bc89c616dfef9a7fe  linux-libc-dev_6.1.192-2_amd64.deb
16277eb380d940c7f24eeb3876906919214eda13e5e97f5f51bd5ca96aae0fbd  linux-libc-dev_6.1.192-2_arm64.deb
```

Extract `./usr/include` from each `libc6-dev` and `linux-libc-dev` package
and `./lib/<multiarch>/libc.so.6` from the matching `libc6` package. Use
`x86_64-linux-gnu` and `aarch64-linux-gnu` as the multiarch names. On Linux,
pass each package through `dpkg-deb --fsys-tarfile` into `tar -x -C <root>`
with the listed member. On Windows, unpack the `data.tar.*` member with
`llvm-ar x` and extract the same path with `tar -xf` into the sysroot.

From the repository root, pass the two extracted directories to the generator.

```sh
go run ./tools/sysview -glibc-x86_64 /path/to/amd64 -glibc-aarch64 /path/to/arm64
go run ./tools/sysview -glibc-x86_64 /path/to/amd64 -glibc-aarch64 /path/to/arm64 -check
```

The generated `views.txt` beside each archive records the header set,
Clang version, glibc header and UAPI versions, `libc.so.6` hash, and archive
identity. It also lists declarations dropped during generation.

The generator reads the headers with `_POSIX_C_SOURCE=200809L`. It gives
automatic link entries to default public `libc.so.6` function exports through
`GLIBC_2.34`, including their symbol versions. Functions whose headers
redirect to a different linker symbol and functions that need inline shims
are recorded as dropped. Macro-only interfaces such as the `errno` lvalue
are outside the view. The `__errno_location` function is available in both
archives. Variadic functions such as `open` and `printf` remain variadic.
