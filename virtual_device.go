package lplex

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ClaimState represents where a virtual device is in the address claim lifecycle.
type ClaimState int

const (
	// ClaimIdle is the initial state before any claim attempt.
	ClaimIdle ClaimState = iota
	// ClaimInProgress means a claim frame has been sent but the 250ms holdoff hasn't elapsed.
	ClaimInProgress
	// ClaimHeld means the address is successfully claimed and ready for use.
	ClaimHeld
	// ClaimCannotClaim means all 253 addresses (0-252) were exhausted.
	ClaimCannotClaim
)

func (s ClaimState) String() string {
	switch s {
	case ClaimIdle:
		return "idle"
	case ClaimInProgress:
		return "in_progress"
	case ClaimHeld:
		return "held"
	case ClaimCannotClaim:
		return "cannot_claim"
	default:
		return fmt.Sprintf("ClaimState(%d)", int(s))
	}
}

// VirtualProductInfo holds the PGN 126996 fields for a virtual device.
type VirtualProductInfo struct {
	ModelID         string
	SoftwareVersion string
	ModelVersion    string
	ModelSerial     string
	ProductCode     uint16
}

// VirtualDeviceConfig configures a single virtual NMEA 2000 device.
type VirtualDeviceConfig struct {
	// NAME is the 64-bit ISO NAME. Lower values win address conflicts.
	NAME        uint64
	ProductInfo VirtualProductInfo
}

// VirtualDevice is a single virtual NMEA 2000 device managed by the VirtualDeviceManager.
type VirtualDevice struct {
	cfg     VirtualDeviceConfig
	state   ClaimState
	source  uint8     // current or attempted source address
	readyAt time.Time // when the 250ms holdoff expires after sending a claim
}

// VirtualDeviceManager manages a set of virtual NMEA 2000 devices that claim
// addresses on the CAN bus, making lplex-server a legitimate bus participant.
//
// Thread safety: the manager is called from the broker's single goroutine for
// frame handling (HandleBusClaim, HandleISORequest) and from HTTP
// handlers for ClaimedSource/Ready. The mutex protects the device state for
// the latter case.
type VirtualDeviceManager struct {
	mu      sync.RWMutex
	devices []*VirtualDevice
	logger  *slog.Logger

	// txFunc sends a frame to the CAN bus via the broker's tx channel.
	txFunc func(TxRequest)

	// registry is used to find free addresses and check for conflicts.
	registry *DeviceRegistry
}

// NewVirtualDeviceManager creates a new manager. txFunc is called to send
// frames to the CAN bus. registry is consulted for address selection.
func NewVirtualDeviceManager(txFunc func(TxRequest), registry *DeviceRegistry, logger *slog.Logger) *VirtualDeviceManager {
	return &VirtualDeviceManager{
		txFunc:   txFunc,
		registry: registry,
		logger:   logger,
	}
}

// Add registers a virtual device configuration. Call before Start.
func (m *VirtualDeviceManager) Add(cfg VirtualDeviceConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices = append(m.devices, &VirtualDevice{cfg: cfg})
}

// StartAfterDiscovery waits for the device table to populate (the broker
// broadcasts an ISO Request for PGN 60928 on startup), then claims addresses
// for all configured virtual devices.
func (m *VirtualDeviceManager) StartAfterDiscovery(delay time.Duration) {
	time.Sleep(delay)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range m.devices {
		m.claimForDevice(dev)
	}
}

// claimForDevice picks a free address and sends an address claim. Must be
// called with m.mu held.
func (m *VirtualDeviceManager) claimForDevice(dev *VirtualDevice) {
	addr, ok := m.findFreeAddress()
	if !ok {
		dev.state = ClaimCannotClaim
		dev.source = 254
		m.logger.Error("virtual device cannot claim: all addresses exhausted",
			"name", fmt.Sprintf("%016x", dev.cfg.NAME))
		// Send "cannot claim" (source 254) per the spec.
		m.sendAddressClaim(dev, 254)
		return
	}

	dev.source = addr
	dev.state = ClaimInProgress
	dev.readyAt = time.Now().Add(250 * time.Millisecond)
	m.sendAddressClaim(dev, addr)
	m.logger.Info("virtual device claiming address",
		"src", addr, "name", fmt.Sprintf("%016x", dev.cfg.NAME))
}

// sendAddressClaim broadcasts PGN 60928 with the device's NAME from the given source.
func (m *VirtualDeviceManager) sendAddressClaim(dev *VirtualDevice, source uint8) {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, dev.cfg.NAME)
	m.txFunc(TxRequest{
		Header: CANHeader{
			Priority:    6,
			PGN:         60928,
			Source:      source,
			Destination: 255,
		},
		Data: data,
	})
}

// findFreeAddress scans from 252 down to 0 looking for an address not held by
// any device in the registry or by another virtual device. Must be called with
// m.mu held.
func (m *VirtualDeviceManager) findFreeAddress() (uint8, bool) {
	snapshot := m.registry.Snapshot()
	taken := make(map[uint8]bool, len(snapshot)+len(m.devices))
	for _, dev := range snapshot {
		taken[dev.Source] = true
	}
	for _, vd := range m.devices {
		if vd.state == ClaimInProgress || vd.state == ClaimHeld {
			taken[vd.source] = true
		}
	}

	// Start high (252) to stay out of the way of real hardware.
	for addr := 252; addr >= 0; addr-- {
		if !taken[uint8(addr)] {
			return uint8(addr), true
		}
	}
	return 0, false
}

