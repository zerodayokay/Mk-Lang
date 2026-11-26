// Why: std.x509's Verify with no pool of its own trusts what Windows trusts,
// so it asks the platform's certificate verifier.
//
// What: CertVerify, CertVerifyWith and CertChainMax, over crypt32.dll's
// externs.
//
// How: crypt32.dll binds lazily (crypt32.lazy.def): it is loaded on the
// first call, from System32 only, never through the import table, so a
// binary that never verifies loads and imports nothing new; the module
// stays loaded. CertVerifyWith asks LazyPresent for each entry first, so a
// crypt32 that cannot be loaded is an error, not a panic. Called only from a pool thread, since chain
// building may fetch an issuer from the network. As Go's crypto/x509
// (verify_windows.go): the leaf as a context, the rest of the chain in a
// memory store, CertGetCertificateChain for server authentication at the
// caller's time with no revocation check, then the SSL policy
// (CERT_CHAIN_POLICY_SSL) with the host name.

import std.core
import std.raw

/// CertChainMax bounds the certificates of one chain (the leaf and what
/// follows it).
export const CertChainMax: int = 16

const PolicySsl: uint = 4
const AuthServer: u32 = 2
// X509_ASN_ENCODING | PKCS_7_ASN_ENCODING.
const CertEncoding: u32 = 0x0001_0001
const StoreMemory: uint = 2
const StoreDeferClose: u32 = 4
const AddAlways: u32 = 4
// FILETIME of the Unix epoch, in 100 ns units since 1601.
const CertEpoch: i64 = 116_444_736_000_000_000
// HostMax is a DNS name's most bytes (253) and its NUL, rounded up.
const HostMax: int = 256
// CERT_CHAIN_CACHE_ONLY_URL_RETRIEVAL | CERT_CHAIN_DISABLE_AUTH_ROOT_AUTO_UPDATE:
// retrieval from the local cache only, and no root list update, so a test
// of the platform path touches no network.
const ChainCacheOnly: u32 = 0x104
// CERT_STRONG_SIGN_OID_INFO_CHOICE: CERT_STRONG_SIGN_PARA names its
// policy by an OID.
const StrongSignOid: u32 = 2
// CERT_CHAIN_STRONG_SIGN_DISABLE_END_CHECK_FLAG: the end certificate's
// public key is not checked against the strong-sign policy.
const StrongSignNoEndCheck: u32 = 1

// crypt32.dll's entries, each with its documented signature (handles and
// the store provider as uint).
extern CertCreateCertificateContext(enc: u32, data: *u8, n: u32): uint
extern CertOpenStore(provider: uint, enc: u32, prov: uint, flags: u32, para: uint): uint
extern CertAddEncodedCertificateToStore(store: uint, enc: u32, data: *u8, n: u32, disposition: u32, out: uint): i32
extern CertGetCertificateChain(engine: uint, cert: uint, time: *u8, store: uint, para: *u8, flags: u32, reserved: uint, ctx: *uint): i32
extern CertVerifyCertificateChainPolicy(policy: uint, chain: uint, para: *u8, status: *u8): i32
extern CertFreeCertificateChain(chain: uint)
extern CertFreeCertificateContext(cert: uint): i32
extern CertCloseStore(store: uint, flags: u32): i32

// CertPresent reports whether every crypt32 entry CertVerifyWith calls can
// be called, loading crypt32.dll on the first ask.
CertPresent(): bool {
    first := LazyPresent(CertCreateCertificateContext) && LazyPresent(CertOpenStore) && LazyPresent(CertAddEncodedCertificateToStore) && LazyPresent(CertGetCertificateChain)
    return first && LazyPresent(CertVerifyCertificateChainPolicy) && LazyPresent(CertFreeCertificateChain) && LazyPresent(CertFreeCertificateContext) && LazyPresent(CertCloseStore)
}

// CertRecords checks chain's records (a 4-byte little-endian length, then
// that many DER bytes, the leaf first) and returns how many there are, or 0
// when one is empty or runs past the end, or there are none or more than
// CertChainMax.
CertRecords(chain: []u8): int {
    var at := 0
    var n := 0
    for at < chain.Len() {
        if chain.Len() - at < 4 || n == CertChainMax {
            return 0
        }
        k := int(chain[at]) | (int(chain[at + 1]) << 8) | (int(chain[at + 2]) << 16) | (int(chain[at + 3]) << 24)
        if k == 0 || k > chain.Len() - at - 4 {
            return 0
        }
        at += 4 + k
        n += 1
    }
    return n
}

