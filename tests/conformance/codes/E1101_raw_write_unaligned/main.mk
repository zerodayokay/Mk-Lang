// verdict: reject E1101
// note: raw.WriteUnaligned needs unsafe, as raw.Write and raw.ReadUnaligned do, called or named as a value; pkg.mk lists k in unsafe_modules
import k

Run() {
    k.Use()
}
