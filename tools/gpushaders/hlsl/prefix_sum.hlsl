RWStructuredBuffer<uint> data : register(u0, space0);

groupshared uint sdata[64];

[numthreads(64, 1, 1)]
void CsMain(uint3 gid : SV_GroupThreadID, uint3 tid : SV_DispatchThreadID) {
    uint local = gid.x;
    sdata[local] = data[tid.x];
    GroupMemoryBarrierWithGroupSync();
    for (uint offset = 1; offset < 64; offset <<= 1) {
        uint add = 0;
        if (local >= offset) {
            add = sdata[local - offset];
        }
        GroupMemoryBarrierWithGroupSync();
        sdata[local] += add;
        GroupMemoryBarrierWithGroupSync();
    }
    data[tid.x] = sdata[local];
}
