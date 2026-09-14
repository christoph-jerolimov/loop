//go:build !windows

package term

import "os"

// enableVT is a no-op outside Windows: every Unix terminal speaks ANSI.
func enableVT(*os.File) bool { return true }