/// CertVerify asks the platform to build and check chain (CertRecords'
/// form: the leaf first, then any intermediates, each a 4-byte
/// little-endian length and its DER) for server authentication at now
/// (Unix nanoseconds), with host checked against the leaf when it is not
/// empty. A chain that starts with an empty record (4 zero bytes, which
/// CertRecords refuses) asks for retrieval from the local cache only and no
/// update of the root list; the records after it are the chain
/// (rt.CertVerifyCached, for tests; rt.CertVerify never passes it). value
/// is the SSL policy's verdict, 0 when trusted, else its code
/// (CERT_E_UNTRUSTEDROOT 0x800B0109, CERT_E_EXPIRED 0x800B0101,
/// CERT_E_CN_NO_MATCH 0x800B010F, CERT_E_WRONG_USAGE 0x800B0110,
/// TRUST_E_CERT_SIGNATURE 0x80096004, ...); os is the chain's
/// dwErrorStatus bits. host is a str's bytes (UTF-8). Other: a malformed
/// request (CertRecords 0, a host of 254 bytes or more or holding a NUL), a certificate crypt32
/// cannot parse (os its code, e.g. CRYPT_E_ASN1_BADTAG), crypt32 not
/// loadable, or a call's failure (os GetLastError). Blocking, possibly on
/// the network: a pool thread only. Every context, store and chain it makes
/// is freed on every path. O(chain) calls; allocates nothing of its own
/// (crypt32 allocates and frees its chain).
export CertVerify(chain: []u8, host: []u8, now: i64): OsResult {
    var flags: u32 = 0
    var recs := chain
    if CertCacheMarked(chain) {
        flags = ChainCacheOnly
        recs = chain[4..]
    }
    return CertVerifyWith(recs, host, now, flags)
}

// CertCacheMarked is whether chain starts with an empty record, the mark
// of a cache-only request. O(1).
CertCacheMarked(chain: []u8): bool {
    return chain.Len() >= 4 && (chain[0] | chain[1] | chain[2] | chain[3]) == 0
}

