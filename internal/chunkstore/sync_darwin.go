package chunkstore

import (
	"os"
	"syscall"
)

// syncFile flushes f to the storage device.
//
// On macOS, os.File.Sync issues F_FULLFSYNC, which also flushes the drive's
// write cache and costs ~4 ms per call (~18x slower than fsync). Linux nodes
// get plain fsync from os.File.Sync, so we match that here: data survives a
// process crash or kernel panic, but not necessarily a power cut. macOS is a
// development platform for Vaultline, so that trade is acceptable.
func syncFile(f *os.File) error {
	for {
		err := syscall.Fsync(int(f.Fd()))
		if err != syscall.EINTR {
			return err
		}
	}
}
