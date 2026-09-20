//go:build !windows

package ui

import "os"

// enableVirtualTerminal is a no-op outside Windows: terminals interpret ANSI
// escapes natively.
func enableVirtualTerminal(*os.File) bool { return true }
