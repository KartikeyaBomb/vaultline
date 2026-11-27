package hash

import (
	"encoding/json"
	"testing"
)

func TestSumDeterministic(t *testing.T) {
	a := Sum([]byte("vaultline"))
	b := Sum([]byte("vaultline"))
	if a != b {
		t.Fatalf("same input produced different ids: %s vs %s", a, b)
	}
	if a == Sum([]byte("vaultlinf")) {
		t.Fatal("different inputs produced the same id")
	}
}

func TestKnownVector(t *testing.T) {
	// BLAKE3-256 of the empty input, from the BLAKE3 spec test vectors.
	const want = "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
	if got := Sum(nil).String(); got != want {
		t.Fatalf("Sum(nil) = %s, want %s", got, want)
	}
}

func TestParseRoundTrip(t *testing.T) {
	id := Sum([]byte("round trip"))
	parsed, err := Parse(id.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != id {
		t.Fatalf("round trip mismatch: %s vs %s", parsed, id)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	for _, s := range []string{"", "abc", Sum(nil).String()[:63] + "z", Sum(nil).String() + "00"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", s)
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	id := Sum([]byte("json"))
	b, err := json.Marshal(map[string]ChunkID{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]ChunkID
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["id"] != id {
		t.Fatalf("json round trip mismatch")
	}
}

func TestIsZero(t *testing.T) {
	if !(ChunkID{}).IsZero() {
		t.Fatal("zero id not reported as zero")
	}
	if Sum(nil).IsZero() {
		t.Fatal("real id reported as zero")
	}
}
