// journalbench reads frames from a .lpj journal file and re-encodes them
// with every combination of block size and compression method to find the
// optimal parameters for CAN bus journal data.
package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"os"
	"time"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
	"github.com/sixfathoms/lplex/canbus"
	"github.com/sixfathoms/lplex/journal"
)

const maxFrames = 20000

type frame struct {
	tsUs  int64
	canID uint32
	data  []byte
}

// compressor is a block compression function: input -> compressed output.
type compressor struct {
	name string
	fn   func([]byte) []byte
}

func main() {
	filePath := flag.String("file", "", "path to .lpj journal file")
	flag.Parse()
	if *filePath == "" {
		fmt.Fprintf(os.Stderr, "usage: journalbench -file <path.lpj>\n")
		os.Exit(1)
	}

	frames, err := readFrames(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Read %d frames from %s\n", len(frames), *filePath)
	spanUs := frames[len(frames)-1].tsUs - frames[0].tsUs
	fmt.Printf("Time span: %v\n\n", time.Duration(spanUs)*time.Microsecond)

	// Build compressors
	compressors := buildCompressors()

	blockSizes := []int{8192, 16384, 32768, 65536, 131072, 262144, 524288}

	type result struct {
		blockSize      int
		method         string
		totalRaw       int64
		totalComp      int64
		blocks         int
		compressTimeMs int64
	}

	var results []result

	for _, bs := range blockSizes {
		blocks := buildBlocks(frames, bs)
		rawSize := int64(len(blocks)) * int64(bs)

		for _, c := range compressors {
			t0 := time.Now()
			var totalComp int64
			for _, block := range blocks {
				compressed := c.fn(block)
				totalComp += int64(len(compressed))
			}
			elapsed := time.Since(t0)

			results = append(results, result{
				blockSize:      bs,
				method:         c.name,
				totalRaw:       rawSize,
				totalComp:      totalComp,
				blocks:         len(blocks),
				compressTimeMs: elapsed.Milliseconds(),
			})
		}
		fmt.Fprintf(os.Stderr, "  %s done (%d blocks)\n", fmtSize(bs), len(blocks))
	}

	// Print table grouped by block size
	fmt.Printf("\n%-8s  %-22s  %10s  %10s  %6s  %6s  %8s\n",
		"BLOCK", "METHOD", "RAW", "COMPRESSED", "RATIO", "BLOCKS", "TIME")
	fmt.Println("--------  ----------------------  ----------  ----------  ------  ------  --------")

	lastBS := 0
	for _, r := range results {
		if r.blockSize != lastBS {
			if lastBS != 0 {
				fmt.Println()
			}
			lastBS = r.blockSize
		}
		ratio := float64(r.totalRaw) / float64(r.totalComp)
		fmt.Printf("%-8s  %-22s  %10s  %10s  %5.1fx  %6d  %6dms\n",
			fmtSize(r.blockSize), r.method,
			fmtBytes(r.totalRaw), fmtBytes(r.totalComp),
			ratio, r.blocks, r.compressTimeMs)
	}

	// Summary: best ratio per block size
	fmt.Printf("\n\nBest ratio per block size:\n")
	fmt.Printf("%-8s  %-22s  %6s\n", "BLOCK", "METHOD", "RATIO")
	fmt.Println("--------  ----------------------  ------")
	for _, bs := range blockSizes {
		var bestMethod string
		var bestRatio float64
		for _, r := range results {
			if r.blockSize != bs {
				continue
			}
			ratio := float64(r.totalRaw) / float64(r.totalComp)
			if ratio > bestRatio {
				bestRatio = ratio
				bestMethod = r.method
			}
		}
		fmt.Printf("%-8s  %-22s  %5.1fx\n", fmtSize(bs), bestMethod, bestRatio)
	}

	// Summary: best ratio overall per method (at their best block size)
	fmt.Printf("\n\nBest result per method (across all block sizes):\n")
	fmt.Printf("%-22s  %-8s  %10s  %6s\n", "METHOD", "BLOCK", "SIZE", "RATIO")
	fmt.Println("----------------------  --------  ----------  ------")
	seen := map[string]bool{}
	for _, c := range compressors {
		if seen[c.name] {
			continue
		}
		seen[c.name] = true
		var bestBS int
		var bestRatio float64
		var bestSize int64
		for _, r := range results {
			if r.method != c.name {
				continue
			}
			ratio := float64(r.totalRaw) / float64(r.totalComp)
			if ratio > bestRatio {
				bestRatio = ratio
				bestBS = r.blockSize
				bestSize = r.totalComp
			}
		}
		fmt.Printf("%-22s  %-8s  %10s  %5.1fx\n", c.name, fmtSize(bestBS), fmtBytes(bestSize), bestRatio)
	}
}

func buildCompressors() []compressor {
	var cs []compressor

	// zstd at different levels
	for _, level := range []struct {
		name  string
		level zstd.EncoderLevel
	}{
		{"zstd-fastest", zstd.SpeedFastest},
		{"zstd-default", zstd.SpeedDefault},
		{"zstd-better", zstd.SpeedBetterCompression},
		{"zstd-best", zstd.SpeedBestCompression},
	} {
		l := level
		enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(l.level))
		if err != nil {
			continue
		}
		cs = append(cs, compressor{
			name: l.name,
			fn: func(data []byte) []byte {
				return enc.EncodeAll(data, nil)
			},
		})
	}

	// s2 (improved snappy) at different levels
	cs = append(cs, compressor{
		name: "s2",
		fn: func(data []byte) []byte {
			return s2.Encode(nil, data)
		},
	})
	cs = append(cs, compressor{
		name: "s2-better",
		fn: func(data []byte) []byte {
			return s2.EncodeBetter(nil, data)
		},
	})
	cs = append(cs, compressor{
		name: "s2-best",
		fn: func(data []byte) []byte {
			return s2.EncodeBest(nil, data)
		},
	})

	// deflate (same algo as gzip but without the header overhead)
	for _, level := range []struct {
		name  string
		level int
	}{
		{"deflate-1", flate.BestSpeed},
		{"deflate-6", flate.DefaultCompression},
		{"deflate-9", flate.BestCompression},
	} {
		l := level
		cs = append(cs, compressor{
			name: l.name,
			fn: func(data []byte) []byte {
				var buf bytes.Buffer
				w, _ := flate.NewWriter(&buf, l.level)
				_, _ = w.Write(data)
				_ = w.Close()
				return buf.Bytes()
			},
		})
	}

	return cs
}

