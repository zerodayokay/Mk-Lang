// Why: the imported half of the constwiden run test.
// What: negative consts of the signed widths below 64 bits.
// How: exported consts, named from main.mk as consts.C.

export const C: i32 = -4
export const B: i8 = -128
export const H: i16 = -300
