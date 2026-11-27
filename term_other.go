//go:build !linux

package main

import "os"

func isTerminalFile(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
