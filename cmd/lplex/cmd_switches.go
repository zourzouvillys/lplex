package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sixfathoms/lplex/lplexc"
	"github.com/sixfathoms/lplex/pgn"
	"github.com/spf13/cobra"
)

var (
	switchesInstance int
	switchesWatch    bool
)

var switchesCmd = &cobra.Command{
	Use:   "switches",
	Short: "Show binary switch bank status",
	Long:  "Display the state of binary switch banks (PGN 127501) with colored ON/OFF indicators.",
	RunE:  runSwitches,
}

var switchesSetCmd = &cobra.Command{
	Use:   "set SWITCH=STATE [SWITCH=STATE ...]",
	Short: "Control binary switches",
	Long: `Send a Binary Switch Bank Control (PGN 127502) to set switch states.

Each argument is a SWITCH=STATE pair where SWITCH is the 1-based switch number
and STATE is "on" or "off". Switches not specified are left unchanged.

Examples:
  lplex switches set --instance 0 1=on
  lplex switches set --instance 0 1=on 3=off 5=on`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSwitchesSet,
}

var (
	switchSetInstance int
	switchSetSrc     uint8
	switchSetPrio    uint8
)

func init() {
	f := switchesCmd.Flags()
	f.IntVar(&switchesInstance, "instance", -1, "filter to specific switch bank instance (-1 = all)")
	f.BoolVar(&switchesWatch, "watch", false, "live-updating switch status")

	sf := switchesSetCmd.Flags()
	sf.IntVar(&switchSetInstance, "instance", -1, "switch bank instance (required)")
	sf.Uint8Var(&switchSetSrc, "src", 0, "source address")
	sf.Uint8Var(&switchSetPrio, "prio", 3, "priority (0-7, default 3)")
	_ = switchesSetCmd.MarkFlagRequired("instance")

	switchesCmd.AddCommand(switchesSetCmd)
}

// switchState represents one switch's state from PGN 127501.
type switchState int

const (
	switchOff         switchState = 0
	switchOn          switchState = 1
	switchError       switchState = 2
	switchUnavailable switchState = 3
)

func (s switchState) String() string {
	switch s {
	case switchOff:
		return "OFF"
	case switchOn:
		return "ON"
	case switchError:
		return "ERR"
	case switchUnavailable:
		return "N/A"
	default:
		return "?"
	}
}

func (s switchState) color() string {
	switch s {
	case switchOff:
		return ansiDim
	case switchOn:
		return ansiGreen + ansiBold
	case switchError:
		return ansiRed + ansiBold
	default:
		return ansiDim
	}
}

func runSwitches(_ *cobra.Command, _ []string) error {
	jsonMode := flagJSON || !isTerminal(os.Stdout)

	if flagQuiet {
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(os.Stderr)
	}
	log.SetFlags(log.Ltime)

	serverURL := resolveServerURL(flagServer, nil, 0)
	if flagBoat != "" || flagConfig != "" {
		boat, mdnsTimeout, _, _, err := loadBoatConfig(flagBoat, flagConfig, flagBoat != "")
		if err != nil {
			return err
		}
		serverURL = resolveServerURL(flagServer, boat, mdnsTimeout)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := lplexc.NewClient(serverURL)

	// PGN 127501 = Binary Switch Bank Status
	const switchPGN uint32 = 127501

	printSwitches := func() error {
		filter := &lplexc.Filter{PGNs: []uint32{switchPGN}}
		values, err := client.Values(ctx, filter)
		if err != nil {
			return fmt.Errorf("fetching switch values: %w", err)
		}

		if jsonMode {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(values)
		}

		found := false
		for _, dv := range values {
			for _, pv := range dv.Values {
				if pv.PGN != switchPGN {
					continue
				}

				data, err := hex.DecodeString(pv.Data)
				if err != nil || len(data) < 1 {
					continue
				}

				instance := int(data[0])
				if switchesInstance >= 0 && instance != switchesInstance {
					continue
				}

				found = true
				states := decodeSwitchBank(data)

				sc := colorForSrc(dv.Source)
				fmt.Printf("%s%s[src=%d] %s%s  Bank %d\n",
					sc, ansiBold, dv.Source, dv.Manufacturer, ansiReset, instance)

				for i, st := range states {
					fmt.Printf("  Switch %2d: %s%-3s%s\n",
						i+1, st.color(), st.String(), ansiReset)
				}
				fmt.Println()
			}
		}

		if !found {
			fmt.Println("No switch banks found.")
		}
		return nil
	}

	if err := printSwitches(); err != nil {
		return err
	}

	if !switchesWatch {
		return nil
	}

	// Watch mode: subscribe to ephemeral stream filtered to PGN 127501.
	sub, err := client.Subscribe(ctx, &lplexc.Filter{PGNs: []uint32{switchPGN}})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer func() { _ = sub.Close() }()

	// Refresh on each matching frame (throttled to avoid flicker).
	lastPrint := time.Now()
	for {
		ev, err := sub.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("stream: %w", err)
		}
		if ev.Frame != nil && ev.Frame.PGN == switchPGN {
			if time.Since(lastPrint) < 500*time.Millisecond {
				continue
			}
			lastPrint = time.Now()
			fmt.Fprint(os.Stdout, "\033[2J\033[H")
			if err := printSwitches(); err != nil {
				log.Printf("refresh: %v", err)
			}
		}
	}
}

