// httpparse, C#, std: the task of README.md, a strict RFC 9112 HTTP/1.1 request
// parser over a splitmix64-generated batch of valid and malformed requests
// (GET/POST/PUT/DELETE, paths and query strings, headers, fixed Content-Length
// and chunked bodies, plus 10 malformed classes). Uses ReadOnlySpan<byte>.IndexOf
// for line and delimiter scans, reused header array and chunked body buffer,
// and wrapping ulong FNV-1a digest. Bounds-checked (class bounds). No unsafe,
// no MethodImpl, no Unsafe.*, no System.Runtime.Intrinsics, no Vector<T>.
// Usage: httpparse <num_requests> <rounds> <seed>.
using System;
using System.Globalization;
using System.Text;

internal static class Program
{
    private const ulong FnvBasis = 0xcbf29ce484222325UL;
    private const ulong FnvPrime = 0x100000001b3UL;
    private const int MaxHeaders = 64;

    private struct Rng
    {
        public ulong S;
        public ulong Next()
        {
            S = unchecked(S + 0x9e3779b97f4a7c15UL);
            ulong z = S;
            z = unchecked((z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL);
            z = unchecked((z ^ (z >> 27)) * 0x94d049bb133111ebUL);
            return z ^ (z >> 31);
        }
    }

    private sealed class ByteBuf
    {
        public byte[] Data;
        public int Len;

        public ByteBuf(int cap)
        {
            Data = new byte[cap];
        }

        private void Ensure(int add)
        {
            int need = Len + add;
            if (need > Data.Length)
            {
                int cap = Data.Length;
                while (cap < need) cap *= 2;
                Array.Resize(ref Data, cap);
            }
        }

        public void Push(byte b)
        {
            Ensure(1);
            Data[Len++] = b;
        }

        public void Append(ReadOnlySpan<byte> s)
        {
            if (s.Length == 0) return;
            Ensure(s.Length);
            s.CopyTo(Data.AsSpan(Len));
            Len += s.Length;
        }

        public void AppendAscii(string s)
        {
            Ensure(s.Length);
            for (int i = 0; i < s.Length; i++)
            {
                Data[Len++] = (byte)s[i];
            }
        }

        public void AppendU64(ulong v)
        {
            if (v == 0)
            {
                Push((byte)'0');
                return;
            }
            Ensure(24);
            int start = Len;
            while (v > 0)
            {
                Data[Len++] = (byte)('0' + (v % 10));
                v /= 10;
            }
            Array.Reverse(Data, start, Len - start);
        }

        public void AppendHex(int v)
        {
            ReadOnlySpan<byte> hex = "0123456789abcdef"u8;
            if (v == 0)
            {
                Push((byte)'0');
                return;
            }
            Ensure(16);
            int start = Len;
            while (v > 0)
            {
                Data[Len++] = hex[v & 15];
                v >>= 4;
            }
            Array.Reverse(Data, start, Len - start);
        }
    }

