package engine

import "path/filepath"

// isWindows is a constant rather than a runtime.GOOS check inline because the same
// comparison is needed by two platform files and a constant folds away.
const isWindows = filepath.Separator == '\\'
