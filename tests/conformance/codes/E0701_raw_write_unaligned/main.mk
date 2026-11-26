// verdict: reject E0701
// note: raw.WriteUnaligned, as raw.ReadUnaligned, takes an integer T only, stored at alignment 1 at any offset: a float, a struct or a record is E0701 at the call; a float is stored through its bits; pkg.mk lists k in unsafe_modules
import k

Run() {
    k.Use()
}
