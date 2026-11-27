// Package hash defines ChunkID, the content address of every chunk in Vaultline.
//
// A ChunkID is the BLAKE3-256 digest of a chunk's uncompressed bytes.
package hash

import (
	"encoding/hex"
	"fmt"

	"github.com/zeebo/blake3"
)

// Size is the length of a ChunkID in bytes.
const Size = 32

// ChunkID is the BLAKE3-256 digest of a chunk's plaintext.
type ChunkID [Size]byte

// Sum returns the ChunkID of data.
func Sum(data []byte) ChunkID {
	return ChunkID(blake3.Sum256(data))
}

// String returns the lowercase hex encoding of id.
func (id ChunkID) String() string {
	return hex.EncodeToString(id[:])
}

// Short returns the first 12 hex characters, for logs.
func (id ChunkID) Short() string {
	return id.String()[:12]
}

// IsZero reports whether id is the zero value.
func (id ChunkID) IsZero() bool {
	return id == ChunkID{}
}

// Parse decodes a 64-character hex string into a ChunkID.
func Parse(s string) (ChunkID, error) {
	var id ChunkID
	if len(s) != 2*Size {
		return id, fmt.Errorf("hash: invalid chunk id length %d, want %d", len(s), 2*Size)
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return id, fmt.Errorf("hash: invalid chunk id %q: %w", s, err)
	}
	return id, nil
}

// MarshalText implements encoding.TextMarshaler.
func (id ChunkID) MarshalText() ([]byte, error) {
	return []byte(id.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *ChunkID) UnmarshalText(b []byte) error {
	parsed, err := Parse(string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
