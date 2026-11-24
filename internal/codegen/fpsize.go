// Why: --frame-pointers=on lets profilers and the panic report walk the
// stack by frame chain, and --profile size wants the smallest code; both
// are per-function attributes, and both must change the cache keys.
//
// What: fpSizeAttrs, which adds the attributes to a Mk function, and
// fpSizeFacts, which hashes the two settings into the pack key.
//
// How: with Config.FramePointers every Mk function carries
// "frame-pointer"="non-leaf", so rbp (x29 on aarch64) is the frame chain a
// walk reads through every function that calls. A leaf keeps no frame
// record (that keeps hello world small), so a chain walk from inside a leaf
// misses the leaf's caller (its return address is not in the chain), while
// a walk starting at a non-leaf, as the panic report's does, sees every
// frame. With Config.Size every Mk function carries minsize and optsize
// beside the -Oz compile. Both reach the pack key through PackFacts
// (fpSizeFacts), and objKey through the IR. The support pack's functions
// are support.go's.

package codegen

import (
	"mk/internal/llvm"
	"mk/internal/source"
)

// fpSizeAttrs adds the frame-pointer and size attributes cfg asks for.
func fpSizeAttrs(a llvm.Attrs, cfg *Config) llvm.Attrs {
	if cfg.FramePointers {
		a = a.Str("frame-pointer", "non-leaf")
	}
	if cfg.Size {
		a = a.Flag("minsize").Flag("optsize")
	}
	return a
}

// fpSizeFacts hashes the two settings into a pack key, each only when on,
// so a build with neither keeps the keys it had.
func fpSizeFacts(cfg Config, h *source.Hasher) {
	if cfg.FramePointers {
		h.String("frame-pointer=non-leaf")
	}
	if cfg.Size {
		h.String("minsize")
	}
	if cfg.Cover {
		h.String("cover.v2") // cover.go's counters and live CoverCmp
	}
}
