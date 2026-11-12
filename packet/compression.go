package packet

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"slices"
	"sync"

	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/s2"
)

// Compression represents a compression algorithm that can compress and decompress data.
type Compression interface {
	// EncodeCompression encodes the compression algorithm into a uint16 ID.
	EncodeCompression() uint16
	// Compress compresses the given data and returns the compressed data.
	Compress(decompressed []byte) ([]byte, error)
	// Decompress decompresses the given data and returns the decompressed data.
	Decompress(compressed []byte, limit int) ([]byte, error)
}

type appendCompression interface {
	CompressAppend(dst, decompressed []byte) ([]byte, error)
	MaxCompressedLen(decompressedLen int) int
}

// appendDecompression is implemented by Compression algorithms that can decompress into an existing
// destination buffer instead of allocating a new one on every call.
type appendDecompression interface {
	// DecompressAppend appends the decompressed form of compressed to dst and returns the result. It
	// returns an error if the decompressed size exceeds limit.
	DecompressAppend(dst, compressed []byte, limit int) ([]byte, error)
}

var (
	// NopCompression is an empty implementation that does not compress data.
	NopCompression nopCompression
	// FlateCompression is the implementation of the Flate compression
	// algorithm, at DefaultFlateLevel. This is used by default.
	FlateCompression flateCompression
	// SnappyCompression is the implementation of the Snappy compression
	// algorithm. Snappy currently crashes devices without `avx2`.
	SnappyCompression snappyCompression

	DefaultCompression Compression = FlateCompression
)

type (
	// nopCompression is an empty implementation that does not compress data.
	nopCompression struct{}
	// flateCompression is the implementation of the Flate compression algorithm. level 0
	// means DefaultFlateLevel; any level decompresses the same.
	flateCompression struct{ level int8 }
	// snappyCompression is the implementation of the Snappy compression algorithm.
	snappyCompression struct{}
)

// DefaultFlateLevel is the flate level FlateCompression compresses at. Level 3 is both
// faster and smaller than level 6 on the batches a server sends most (movement:
// 120 µs and 5358 bytes against 181 µs and 5850 bytes for 400 MovePlayers), and the
// client reads any level.
const DefaultFlateLevel = 3

// Flate levels from flate.HuffmanOnly (-2) to flate.BestCompression (9) index the
// compressor pools from 0.
const (
	minFlateLevel   = flate.HuffmanOnly
	flateLevelCount = flate.BestCompression - flate.HuffmanOnly + 1
)

// NewFlateCompression is flate compressing at level: flate.HuffmanOnly (-2), 1 (fastest)
// to 9 (smallest); 0 or a level out of range means DefaultFlateLevel. It decompresses
// anything flate, and is the same algorithm (ID) as FlateCompression.
func NewFlateCompression(level int) Compression {
	if level < minFlateLevel || level > flate.BestCompression || level == 0 || level == -1 {
		level = DefaultFlateLevel
	}
	return flateCompression{level: int8(level)} //nolint:gosec // -2 to 9
}

// appendWriter is an io.Writer appending to b, so flate writes straight into the
// caller's buffer.
type appendWriter struct{ b []byte }

func (a *appendWriter) Write(p []byte) (int, error) {
	a.b = append(a.b, p...)
	return len(p), nil
}

// flateDecompressor is a flate reader and the reader over its input, pooled together so
// decompressing allocates nothing.
type flateDecompressor struct {
	r   io.ReadCloser
	src bytes.Reader
}

// flateCompressor is a flate writer and the buffer it writes into, pooled together so
// compressing allocates nothing.
type flateCompressor struct {
	w   *flate.Writer
	out appendWriter
}

// flateDecompressPool is a sync.Pool for io.ReadCloser flate readers, and
// flateCompressPools one pool of compressors per level. These are pooled for
// connections.
var (
	flateDecompressPool = sync.Pool{
		New: func() any {
			d := &flateDecompressor{}
			d.r = flate.NewReader(&d.src)
			return d
		},
	}
	flateCompressPools = newFlateCompressPools()
)

