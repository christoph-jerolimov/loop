//go:build windows

package term

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT switches the console to virtual terminal processing so ANSI
// sequences are interpreted (Windows 10 and later). It reports false on
// consoles that cannot, so those get plain text.
func enableVT(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
