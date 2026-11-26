// Why: an origin's alternative services affect routing across requests.
// What: Alt-Svc grammar, authority restrictions and finite freshness tests.
// How: feed the internal parser wire-shaped fields and check its chosen ports.

test "Alt-Svc keeps same-host h3, subtracts Age and honors quoted escapes" {
    r := ParseAltSvc("h2=\":443\", h3=\"other.test:9443\", h3=\"ExAmPlE.TeSt:8443\"; x=\"q,\\\"z\"; ma=\"20\", h3=\":44\\3\"", "example.test", 10).Must()
    Assert(r.n == 2)
    Assert(r.ports[0].port == 8443 && r.ports[0].age == 10)
    Assert(r.ports[1].port == 443 && r.ports[1].age == 86390)
}

test "Alt-Svc clear takes precedence and alternatives are bounded" {
    r := ParseAltSvc("h3=\":1\",h3=\":2\",h3=\":3\",h3=\":4\",h3=\":5\",h3=\":6\",h3=\":7\",h3=\":8\",h3=\":9\",clear", "example.test", 0).Must()
    Assert(r.clear)
    Assert(r.n == 8)
    Assert(r.ports[7].port == 8)
}

test "Alt-Svc discards an invalid quote and ignores unusable ports or ages" {
    Assert(ParseAltSvc("h3=\":443\"; unknown=\"unterminated", "example.test", 0).IsNone())
    r := ParseAltSvc("h3=\":65536\",h3=\":0\",h3=\":443\";ma=60", "example.test", 60).Must()
    Assert(r.n == 0)
    Assert(AltSeconds("9999999999999999999999999".AsBytes()).Must() == AltMaxAge)
}

test "Alt-Svc matches a bracketed IPv6 origin" {
    r := ParseAltSvc("h3=\"[::1]:443\",h3=\"[::2]:8443\"", "::1", 0).Must()
    Assert(r.n == 1 && r.ports[0].port == 443)
}
