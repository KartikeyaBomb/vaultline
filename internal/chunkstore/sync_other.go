//go:build !darwin

package chunkstore

import "os"

// syncFile flushes f to the storage device. On Linux this is fsync(2).
func syncFile(f *os.File) error {
	return f.Sync()
}