    private static (byte[] Data, int[] Starts) BuildBatch(int n, ulong seed)
    {
        Rng rng = new Rng { S = seed };
        ByteBuf data = new ByteBuf(Math.Max(4096, n * 384));
        int[] starts = new int[n + 1];
        for (int i = 0; i < n; i++)
        {
            starts[i] = data.Len;
            ulong r = rng.Next();
            if (r % 20 == 0)
            {
                switch ((r >> 8) % 10)
                {
                    case 0: data.Append("GET /api/v1/bad HTTP/2.0\r\nHost: api.example.com\r\n\r\n"u8); break;
                    case 1: data.Append("GET /api/v1/bad HTTP/1.1\nHost: api.example.com\r\n\r\n"u8); break;
                    case 2: data.Append("GET /api/v1/bad HTTP/1.1\r\nHost : api.example.com\r\n\r\n"u8); break;
                    case 3: data.Append("GET /api/v1/bad HTTP/1.1\r\nUser-Agent: bench/1.0\r\n\r\n"u8); break;
                    case 4: data.Append("POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n0\r\n\r\n"u8); break;
                    case 5: data.Append("POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\nabcd"u8); break;
                    case 6: data.Append("POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 12abc\r\n\r\nabcd"u8); break;
                    case 7: data.Append("POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 20\r\n\r\nshort"u8); break;
                    case 8: data.Append("POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\nabcd\r\n0\r\n\r\n"u8); break;
                    default: data.Append("POST /api/v1/bad HTTP/1.1\r\nHost: api.example.com\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n"u8); break;
                }
                continue;
            }
            ulong m = (r >> 5) % 4;
            switch (m)
            {
                case 0: data.Append("GET "u8); break;
                case 1: data.Append("POST "u8); break;
                case 2: data.Append("PUT "u8); break;
                default: data.Append("DELETE "u8); break;
            }
            data.Append("/api/v1/users/"u8);
            data.AppendU64((r >> 8) % 10000);
            data.Append("/items/"u8);
            data.AppendU64((r >> 22) % 100000);
            if (((r >> 40) & 1) == 1)
            {
                data.Append("?page="u8);
                data.AppendU64((r >> 41) % 100);
                data.Append("&limit="u8);
                data.AppendU64(10 + ((r >> 48) % 90));
                data.Append("&sort=asc"u8);
            }
            data.Append(" HTTP/1.1\r\nHost: api"u8);
            data.AppendU64(r & 7);
            data.Append(".example.com\r\nUser-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) BenchAgent/1.0\r\nAccept: application/json, text/plain;q=0.9, */*;q=0.8\r\nAccept-Encoding: gzip, deflate, br\r\n"u8);
            if (((r >> 39) & 1) == 1)
            {
                data.Append("Connection: close\r\n"u8);
            }
            else
            {
                data.Append("Connection: keep-alive\r\n"u8);
            }
            ulong r2 = rng.Next();
            data.Append("X-Request-Id: req-"u8);
            data.AppendU64(r2 % 100000000);
            data.Append("\r\n"u8);
            if (m == 1 || m == 2)
            {
                data.Append("Content-Type: application/json\r\n"u8);
                if (((r2 >> 30) & 1) == 1)
                {
                    data.Append("Transfer-Encoding: chunked\r\n\r\n"u8);
                    int nchunks = 2 + (int)((r2 >> 32) % 4);
                    for (int ci = 0; ci < nchunks; ci++)
                    {
                        int shift = ci * 7;
                        int clen = 16 + (int)(((r2 >> shift) ^ ((ulong)ci * 13UL)) % 64);
                        data.AppendHex(clen);
                        data.Append("\r\n"u8);
                        for (int j = 0; j < clen; j++)
                        {
                            data.Push((byte)('a' + ((ci * 7 + j) % 26)));
                        }
                        data.Append("\r\n"u8);
                    }
                    data.Append("0\r\n\r\n"u8);
                }
                else
                {
                    int blen = 32 + (int)((r2 >> 32) % 224);
                    data.Append("Content-Length: "u8);
                    data.AppendU64((ulong)blen);
                    data.Append("\r\n\r\n"u8);
                    int off = (int)(r2 % 26);
                    for (int j = 0; j < blen; j++)
                    {
                        data.Push((byte)('a' + ((j + off) % 26)));
                    }
                }
            }
            else
            {
                data.Append("\r\n"u8);
            }
        }
        starts[n] = data.Len;
        return (data.Data, starts);
    }

    private struct SliceSpan { public int Off; public int Len; }
    private struct HeaderSpan { public SliceSpan Name; public SliceSpan Value; }
    private struct Parsed
    {
        public SliceSpan Method;
        public SliceSpan Path;
        public SliceSpan Query;
        public int NHdrs;
        public int BodyOff;
        public int BodyLen;
        public bool BodyFromBuf;
        public bool Chunked;
        public bool KeepAlive;
    }

