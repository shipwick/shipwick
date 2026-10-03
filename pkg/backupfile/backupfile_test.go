package backupfile

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func init() { Iterations = 1000 }

func seal(t *testing.T, plain []byte, passphrase string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, passphrase)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Odd-sized writes: chunk boundaries must not depend on the caller's.
	for rest := plain; len(rest) > 0; {
		n := min(len(rest), 10_007)
		if _, err := w.Write(rest[:n]); err != nil {
			t.Fatalf("Write: %v", err)
		}
		rest = rest[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func open(data []byte, passphrase string) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(data), passphrase)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func random(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTripAtEverySizeAroundAChunk(t *testing.T) {
	for _, size := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 3*ChunkSize + 17} {
		plain := random(t, size)
		sealed := seal(t, plain, "correct horse")
		chunks := max(1, (size+ChunkSize-1)/ChunkSize)
		if want := headerSize + size + chunks*tagSize; len(sealed) != want {
			t.Errorf("size %d: sealed to %d bytes, want %d", size, len(sealed), want)
		}
		got, err := open(sealed, "correct horse")
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip changed the data", size)
		}
	}
}

func TestTheSamePlaintextSealsDifferentlyEveryTime(t *testing.T) {
	plain := []byte("the same archive, twice")
	if bytes.Equal(seal(t, plain, "p"), seal(t, plain, "p")) {
		t.Fatal("two files of the same plaintext are identical: the salt is not random")
	}
}

func TestWrongPassphraseIsToldApartFromDamage(t *testing.T) {
	sealed := seal(t, random(t, 3*ChunkSize), "right")
	if _, err := open(sealed, "wrong"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("got %v, want ErrPassphrase", err)
	}
}

func TestTruncationIsDetectedWhereverTheFileIsCut(t *testing.T) {
	plain := random(t, 3*ChunkSize)
	sealed := seal(t, plain, "p")
	chunk := ChunkSize + tagSize
	cuts := map[string]int{
		"inside the header":           headerSize - 3,
		"right after the header":      headerSize,
		"inside the first chunk":      headerSize + 100,
		"at a chunk boundary":         headerSize + chunk,
		"at the last boundary":        headerSize + 2*chunk,
		"inside the last chunk":       len(sealed) - 1,
		"inside the last chunk's tag": len(sealed) - tagSize + 2,
	}
	for name, at := range cuts {
		got, err := open(sealed[:at], "p")
		if err == nil {
			t.Errorf("cut %s: read %d bytes without an error", name, len(got))
		}
	}
}

func TestATruncatedFileYieldsNoDataPastTheCut(t *testing.T) {
	plain := random(t, 3*ChunkSize)
	sealed := seal(t, plain, "p")
	r, err := NewReader(bytes.NewReader(sealed[:headerSize+2*(ChunkSize+tagSize)]), "p")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v, want ErrCorrupt", err)
	}
	// The first chunk is whole and authentic; the second, read as the last
	// one of a file that ends there, is not.
	if !bytes.Equal(got, plain[:ChunkSize]) {
		t.Fatalf("read %d bytes before the error, want exactly the first chunk", len(got))
	}
}

func TestATamperedChunkIsRefused(t *testing.T) {
	plain := random(t, 3*ChunkSize)
	for name, at := range map[string]int{
		"first chunk":  headerSize + 5,
		"second chunk": headerSize + ChunkSize + tagSize + 5,
		"last tag":     -1,
	} {
		sealed := seal(t, plain, "p")
		if at < 0 {
			at = len(sealed) - 1
		}
		sealed[at] ^= 0x01
		if _, err := open(sealed, "p"); err == nil {
			t.Errorf("%s: a flipped bit went unnoticed", name)
		}
	}
}

func TestChunksCannotBeReorderedOrDropped(t *testing.T) {
	plain := random(t, 3*ChunkSize)
	sealed := seal(t, plain, "p")
	chunk := ChunkSize + tagSize
	first := sealed[headerSize : headerSize+chunk]
	second := sealed[headerSize+chunk : headerSize+2*chunk]

	swapped := append([]byte(nil), sealed...)
	copy(swapped[headerSize:], second)
	copy(swapped[headerSize+chunk:], first)
	if _, err := open(swapped, "p"); err == nil {
		t.Error("swapped chunks went unnoticed")
	}

	dropped := append(append([]byte(nil), sealed[:headerSize+chunk]...), sealed[headerSize+2*chunk:]...)
	if _, err := open(dropped, "p"); err == nil {
		t.Error("a dropped chunk went unnoticed")
	}
}

func TestAnAlteredHeaderIsRefused(t *testing.T) {
	sealed := seal(t, random(t, 100), "p")
	sealed[30] ^= 0x01 // the chunk size
	if _, err := open(sealed, "p"); err == nil {
		t.Fatal("an altered header went unnoticed")
	}
}

func TestPlainDataIsNotMistakenForABackup(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("short"), bytes.Repeat([]byte("tar data "), 100)} {
		if _, err := NewReader(bytes.NewReader(data), "p"); !errors.Is(err, ErrNotEncrypted) {
			t.Errorf("%d bytes: got %v, want ErrNotEncrypted", len(data), err)
		}
	}
	if IsEncrypted([]byte("tar")) || !IsEncrypted(seal(t, nil, "p")) {
		t.Error("IsEncrypted is wrong about one of them")
	}
}

func TestAnUnknownVersionSaysSo(t *testing.T) {
	sealed := seal(t, []byte("x"), "p")
	sealed[8] = 2
	if _, err := NewReader(bytes.NewReader(sealed), "p"); err == nil || errors.Is(err, ErrNotEncrypted) {
		t.Fatalf("got %v, want an error about the version", err)
	}
}

func TestAnEmptyPassphraseIsRefused(t *testing.T) {
	if _, err := NewWriter(io.Discard, ""); err == nil {
		t.Fatal("a writer without a passphrase would write files anyone can read")
	}
}
