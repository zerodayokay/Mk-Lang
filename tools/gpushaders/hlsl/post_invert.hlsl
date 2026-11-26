StructuredBuffer<uint> src : register(t0, space0);
RWStructuredBuffer<uint> dst : register(u1, space0);

[numthreads(64, 1, 1)]
void CsMain(uint3 tid : SV_DispatchThreadID) {
    uint v = src[tid.x];
    dst[tid.x] = v ^ 0x00FFFFFFu;
}
