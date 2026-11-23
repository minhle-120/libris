//go:build !linux && !windows && !darwin

package deviceinfo

import "context"

// collect for anything else this is cross-compiled to. The release workflow builds
// linux/amd64 and windows/amd64, so this is a compile target rather than a supported
// platform, and the honest answer is that nothing was probed — Detect then adds the
// "no graphics adapter was reported" warning, which is true and not an overstatement.
func collect(_ context.Context, _ Options) []Device { return nil }