    private static bool IsTchar(byte b)
    {
        if ((b >= (byte)'0' && b <= (byte)'9') || (b >= (byte)'A' && b <= (byte)'Z') || (b >= (byte)'a' && b <= (byte)'z'))
        {
            return true;
        }
        return b switch
        {
            (byte)'!' or (byte)'#' or (byte)'$' or (byte)'%' or (byte)'&' or (byte)'\'' or (byte)'*'
            or (byte)'+' or (byte)'-' or (byte)'.' or (byte)'^' or (byte)'_' or (byte)'`' or (byte)'|' or (byte)'~' => true,
            _ => false,
        };
    }

    private static byte LowerAscii(byte b) => (b >= (byte)'A' && b <= (byte)'Z') ? (byte)(b + 32) : b;

    private static bool EqIgnoreAscii(ReadOnlySpan<byte> a, ReadOnlySpan<byte> lit)
    {
        if (a.Length != lit.Length) return false;
        for (int i = 0; i < a.Length; i++)
        {
            if (LowerAscii(a[i]) != lit[i]) return false;
        }
        return true;
    }

    private static int HexVal(byte b)
    {
        if (b >= (byte)'0' && b <= (byte)'9') return b - (byte)'0';
        if (b >= (byte)'a' && b <= (byte)'f') return b - (byte)'a' + 10;
        if (b >= (byte)'A' && b <= (byte)'F') return b - (byte)'A' + 10;
        return -1;
    }

