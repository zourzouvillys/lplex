// Package journal provides the block-based binary journal format (.lpj files)
// for recording and replaying NMEA 2000 CAN frames.
package journal

import (
	"fmt"
	"hash/crc32"
)

// CompressionType identifies the block compression algorithm.
type CompressionType uint8

const (
	CompressionNone    CompressionType = 0
	CompressionZstd    CompressionType = 1
	CompressionZstdDict CompressionType = 2
)

var (
	Magic           = [3]byte{'L', 'P', 'J'}
	Version         = byte(0x01)
	CRC32cTable     = crc32.MakeTable(crc32.Castagnoli)
	FileHeaderSize  = 16
	BlockTrailerLen = 10 // DeviceTableOffset(2) + FrameCount(4) + Checksum(4)
	BlockIndexMagic = [4]byte{'L', 'P', 'J', 'I'}
	BlockHeaderLen     = 12 // BaseTime(8) + CompressedLen(4), for zstd blocks
	BlockHeaderLenDict = 16 // BaseTime(8) + DictLen(4) + CompressedLen(4), for zstd+dict blocks
)

// DeviceEntryMaxSize is the worst-case size of a single device table entry:
// Source(1) + NAME(8) + ActiveFrom(4) + ProductCode(2) +
// 4 length-prefixed strings at max lengths (32+40+24+32 = 128 bytes + 4 length bytes).
const DeviceEntryMaxSize = 1 + 8 + 4 + 2 + 4 + 128 // = 147

// UvarintSize returns the number of bytes needed to encode v as a uvarint.
func UvarintSize(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// Device is a NAME-to-Source binding from the journal device table,
// optionally carrying PGN 126996 product info.
type Device struct {
	Source          uint8
	NAME            uint64
	ProductCode     uint16
	ModelID         string
	SoftwareVersion string
	ModelVersion    string
	ModelSerial     string
}

// PutLenPrefixedString writes a length-prefixed string (1-byte length + data)
// into buf at offset 0. Returns the number of bytes written.
// Truncates to 255 bytes if needed.
func PutLenPrefixedString(buf []byte, s string) int {
	n := min(len(s), 255)
	buf[0] = byte(n)
	copy(buf[1:], s[:n])
	return 1 + n
}

// ReadLenPrefixedString reads a length-prefixed string from block at offset off.
// Returns the string, number of bytes consumed, and any error.
func ReadLenPrefixedString(block []byte, off int) (string, int, error) {
	if off >= len(block) {
		return "", 0, fmt.Errorf("string length byte out of range at offset %d", off)
	}
	n := int(block[off])
	if off+1+n > len(block) {
		return "", 0, fmt.Errorf("string data out of range at offset %d (len=%d)", off, n)
	}
	return string(block[off+1 : off+1+n]), 1 + n, nil
}
