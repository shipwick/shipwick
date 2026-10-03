// Package backupfile is the encrypted form of a backup: a stream sealed with
// a passphrase, written by the agent and read by the agent and by
// `shipwick backups decrypt`. Only the standard library is involved, and the
// format is small enough to implement again from its description.
//
// A file is a header followed by chunks.
//
//	header (33 bytes)
//	  0   8  the ASCII bytes "SWBACKUP"
//	  8   1  format version, 1
//	  9   4  PBKDF2 iteration count, big-endian
//	 13  16  salt, random per file
//	 29   4  chunk size: plaintext bytes per chunk, big-endian
//
//	key    = PBKDF2-HMAC-SHA256(passphrase, salt, iterations), 32 bytes
//	chunk  = AES-256-GCM(key, nonce, plaintext, additional data = header)
//	nonce  = chunk index as 8 bytes big-endian, counting from 0,
//	         3 zero bytes, then 1 for the last chunk and 0 for the others
//
// Every chunk but the last holds exactly `chunk size` bytes of plaintext and
// is 16 bytes longer on disk, GCM's tag. The last chunk holds the rest — up
// to a full chunk, and nothing at all only when the whole plaintext is empty
// — and is always present. Its marker is part of the nonce, so a file cut at
// a chunk boundary fails to decrypt, like one cut anywhere else; the header
// is authenticated with every chunk, so neither can be altered unnoticed.
// The salt is random, which makes the key unique to the file and lets the
// nonce be a counter.
package backupfile

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	magic      = "SWBACKUP"
	version    = 1
	saltSize   = 16
	headerSize = len(magic) + 1 + 4 + saltSize + 4
	keySize    = 32
	tagSize    = 16

	// ChunkSize is the plaintext per chunk in files this package writes.
	ChunkSize = 64 << 10
	// maxChunkSize and maxIterations bound what a header may ask of a reader:
	// memory and time, before anything has been authenticated.
	maxChunkSize  = 16 << 20
	maxIterations = 10_000_000
)

// Iterations is the PBKDF2 work factor of new files. A variable so that tests
// do not spend their time deriving keys.
var Iterations = 600_000

var (
	// ErrNotEncrypted means the data does not start with this format's header.
	ErrNotEncrypted = errors.New("not an encrypted Shipwick backup")
	// ErrPassphrase means the first chunk did not decrypt: the passphrase is
	// not the one the file was written with, or the file's start is damaged.
	ErrPassphrase = errors.New("the passphrase does not match this backup, or the file is damaged")
	// ErrCorrupt means a later chunk did not decrypt: the file was cut short
	// or altered.
	ErrCorrupt = errors.New("the backup is truncated or damaged")
)

// IsEncrypted reports whether data starts like a file of this format.
func IsEncrypted(head []byte) bool {
	return len(head) >= len(magic) && string(head[:len(magic)]) == magic
}

// PlainSize returns how many bytes of plaintext a file of the given size
// holds, for files this package writes: the header and one tag per chunk are
// all that encryption adds, so the size says it without the passphrase. The
// second result is false for a size no such file has.
func PlainSize(encrypted int64) (int64, bool) {
	body := encrypted - int64(headerSize)
	const sealedChunk = ChunkSize + tagSize
	full, rest := body/sealedChunk, body%sealedChunk
	switch {
	case body < tagSize:
		return 0, false
	case rest == 0:
		return full * ChunkSize, true
	case rest < tagSize, rest == tagSize && full > 0:
		// Less than a tag, or an empty last chunk after full ones: the writer
		// seals a full buffer as the last chunk instead.
		return 0, false
	}
	return full*ChunkSize + rest - tagSize, true
}

func newAEAD(passphrase string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, keySize)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(index uint64, last bool) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n, index)
	if last {
		n[11] = 1
	}
	return n
}

// Writer encrypts what is written to it. Close writes the last chunk; without
// it the file is not a valid backup, which is the point.
type Writer struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	buf    []byte
	out    []byte
	index  uint64
	err    error
	closed bool
}

