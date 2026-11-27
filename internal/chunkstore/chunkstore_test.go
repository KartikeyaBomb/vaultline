package chunkstore

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/KartikeyaBomb/vaultline/internal/hash"
)

func randBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustPut(t *testing.T, s *Store, data []byte) hash.ChunkID {
	t.Helper()
	id := hash.Sum(data)
	if _, err := s.Put(id, data); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRoundTrip(t *testing.T) {
	s := openTest(t)
	cases := map[string][]byte{
		"empty":        {},
		"one byte":     {0x42},
		"compressible": bytes.Repeat([]byte("vaultline "), 6554),
		"random 64k":   randBytes(1, 64<<10),
		"random 256k":  randBytes(2, 256<<10),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			id := mustPut(t, s, data)
			got, err := s.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(data))
			}
		})
	}
}

func TestPutIdempotent(t *testing.T) {
	s := openTest(t)
	data := randBytes(3, 1000)
	id := hash.Sum(data)
	written, err := s.Put(id, data)
	if err != nil || !written {
		t.Fatalf("first put: written=%v err=%v", written, err)
	}
	written, err = s.Put(id, data)
	if err != nil || written {
		t.Fatalf("second put: written=%v err=%v, want written=false", written, err)
	}
}

func TestPutRejectsHashMismatch(t *testing.T) {
	s := openTest(t)
	_, err := s.Put(hash.Sum([]byte("a")), []byte("b"))
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("got %v, want ErrHashMismatch", err)
	}
	if ok, _ := s.Has(hash.Sum([]byte("a"))); ok {
		t.Fatal("mismatched chunk was stored")
	}
}

func TestPutRejectsTooLarge(t *testing.T) {
	s := openTest(t)
	data := make([]byte, MaxChunkSize+1)
	if _, err := s.Put(hash.Sum(data), data); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

func TestGetMissing(t *testing.T) {
	s := openTest(t)
	if _, err := s.Get(hash.Sum([]byte("nope"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestCorruptionDetected(t *testing.T) {
	corruptions := map[string]func(t *testing.T, path string){
		"flipped byte": func(t *testing.T, path string) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			b[len(b)/2] ^= 0xff
			writeFile(t, path, b)
		},
		"truncated": func(t *testing.T, path string) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, b[:len(b)/3])
		},
		"valid zstd, wrong content": func(t *testing.T, path string) {
			s, err := Open(t.TempDir(), Options{NoSync: true})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			writeFile(t, path, s.enc.EncodeAll([]byte("some other chunk"), nil))
		},
		"empty file": func(t *testing.T, path string) {
			writeFile(t, path, nil)
		},
	}
	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			s := openTest(t)
			id := mustPut(t, s, randBytes(4, 32<<10))
			corrupt(t, s.path(id))
			if _, err := s.Get(id); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("got %v, want ErrCorrupt", err)
			}
		})
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantine(t *testing.T) {
	s := openTest(t)
	id := mustPut(t, s, randBytes(5, 4096))
	if err := s.Quarantine(id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(id); ok {
		t.Fatal("quarantined chunk still present")
	}
	if _, err := os.Stat(filepath.Join(s.Root(), dirQuarantine, id.String())); err != nil {
		t.Fatalf("quarantined file missing: %v", err)
	}
	if err := s.Quarantine(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second quarantine: got %v, want ErrNotFound", err)
	}
	// A good copy can be written again after quarantine.
	mustPut(t, s, randBytes(5, 4096))
	if _, err := s.Get(id); err != nil {
		t.Fatal(err)
	}
}

func TestDelete(t *testing.T) {
	s := openTest(t)
	id := mustPut(t, s, randBytes(6, 100))
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(id); ok {
		t.Fatal("deleted chunk still present")
	}
	if err := s.Delete(id); err != nil {
		t.Fatalf("deleting absent chunk: %v", err)
	}
}

func TestIterate(t *testing.T) {
	s := openTest(t)
	want := map[hash.ChunkID]bool{}
	for i := range 50 {
		want[mustPut(t, s, randBytes(uint64(100+i), 512))] = true
	}
	// A stray file in the shard tree must be ignored.
	writeFile(t, filepath.Join(s.Root(), dirChunks, "README"), []byte("not a chunk"))

	got := map[hash.ChunkID]bool{}
	if err := s.Iterate(func(id hash.ChunkID) error {
		got[id] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("iterated %d chunks, want %d", len(got), len(want))
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("missing %s", id.Short())
		}
	}

	stop := errors.New("stop")
	n := 0
	err := s.Iterate(func(hash.ChunkID) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("early stop: n=%d err=%v", n, err)
	}
}

func TestConcurrentPuts(t *testing.T) {
	s := openTest(t)
	const workers, perWorker = 8, 50
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				// Half the chunks are shared across workers, half are unique.
				seed := uint64(i)
				if i%2 == 1 {
					seed = uint64(1000 + w*perWorker + i)
				}
				data := randBytes(seed, 2048)
				if _, err := s.Put(hash.Sum(data), data); err != nil {
					errs <- fmt.Errorf("worker %d: %w", w, err)
					return
				}
				if _, err := s.Get(hash.Sum(data)); err != nil {
					errs <- fmt.Errorf("worker %d get: %w", w, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	n := 0
	s.Iterate(func(hash.ChunkID) error { n++; return nil })
	if want := perWorker/2 + workers*perWorker/2; n != want {
		t.Fatalf("store has %d chunks, want %d", n, want)
	}
}

func TestOpenClearsTmp(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	writeFile(t, filepath.Join(dir, dirTmp, "leftover"), []byte("partial"))
	s, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entries, _ := os.ReadDir(filepath.Join(dir, dirTmp))
	if len(entries) != 0 {
		t.Fatalf("tmp not cleared: %d entries", len(entries))
	}
}

func BenchmarkPut(b *testing.B) {
	s, err := Open(b.TempDir(), Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	data := randBytes(7, 64<<10)
	b.SetBytes(int64(len(data)))
	for i := 0; b.Loop(); i++ {
		data[0], data[1], data[2], data[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
		if _, err := s.Put(hash.Sum(data), data); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPutParallel shows whether concurrent writers amortize fsync cost.
func BenchmarkPutParallel(b *testing.B) {
	s, err := Open(b.TempDir(), Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	var seed atomic.Uint64
	b.SetBytes(64 << 10)
	b.RunParallel(func(pb *testing.PB) {
		data := randBytes(seed.Add(1)<<32, 64<<10)
		for i := 0; pb.Next(); i++ {
			data[0], data[1], data[2], data[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
			if _, err := s.Put(hash.Sum(data), data); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkGet(b *testing.B) {
	s, err := Open(b.TempDir(), Options{NoSync: true})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	data := randBytes(8, 64<<10)
	id := hash.Sum(data)
	if _, err := s.Put(id, data); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := s.Get(id); err != nil {
			b.Fatal(err)
		}
	}
}
