// Package chunkstore is a local, content-addressed chunk store.
//
// On-disk layout under the store root:
//
//	chunks/ab/cd/<64-hex-id>   zstd-compressed chunk data
//	tmp/                       in-progress writes (cleared on Open)
//	quarantine/                chunks that failed verification
//
// Writes are atomic: data goes to a temp file, which is fsynced, renamed into
// place, and then the parent directory is fsynced. A crash at any point leaves
// either no chunk or a complete chunk, never a partial one.
//
// Reads always verify the BLAKE3 hash of the decompressed bytes against the
// chunk ID and return ErrCorrupt on mismatch.
package chunkstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"

	"github.com/KartikeyaBomb/vaultline/internal/hash"
)

// MaxChunkSize bounds the plaintext size of any single chunk. Data chunks are
// at most 256 KiB (FastCDC max), but tree nodes for very large directories can
// be bigger, so the limit is generous.
const MaxChunkSize = 16 << 20

var (
	// ErrNotFound is returned when a chunk is not in the store.
	ErrNotFound = errors.New("chunkstore: chunk not found")
	// ErrCorrupt is returned when stored bytes do not decompress or do not
	// hash to their chunk ID.
	ErrCorrupt = errors.New("chunkstore: chunk corrupt")
	// ErrHashMismatch is returned by Put when data does not hash to id.
	ErrHashMismatch = errors.New("chunkstore: data does not match chunk id")
	// ErrTooLarge is returned by Put when data exceeds MaxChunkSize.
	ErrTooLarge = errors.New("chunkstore: chunk exceeds max size")
)

const (
	dirChunks     = "chunks"
	dirTmp        = "tmp"
	dirQuarantine = "quarantine"
)

// Options configures a Store.
type Options struct {
	// NoSync skips fsync calls. Only for tests and benchmarks: a crash can
	// lose recently written chunks.
	NoSync bool
}

// Store is a content-addressed chunk store rooted at a directory.
// It is safe for concurrent use.
type Store struct {
	root string
	opts Options
	enc  *zstd.Encoder
	dec  *zstd.Decoder

	// testHookMidWrite, if set, is called after part of the temp file has
	// been written and before it is synced and renamed.
	testHookMidWrite func()
}

// Open opens (creating if needed) a store rooted at dir. Leftover temp files
// from interrupted writes are removed.
func Open(dir string, opts Options) (*Store, error) {
	for _, d := range []string{dirChunks, dirTmp, dirQuarantine} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return nil, fmt.Errorf("chunkstore: create %s: %w", d, err)
		}
	}
	if err := clearDir(filepath.Join(dir, dirTmp)); err != nil {
		return nil, fmt.Errorf("chunkstore: clear tmp: %w", err)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(MaxChunkSize), zstd.WithDecoderConcurrency(0))
	if err != nil {
		enc.Close()
		return nil, err
	}
	return &Store{root: dir, opts: opts, enc: enc, dec: dec}, nil
}

// Close releases the store's resources.
func (s *Store) Close() error {
	s.dec.Close()
	return s.enc.Close()
}

// Root returns the store's root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) path(id hash.ChunkID) string {
	h := id.String()
	return filepath.Join(s.root, dirChunks, h[0:2], h[2:4], h)
}

// Put stores data under id. It returns written=false if the chunk was already
// present. Put verifies that data hashes to id.
func (s *Store) Put(id hash.ChunkID, data []byte) (written bool, err error) {
	if len(data) > MaxChunkSize {
		return false, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	if hash.Sum(data) != id {
		return false, fmt.Errorf("%w: %s", ErrHashMismatch, id.Short())
	}
	ok, err := s.Has(id)
	if err != nil || ok {
		return false, err
	}

	compressed := s.enc.EncodeAll(data, make([]byte, 0, len(data)/2+64))

	tmp, err := os.CreateTemp(filepath.Join(s.root, dirTmp), id.Short()+"-*")
	if err != nil {
		return false, fmt.Errorf("chunkstore: create temp: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	half := len(compressed) / 2
	if _, err := tmp.Write(compressed[:half]); err != nil {
		return false, fmt.Errorf("chunkstore: write temp: %w", err)
	}
	if s.testHookMidWrite != nil {
		s.testHookMidWrite()
	}
	if _, err := tmp.Write(compressed[half:]); err != nil {
		return false, fmt.Errorf("chunkstore: write temp: %w", err)
	}
	if !s.opts.NoSync {
		if err := syncFile(tmp); err != nil {
			return false, fmt.Errorf("chunkstore: sync temp: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("chunkstore: close temp: %w", err)
	}

	final := s.path(id)
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("chunkstore: create shard dir: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return false, fmt.Errorf("chunkstore: rename: %w", err)
	}
	committed = true
	if err := s.syncDir(dir); err != nil {
		return false, err
	}
	return true, nil
}

// Get returns the plaintext of chunk id. It returns ErrNotFound if the chunk
// is absent and an error wrapping ErrCorrupt if verification fails.
func (s *Store) Get(id hash.ChunkID) ([]byte, error) {
	compressed, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id.Short())
	}
	if err != nil {
		return nil, fmt.Errorf("chunkstore: read %s: %w", id.Short(), err)
	}
	data, err := s.dec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: decompress: %v", ErrCorrupt, id.Short(), err)
	}
	if got := hash.Sum(data); got != id {
		return nil, fmt.Errorf("%w: %s: content hashes to %s", ErrCorrupt, id.Short(), got.Short())
	}
	return data, nil
}

// Has reports whether chunk id is present. It does not verify contents.
func (s *Store) Has(id hash.ChunkID) (bool, error) {
	_, err := os.Stat(s.path(id))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("chunkstore: stat %s: %w", id.Short(), err)
	}
}

// Delete removes chunk id. Deleting an absent chunk is not an error.
func (s *Store) Delete(id hash.ChunkID) error {
	p := s.path(id)
	err := os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("chunkstore: delete %s: %w", id.Short(), err)
	}
	return s.syncDir(filepath.Dir(p))
}

// Quarantine moves chunk id out of the live store into quarantine/, so it is
// no longer served but can be inspected later.
func (s *Store) Quarantine(id hash.ChunkID) error {
	dst := filepath.Join(s.root, dirQuarantine, id.String())
	err := os.Rename(s.path(id), dst)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, id.Short())
	}
	if err != nil {
		return fmt.Errorf("chunkstore: quarantine %s: %w", id.Short(), err)
	}
	return nil
}

// Iterate calls fn for every chunk ID in the store, in no particular order.
// If fn returns an error, iteration stops and that error is returned.
// Files whose names are not valid chunk IDs are skipped.
func (s *Store) Iterate(fn func(hash.ChunkID) error) error {
	return filepath.WalkDir(filepath.Join(s.root, dirChunks), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		id, perr := hash.Parse(d.Name())
		if perr != nil {
			return nil
		}
		return fn(id)
	})
}

func (s *Store) syncDir(dir string) error {
	if s.opts.NoSync {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("chunkstore: open dir for sync: %w", err)
	}
	defer f.Close()
	if err := syncFile(f); err != nil {
		return fmt.Errorf("chunkstore: sync dir: %w", err)
	}
	return nil
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