    private static bool ParseRequest(ReadOnlySpan<byte> buf, HeaderSpan[] hdrs, ByteBuf bodyBuf, ref Parsed p)
    {
        int n = buf.Length;
        int nl = buf.IndexOf((byte)'\n');
        if (nl < 1 || buf[nl - 1] != (byte)'\r') return false;
        int lineEnd = nl - 1;
        int sp1 = buf.Slice(0, lineEnd).IndexOf((byte)' ');
        if (sp1 <= 0) return false;
        for (int i = 0; i < sp1; i++)
        {
            if (!IsTchar(buf[i])) return false;
        }
        int rest1 = sp1 + 1;
        int sp2Rel = buf.Slice(rest1, lineEnd - rest1).IndexOf((byte)' ');
        if (sp2Rel <= 0) return false;
        int sp2 = rest1 + sp2Rel;
        if (buf[rest1] != (byte)'/') return false;

        int qpos = -1;
        for (int i = rest1; i < sp2; i++)
        {
            byte c = buf[i];
            if (c < 0x21 || c > 0x7e) return false;
            if (c == (byte)'?' && qpos < 0) qpos = i;
        }
        int verStart = sp2 + 1;
        if (!buf.Slice(verStart, lineEnd - verStart).SequenceEqual("HTTP/1.1"u8)) return false;

        p.Method = new SliceSpan { Off = 0, Len = sp1 };
        if (qpos >= 0)
        {
            p.Path = new SliceSpan { Off = rest1, Len = qpos - rest1 };
            p.Query = new SliceSpan { Off = qpos + 1, Len = sp2 - (qpos + 1) };
        }
        else
        {
            p.Path = new SliceSpan { Off = rest1, Len = sp2 - rest1 };
            p.Query = default;
        }

        int pos = nl + 1;
        int nhdrs = 0;
        bool hasHost = false;
        bool hasCl = false;
        int clVal = 0;
        bool chunked = false;
        bool keepAlive = true;
        bool headDone = false;

        while (pos < n)
        {
            int lnRel = buf.Slice(pos).IndexOf((byte)'\n');
            if (lnRel < 1) return false;
            int lnEnd = pos + lnRel - 1;
            if (buf[lnEnd] != (byte)'\r') return false;
            if (lnEnd == pos)
            {
                pos += lnRel + 1;
                headDone = true;
                break;
            }
            if (buf[pos] == (byte)' ' || buf[pos] == (byte)'\t') return false;
            int colonRel = buf.Slice(pos, lnEnd - pos).IndexOf((byte)':');
            if (colonRel <= 0) return false;
            int nameStart = pos;
            int nameLen = colonRel;
            for (int i = 0; i < nameLen; i++)
            {
                if (!IsTchar(buf[nameStart + i])) return false;
            }
            int vs = nameStart + nameLen + 1;
            int ve = lnEnd;
            while (vs < ve && (buf[vs] == (byte)' ' || buf[vs] == (byte)'\t')) vs++;
            while (ve > vs && (buf[ve - 1] == (byte)' ' || buf[ve - 1] == (byte)'\t')) ve--;
            for (int i = vs; i < ve; i++)
            {
                byte c = buf[i];
                if ((c < 0x20 && c != (byte)'\t') || c == 0x7f) return false;
            }
            if (nhdrs >= MaxHeaders) return false;
            hdrs[nhdrs] = new HeaderSpan
            {
                Name = new SliceSpan { Off = nameStart, Len = nameLen },
                Value = new SliceSpan { Off = vs, Len = ve - vs },
            };
            nhdrs++;

            ReadOnlySpan<byte> name = buf.Slice(nameStart, nameLen);
            ReadOnlySpan<byte> val = buf.Slice(vs, ve - vs);
            if (nameLen == 4 && EqIgnoreAscii(name, "host"u8))
            {
                if (hasHost || val.Length == 0) return false;
                hasHost = true;
            }
            else if (nameLen == 14 && EqIgnoreAscii(name, "content-length"u8))
            {
                if (val.Length == 0) return false;
                int v = 0;
                for (int i = 0; i < val.Length; i++)
                {
                    byte c = val[i];
                    if (c < (byte)'0' || c > (byte)'9') return false;
                    v = v * 10 + (c - (byte)'0');
                    if (v > 1_073_741_824) return false;
                }
                if (hasCl && v != clVal) return false;
                hasCl = true;
                clVal = v;
            }
            else if (nameLen == 17 && EqIgnoreAscii(name, "transfer-encoding"u8))
            {
                if (chunked || !EqIgnoreAscii(val, "chunked"u8)) return false;
                chunked = true;
            }
            else if (nameLen == 10 && EqIgnoreAscii(name, "connection"u8))
            {
                if (EqIgnoreAscii(val, "close"u8)) keepAlive = false;
                else if (EqIgnoreAscii(val, "keep-alive"u8)) keepAlive = true;
            }
            pos += lnRel + 1;
        }

        if (!headDone || !hasHost || (hasCl && chunked)) return false;
        p.NHdrs = nhdrs;
        p.Chunked = chunked;
        p.KeepAlive = keepAlive;

        if (chunked)
        {
            bodyBuf.Len = 0;
            while (true)
            {
                if (pos >= n) return false;
                int szRel = buf.Slice(pos).IndexOf((byte)'\n');
                if (szRel < 1) return false;
                int szEnd = pos + szRel - 1;
                if (buf[szEnd] != (byte)'\r') return false;
                int sz = 0;
                int digits = 0;
                for (int k = pos; k < szEnd; k++)
                {
                    byte c = buf[k];
                    if (c == (byte)';') break;
                    int hv = HexVal(c);
                    if (hv < 0) return false;
                    sz = sz * 16 + hv;
                    if (sz > 16_777_216) return false;
                    digits++;
                }
                if (digits == 0) return false;
                pos += szRel + 1;
                if (sz == 0)
                {
                    if (pos + 2 != n || buf[pos] != (byte)'\r' || buf[pos + 1] != (byte)'\n') return false;
                    break;
                }
                if (pos + sz + 2 > n) return false;
                bodyBuf.Append(buf.Slice(pos, sz));
                pos += sz;
                if (buf[pos] != (byte)'\r' || buf[pos + 1] != (byte)'\n') return false;
                pos += 2;
            }
            p.BodyOff = 0;
            p.BodyLen = bodyBuf.Len;
            p.BodyFromBuf = true;
        }
        else if (hasCl)
        {
            if (n - pos != clVal) return false;
            p.BodyOff = pos;
            p.BodyLen = clVal;
            p.BodyFromBuf = false;
        }
        else
        {
            if (pos != n) return false;
            p.BodyOff = 0;
            p.BodyLen = 0;
            p.BodyFromBuf = false;
        }
        return true;
    }

