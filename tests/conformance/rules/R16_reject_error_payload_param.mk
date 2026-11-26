// verdict: reject E0605
// rule: R16
// note: a payload str from a parameter could outlive the caller's bytes once the error travels up
error NoSuchKey(key: str)

Find(key: str): NoSuchKey!int {
    fail NoSuchKey(key: key) // <- E0605
}