// parseSwitchArg parses a "SWITCH=STATE" argument.
// Returns the 1-based switch number and the desired state (0=off, 1=on).
func parseSwitchArg(arg string) (int, uint8, error) {
	parts := strings.SplitN(arg, "=", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid argument %q: expected SWITCH=STATE (e.g. 1=on)", arg)
	}

	num, err := strconv.Atoi(parts[0])
	if err != nil || num < 1 || num > 28 {
		return 0, 0, fmt.Errorf("invalid switch number %q: must be 1-28", parts[0])
	}

	var state uint8
	switch strings.ToLower(parts[1]) {
	case "on", "1":
		state = 1
	case "off", "0":
		state = 0
	default:
		return 0, 0, fmt.Errorf("invalid state %q: must be on/off", parts[1])
	}

	return num, state, nil
}

func runSwitchesSet(_ *cobra.Command, args []string) error {
	if flagQuiet {
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(os.Stderr)
	}
	log.SetFlags(log.Ltime)

	serverURL := resolveServerURL(flagServer, nil, 0)
	if flagBoat != "" || flagConfig != "" {
		boat, mdnsTimeout, _, _, err := loadBoatConfig(flagBoat, flagConfig, flagBoat != "")
		if err != nil {
			return err
		}
		serverURL = resolveServerURL(flagServer, boat, mdnsTimeout)
	}

	// Parse switch=state pairs from args.
	type switchSet struct {
		num   int
		state uint8
	}
	var sets []switchSet
	for _, arg := range args {
		num, state, err := parseSwitchArg(arg)
		if err != nil {
			return err
		}
		sets = append(sets, switchSet{num: num, state: state})
	}

	// Build PGN 127502 payload: all indicators set to 3 (no change),
	// then override the ones the user specified.
	ctrl := pgn.BinarySwitchBankControl{
		Instance: uint8(switchSetInstance),
	}
	ctrl.Indicators = make(pgn.Uint8s, 28)
	for i := range ctrl.Indicators {
		ctrl.Indicators[i] = 3 // no change
	}
	for _, s := range sets {
		ctrl.Indicators[s.num-1] = s.state
	}

	data := ctrl.Encode()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := lplexc.NewClient(serverURL)

	// PGN 127502 is PDU2 (PF >= 240), so destination is always broadcast.
	// Targeting happens via the instance field in the payload.
	const controlPGN uint32 = 127502
	if err := client.Send(ctx, controlPGN, switchSetSrc, 255, switchSetPrio, data); err != nil {
		return fmt.Errorf("send failed: %w", err)
	}

	// Pretty-print what we sent.
	for _, s := range sets {
		state := "OFF"
		if s.state == 1 {
			state = "ON"
		}
		log.Printf("bank %d switch %d → %s", switchSetInstance, s.num, state)
	}

	return nil
}

// decodeSwitchBank extracts switch states from PGN 127501 data.
// Byte 0 is the instance, bytes 1+ contain 2-bit switch states.
func decodeSwitchBank(data []byte) []switchState {
	if len(data) < 2 {
		return nil
	}

	var states []switchState
	for i := 1; i < len(data); i++ {
		b := data[i]
		for bit := 0; bit < 4; bit++ {
			st := switchState((b >> (bit * 2)) & 0x03)
			if st == switchUnavailable {
				return states
			}
			states = append(states, st)
		}
	}
	return states
}