func readFrames(path string) ([]frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r, err := journal.NewReader(f)
	if err != nil {
		return nil, err
	}

	var frames []frame
	for r.Next() {
		e := r.Frame()
		frames = append(frames, frame{
			tsUs:  e.Timestamp.UnixMicro(),
			canID: canbus.BuildCANID(e.Header),
			data:  append([]byte(nil), e.Data...),
		})
		if len(frames) >= maxFrames {
			break
		}
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames in file")
	}
	return frames, nil
}

func buildBlocks(frames []frame, blockSize int) [][]byte {
	const devTableBytes = 2
	available := blockSize - 8 - devTableBytes - journal.BlockTrailerLen

	var blocks [][]byte
	block := make([]byte, blockSize)
	off := 8
	var frameCount uint32
	var lastTsUs int64

	flush := func() {
		if frameCount == 0 {
			return
		}
		devTableOff := blockSize - journal.BlockTrailerLen - devTableBytes
		binary.LittleEndian.PutUint16(block[devTableOff:], 0)
		trailerOff := blockSize - journal.BlockTrailerLen
		binary.LittleEndian.PutUint16(block[trailerOff:], uint16(devTableOff))
		binary.LittleEndian.PutUint32(block[trailerOff+2:], frameCount)
		crc := crc32.Checksum(block[:blockSize-4], journal.CRC32cTable)
		binary.LittleEndian.PutUint32(block[blockSize-4:], crc)

		out := make([]byte, blockSize)
		copy(out, block)
		blocks = append(blocks, out)
	}

	for i := range frames {
		fr := &frames[i]
		var deltaUs uint64
		if frameCount > 0 && fr.tsUs >= lastTsUs {
			deltaUs = uint64(fr.tsUs - lastTsUs)
		}

		standard := len(fr.data) == 8
		size := journal.UvarintSize(deltaUs) + 4
		if standard {
			size += 8
		} else {
			size += journal.UvarintSize(uint64(len(fr.data))) + len(fr.data)
		}

		if off+size > 8+available {
			flush()
			block = make([]byte, blockSize)
			off = 8
			frameCount = 0
			deltaUs = 0
		}

		if frameCount == 0 {
			binary.LittleEndian.PutUint64(block[0:8], uint64(fr.tsUs))
		}

		off += binary.PutUvarint(block[off:], deltaUs)
		storedID := fr.canID
		if standard {
			storedID |= 0x80000000
		}
		binary.LittleEndian.PutUint32(block[off:], storedID)
		off += 4
		if standard {
			copy(block[off:], fr.data)
			off += 8
		} else {
			off += binary.PutUvarint(block[off:], uint64(len(fr.data)))
			copy(block[off:], fr.data)
			off += len(fr.data)
		}

		lastTsUs = fr.tsUs
		frameCount++
	}
	flush()
	return blocks
}

func fmtSize(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%dM", n/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%dK", n/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
