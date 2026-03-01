package main

import (
	"bufio"
	"context"
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

	"github.com/sixfathoms/lplex/lplexc"
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
	n, err := strconv.ParseUint(v, 10, 64)
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

	var filterPGNs uintSlice
	var filterManufacturers stringSlice
	var filterInstances uintSlice
	var filterNames stringSlice
	flag.Var(&filterPGNs, "pgn", "filter by PGN (repeatable)")
	flag.Var(&filterManufacturers, "manufacturer", "filter by manufacturer name (repeatable)")
	flag.Var(&filterInstances, "instance", "filter by device instance (repeatable)")
	flag.Var(&filterNames, "name", "filter by 64-bit CAN NAME in hex (repeatable)")

	flag.Parse()

	if *showVersion {
		fmt.Printf("lplexdump %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

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

	if !*jsonMode && !isTerminal(os.Stdout) {
		*jsonMode = true
	}

	if *quiet {
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(os.Stderr)
	}
	log.SetFlags(log.Ltime)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := lplexc.NewClient(*serverURL)
	devices := newDeviceMap()
	var lastSeq atomic.Uint64

	filter := buildFilter(filterPGNs, filterManufacturers, filterInstances, filterNames)
	buffered := *bufferTimeout != ""

	for {
		var err error
		if buffered {
			err = runBuffered(ctx, client, *clientID, *bufferTimeout, *ackInterval, *jsonMode, filter, devices, &lastSeq)
		} else {
			err = runEphemeral(ctx, client, *jsonMode, filter, devices, &lastSeq)
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

func buildFilter(pgns uintSlice, manufacturers stringSlice, instances uintSlice, names stringSlice) *lplexc.Filter {
	if len(pgns) == 0 && len(manufacturers) == 0 && len(instances) == 0 && len(names) == 0 {
		return nil
	}
	f := &lplexc.Filter{
		Manufacturers: []string(manufacturers),
		Names:         []string(names),
	}
	for _, p := range pgns {
		f.PGNs = append(f.PGNs, uint32(p))
	}
	for _, i := range instances {
		f.Instances = append(f.Instances, uint8(i))
	}
	return f
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

func runEphemeral(ctx context.Context, client *lplexc.Client, jsonMode bool, filter *lplexc.Filter, devices *deviceMap, lastSeq *atomic.Uint64) error {
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

	return streamEvents(sub, jsonMode, devices, lastSeq)
}

func runBuffered(ctx context.Context, client *lplexc.Client, clientID, bufferTimeout string, ackInterval time.Duration, jsonMode bool, filter *lplexc.Filter, devices *deviceMap, lastSeq *atomic.Uint64) error {
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

	err = streamEvents(sub, jsonMode, devices, lastSeq)

	ackCancel()
	wg.Wait()

	return err
}

func streamEvents(sub *lplexc.Subscription, jsonMode bool, devices *deviceMap, lastSeq *atomic.Uint64) error {
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
				// TODO: we could json.Marshal but the raw SSE data is already JSON,
				// however the Subscription parses it. For now just re-marshal.
				fmt.Fprintf(out, "{\"seq\":%d,\"ts\":\"%s\",\"prio\":%d,\"pgn\":%d,\"src\":%d,\"dst\":%d,\"data\":\"%s\"}\n",
					ev.Frame.Seq, ev.Frame.Ts, ev.Frame.Prio, ev.Frame.PGN, ev.Frame.Src, ev.Frame.Dst, ev.Frame.Data)
			} else {
				formatFrame(out, ev.Frame, devices)
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
)

var srcPalette = []string{
	ansiGreen, ansiYellow, ansiBlue, ansiMagenta, ansiCyan,
	ansiHiGreen, ansiHiYell, ansiHiBlue, ansiHiMag, ansiHiCyan,
}

func colorForSrc(src uint8) string {
	return srcPalette[int(src)%len(srcPalette)]
}

func formatFrame(w *bufio.Writer, f *lplexc.Frame, dm *deviceMap) {
	ts := f.Ts
	if t, err := time.Parse(time.RFC3339Nano, f.Ts); err == nil {
		ts = t.Local().Format("15:04:05.000")
	}

	srcLabel := fmt.Sprintf("[%d]", f.Src)
	if d, ok := dm.get(f.Src); ok && d.Manufacturer != "" {
		srcLabel = fmt.Sprintf("%s(%d)[%d]", d.Manufacturer, d.ManufacturerCode, f.Src)
	}

	pgnName := pgnNames[f.PGN]
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
	fmt.Fprintf(w, " %sp%d%s  %s\n",
		ansiDim, f.Prio, ansiReset,
		f.Data,
	)
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
// NMEA 2000 PGN names (common subset)
// ---------------------------------------------------------------------------

var pgnNames = map[uint32]string{
	59392:  "ISO Ack",
	59904:  "ISO Request",
	60160:  "ISO Transport Proto",
	60416:  "ISO Transport Proto",
	60928:  "ISO Address Claim",
	65240:  "Commanded Address",
	126208: "NMEA Req/Cmd/Ack",
	126464: "PGN List",
	126720: "Proprietary",
	126983: "Alert",
	126984: "Alert Response",
	126985: "Alert Text",
	126986: "Alert Config",
	126987: "Alert Threshold",
	126988: "Alert Value",
	126992: "System Time",
	126993: "Heartbeat",
	126996: "Product Info",
	126998: "Config Info",
	127233: "Man Overboard",
	127237: "Heading/Track Ctrl",
	127245: "Rudder",
	127250: "Vessel Heading",
	127251: "Rate of Turn",
	127252: "Heave",
	127257: "Attitude",
	127258: "Magnetic Variation",
	127488: "Engine Rapid",
	127489: "Engine Dynamic",
	127493: "Transmission",
	127497: "Trip Engine",
	127498: "Engine Param Static",
	127501: "Binary Switch Bank",
	127505: "Fluid Level",
	127506: "DC Status",
	127507: "Charger Status",
	127508: "Battery Status",
	127509: "Inverter Status",
	127510: "AC Status",
	128259: "Speed, Water",
	128267: "Water Depth",
	128275: "Distance Log",
	129025: "Position Rapid",
	129026: "COG/SOG Rapid",
	129027: "Position Delta Rapid",
	129029: "GNSS Position",
	129033: "Time & Date",
	129038: "AIS Class A",
	129039: "AIS Class B",
	129040: "AIS Class B Ext",
	129041: "AIS Aids Nav",
	129044: "Datum",
	129283: "Cross Track Error",
	129284: "Navigation Data",
	129285: "Route/WP Info",
	129291: "Set & Drift Rapid",
	129539: "GNSS DOPs",
	129540: "GNSS Sats in View",
	129542: "GNSS Range Residuals",
	129794: "AIS Static Data A",
	129795: "AIS Addressed Msg",
	129796: "AIS Ack",
	129797: "AIS Binary Broadcast",
	129798: "AIS SAR Position",
	129799: "Radio Freq/Mode",
	129800: "AIS UTC/Date Inquiry",
	129801: "AIS Addressed Safe",
	129802: "AIS Broadcast Safe",
	129809: "AIS Static Data B",
	129810: "AIS Static Data B",
	130060: "Label",
	130061: "Channel Source",
	130064: "Route Leg/WP",
	130065: "Route/WP Filter",
	130066: "Route/WP Time",
	130067: "Route/WP Plan",
	130069: "Waypoint List",
	130070: "Waypoint",
	130074: "Route/WP Time to From",
	130306: "Wind Data",
	130310: "Env Parameters",
	130311: "Env Parameters",
	130312: "Temperature",
	130313: "Humidity",
	130314: "Actual Pressure",
	130316: "Temp Extended",
	130560: "Payload Mass",
	130567: "Watermaker Input",
	130569: "Current Status/Ctrl",
	130570: "AC Input Status",
	130571: "AC Output Status",
	130572: "AC Input Config",
	130573: "AC Output Config",
	130577: "Direction Data",
	130578: "Vessel Speed",
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