// newFlateCompressPools makes the compressor pool of every level.
func newFlateCompressPools() *[flateLevelCount]sync.Pool {
	var pools [flateLevelCount]sync.Pool
	for i := range pools {
		level := i + minFlateLevel
		pools[i].New = func() any {
			w, _ := flate.NewWriter(io.Discard, level)
			return &flateCompressor{w: w}
		}
	}
	return &pools
}

// levelOf is the level c compresses at.
func (c flateCompression) levelOf() int {
	if c.level == 0 {
		return DefaultFlateLevel
	}
	return int(c.level)
}

// EncodeCompression ...
func (nopCompression) EncodeCompression() uint16 {
	return CompressionAlgorithmNone
}

// Compress ...
func (nopCompression) Compress(decompressed []byte) ([]byte, error) {
	return decompressed, nil
}

// Decompress ...
func (nopCompression) Decompress(compressed []byte, limit int) ([]byte, error) {
	if len(compressed) > limit {
		return nil, fmt.Errorf("nop decompression: size %d exceeds limit %d", len(compressed), limit)
	}
	return compressed, nil
}

// EncodeCompression ...
func (flateCompression) EncodeCompression() uint16 {
	return CompressionAlgorithmFlate
}

// Compress ...
func (c flateCompression) Compress(decompressed []byte) ([]byte, error) {
	return c.CompressAppend(nil, decompressed)
}

// CompressAppend compresses decompressed onto the end of dst, with no copy in between.
func (c flateCompression) CompressAppend(dst, decompressed []byte) ([]byte, error) {
	pool := &flateCompressPools[c.levelOf()-minFlateLevel]
	fc, _ := pool.Get().(*flateCompressor)
	fc.out.b = dst
	defer func() {
		fc.out.b = nil
		pool.Put(fc)
	}()
	fc.w.Reset(&fc.out)
	if _, err := fc.w.Write(decompressed); err != nil {
		return dst, fmt.Errorf("compress flate: %w", err)
	}
	if err := fc.w.Close(); err != nil {
		return dst, fmt.Errorf("close flate writer: %w", err)
	}
	return fc.out.b, nil
}

// MaxCompressedLen is room for what flate makes of decompressedLen bytes in most cases:
// small batches of game packets rarely shrink less than this, and a larger one grows
// the buffer once.
func (flateCompression) MaxCompressedLen(decompressedLen int) int {
	return decompressedLen/2 + flateOverhead
}

// flateOverhead is room for flate's block headers and end.
const flateOverhead = 64

// Decompress ...
func (c flateCompression) Decompress(compressed []byte, limit int) ([]byte, error) {
	pooled := getDecompressBuffer()
	defer putDecompressBuffer(pooled)

	data, err := c.DecompressAppend(*pooled, compressed, limit)
	if err != nil {
		return nil, err
	}
	*pooled = data
	return append([]byte(nil), data...), nil
}

// DecompressAppend decompresses flate data into dst. Flate has no length prefix, so dst is grown to double
// the compressed size up front and appendReader grows it further if needed.
func (flateCompression) DecompressAppend(dst, compressed []byte, limit int) ([]byte, error) {
	limit = max(limit, 0)
	hint := len(compressed)
	if hint > math.MaxInt/2 {
		hint = math.MaxInt
	} else {
		hint *= 2
	}
	hint = max(hint, 32*1024)
	if limit != math.MaxInt {
		hint = min(hint, limit+1)
	}
	dst = slices.Grow(dst, hint)

	d, _ := flateDecompressPool.Get().(*flateDecompressor)
	defer func() {
		_ = d.r.Close()
		d.src.Reset(nil)
		flateDecompressPool.Put(d)
	}()
	d.src.Reset(compressed)
	resetter, ok := d.r.(flate.Resetter)
	if !ok {
		return nil, fmt.Errorf("reset flate: reader cannot be reset")
	}
	if err := resetter.Reset(&d.src, nil); err != nil {
		return nil, fmt.Errorf("reset flate: %w", err)
	}
	decompressed, err := appendReader(dst, d.r, limit)
	if err != nil {
		return nil, fmt.Errorf("decompress flate: %w", err)
	}
	return decompressed, nil
}