// HandleBusClaim is called by the broker when a PGN 60928 address claim is
// received from the bus. It checks if any of our virtual devices conflict and
// resolves per NMEA 2000: lower NAME wins.
//
// Returns true if the frame was handled (either as an echo or conflict) and
// should NOT be registered in the device registry.
func (m *VirtualDeviceManager) HandleBusClaim(source uint8, name uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range m.devices {
		if dev.state != ClaimInProgress && dev.state != ClaimHeld {
			continue
		}
		if dev.source != source {
			continue
		}

		// Same address. Is it our own echo?
		if dev.cfg.NAME == name {
			// Our claim frame echoed back from the CAN bus. Transition to
			// Held; the 250ms holdoff is enforced separately in Ready().
			if dev.state == ClaimInProgress {
				dev.state = ClaimHeld
				m.logger.Info("virtual device claimed address",
					"src", dev.source, "name", fmt.Sprintf("%016x", dev.cfg.NAME))
			}
			return true
		}

		// Conflict: another device claimed our address.
		if dev.cfg.NAME < name {
			// We win (lower NAME). Re-assert our claim.
			m.sendAddressClaim(dev, dev.source)
			m.logger.Info("virtual device won address conflict, re-asserting",
				"src", dev.source, "our_name", fmt.Sprintf("%016x", dev.cfg.NAME),
				"their_name", fmt.Sprintf("%016x", name))
			return false // let the broker process their claim too
		}

		// We lose. Find a new address.
		m.logger.Info("virtual device lost address conflict, relocating",
			"old_src", dev.source, "our_name", fmt.Sprintf("%016x", dev.cfg.NAME),
			"their_name", fmt.Sprintf("%016x", name))
		m.claimForDevice(dev)
		return false // let the broker register the winner
	}

	return false
}

// HandleISORequest is called when PGN 59904 is received. If the request
// targets one of our virtual devices, we respond with the appropriate data.
func (m *VirtualDeviceManager) HandleISORequest(dst uint8, requestedPGN uint32, requesterSrc uint8) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, dev := range m.devices {
		if dev.state != ClaimHeld && dev.state != ClaimInProgress {
			continue
		}
		// Respond if addressed to us specifically or broadcast.
		if dst != 255 && dst != dev.source {
			continue
		}

		switch requestedPGN {
		case 60928:
			m.sendAddressClaim(dev, dev.source)
		case 126996:
			m.sendProductInfo(dev, requesterSrc)
		}
	}
}

// sendProductInfo sends PGN 126996 Product Information as a response.
func (m *VirtualDeviceManager) sendProductInfo(dev *VirtualDevice, dst uint8) {
	info := dev.cfg.ProductInfo
	data := make([]byte, 134)
	// bytes 0-1: NMEA 2000 version (2.0 = 0x0834)
	binary.LittleEndian.PutUint16(data[0:2], 0x0834)
	binary.LittleEndian.PutUint16(data[2:4], info.ProductCode)
	encodeFixedString(data[4:36], info.ModelID)
	encodeFixedString(data[36:76], info.SoftwareVersion)
	encodeFixedString(data[76:100], info.ModelVersion)
	encodeFixedString(data[100:132], info.ModelSerial)
	// bytes 132-133: certification level + load equivalency (0)

	m.txFunc(TxRequest{
		Header: CANHeader{
			Priority:    6,
			PGN:         126996,
			Source:      dev.source,
			Destination: 255, // product info is broadcast per spec
		},
		Data: data,
	})
}

// ClaimedSource returns the source address of the first virtual device that
// has successfully claimed an address. Returns (0, false) if none are claimed.
func (m *VirtualDeviceManager) ClaimedSource() (uint8, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, dev := range m.devices {
		if dev.state == ClaimHeld {
			return dev.source, true
		}
	}
	return 0, false
}

// Ready returns true if at least one virtual device has claimed an address
// and the holdoff period has elapsed.
func (m *VirtualDeviceManager) Ready() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	for _, dev := range m.devices {
		if dev.state == ClaimHeld && !now.Before(dev.readyAt) {
			return true
		}
	}
	return false
}

// Devices returns a snapshot of virtual device states for diagnostics.
func (m *VirtualDeviceManager) Devices() []VirtualDeviceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]VirtualDeviceStatus, len(m.devices))
	for i, dev := range m.devices {
		result[i] = VirtualDeviceStatus{
			NAME:    fmt.Sprintf("%016x", dev.cfg.NAME),
			Source:  dev.source,
			State:   dev.state.String(),
			ModelID: dev.cfg.ProductInfo.ModelID,
		}
	}
	return result
}

// ProductInfoPayload returns the 134-byte PGN 126996 payload for the virtual
// device at the given source address, or nil if no device uses that address.
func (m *VirtualDeviceManager) ProductInfoPayload(source uint8) []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, dev := range m.devices {
		if dev.source == source {
			info := dev.cfg.ProductInfo
			data := make([]byte, 134)
			binary.LittleEndian.PutUint16(data[0:2], 0x0834)
			binary.LittleEndian.PutUint16(data[2:4], info.ProductCode)
			encodeFixedString(data[4:36], info.ModelID)
			encodeFixedString(data[36:76], info.SoftwareVersion)
			encodeFixedString(data[76:100], info.ModelVersion)
			encodeFixedString(data[100:132], info.ModelSerial)
			return data
		}
	}
	return nil
}

// VirtualDeviceStatus is a diagnostic snapshot of a virtual device.
type VirtualDeviceStatus struct {
	NAME    string `json:"name"`
	Source  uint8  `json:"src"`
	State   string `json:"state"`
	ModelID string `json:"model_id,omitempty"`
}
