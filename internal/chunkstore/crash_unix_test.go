//go:build unix

package chunkstore

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/KartikeyaBomb/vaultline/internal/hash"
)

const crashDirEnv = "VAULTLINE_CHUNKSTORE_CRASH_DIR"

func crashData() []byte { return randBytes(42, 256<<10) }

// TestCrashChild is the child half of TestCrashMidWrite. It SIGKILLs itself
// halfway through writing a chunk.
func TestCrashChild(t *testing.T) {
	dir := os.Getenv(crashDirEnv)
	if dir == "" {
		t.Skip("only runs as a subprocess of TestCrashMidWrite")
	}
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.testHookMidWrite = func() {
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {} // wait for the signal to land
	}
	data := crashData()
	s.Put(hash.Sum(data), data)
	t.Fatal("process survived SIGKILL")
}

// TestCrashMidWrite kills a real process in the middle of Put and checks that
// no partial chunk is ever visible and the store recovers cleanly.
func TestCrashMidWrite(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashDirEnv+"="+dir)
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child did not crash: %v", err)
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("child exited without SIGKILL: %v", err)
	}

	// The partial temp file is left behind by the crash...
	leftovers, _ := os.ReadDir(filepath.Join(dir, dirTmp))
	if len(leftovers) == 0 {
		t.Fatal("expected a partial temp file after the crash; hook may not have fired")
	}

	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// ...but the chunk is not visible, and reopening clears the temp file.
	data := crashData()
	id := hash.Sum(data)
	if ok, _ := s.Has(id); ok {
		t.Fatal("partially written chunk is visible after crash")
	}
	if leftovers, _ := os.ReadDir(filepath.Join(dir, dirTmp)); len(leftovers) != 0 {
		t.Fatalf("tmp not cleared after reopen: %d entries", len(leftovers))
	}

	// The store is fully usable afterwards.
	if _, err := s.Put(id, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(id)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get after recovery: err=%v equal=%v", err, bytes.Equal(got, data))
	}
}