// EncodeCompression ...
func (snappyCompression) EncodeCompression() uint16 {
	return CompressionAlgorithmSnappy
}

// Compress ...
func (snappyCompression) Compress(decompressed []byte) ([]byte, error) {
	// Use the fast Snappy-compatible S2 encoder. The snappy.Encode wrapper uses
	// EncodeSnappyBetter, which trades more CPU and retained scratch memory for a
	// smaller output size.
	return s2.EncodeSnappy(nil, decompressed), nil
}

func (snappyCompression) CompressAppend(dst, decompressed []byte) ([]byte, error) {
	// Append into the caller-provided output buffer so Encoder can keep the
	// batch header and compression ID in the same allocation as the payload.
	n := s2.MaxEncodedLen(len(decompressed))
	if n < 0 {
		return dst, s2.ErrTooLarge
	}
	offset := len(dst)
	dst = slices.Grow(dst, n)
	encoded := s2.EncodeSnappy(dst[offset:offset:cap(dst)], decompressed)
	return dst[:offset+len(encoded)], nil
}

func (snappyCompression) MaxCompressedLen(decompressedLen int) int {
	return s2.MaxEncodedLen(decompressedLen)
}

// Decompress ...
func (c snappyCompression) Decompress(compressed []byte, limit int) ([]byte, error) {
	return c.DecompressAppend(nil, compressed, limit)
}

// DecompressAppend decompresses snappy data into dst. Snappy writes a decoded data length prefix, so the
// exact size can be checked against limit and reserved in dst before decompressing.
func (snappyCompression) DecompressAppend(dst, compressed []byte, limit int) ([]byte, error) {
	decodedLen, err := s2.DecodedLen(compressed)
	if err != nil {
		return nil, fmt.Errorf("snappy decoded length: %w", err)
	}
	if decodedLen > limit {
		return nil, fmt.Errorf("snappy decoded size %d exceeds limit %d", decodedLen, limit)
	}
	offset := len(dst)
	dst = slices.Grow(dst, decodedLen)
	decompressed, err := s2.Decode(dst[offset:offset:cap(dst)], compressed)
	if err != nil {
		return nil, fmt.Errorf("decompress snappy: %w", err)
	}
	return append(dst[:offset], decompressed...), nil
}

// appendReader appends everything read from r to dst and returns the result. It returns an error if more
// than limit bytes are read.
func appendReader(dst []byte, r io.Reader, limit int) ([]byte, error) {
	const chunkSize = 32 * 1024

	// Reading limit+1 bytes detects overflow; cap the limit so the +1 cannot itself overflow.
	limit = max(min(limit, math.MaxInt-1), 0)
	start := len(dst)
	for {
		remaining := limit + 1 - (len(dst) - start)
		if cap(dst) == len(dst) {
			dst = slices.Grow(dst, min(chunkSize, remaining))
		}
		readLen := min(cap(dst)-len(dst), chunkSize, remaining)

		n := len(dst)
		dst = dst[:n+readLen]
		read, err := r.Read(dst[n:])
		dst = dst[:n+read]
		if len(dst)-start > limit {
			return nil, fmt.Errorf("size exceeds limit %d", limit)
		}
		if err == io.EOF {
			return dst, nil
		}
		if err != nil {
			return nil, err
		}
		if read == 0 {
			return nil, io.ErrNoProgress
		}
	}
}

// init registers all valid compressions with the protocol.
func init() {
	RegisterCompression(flateCompression{})
	RegisterCompression(snappyCompression{})
}

var compressions = map[uint16]Compression{}

// RegisterCompression registers a compression so that it can be used by the protocol.
func RegisterCompression(compression Compression) {
	compressions[compression.EncodeCompression()] = compression
}

// CompressionByID attempts to return a compression by the ID it was registered with. If found, the compression found
// is returned and the bool is true.
func CompressionByID(id uint16) (Compression, bool) {
	c, ok := compressions[id]
	if !ok {
		c = DefaultCompression
	}
	return c, ok
}
