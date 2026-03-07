package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sixfathoms/lplex/canbus"
	"github.com/sixfathoms/lplex/journal"
	"github.com/sixfathoms/lplex/lplexc"
	"github.com/sixfathoms/lplex/pgn"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// stringSlice implements flag.Value for repeatable string flags (-flag a -flag b).
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// uintSlice implements flag.Value for repeatable uint flags.
type uintSlice []uint

func (s *uintSlice) String() string {
	parts := make([]string, len(*s))
	for i, v := range *s {
		parts[i] = strconv.FormatUint(uint64(v), 10)
	}
	return strings.Join(parts, ",")
}
func (s *uintSlice) Set(v string) error {
	n, err := strconv.ParseUint(v, 10, strconv.IntSize)
	if err != nil {
		return err
	}
	*s = append(*s, uint(n))
	return nil
}

func main() {
	serverURL := flag.String("server", "", "lplex server URL (auto-discovered via mDNS if empty)")
	clientID := flag.String("client-id", "", "client session ID (default: hostname)")
	bufferTimeout := flag.String("buffer-timeout", "", "enable session buffering with this duration (ISO 8601, e.g. PT5M)")
	reconnect := flag.Bool("reconnect", true, "auto-reconnect on disconnect")
	reconnectDelay := flag.Duration("reconnect-delay", 2*time.Second, "delay between reconnect attempts")
	ackInterval := flag.Duration("ack-interval", 5*time.Second, "how often to ACK the latest seq (only with buffered sessions)")
	quiet := flag.Bool("quiet", false, "suppress stderr status messages")
	jsonMode := flag.Bool("json", false, "raw JSON lines (default when stdout is piped)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	decode := flag.Bool("decode", false, "decode known PGNs and display field values")

	// Journal flags.
	filePath := flag.String("file", "", "replay from .lpj journal file (mutually exclusive with -server)")
	inspect := flag.Bool("inspect", false, "inspect journal file structure (use with -file)")
	speed := flag.Float64("speed", 0, "playback speed multiplier (0 = as fast as possible, 1.0 = real-time)")
	startTime := flag.String("start", "", "seek to RFC3339 timestamp before replaying")

	var filterPGNs uintSlice
	var excludePGNs uintSlice
	var filterManufacturers stringSlice
	var filterInstances uintSlice
	var filterNames stringSlice
	flag.Var(&filterPGNs, "pgn", "filter by PGN (repeatable)")
	flag.Var(&excludePGNs, "exclude-pgn", "exclude PGN from output (repeatable)")
	flag.Var(&filterManufacturers, "manufacturer", "filter by manufacturer name (repeatable)")
	flag.Var(&filterInstances, "instance", "filter by device instance (repeatable)")
	flag.Var(&filterNames, "name", "filter by 64-bit CAN NAME in hex (repeatable)")

	flag.Parse()

	if *showVersion {
		fmt.Printf("lplexdump %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	if !*jsonMode && !isTerminal(os.Stdout) {
		*jsonMode = true
	}

	if *quiet {
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(os.Stderr)
	}
	log.SetFlags(log.Ltime)

	// Journal inspect mode.
	if *inspect {
		if *filePath == "" {
			fmt.Fprintf(os.Stderr, "-inspect requires -file\n")
			os.Exit(1)
		}
		if err := runInspect(*filePath); err != nil {
			fmt.Fprintf(os.Stderr, "inspect error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Journal replay mode.
	if *filePath != "" {
		if *serverURL != "" || *bufferTimeout != "" {
			fmt.Fprintf(os.Stderr, "-file cannot be used with -server or -buffer-timeout\n")
			os.Exit(1)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		devices := newDeviceMap()
		if err := runReplay(ctx, *filePath, *speed, *startTime, *jsonMode, *decode, filterPGNs, excludePGNs, filterManufacturers, filterInstances, filterNames, devices); err != nil {
			fmt.Fprintf(os.Stderr, "replay error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Live streaming mode.
	if *serverURL == "" {
		discovered, err := lplexc.Discover(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "mDNS discovery failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "specify -server explicitly, e.g. -server http://inuc1.local:8089\n")
			os.Exit(1)
		}
		log.Printf("discovered lplex at %s", discovered)
		*serverURL = discovered
	}

	if *clientID == "" && *bufferTimeout != "" {
		h, err := os.Hostname()
		if err != nil {
			h = "lplexdump"
		}
		*clientID = h
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := lplexc.NewClient(*serverURL)
	devices := newDeviceMap()
	var lastSeq atomic.Uint64

	filter := buildFilter(filterPGNs, excludePGNs, filterManufacturers, filterInstances, filterNames)
	buffered := *bufferTimeout != ""

	for {
		var err error
		if buffered {
			err = runBuffered(ctx, client, *clientID, *bufferTimeout, *ackInterval, *jsonMode, *decode, filter, devices, &lastSeq)
		} else {
			err = runEphemeral(ctx, client, *jsonMode, *decode, filter, devices, &lastSeq)
		}
		if ctx.Err() != nil {
			if buffered {
				ackFinal(client, *clientID, lastSeq.Load())
			}
			return
		}
		if err != nil {
			log.Printf("disconnected: %v", err)
		}
		if !*reconnect {
			if buffered {
				ackFinal(client, *clientID, lastSeq.Load())
			}
			os.Exit(1)
		}
		log.Printf("reconnecting in %s", *reconnectDelay)
		select {
		case <-time.After(*reconnectDelay):
		case <-ctx.Done():
			if buffered {
				ackFinal(client, *clientID, lastSeq.Load())
			}
			return
		}
	}
}

func buildFilter(pgns uintSlice, excludePGNs uintSlice, manufacturers stringSlice, instances uintSlice, names stringSlice) *lplexc.Filter {
	if len(pgns) == 0 && len(excludePGNs) == 0 && len(manufacturers) == 0 && len(instances) == 0 && len(names) == 0 {
		return nil
	}
	f := &lplexc.Filter{
		Manufacturers: []string(manufacturers),
		Names:         []string(names),
	}
	for _, p := range pgns {
		f.PGNs = append(f.PGNs, uint32(p))
	}
	for _, p := range excludePGNs {
		f.ExcludePGNs = append(f.ExcludePGNs, uint32(p))
	}
	for _, i := range instances {
		f.Instances = append(f.Instances, uint8(i))
	}
	return f
}

// ---------------------------------------------------------------------------
// Journal inspect
// ---------------------------------------------------------------------------

func compressionName(c journal.CompressionType) string {
	switch c {
	case journal.CompressionNone:
		return "none"
	case journal.CompressionZstd:
		return "zstd"
	case journal.CompressionZstdDict:
		return "zstd+dict"
	default:
		return fmt.Sprintf("unknown(%d)", c)
	}
}

func runInspect(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return err
	}

	reader, err := journal.NewReader(f)
	if err != nil {
		return err
	}

	compressed := reader.Compression() != journal.CompressionNone
	hasDict := reader.Compression() == journal.CompressionZstdDict

	// Header
	fmt.Printf("File: %s (%s)\n\n", path, formatBytes(uint64(fi.Size())))
	fmt.Printf("Header:\n")
	fmt.Printf("  Magic:       LPJ\n")
	fmt.Printf("  Version:     %d\n", reader.Version())
	fmt.Printf("  BlockSize:   %d\n", reader.BlockSize())
	fmt.Printf("  Compression: %s (%d)\n", compressionName(reader.Compression()), reader.Compression())
	fmt.Println()

	nBlocks := reader.BlockCount()
	if nBlocks == 0 {
		fmt.Printf("Blocks: 0\n")
		return nil
	}

	fmt.Printf("Blocks: %d\n\n", nBlocks)

	// Table header
	isV2 := reader.Version() == journal.Version2
	if compressed && hasDict {
		if isV2 {
			fmt.Printf("  %-4s  %10s  %12s  %10s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"#", "OFFSET", "BASE SEQ", "DICT", "COMPRESSED", "BLOCK SIZE", "RATIO", "FRAMES", "DEVS", "BASE TIME (UTC)")
			fmt.Printf("  %-4s  %10s  %12s  %10s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"----", "----------", "------------", "----------", "----------", "----------", "------", "------", "-----", "----------------------------")
		} else {
			fmt.Printf("  %-4s  %10s  %10s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"#", "OFFSET", "DICT", "COMPRESSED", "BLOCK SIZE", "RATIO", "FRAMES", "DEVS", "BASE TIME (UTC)")
			fmt.Printf("  %-4s  %10s  %10s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"----", "----------", "----------", "----------", "----------", "------", "------", "-----", "----------------------------")
		}
	} else if compressed {
		if isV2 {
			fmt.Printf("  %-4s  %10s  %12s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"#", "OFFSET", "BASE SEQ", "COMPRESSED", "BLOCK SIZE", "RATIO", "FRAMES", "DEVS", "BASE TIME (UTC)")
			fmt.Printf("  %-4s  %10s  %12s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"----", "----------", "------------", "----------", "----------", "------", "------", "-----", "----------------------------")
		} else {
			fmt.Printf("  %-4s  %10s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"#", "OFFSET", "COMPRESSED", "BLOCK SIZE", "RATIO", "FRAMES", "DEVS", "BASE TIME (UTC)")
			fmt.Printf("  %-4s  %10s  %10s  %10s  %6s  %6s  %5s  %s\n",
				"----", "----------", "----------", "----------", "------", "------", "-----", "----------------------------")
		}
	} else {
		if isV2 {
			fmt.Printf("  %-4s  %10s  %12s  %10s  %6s  %5s  %s\n",
				"#", "OFFSET", "BASE SEQ", "BLOCK SIZE", "FRAMES", "DEVS", "BASE TIME (UTC)")
			fmt.Printf("  %-4s  %10s  %12s  %10s  %6s  %5s  %s\n",
				"----", "----------", "------------", "----------", "------", "-----", "----------------------------")
		} else {
			fmt.Printf("  %-4s  %10s  %10s  %6s  %5s  %s\n",
				"#", "OFFSET", "BLOCK SIZE", "FRAMES", "DEVS", "BASE TIME (UTC)")
			fmt.Printf("  %-4s  %10s  %10s  %6s  %5s  %s\n",
				"----", "----------", "----------", "------", "-----", "----------------------------")
		}
	}

	var totalCompressed int64
	var totalUncompressed int64
	var totalDictOverhead int64

	for i := range nBlocks {
		bi, err := reader.InspectBlock(i)
		if err != nil {
			fmt.Printf("  %-4d  error: %v\n", i, err)
			continue
		}

		tsStr := bi.BaseTime.UTC().Format("2006-01-02T15:04:05Z")

		if compressed && hasDict {
			ratio := float64(reader.BlockSize()) / float64(bi.CompressedLen+bi.DictLen)
			if isV2 {
				fmt.Printf("  %-4d  %10d  %12d  %10d  %10d  %10d  %5.1fx  %6d  %5d  %s\n",
					i, bi.Offset, bi.BaseSeq, bi.DictLen, bi.CompressedLen, reader.BlockSize(), ratio, bi.FrameCount, bi.DeviceCount, tsStr)
			} else {
				fmt.Printf("  %-4d  %10d  %10d  %10d  %10d  %5.1fx  %6d  %5d  %s\n",
					i, bi.Offset, bi.DictLen, bi.CompressedLen, reader.BlockSize(), ratio, bi.FrameCount, bi.DeviceCount, tsStr)
			}
			totalCompressed += int64(bi.CompressedLen) + int64(bi.DictLen) + int64(journal.BlockHeaderLenDict)
			totalDictOverhead += int64(bi.DictLen)
			totalUncompressed += int64(reader.BlockSize())
		} else if compressed {
			ratio := float64(reader.BlockSize()) / float64(bi.CompressedLen)
			if isV2 {
				fmt.Printf("  %-4d  %10d  %12d  %10d  %10d  %5.1fx  %6d  %5d  %s\n",
					i, bi.Offset, bi.BaseSeq, bi.CompressedLen, reader.BlockSize(), ratio, bi.FrameCount, bi.DeviceCount, tsStr)
			} else {
				fmt.Printf("  %-4d  %10d  %10d  %10d  %5.1fx  %6d  %5d  %s\n",
					i, bi.Offset, bi.CompressedLen, reader.BlockSize(), ratio, bi.FrameCount, bi.DeviceCount, tsStr)
			}
			totalCompressed += int64(bi.CompressedLen) + int64(journal.BlockHeaderLen)
			totalUncompressed += int64(reader.BlockSize())
		} else {
			if isV2 {
				fmt.Printf("  %-4d  %10d  %12d  %10d  %6d  %5d  %s\n",
					i, bi.Offset, bi.BaseSeq, reader.BlockSize(), bi.FrameCount, bi.DeviceCount, tsStr)
			} else {
				fmt.Printf("  %-4d  %10d  %10d  %6d  %5d  %s\n",
					i, bi.Offset, reader.BlockSize(), bi.FrameCount, bi.DeviceCount, tsStr)
			}
			totalUncompressed += int64(reader.BlockSize())
		}
	}

	fmt.Println()

	// Summary
	if compressed {
		ratio := float64(totalUncompressed) / float64(totalCompressed)
		fmt.Printf("Totals:\n")
		fmt.Printf("  Uncompressed: %s (%d bytes)\n", formatBytes(uint64(totalUncompressed)), totalUncompressed)
		fmt.Printf("  Compressed:   %s (%d bytes, including block headers)\n", formatBytes(uint64(totalCompressed)), totalCompressed)
		if totalDictOverhead > 0 {
			fmt.Printf("  Dict overhead: %s (%d bytes)\n", formatBytes(uint64(totalDictOverhead)), totalDictOverhead)
		}
		fmt.Printf("  Ratio:        %.1fx\n", ratio)
	}

	// Footer
	if compressed {
		hasIndex := reader.HasBlockIndex()
		fmt.Println()
		fmt.Printf("Footer:\n")
		if hasIndex {
			indexSize := nBlocks*8 + 8
			fmt.Printf("  Block Index: present (LPJI magic)\n")
			fmt.Printf("  Entries:     %d\n", nBlocks)
			fmt.Printf("  Index Size:  %d bytes\n", indexSize)
		} else {
			fmt.Printf("  Block Index: missing (recovered via forward scan)\n")
		}
	}

	// Time span + seq range
	first, err := reader.InspectBlock(0)
	if err == nil {
		last, err := reader.InspectBlock(nBlocks - 1)
		if err == nil {
			duration := last.BaseTime.Sub(first.BaseTime)
			fmt.Println()
			fmt.Printf("Time Span:\n")
			fmt.Printf("  First: %s\n", first.BaseTime.UTC().Format(time.RFC3339))
			fmt.Printf("  Last:  %s\n", last.BaseTime.UTC().Format(time.RFC3339))
			fmt.Printf("  Span:  %s\n", duration.Truncate(time.Second))

			if isV2 && first.BaseSeq > 0 {
				lastSeq := last.BaseSeq + uint64(last.FrameCount) - 1
				fmt.Println()
				fmt.Printf("Sequence Range:\n")
				fmt.Printf("  First: %d\n", first.BaseSeq)
				fmt.Printf("  Last:  %d\n", lastSeq)
				fmt.Printf("  Total: %d frames\n", lastSeq-first.BaseSeq+1)
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Journal replay
// ---------------------------------------------------------------------------

func runReplay(ctx context.Context, path string, speed float64, startTimeStr string, jsonMode, decode bool, pgns uintSlice, excludePGNs uintSlice, manufacturers stringSlice, instances uintSlice, names stringSlice, devices *deviceMap) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		return err
	}

	log.Printf("journal: %s (%d blocks)", path, reader.BlockCount())

	if startTimeStr != "" {
		t, err := time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			return fmt.Errorf("invalid -start time: %w", err)
		}
		if err := reader.SeekToTime(t); err != nil {
			return fmt.Errorf("seek: %w", err)
		}
		log.Printf("seeked to %s (block %d)", t.Format(time.RFC3339), reader.CurrentBlock())
	}

	// Load initial device table if we seeked into a block.
	lastBlock := reader.CurrentBlock()
	if lastBlock >= 0 {
		loadDeviceTable(reader, devices)
		printDeviceTable(os.Stderr, devices)
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	var (
		frameCount    uint64
		matchCount    uint64
		firstTs       time.Time
		lastTs        time.Time
		prevTs        time.Time
		blocksRead    int
		printedDevices bool
	)

	for reader.Next() {
		if ctx.Err() != nil {
			break
		}

		entry := reader.Frame()
		frameSeq := reader.FrameSeq() // v2: actual seq; v1: 0

		// Refresh device table on block transitions.
		if reader.CurrentBlock() != lastBlock {
			lastBlock = reader.CurrentBlock()
			blocksRead++
			loadDeviceTable(reader, devices)
			// Print device table once after the first block loads.
			if !printedDevices {
				printedDevices = true
				printDeviceTable(os.Stderr, devices)
			}
		}

		frameCount++
		if firstTs.IsZero() {
			firstTs = entry.Timestamp
		}
		lastTs = entry.Timestamp

		// Client-side filtering.
		if !matchesReplayFilter(entry, devices, pgns, excludePGNs, manufacturers, instances, names) {
			prevTs = entry.Timestamp
			continue
		}
		matchCount++

		// Speed throttle: sleep for (delta / speed) between frames.
		if speed > 0 && !prevTs.IsZero() {
			delta := entry.Timestamp.Sub(prevTs)
			if delta > 0 {
				time.Sleep(time.Duration(float64(delta) / speed))
			}
		}
		prevTs = entry.Timestamp

		// Use actual journal seq for v2, fall back to match count for v1.
		seq := frameSeq
		if seq == 0 {
			seq = matchCount
		}
		fr := entryToFrame(entry, seq)

		if jsonMode {
			writeJSONFrame(out, &fr, decode)
		} else {
			formatFrame(out, &fr, devices, decode)
		}
		out.Flush()
	}

	if err := reader.Err(); err != nil {
		return fmt.Errorf("journal read: %w", err)
	}

	duration := lastTs.Sub(firstTs)
	if blocksRead == 0 && lastBlock >= 0 {
		blocksRead = 1
	}
	log.Printf("replay done: %d frames read, %d matched, %d blocks, %s span (%s to %s)",
		frameCount, matchCount, blocksRead,
		duration.Truncate(time.Second),
		firstTs.UTC().Format("15:04:05"),
		lastTs.UTC().Format("15:04:05"),
	)

	return nil
}

// loadDeviceTable reads the current block's device table and populates the device map.
func loadDeviceTable(reader *journal.Reader, devices *deviceMap) {
	for _, jd := range reader.BlockDevices() {
		fields := canbus.DecodeNAME(jd.NAME)
		devices.update(lplexc.Device{
			Src:              jd.Source,
			Name:             fields.NAMEHex,
			Manufacturer:     fields.Manufacturer,
			ManufacturerCode: fields.ManufacturerCode,
			DeviceClass:      fields.DeviceClass,
			DeviceFunction:   fields.DeviceFunction,
			DeviceInstance:   fields.DeviceInstance,
			UniqueNumber:     fields.UniqueNumber,
			ProductCode:      jd.ProductCode,
			ModelID:          jd.ModelID,
			SoftwareVersion:  jd.SoftwareVersion,
			ModelVersion:     jd.ModelVersion,
			ModelSerial:      jd.ModelSerial,
		})
	}
}

// entryToFrame converts a journal entry to the lplexc frame struct.
func entryToFrame(entry journal.Entry, seq uint64) lplexc.Frame {
	return lplexc.Frame{
		Seq:  seq,
		Ts:   entry.Timestamp.UTC().Format(time.RFC3339Nano),
		Prio: entry.Header.Priority,
		PGN:  entry.Header.PGN,
		Src:  entry.Header.Source,
		Dst:  entry.Header.Destination,
		Data: hex.EncodeToString(entry.Data),
	}
}

// matchesReplayFilter applies client-side filters to a journal entry.
// Categories are AND'd, values within a category are OR'd.
func matchesReplayFilter(entry journal.Entry, devices *deviceMap, pgns uintSlice, excludePGNs uintSlice, manufacturers stringSlice, instances uintSlice, names stringSlice) bool {
	if len(pgns) == 0 && len(excludePGNs) == 0 && len(manufacturers) == 0 && len(instances) == 0 && len(names) == 0 {
		return true
	}

	if len(pgns) > 0 {
		matched := false
		for _, pgn := range pgns {
			if uint32(pgn) == entry.Header.PGN {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if len(excludePGNs) > 0 {
		for _, pgn := range excludePGNs {
			if uint32(pgn) == entry.Header.PGN {
				return false
			}
		}
	}

	if len(manufacturers) > 0 || len(instances) > 0 || len(names) > 0 {
		dev, ok := devices.get(entry.Header.Source)
		if !ok {
			return false
		}

		if len(manufacturers) > 0 {
			matched := false
			for _, m := range manufacturers {
				if strings.EqualFold(dev.Manufacturer, m) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}

		if len(instances) > 0 {
			matched := false
			for _, inst := range instances {
				if uint8(inst) == dev.DeviceInstance {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}

		if len(names) > 0 {
			matched := false
			for _, n := range names {
				if strings.EqualFold(dev.Name, n) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}

	return true
}

func ackFinal(client *lplexc.Client, clientID string, seq uint64) {
	if seq > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		session, err := client.CreateSession(ctx, lplexc.SessionConfig{
			ClientID:      clientID,
			BufferTimeout: "PT0S",
		})
		if err == nil {
			if err := session.Ack(ctx, seq); err != nil {
				log.Printf("final ack failed: %v", err)
			}
		}
	}
	log.Printf("bye")
}

func runEphemeral(ctx context.Context, client *lplexc.Client, jsonMode, decode bool, filter *lplexc.Filter, devices *deviceMap, lastSeq *atomic.Uint64) error {
	// Fetch devices for display.
	devs, err := client.Devices(ctx)
	if err == nil && len(devs) > 0 {
		devices.loadAll(devs)
		if !jsonMode {
			printDeviceTable(os.Stderr, devices)
		}
	}

	sub, err := client.Subscribe(ctx, filter)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer func() { _ = sub.Close() }()

	log.Printf("streaming (ephemeral)")

	return streamEvents(sub, jsonMode, decode, devices, lastSeq)
}

func runBuffered(ctx context.Context, client *lplexc.Client, clientID, bufferTimeout string, ackInterval time.Duration, jsonMode, decode bool, filter *lplexc.Filter, devices *deviceMap, lastSeq *atomic.Uint64) error {
	session, err := client.CreateSession(ctx, lplexc.SessionConfig{
		ClientID:      clientID,
		BufferTimeout: bufferTimeout,
		Filter:        filter,
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	info := session.Info()
	log.Printf("session %q: head=%d cursor=%d", info.ClientID, info.Seq, info.Cursor)

	if len(info.Devices) > 0 {
		devices.loadAll(info.Devices)
		if !jsonMode {
			printDeviceTable(os.Stderr, devices)
		}
	}

	sub, err := session.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer func() { _ = sub.Close() }()

	log.Printf("streaming (buffered, session=%s)", clientID)

	// Periodic ACK goroutine.
	ackCtx, ackCancel := context.WithCancel(ctx)
	defer ackCancel()
	var wg sync.WaitGroup
	wg.Go(func() {
		ticker := time.NewTicker(ackInterval)
		defer ticker.Stop()
		var lastAcked uint64
		for {
			select {
			case <-ticker.C:
				seq := lastSeq.Load()
				if seq > lastAcked {
					if err := session.Ack(ackCtx, seq); err != nil {
						log.Printf("ack: %v", err)
					} else {
						lastAcked = seq
						log.Printf("ack seq=%d", seq)
					}
				}
			case <-ackCtx.Done():
				return
			}
		}
	})

	err = streamEvents(sub, jsonMode, decode, devices, lastSeq)

	ackCancel()
	wg.Wait()

	return err
}

func streamEvents(sub *lplexc.Subscription, jsonMode, decode bool, devices *deviceMap, lastSeq *atomic.Uint64) error {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for {
		ev, err := sub.Next()
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("stream closed by server")
			}
			return fmt.Errorf("reading stream: %w", err)
		}

		if ev.Device != nil {
			devices.update(*ev.Device)
			if !jsonMode {
				log.Printf("device discovered: %s at src=%d", ev.Device.Manufacturer, ev.Device.Src)
				printDeviceTable(os.Stderr, devices)
			}
			continue
		}

		if ev.Frame != nil {
			if ev.Frame.Seq > 0 {
				lastSeq.Store(ev.Frame.Seq)
			}
			if jsonMode {
				writeJSONFrame(out, ev.Frame, decode)
			} else {
				formatFrame(out, ev.Frame, devices, decode)
			}
			out.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// Output formatting
// ---------------------------------------------------------------------------

// ANSI escape codes.
const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiBlue    = "\033[34m"
	ansiMagenta = "\033[35m"
	ansiCyan    = "\033[36m"
	ansiHiGreen = "\033[92m"
	ansiHiYell  = "\033[93m"
	ansiHiBlue  = "\033[94m"
	ansiHiMag   = "\033[95m"
	ansiHiCyan  = "\033[96m"
	ansiRed     = "\033[31m"
)

var srcPalette = []string{
	ansiGreen, ansiYellow, ansiBlue, ansiMagenta, ansiCyan,
	ansiHiGreen, ansiHiYell, ansiHiBlue, ansiHiMag, ansiHiCyan,
}

func colorForSrc(src uint8) string {
	return srcPalette[int(src)%len(srcPalette)]
}

func formatFrame(w *bufio.Writer, f *lplexc.Frame, dm *deviceMap, decode bool) {
	ts := f.Ts
	if t, err := time.Parse(time.RFC3339Nano, f.Ts); err == nil {
		ts = t.Local().Format("15:04:05.000")
	}

	srcLabel := fmt.Sprintf("[%d]", f.Src)
	if d, ok := dm.get(f.Src); ok && d.Manufacturer != "" {
		srcLabel = fmt.Sprintf("%s(%d)[%d]", d.Manufacturer, d.ManufacturerCode, f.Src)
	}

	var pgnName string
	if info, ok := pgn.Registry[f.PGN]; ok {
		pgnName = info.Description
	}
	sc := colorForSrc(f.Src)

	fmt.Fprintf(w, "%s%s%s %s#%-7d%s %s%-20s%s %s>%02x%s  %s%s%-6d%s",
		ansiDim, ts, ansiReset,
		ansiDim, f.Seq, ansiReset,
		sc+ansiBold, srcLabel, ansiReset,
		ansiDim, f.Dst, ansiReset,
		ansiCyan, ansiBold, f.PGN, ansiReset,
	)
	if pgnName != "" {
		fmt.Fprintf(w, " %s%-22s%s", ansiCyan, pgnName, ansiReset)
	} else {
		w.WriteString(strings.Repeat(" ", 23))
	}
	if !decode {
		fmt.Fprintf(w, " %sp%d%s  %s\n",
			ansiDim, f.Prio, ansiReset,
			f.Data,
		)
		return
	}
	decoded, err := decodeFrame(f)
	if err != nil {
		fmt.Fprintf(w, " %sp%d%s  %s  %s%s%s\n",
			ansiDim, f.Prio, ansiReset,
			f.Data,
			ansiRed, err.Error(), ansiReset,
		)
		return
	}
	if decoded == nil {
		fmt.Fprintf(w, " %sp%d%s  %s\n",
			ansiDim, f.Prio, ansiReset,
			f.Data,
		)
		return
	}
	b, _ := json.Marshal(decoded)
	fmt.Fprintf(w, " %sp%d%s  %s%s%s\n",
		ansiDim, f.Prio, ansiReset,
		ansiDim, string(b), ansiReset,
	)
}

// decodeFrame attempts to decode a frame's hex data using the pgn.Registry.
func decodeFrame(f *lplexc.Frame) (any, error) {
	info, ok := pgn.Registry[f.PGN]
	if !ok || info.Decode == nil {
		return nil, nil
	}
	data, err := hex.DecodeString(f.Data)
	if err != nil {
		return nil, err
	}
	return info.Decode(data)
}

// writeJSONFrame writes a frame as a JSON line, optionally with decoded fields.
func writeJSONFrame(w *bufio.Writer, f *lplexc.Frame, decode bool) {
	if !decode {
		fmt.Fprintf(w, "{\"seq\":%d,\"ts\":\"%s\",\"prio\":%d,\"pgn\":%d,\"src\":%d,\"dst\":%d,\"data\":\"%s\"}\n",
			f.Seq, f.Ts, f.Prio, f.PGN, f.Src, f.Dst, f.Data)
		return
	}
	type jsonFrame struct {
		Seq         uint64 `json:"seq"`
		Ts          string `json:"ts"`
		Prio        uint8  `json:"prio"`
		PGN         uint32 `json:"pgn"`
		Src         uint8  `json:"src"`
		Dst         uint8  `json:"dst"`
		Data        string `json:"data"`
		Decoded     any    `json:"decoded,omitempty"`
		DecodeError string `json:"decode_error,omitempty"`
	}
	jf := jsonFrame{
		Seq: f.Seq, Ts: f.Ts, Prio: f.Prio,
		PGN: f.PGN, Src: f.Src, Dst: f.Dst, Data: f.Data,
	}
	if v, err := decodeFrame(f); err != nil {
		jf.DecodeError = err.Error()
	} else if v != nil {
		jf.Decoded = v
	}
	b, _ := json.Marshal(jf)
	_, _ = w.Write(b)
	_ = w.WriteByte('\n')
}

func classLabel(code uint8) string {
	if name, ok := deviceClasses[code]; ok {
		return fmt.Sprintf("%s (%d)", name, code)
	}
	return fmt.Sprintf("%d", code)
}

func funcLabel(class, fn uint8) string {
	key := uint16(class)<<8 | uint16(fn)
	if name, ok := deviceFunctions[key]; ok {
		return fmt.Sprintf("%s (%d)", name, fn)
	}
	return fmt.Sprintf("%d", fn)
}

func formatTime(s string) string {
	if s == "" || s == "0001-01-01T00:00:00Z" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return ""
	}
	return t.Local().Format("15:04:05")
}

func displayManufacturer(d lplexc.Device) string {
	if d.Manufacturer != "" {
		return fmt.Sprintf("%s (%d)", d.Manufacturer, d.ManufacturerCode)
	}
	return fmt.Sprintf("[src=%d]", d.Src)
}

func formatBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func printDeviceTable(w *os.File, dm *deviceMap) {
	devs := dm.sorted()
	if len(devs) == 0 {
		return
	}

	type row struct {
		dev                        lplexc.Device
		mfctrStr, modelStr         string
		classStr, funcStr          string
		trafficStr                 string
		firstStr, lastStr          string
	}
	rows := make([]row, len(devs))
	mfctrW := len("MANUFACTURER")
	modelW := len("MODEL")
	classW := len("CLASS")
	funcW := len("FUNCTION")
	trafficW := len("TRAFFIC")
	for i, d := range devs {
		mfctr := displayManufacturer(d)
		model := d.ModelID
		if d.ProductCode > 0 {
			model = fmt.Sprintf("%s (%d)", d.ModelID, d.ProductCode)
		}
		traffic := formatBytes(d.ByteCount)
		rows[i] = row{
			dev:        d,
			mfctrStr:   mfctr,
			modelStr:   model,
			classStr:   classLabel(d.DeviceClass),
			funcStr:    funcLabel(d.DeviceClass, d.DeviceFunction),
			trafficStr: traffic,
			firstStr:   formatTime(d.FirstSeen),
			lastStr:    formatTime(d.LastSeen),
		}
		mfctrW = max(mfctrW, len(mfctr))
		modelW = max(modelW, len(model))
		classW = max(classW, len(rows[i].classStr))
		funcW = max(funcW, len(rows[i].funcStr))
		trafficW = max(trafficW, len(traffic))
	}

	hLine := func(left, mid, right, fill string) string {
		return left +
			strings.Repeat(fill, 5) + mid +
			strings.Repeat(fill, 18) + mid +
			strings.Repeat(fill, mfctrW+2) + mid +
			strings.Repeat(fill, modelW+2) + mid +
			strings.Repeat(fill, classW+2) + mid +
			strings.Repeat(fill, funcW+2) + mid +
			strings.Repeat(fill, 6) + mid +
			strings.Repeat(fill, trafficW+2) + mid +
			strings.Repeat(fill, 10) + mid +
			strings.Repeat(fill, 10) + right
	}

	top := hLine("┌", "┬", "┐", "─")
	sep := hLine("├", "┼", "┤", "─")
	bot := hLine("└", "┴", "┘", "─")

	fmt.Fprintf(w, "\n%s%s%s\n", ansiDim, top, ansiReset)
	fmt.Fprintf(w, "%s│%s %sSRC%s %s│%s %sNAME            %s %s│%s %s%-*s%s %s│%s %s%-*s%s %s│%s %s%-*s%s %s│%s %s%-*s%s %s│%s %sINST%s %s│%s %s%*s%s %s│%s %sFIRST   %s %s│%s %sLAST    %s %s│%s\n",
		ansiDim, ansiReset,
		ansiBold, ansiReset,
		ansiDim, ansiReset,
		ansiBold, ansiReset,
		ansiDim, ansiReset,
		ansiBold, mfctrW, "MANUFACTURER", ansiReset,
		ansiDim, ansiReset,
		ansiBold, modelW, "MODEL", ansiReset,
		ansiDim, ansiReset,
		ansiBold, classW, "CLASS", ansiReset,
		ansiDim, ansiReset,
		ansiBold, funcW, "FUNCTION", ansiReset,
		ansiDim, ansiReset,
		ansiBold, ansiReset,
		ansiDim, ansiReset,
		ansiBold, trafficW, "TRAFFIC", ansiReset,
		ansiDim, ansiReset,
		ansiBold, ansiReset,
		ansiDim, ansiReset,
		ansiBold, ansiReset,
		ansiDim, ansiReset,
	)
	fmt.Fprintf(w, "%s%s%s\n", ansiDim, sep, ansiReset)

	for _, r := range rows {
		sc := colorForSrc(r.dev.Src)
		fmt.Fprintf(w, "%s│%s %s%3d%s %s│%s %s%-16s%s %s│%s %s%-*s%s %s│%s %-*s %s│%s %-*s %s│%s %-*s %s│%s %4d %s│%s %*s %s│%s %8s %s│%s %8s %s│%s\n",
			ansiDim, ansiReset,
			sc+ansiBold, r.dev.Src, ansiReset,
			ansiDim, ansiReset,
			ansiDim, r.dev.Name, ansiReset,
			ansiDim, ansiReset,
			sc, mfctrW, r.mfctrStr, ansiReset,
			ansiDim, ansiReset,
			modelW, r.modelStr,
			ansiDim, ansiReset,
			classW, r.classStr,
			ansiDim, ansiReset,
			funcW, r.funcStr,
			ansiDim, ansiReset,
			r.dev.DeviceInstance,
			ansiDim, ansiReset,
			trafficW, r.trafficStr,
			ansiDim, ansiReset,
			r.firstStr,
			ansiDim, ansiReset,
			r.lastStr,
			ansiDim, ansiReset,
		)
	}

	fmt.Fprintf(w, "%s%s%s\n\n", ansiDim, bot, ansiReset)
}

// ---------------------------------------------------------------------------
// Device tracking
// ---------------------------------------------------------------------------

type deviceMap struct {
	mu      sync.RWMutex
	devices map[uint8]lplexc.Device
}

func newDeviceMap() *deviceMap {
	return &deviceMap{devices: make(map[uint8]lplexc.Device)}
}

func (dm *deviceMap) update(d lplexc.Device) {
	dm.mu.Lock()
	dm.devices[d.Src] = d
	dm.mu.Unlock()
}

func (dm *deviceMap) get(src uint8) (lplexc.Device, bool) {
	dm.mu.RLock()
	d, ok := dm.devices[src]
	dm.mu.RUnlock()
	return d, ok
}

func (dm *deviceMap) loadAll(devs []lplexc.Device) {
	dm.mu.Lock()
	for _, d := range devs {
		dm.devices[d.Src] = d
	}
	dm.mu.Unlock()
}

func (dm *deviceMap) sorted() []lplexc.Device {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	result := make([]lplexc.Device, 0, len(dm.devices))
	for _, d := range dm.devices {
		result = append(result, d)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Src < result[j].Src })
	return result
}

// ---------------------------------------------------------------------------
// Terminal detection
// ---------------------------------------------------------------------------

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ---------------------------------------------------------------------------
// NMEA 2000 device class names
// ---------------------------------------------------------------------------

var deviceClasses = map[uint8]string{
	0:   "Reserved",
	10:  "System Tools",
	20:  "Safety",
	25:  "Internetwork",
	30:  "Electrical Distribution",
	35:  "Electrical Generation",
	40:  "Steering/Control",
	50:  "Propulsion",
	60:  "Navigation",
	70:  "Communication",
	75:  "Sensor Interface",
	80:  "Instrumentation",
	85:  "External Environment",
	90:  "Internal Environment",
	100: "Deck/Cargo/Fishing",
	110: "Human Interface",
	120: "Display",
	125: "Entertainment",
}

// ---------------------------------------------------------------------------
// NMEA 2000 device function names, keyed by (class<<8 | function)
// ---------------------------------------------------------------------------

var deviceFunctions = map[uint16]string{
	10<<8 | 130: "Diagnostic",
	10<<8 | 140: "Bus Traffic Logger",
	20<<8 | 110: "Alarm Enunciator",
	20<<8 | 130: "EPIRB",
	20<<8 | 135: "Man Overboard",
	20<<8 | 140: "Voyage Data Recorder",
	20<<8 | 150: "Camera",
	25<<8 | 130: "PC Gateway",
	25<<8 | 131: "N2K-Analog Gateway",
	25<<8 | 132: "Analog-N2K Gateway",
	25<<8 | 133: "N2K-Serial Gateway",
	25<<8 | 135: "NMEA 0183 Gateway",
	25<<8 | 136: "NMEA Network Gateway",
	25<<8 | 137: "N2K Wireless Gateway",
	25<<8 | 140: "Router",
	25<<8 | 150: "Bridge",
	25<<8 | 160: "Repeater",
	30<<8 | 130: "Binary Event Monitor",
	30<<8 | 140: "Load Controller",
	30<<8 | 141: "AC/DC Input",
	30<<8 | 150: "Function Controller",
	35<<8 | 140: "Engine",
	35<<8 | 141: "DC Generator",
	35<<8 | 142: "Solar Panel",
	35<<8 | 143: "Wind Generator",
	35<<8 | 144: "Fuel Cell",
	35<<8 | 145: "Network Power Supply",
	35<<8 | 151: "AC Generator",
	35<<8 | 152: "AC Bus",
	35<<8 | 153: "AC Mains/Shore",
	35<<8 | 154: "AC Output",
	35<<8 | 160: "Battery Charger",
	35<<8 | 161: "Charger+Inverter",
	35<<8 | 162: "Inverter",
	35<<8 | 163: "DC Converter",
	35<<8 | 170: "Battery",
	35<<8 | 180: "Engine Gateway",
	40<<8 | 130: "Follow-up Controller",
	40<<8 | 140: "Mode Controller",
	40<<8 | 150: "Autopilot",
	40<<8 | 155: "Rudder",
	40<<8 | 160: "Heading Sensors",
	40<<8 | 170: "Trim/Interceptors",
	40<<8 | 180: "Attitude Control",
	50<<8 | 130: "Engineroom Monitor",
	50<<8 | 140: "Engine",
	50<<8 | 141: "DC Generator",
	50<<8 | 150: "Engine Controller",
	50<<8 | 151: "AC Generator",
	50<<8 | 155: "Motor",
	50<<8 | 160: "Engine Gateway",
	50<<8 | 165: "Transmission",
	50<<8 | 170: "Throttle/Shift",
	50<<8 | 180: "Actuator",
	50<<8 | 190: "Gauge Interface",
	50<<8 | 200: "Gauge Large",
	50<<8 | 210: "Gauge Small",
	60<<8 | 130: "Depth",
	60<<8 | 135: "Depth/Speed",
	60<<8 | 136: "Depth/Speed/Temp",
	60<<8 | 140: "Attitude",
	60<<8 | 145: "GNSS",
	60<<8 | 150: "Loran C",
	60<<8 | 155: "Speed",
	60<<8 | 160: "Turn Rate",
	60<<8 | 170: "Integrated Nav",
	60<<8 | 175: "Integrated Nav System",
	60<<8 | 190: "Nav Management",
	60<<8 | 195: "AIS",
	60<<8 | 200: "Radar",
	60<<8 | 201: "Infrared Imaging",
	60<<8 | 205: "ECDIS",
	60<<8 | 210: "ECS",
	60<<8 | 220: "Direction Finder",
	60<<8 | 230: "Voyage Status",
	70<<8 | 130: "EPIRB",
	70<<8 | 140: "AIS",
	70<<8 | 150: "DSC",
	70<<8 | 160: "Data Transceiver",
	70<<8 | 170: "Satellite",
	70<<8 | 180: "MF/HF Radio",
	70<<8 | 190: "VHF Radio",
	75<<8 | 130: "Temperature",
	75<<8 | 140: "Pressure",
	75<<8 | 150: "Fluid Level",
	75<<8 | 160: "Flow",
	75<<8 | 170: "Humidity",
	80<<8 | 130: "Time/Date",
	80<<8 | 140: "VDR",
	80<<8 | 150: "Integrated Instrumentation",
	80<<8 | 160: "General Purpose Display",
	80<<8 | 170: "General Sensor Box",
	80<<8 | 180: "Weather Instruments",
	80<<8 | 190: "Transducer/General",
	80<<8 | 200: "NMEA 0183 Converter",
	85<<8 | 130: "Atmospheric",
	85<<8 | 160: "Aquatic",
	90<<8 | 130: "HVAC",
	100<<8 | 130: "Scale (Catch)",
	110<<8 | 130: "Button Interface",
	110<<8 | 135: "Switch Interface",
	110<<8 | 140: "Analog Interface",
	120<<8 | 130: "Display",
	120<<8 | 140: "Alarm Enunciator",
	125<<8 | 130: "Multimedia Player",
	125<<8 | 140: "Multimedia Controller",
}
