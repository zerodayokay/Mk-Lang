// Why: the web front end needs a TLS identity to negotiate HTTP/2.
// What: WebIdentity reads the certificate chain and private key supplied by
// the operator.
// How: parse PEM into the server's arena and keep the first private key.

import std.crypto
import std.encoding
import std.fs
import std.tls
import std.x509

error BadWebIdentity

WebIdentity(a: &Arena into a, io: Io, chain: str, key: str): fs.Error|x509.Error|encoding.Error|crypto.Error|BadWebIdentity![]tls.Identity from a {
    certs := try x509.ParsePem(a, try fs.ReadText(a, io, chain))
    if certs.Len() == 0 {
        fail BadWebIdentity
    }
    keys := try encoding.ParsePem(a, try fs.ReadText(a, io, key))
    for b in keys {
        if b.label != "PRIVATE KEY" && b.label != "EC PRIVATE KEY" {
            continue
        }
        held := try a.Put(try crypto.ParsePrivateKey(b.der))
        var ids := List[tls.Identity].New(a)
        try ids.Push(tls.Identity(chain: certs.Done()[..], key: &*held))
        return ids.Done()[..]
    }
    fail BadWebIdentity
}
