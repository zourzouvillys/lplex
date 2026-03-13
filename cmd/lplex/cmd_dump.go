package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sixfathoms/lplex"
	"github.com/sixfathoms/lplex/canbus"
	"github.com/sixfathoms/lplex/filter"
	"github.com/sixfathoms/lplex/journal"
	"github.com/sixfathoms/lplex/lplexc"
	"github.com/spf13/cobra"
)

var dumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Stream or replay NMEA 2000 frames",
	Long:  "Stream live frames from an lplex server or replay from journal files.",
	RunE:  runDump,
}

// Dump-specific flags.
var (
	dumpClientID      string
	dumpBufferTimeout string
	dumpReconnect     bool
	dumpReconnectDly  time.Duration
	dumpAckInterval   time.Duration
	dumpDecode        bool
	dumpChanges       bool
	dumpFile          string
	dumpInspect       bool
	dumpSpeed         float64
	dumpStartTime     string
	dumpWhere         string

	dumpFilterPGNs     uintSlice
	dumpExcludePGNs    uintSlice
	dumpManufacturers  stringSlice
	dumpInstances      uintSlice
	dumpFilterNames    stringSlice
	dumpExcludeNames   stringSlice
)

func init() {
	f := dumpCmd.Flags()
	f.StringVar(&dumpClientID, "client-id", "", "client session ID (default: hostname)")
	f.StringVar(&dumpBufferTimeout, "buffer-timeout", "", "enable session buffering with this duration (ISO 8601, e.g. PT5M)")
	f.BoolVar(&dumpReconnect, "reconnect", true, "auto-reconnect on disconnect")
	f.DurationVar(&dumpReconnectDly, "reconnect-delay", 2*time.Second, "delay between reconnect attempts")
	f.DurationVar(&dumpAckInterval, "ack-interval", 5*time.Second, "how often to ACK the latest seq (only with buffered sessions)")
	f.BoolVar(&dumpDecode, "decode", false, "decode known PGNs and display field values")
	f.BoolVar(&dumpChanges, "changes", false, "only show frames with changed data (suppress duplicates)")

	// Journal flags.
	f.StringVar(&dumpFile, "file", "", "replay from .lpj journal file (mutually exclusive with --server)")
	f.BoolVar(&dumpInspect, "inspect", false, "inspect journal file structure (use with --file)")
	f.Float64Var(&dumpSpeed, "speed", 1.0, "playback speed multiplier (0 = as fast as possible, 1.0 = real-time)")
	f.StringVar(&dumpStartTime, "start", "", "seek to RFC3339 timestamp before replaying")

	// Filter flags.
	f.VarP(&dumpFilterPGNs, "pgn", "", "filter by PGN (repeatable)")
	f.VarP(&dumpExcludePGNs, "exclude-pgn", "", "exclude PGN from output (repeatable)")
	f.VarP(&dumpManufacturers, "manufacturer", "", "filter by manufacturer name (repeatable)")
	f.VarP(&dumpInstances, "instance", "", "filter by device instance (repeatable)")
	f.VarP(&dumpFilterNames, "name", "", "filter by 64-bit CAN NAME in hex (repeatable)")
	f.VarP(&dumpExcludeNames, "exclude-name", "", "exclude device by 64-bit CAN NAME in hex (repeatable)")
	f.StringVar(&dumpWhere, "where", "", `display filter expression (e.g. "water_temperature < 280")`)
}

