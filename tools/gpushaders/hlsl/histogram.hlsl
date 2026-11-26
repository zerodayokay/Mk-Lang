StructuredBuffer<uint> keys : register(t0, space0);
RWStructuredBuffer<uint> bins : register(u1, space0);

[numthreads(64, 1, 1)]
void CsMain(uint3 tid : SV_DispatchThreadID) {
    uint b = keys[tid.x] & 63u;
    InterlockedAdd(bins[b], 1u);
}
