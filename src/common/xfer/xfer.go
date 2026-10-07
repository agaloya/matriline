// Package xfer streams files over an MWP connection: each file is deflate-compressed into
// CHUNK frames and closed with a FILE_END frame. The receiver enforces the announced size
// (decompression-bomb defence), verifies SHA-256 and refuses unsafe names (path traversal).
package xfer

import (
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/agaloya/matriline/common/wire"
)

const chunkSize = 256 << 10

// HashFile returns size and hex SHA-256 of a file.
func HashFile(p string) (int64, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// Meta builds FileMeta for a list of (wire name, local path) pairs.
func Meta(names, paths []string) ([]wire.FileMeta, error) {
	out := make([]wire.FileMeta, len(names))
	for i := range names {
		n, h, err := HashFile(paths[i])
		if err != nil {
			return nil, err
		}
		out[i] = wire.FileMeta{Name: names[i], Size: n, SHA256: h}
	}
	return out, nil
}

// SafeName validates a wire file name: relative, slash separated, no "..", no absolute
// paths, no backslashes or NUL bytes, at most 8 levels and 255 bytes per element.
func SafeName(name string) error {
	if name == "" || len(name) > 1024 || strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("unsafe file name %q", name)
	}
	if path.IsAbs(name) || path.Clean(name) != name || !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("unsafe file name %q", name)
	}
	parts := strings.Split(name, "/")
	if len(parts) > 8 {
		return fmt.Errorf("file name too deep %q", name)
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > 255 {
			return fmt.Errorf("unsafe file name %q", name)
		}
	}
	return nil
}

type frameWriter struct{ c wire.Sender }

func (w frameWriter) Write(b []byte) (int, error) {
	for off := 0; off < len(b); off += chunkSize {
		end := off + chunkSize
		if end > len(b) {
			end = len(b)
		}
		if err := w.c.WriteFrame(wire.TChunk, b[off:end]); err != nil {
			return off, err
		}
	}
	return len(b), nil
}

// SendFile streams one file. level is a compress/flate level (0 = store, 1..9).
func SendFile(c wire.Sender, name, localPath string, level int) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return SendOpen(c, name, f, level)
}

// MetaOpen builds FileMeta from files already open, each rewound afterwards: a file
// opened once is hashed and sent as it is, even if it is removed meanwhile.
func MetaOpen(names []string, files []*os.File) ([]wire.FileMeta, error) {
	out := make([]wire.FileMeta, len(names))
	for i, f := range files {
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err == nil {
			_, err = f.Seek(0, io.SeekStart)
		}
		if err != nil {
			return nil, err
		}
		out[i] = wire.FileMeta{Name: names[i], Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	}
	return out, nil
}

// SendOpen sends an open file (from its current position) like SendFile.
func SendOpen(c wire.Sender, name string, f io.Reader, level int) error {
	bw := &bufWriter{w: frameWriter{c}, buf: make([]byte, 0, chunkSize)}
	zw, err := flate.NewWriter(bw, level)
	if err != nil {
		return err
	}
	if _, err := io.Copy(zw, f); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return c.Send(wire.TFileEnd, wire.FileEnd{Name: name})
}

// bufWriter batches small flate writes into full-size chunks.
type bufWriter struct {
	w   io.Writer
	buf []byte
}

func (b *bufWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := cap(b.buf) - len(b.buf)
		if room == 0 {
			if err := b.Flush(); err != nil {
				return 0, err
			}
			continue
		}
		if room > len(p) {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
		p = p[room:]
	}
	return n, nil
}

func (b *bufWriter) Flush() error {
	if len(b.buf) == 0 {
		return nil
	}
	_, err := b.w.Write(b.buf)
	b.buf = b.buf[:0]
	return err
}

// chunkReader turns a CHUNK* FILE_END frame sequence into an io.Reader.
type chunkReader struct {
	c    *wire.Conn
	cur  []byte
	done bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.cur) == 0 {
		if r.done {
			return 0, io.EOF
		}
		t, payload, err := r.c.ReadFrame()
		if err != nil {
			return 0, err
		}
		switch t {
		case wire.TChunk:
			r.cur = payload
		case wire.TFileEnd:
			r.done = true
		default:
			return 0, fmt.Errorf("xfer: unexpected %v inside file transfer", t)
		}
	}
	n := copy(p, r.cur)
	r.cur = r.cur[n:]
	return n, nil
}

// ErrTooLarge is returned when a file decompresses beyond its announced size.
var ErrTooLarge = errors.New("xfer: file larger than announced (possible decompression bomb)")

// ReceiveFile reads one file into dir/meta.Name, verifying size and hash.
func ReceiveFile(c *wire.Conn, meta wire.FileMeta, dir string) error {
	if err := SafeName(meta.Name); err != nil {
		return err
	}
	if meta.Size < 0 {
		return fmt.Errorf("xfer: negative size")
	}
	dst := filepath.Join(dir, filepath.FromSlash(meta.Name))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	cr := &chunkReader{c: c}
	zr := flate.NewReader(cr)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(zr, meta.Size+1))
	if err != nil {
		tmp.Close()
		return err
	}
	if n > meta.Size {
		tmp.Close()
		return ErrTooLarge
	}
	// consume the rest of the frame sequence up to FILE_END
	if _, err := io.Copy(io.Discard, cr); err != nil {
		tmp.Close()
		return err
	}
	if n != meta.Size || hex.EncodeToString(h.Sum(nil)) != meta.SHA256 {
		tmp.Close()
		return fmt.Errorf("xfer: %s: size/hash mismatch", meta.Name)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Drain discards one file transfer (used when rejecting a task after it was sent).
func Drain(c *wire.Conn) error {
	_, err := io.Copy(io.Discard, &chunkReader{c: c})
	return err
}