func runDump(cmd *cobra.Command, _ []string) error {
	jsonMode := flagJSON || !isTerminal(os.Stdout)

	if flagQuiet {
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(os.Stderr)
	}
	log.SetFlags(log.Ltime)

	// Compile display filter expression.
	var displayFilter *filter.Filter
	if dumpWhere != "" {
		var err error
		displayFilter, err = filter.Compile(dumpWhere)
		if err != nil {
			return fmt.Errorf("invalid --where expression: %w", err)
		}
		if displayFilter.NeedsDecode() {
			dumpDecode = true
		}
	}

	// Journal inspect mode (redirect to inspect command for compat).
	if dumpInspect {
		if dumpFile == "" {
			return fmt.Errorf("--inspect requires --file")
		}
		return runInspectFile(dumpFile)
	}

	// Journal replay mode.
	if dumpFile != "" {
		if flagServer != "" || dumpBufferTimeout != "" {
			return fmt.Errorf("--file cannot be used with --server or --buffer-timeout")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		devices := newDeviceMap()
		return runReplay(ctx, dumpFile, dumpSpeed, dumpStartTime, jsonMode, dumpDecode, dumpChanges,
			dumpFilterPGNs, dumpExcludePGNs, dumpManufacturers, dumpInstances, dumpFilterNames, dumpExcludeNames,
			displayFilter, devices)
	}

	// Validate mutually exclusive flags.
	boatSet := cmd.Flags().Changed("boat") || rootCmd.PersistentFlags().Changed("boat")
	if boatSet && flagServer != "" {
		return fmt.Errorf("--boat and --server are mutually exclusive")
	}

	// Load config if --boat is set or --config is specified.
	var boat *BoatConfig
	var mdnsTimeout time.Duration
	if boatSet || flagConfig != "" {
		var cfgExPGNs []uint32
		var cfgExNames []string
		var err error
		boat, mdnsTimeout, cfgExPGNs, cfgExNames, err = loadBoatConfig(flagBoat, flagConfig, boatSet)
		if err != nil {
			return err
		}
		for _, p := range cfgExPGNs {
			dumpExcludePGNs = append(dumpExcludePGNs, uint(p))
		}
		dumpExcludeNames = append(dumpExcludeNames, cfgExNames...)
	}

	// Resolve server URL.
	serverURL := resolveServerURL(flagServer, boat, mdnsTimeout)

	if dumpClientID == "" && dumpBufferTimeout != "" {
		h, err := os.Hostname()
		if err != nil {
			h = "lplex"
		}
		dumpClientID = h
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := lplexc.NewClient(serverURL)
	devices := newDeviceMap()
	var lastSeq atomic.Uint64

	sseFilter := buildFilter(dumpFilterPGNs, dumpExcludePGNs, dumpManufacturers, dumpInstances, dumpFilterNames, dumpExcludeNames)
	buffered := dumpBufferTimeout != ""

	for {
		var err error
		if buffered {
			err = runBuffered(ctx, client, dumpClientID, dumpBufferTimeout, dumpAckInterval,
				jsonMode, dumpDecode, dumpChanges, sseFilter, displayFilter, devices, &lastSeq)
		} else {
			err = runEphemeral(ctx, client, jsonMode, dumpDecode, dumpChanges,
				sseFilter, displayFilter, devices, &lastSeq)
		}
		if ctx.Err() != nil {
			if buffered {
				ackFinal(client, dumpClientID, lastSeq.Load())
			}
			return nil
		}
		if err != nil {
			log.Printf("disconnected: %v", err)
		}
		if !dumpReconnect {
			if buffered {
				ackFinal(client, dumpClientID, lastSeq.Load())
			}
			return fmt.Errorf("disconnected")
		}
		log.Printf("reconnecting in %s", dumpReconnectDly)
		select {
		case <-time.After(dumpReconnectDly):
		case <-ctx.Done():
			if buffered {
				ackFinal(client, dumpClientID, lastSeq.Load())
			}
			return nil
		}

		// Re-resolve server on reconnect.
		if boat != nil {
			newURL := resolveServerURL("", boat, mdnsTimeout)
			if newURL != serverURL {
				log.Printf("server changed: %s -> %s", serverURL, newURL)
				serverURL = newURL
				client = lplexc.NewClient(serverURL)
			}
		}
	}
}

func buildFilter(pgns uintSlice, excludePGNs uintSlice, manufacturers stringSlice, instances uintSlice, names stringSlice, excludeNameList stringSlice) *lplexc.Filter {
	if len(pgns) == 0 && len(excludePGNs) == 0 && len(manufacturers) == 0 && len(instances) == 0 && len(names) == 0 && len(excludeNameList) == 0 {
		return nil
	}
	f := &lplexc.Filter{
		Manufacturers: []string(manufacturers),
		Names:         []string(names),
		ExcludeNames:  []string(excludeNameList),
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
// Journal replay
// ---------------------------------------------------------------------------

func runReplay(ctx context.Context, path string, speed float64, startTimeStr string, jsonMode, decode, changes bool, pgns uintSlice, excludePGNs uintSlice, manufacturers stringSlice, instances uintSlice, names stringSlice, excludeNames stringSlice, df *filter.Filter, devices *deviceMap) error {
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
			return fmt.Errorf("invalid --start time: %w", err)
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

	var tracker *lplex.ChangeTracker
	if changes {
		tracker = lplex.NewChangeTracker(lplex.ChangeTrackerConfig{})
	}

	var (
		frameCount     uint64
		matchCount     uint64
		firstTs        time.Time
		lastTs         time.Time
		prevTs         time.Time
		blocksRead     int
		printedDevices bool
	)

	for reader.Next() {
		if ctx.Err() != nil {
			break
		}

		entry := reader.Frame()
		frameSeq := reader.FrameSeq()

		// Refresh device table on block transitions.
		if reader.CurrentBlock() != lastBlock {
			lastBlock = reader.CurrentBlock()
			blocksRead++
			loadDeviceTable(reader, devices)
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

		if !matchesReplayFilter(entry, devices, pgns, excludePGNs, manufacturers, instances, names, excludeNames) {
			prevTs = entry.Timestamp
			continue
		}
		matchCount++

		// Speed throttle.
		if speed > 0 && !prevTs.IsZero() {
			delta := entry.Timestamp.Sub(prevTs)
			if delta > 0 {
				time.Sleep(time.Duration(float64(delta) / speed))
			}
		}
		prevTs = entry.Timestamp

		seq := frameSeq
		if seq == 0 {
			seq = matchCount
		}
		fr := entryToFrame(entry, seq)

		var ct lplex.ChangeEventType
		var diffBytes, fullBytes int
		if tracker != nil {
			for _, idle := range tracker.Tick(entry.Timestamp) {
				if jsonMode {
					writeJSONIdleEvent(out, idle)
				} else {
					formatIdleEvent(out, idle, devices)
				}
			}

			ce := tracker.Process(entry.Timestamp, entry.Header.Source, entry.Header.PGN, entry.Data, seq)
			if ce == nil {
				continue
			}
			ct = ce.Type
			if ct == lplex.Delta {
				diffBytes = len(ce.Data)
				fullBytes = len(entry.Data)
			}
		}

		var rawDecoded any
		if decode || df != nil {
			rawDecoded, _ = decodeFrameRaw(&fr)
		}
		if !matchesDisplayFilter(df, &fr, rawDecoded, devices) {
			continue
		}

		if jsonMode {
			writeJSONFrame(out, &fr, decode, rawDecoded, ct, diffBytes, fullBytes)
		} else {
			formatFrame(out, &fr, devices, decode, rawDecoded, ct, diffBytes, fullBytes)
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

func matchesReplayFilter(entry journal.Entry, devices *deviceMap, pgns uintSlice, excludePGNs uintSlice, manufacturers stringSlice, instances uintSlice, names stringSlice, excludeNameList stringSlice) bool {
	if len(pgns) == 0 && len(excludePGNs) == 0 && len(manufacturers) == 0 && len(instances) == 0 && len(names) == 0 && len(excludeNameList) == 0 {
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

	if len(excludeNameList) > 0 {
		dev, ok := devices.get(entry.Header.Source)
		if ok {
			for _, n := range excludeNameList {
				if strings.EqualFold(dev.Name, n) {
					return false
				}
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

func runEphemeral(ctx context.Context, client *lplexc.Client, jsonMode, decode, changes bool, f *lplexc.Filter, df *filter.Filter, devices *deviceMap, lastSeq *atomic.Uint64) error {
	devs, err := client.Devices(ctx)
	if err == nil && len(devs) > 0 {
		devices.loadAll(devs)
		if !jsonMode {
			printDeviceTable(os.Stderr, devices)
		}
	}

	sub, err := client.Subscribe(ctx, f)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer func() { _ = sub.Close() }()

	log.Printf("streaming (ephemeral)")

	return streamEvents(sub, jsonMode, decode, changes, f, df, devices, lastSeq)
}

func runBuffered(ctx context.Context, client *lplexc.Client, clientID, bufferTimeout string, ackInterval time.Duration, jsonMode, decode, changes bool, f *lplexc.Filter, df *filter.Filter, devices *deviceMap, lastSeq *atomic.Uint64) error {
	session, err := client.CreateSession(ctx, lplexc.SessionConfig{
		ClientID:      clientID,
		BufferTimeout: bufferTimeout,
		Filter:        f,
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

	err = streamEvents(sub, jsonMode, decode, changes, f, df, devices, lastSeq)

	ackCancel()
	wg.Wait()

	return err
}

func streamEvents(sub *lplexc.Subscription, jsonMode, decode, changes bool, f *lplexc.Filter, df *filter.Filter, devices *deviceMap, lastSeq *atomic.Uint64) error {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	var tracker *lplex.ChangeTracker
	var tickDone chan struct{}

	if changes {
		tracker = lplex.NewChangeTracker(lplex.ChangeTrackerConfig{})

		tickDone = make(chan struct{})
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					for _, ev := range tracker.Tick(time.Now()) {
						if jsonMode {
							writeJSONIdleEvent(out, ev)
						} else {
							formatIdleEvent(out, ev, devices)
						}
						out.Flush()
					}
				case <-tickDone:
					return
				}
			}
		}()
	}
	defer func() {
		if tickDone != nil {
			close(tickDone)
		}
	}()

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

			if f != nil {
				if len(f.PGNs) > 0 && !slices.Contains(f.PGNs, ev.Frame.PGN) {
					continue
				}
				if len(f.ExcludePGNs) > 0 && slices.Contains(f.ExcludePGNs, ev.Frame.PGN) {
					continue
				}
			}

			var ct lplex.ChangeEventType
			var diffBytes, fullBytes int
			if tracker != nil {
				dataBytes, err := hex.DecodeString(ev.Frame.Data)
				if err != nil {
					continue
				}
				ts, _ := time.Parse(time.RFC3339Nano, ev.Frame.Ts)
				ce := tracker.Process(ts, ev.Frame.Src, ev.Frame.PGN, dataBytes, ev.Frame.Seq)
				if ce == nil {
					continue
				}
				ct = ce.Type
				if ct == lplex.Delta {
					diffBytes = len(ce.Data)
					fullBytes = len(dataBytes)
				}
			}

			var rawDecoded any
			if decode || df != nil {
				rawDecoded, _ = decodeFrameRaw(ev.Frame)
			}
			if !matchesDisplayFilter(df, ev.Frame, rawDecoded, devices) {
				continue
			}

			if jsonMode {
				writeJSONFrame(out, ev.Frame, decode, rawDecoded, ct, diffBytes, fullBytes)
			} else {
				formatFrame(out, ev.Frame, devices, decode, rawDecoded, ct, diffBytes, fullBytes)
			}
			out.Flush()
		}
	}
}