    private static ulong FnvByte(ulong h, byte b) => unchecked((h ^ b) * FnvPrime);

    private static ulong FnvBytes(ulong h, ReadOnlySpan<byte> s)
    {
        for (int i = 0; i < s.Length; i++)
        {
            h = unchecked((h ^ s[i]) * FnvPrime);
        }
        return h;
    }

    private static ulong FnvBytesLower(ulong h, ReadOnlySpan<byte> s)
    {
        for (int i = 0; i < s.Length; i++)
        {
            h = unchecked((h ^ LowerAscii(s[i])) * FnvPrime);
        }
        return h;
    }

    public static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: httpparse <num_requests> <rounds> <seed>");
            return 2;
        }
        int numReq = int.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong seed = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        (byte[] data, int[] starts) = BuildBatch(numReq, seed);
        HeaderSpan[] hdrs = new HeaderSpan[MaxHeaders];
        ByteBuf bodyBuf = new ByteBuf(1024);
        Parsed parsed = default;

        long okCnt = 0, errCnt = 0, hdrCnt = 0, bodyCnt = 0, chunkedCnt = 0, kaCnt = 0;
        ulong h = FnvBasis;

        for (int round = 0; round < rounds; round++)
        {
            for (int i = 0; i < numReq; i++)
            {
                ReadOnlySpan<byte> reqBuf = data.AsSpan(starts[i], starts[i + 1] - starts[i]);
                if (ParseRequest(reqBuf, hdrs, bodyBuf, ref parsed))
                {
                    okCnt++;
                    hdrCnt += parsed.NHdrs;
                    bodyCnt += parsed.BodyLen;
                    if (parsed.Chunked) chunkedCnt++;
                    if (parsed.KeepAlive) kaCnt++;
                    h = FnvBytes(h, reqBuf.Slice(parsed.Method.Off, parsed.Method.Len));
                    h = FnvByte(h, 1);
                    h = FnvBytes(h, reqBuf.Slice(parsed.Path.Off, parsed.Path.Len));
                    h = FnvByte(h, 2);
                    h = FnvBytes(h, reqBuf.Slice(parsed.Query.Off, parsed.Query.Len));
                    h = FnvByte(h, 3);
                    for (int k = 0; k < parsed.NHdrs; k++)
                    {
                        HeaderSpan hs = hdrs[k];
                        h = FnvBytesLower(h, reqBuf.Slice(hs.Name.Off, hs.Name.Len));
                        h = FnvByte(h, 4);
                        h = FnvBytes(h, reqBuf.Slice(hs.Value.Off, hs.Value.Len));
                        h = FnvByte(h, 5);
                    }
                    if (parsed.BodyFromBuf)
                    {
                        h = FnvBytes(h, bodyBuf.Data.AsSpan(0, bodyBuf.Len));
                    }
                    else
                    {
                        h = FnvBytes(h, reqBuf.Slice(parsed.BodyOff, parsed.BodyLen));
                    }
                    h = FnvByte(h, 6);
                }
                else
                {
                    errCnt++;
                    h = FnvByte(h, 0xff);
                    h = FnvByte(h, (byte)(i % 251));
                }
            }
        }

        Console.WriteLine(string.Create(CultureInfo.InvariantCulture,
            $"ok={okCnt} err={errCnt} hdrs={hdrCnt} body={bodyCnt} chunked={chunkedCnt} keepalive={kaCnt} fnv={h:x16}"));
        return 0;
    }
}