// CertVerifyWith is CertVerify over chain's records with flags for
// CertGetCertificateChain's dwFlags.
CertVerifyWith(chain: []u8, host: []u8, now: i64, flags: u32): OsResult {
    n := CertRecords(chain)
    if n == 0 {
        return FailWith(core.SysErr.Other)
    }
    var host16: [256]u16 = [0; 256]
    if host.Len() > 0 {
        if host.Len() >= HostMax - 2 {
            return FailWith(core.SysErr.Other)
        }
        // host is a str's bytes (rt.CertVerify takes a str), so UTF-8.
        if Wide(PathOf(host), mut host16[..]) < 0 {
            return FailWith(core.SysErr.Other)
        }
    }
    if !CertPresent() {
        return LastError()
    }
    unsafe "crypt32's entries, present and declared with their documented signatures; chain's records are checked by CertRecords" {
        k0 := int(chain[0]) | (int(chain[1]) << 8) | (int(chain[2]) << 16) | (int(chain[3]) << 24)
        leaf := CertCreateCertificateContext(CertEncoding, raw.Offset(raw.Data(chain), 4), Trunc[u32](k0))
        if leaf == 0 {
            return LastError()
        }
        store := CertOpenStore(StoreMemory, 0, 0, StoreDeferClose, 0)
        if store == 0 {
            r := LastError()
            _ = CertFreeCertificateContext(leaf)
            return r
        }
        var at := 4 + k0
        for _ in 1..n {
            k := int(chain[at]) | (int(chain[at + 1]) << 8) | (int(chain[at + 2]) << 16) | (int(chain[at + 3]) << 24)
            if CertAddEncodedCertificateToStore(store, CertEncoding, raw.Offset(raw.Data(chain), at + 4), Trunc[u32](k), AddAlways, 0) == 0 {
                r := LastError()
                _ = CertCloseStore(store, 0)
                _ = CertFreeCertificateContext(leaf)
                return r
            }
            at += 4 + k
        }
        // CERT_CHAIN_PARA, the whole 96 bytes: cbSize, CERT_USAGE_MATCH
        // (AND, one usage: serverAuth) at 8, no issuance policy, no
        // retrieval timeout, freshness or resync, and at 80
        // pStrongSignPara: CERT_STRONG_SIGN_PARA (16 bytes:
        // cbSize, dwInfoChoice CERT_STRONG_SIGN_OID_INFO_CHOICE, pszOID)
        // naming szOID_CERT_STRONG_SIGN_OS_1, so the platform sets
        // CERT_TRUST_HAS_WEAK_SIGNATURE on a SHA-1, MD5 or MD2 signature of
        // its own path whatever the machine's settings say; and at 88
        // dwStrongSignFlags CERT_CHAIN_STRONG_SIGN_DISABLE_END_CHECK_FLAG,
        // so the end certificate's own public key is not held to OS_1's
        // key rules (RSA of 2047 bits or more, ECDSA), under which an
        // Ed25519 or RSA-1024 leaf key is weak. With
        // it the platform's path agrees with the program-pool path:
        // x509.Verify does not hold the leaf's key to a size on either
        // path; Certificate.PublicKey refuses an RSA key under 2048 bits,
        // and tls.Client calls it.
        oid := raw.StrData("1.3.6.1.5.5.7.3.1\u{0}")
        var oids: [1]u64 = [u64(raw.Addr(oid))]
        var strong: [2]u64 = [0; 2]
        sg := raw.PtrCast[u64, u8](raw.DataMut(mut strong[..]))
        Put32(sg, 0, 16)
        Put32(sg, 4, StrongSignOid)
        Put64(sg, 8, u64(raw.Addr(raw.StrData("1.3.6.1.4.1.311.72.1.1\u{0}"))))
        var para: [12]u64 = [0; 12]
        pp := raw.PtrCast[u64, u8](raw.DataMut(mut para[..]))
        Put32(pp, 0, 96)
        Put32(pp, 16, 1)
        Put64(pp, 24, u64(raw.Addr(raw.Data(oids[..]))))
        Put64(pp, 80, u64(raw.Addr(sg)))
        Put32(pp, 88, StrongSignNoEndCheck)
        // FILETIME of now; now / 100 cannot overflow the sum.
        var ft: [1]i64 = [now / 100 + CertEpoch]
        var ctx: uint = 0
        if CertGetCertificateChain(0, leaf, raw.PtrCast[i64, u8](raw.DataMut(mut ft[..])), store, pp, flags, 0, raw.OfMut(mut ctx)) == 0 {
            r := LastError()
            _ = CertCloseStore(store, 0)
            _ = CertFreeCertificateContext(leaf)
            return r
        }
        // SSL_EXTRA_CERT_CHAIN_POLICY_PARA (24 bytes), CERT_CHAIN_POLICY_PARA
        // (16) and CERT_CHAIN_POLICY_STATUS (24).
        var ssl: [3]u64 = [0; 3]
        sp := raw.PtrCast[u64, u8](raw.DataMut(mut ssl[..]))
        Put32(sp, 0, 24)
        Put32(sp, 4, AuthServer)
        if host.Len() > 0 {
            Put64(sp, 16, u64(raw.Addr(raw.Data(host16[..]))))
        }
        var pol: [2]u64 = [0; 2]
        ppol := raw.PtrCast[u64, u8](raw.DataMut(mut pol[..]))
        Put32(ppol, 0, 16)
        Put64(ppol, 8, u64(raw.Addr(sp)))
        var st: [3]u64 = [0; 3]
        pst := raw.PtrCast[u64, u8](raw.DataMut(mut st[..]))
        Put32(pst, 0, 24)
        ok := CertVerifyCertificateChainPolicy(PolicySsl, ctx, ppol, pst)
        var r := LastError()
        if ok != 0 {
            trust := Get32(raw.FromAddr[u8](ctx), 4)
            r = OsResult(value: i64(Get32(pst, 4)), err: core.SysErr.None, os: i64(trust))
        }
        CertFreeCertificateChain(ctx)
        _ = CertCloseStore(store, 0)
        _ = CertFreeCertificateContext(leaf)
        return r
    }
}