// NewWriter writes the header to w and returns a Writer sealing with
// passphrase. Closing it does not close w.
func NewWriter(w io.Writer, passphrase string) (*Writer, error) {
	if passphrase == "" {
		return nil, errors.New("the passphrase is empty")
	}
	header := make([]byte, 0, headerSize)
	header = append(header, magic...)
	header = append(header, version)
	header = binary.BigEndian.AppendUint32(header, uint32(Iterations))
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, ChunkSize)

	aead, err := newAEAD(passphrase, salt, Iterations)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return &Writer{w: w, aead: aead, header: header, buf: make([]byte, 0, ChunkSize), out: make([]byte, 0, ChunkSize+tagSize)}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("write to a closed backup")
	}
	written := 0
	for len(p) > 0 {
		// A full buffer is sealed only when more arrives: whether it is the
		// last chunk is not known before.
		if len(w.buf) == ChunkSize {
			if w.err = w.seal(false); w.err != nil {
				return written, w.err
			}
		}
		n := copy(w.buf[len(w.buf):ChunkSize], p)
		w.buf = w.buf[:len(w.buf)+n]
		p = p[n:]
		written += n
	}
	return written, nil
}

func (w *Writer) seal(last bool) error {
	w.out = w.aead.Seal(w.out[:0], nonce(w.index, last), w.buf, w.header)
	w.index++
	w.buf = w.buf[:0]
	_, err := w.w.Write(w.out)
	return err
}

// Close seals what is left as the last chunk.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err == nil {
		w.err = w.seal(true)
	}
	return w.err
}

// Reader decrypts a file written by Writer. It returns io.EOF only after the
// last chunk has been authenticated: a caller that reads to the end without
// an error has read everything that was written.
type Reader struct {
	r         *bufio.Reader
	aead      cipher.AEAD
	header    []byte
	chunkSize int
	sealed    []byte
	plain     []byte // what of the current chunk has not been handed out
	index     uint64
	done      bool
	err       error
}

// NewReader reads the header from r and returns a Reader opening with
// passphrase. A wrong passphrase shows on the first Read, as ErrPassphrase.
func NewReader(r io.Reader, passphrase string) (*Reader, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrNotEncrypted
		}
		return nil, err
	}
	if !IsEncrypted(header) {
		return nil, ErrNotEncrypted
	}
	if v := header[len(magic)]; v != version {
		return nil, fmt.Errorf("backup format version %d is not supported by this version of Shipwick", v)
	}
	iter := binary.BigEndian.Uint32(header[9:13])
	chunkSize := binary.BigEndian.Uint32(header[29:33])
	if iter == 0 || iter > maxIterations || chunkSize == 0 || chunkSize > maxChunkSize {
		return nil, ErrCorrupt
	}
	if passphrase == "" {
		return nil, errors.New("the backup is encrypted and no passphrase is set")
	}
	aead, err := newAEAD(passphrase, header[13:29], int(iter))
	if err != nil {
		return nil, err
	}
	return &Reader{
		r:         bufio.NewReaderSize(r, int(chunkSize)+tagSize+1),
		aead:      aead,
		header:    header,
		chunkSize: int(chunkSize),
		sealed:    make([]byte, int(chunkSize)+tagSize),
	}, nil
}

func (r *Reader) Read(p []byte) (int, error) {
	for len(r.plain) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		if r.err = r.open(); r.err != nil {
			return 0, r.err
		}
	}
	n := copy(p, r.plain)
	r.plain = r.plain[n:]
	return n, nil
}

// open reads and decrypts the next chunk. It is the last one when the file
// ends with it; a file that merely stops there fails the last chunk's nonce.
func (r *Reader) open() error {
	n, err := io.ReadFull(r.r, r.sealed)
	last := false
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		last = true
	case err != nil:
		return err
	default:
		if _, err := r.r.Peek(1); errors.Is(err, io.EOF) {
			last = true
		} else if err != nil {
			return err
		}
	}
	failure := ErrCorrupt
	if r.index == 0 {
		failure = ErrPassphrase
	}
	if n < tagSize {
		return failure
	}
	plain, err := r.aead.Open(r.sealed[:0], nonce(r.index, last), r.sealed[:n], r.header)
	if err != nil {
		return failure
	}
	r.index++
	r.plain, r.done = plain, last
	return nil
}
