package lplex

import (
	"encoding/binary"
	"encoding/json"
	"sync"
	"time"

	"github.com/sixfathoms/lplex/canbus"
)

// encodeFixedString writes s into a fixed-width field, padding with 0xFF.
func encodeFixedString(dst []byte, s string) {
	n := copy(dst, s)
	for i := n; i < len(dst); i++ {
		dst[i] = 0xFF
	}
}

// Device represents an NMEA 2000 device discovered via ISO Address Claim (PGN 60928)
// and optionally enriched with Product Information (PGN 126996).
type Device struct {
	Source           uint8  `json:"src"`
	NAME             uint64 `json:"-"`
	NAMEHex          string `json:"name"`
	Manufacturer     string `json:"manufacturer"`
	ManufacturerCode uint16 `json:"manufacturer_code"`
	DeviceClass      uint8  `json:"device_class"`
	DeviceFunction   uint8  `json:"device_function"`
	DeviceInstance    uint8  `json:"device_instance"`
	UniqueNumber     uint32 `json:"unique_number,omitempty"`

	// PGN 126996 Product Information fields.
	ModelID         string `json:"model_id,omitempty"`
	SoftwareVersion string `json:"software_version,omitempty"`
	ModelVersion    string `json:"model_version,omitempty"`
	ModelSerial     string `json:"model_serial,omitempty"`
	ProductCode     uint16 `json:"product_code,omitempty"`

	// Per-source packet statistics.
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	PacketCount uint64    `json:"packet_count"`
	ByteCount   uint64    `json:"byte_count"`
}

// DeviceRegistry tracks NMEA 2000 devices discovered via PGN 60928.
// Thread-safe for concurrent reads (SSE streams) and writes (broker goroutine).
type DeviceRegistry struct {
	mu      sync.RWMutex
	devices map[uint8]*Device // keyed by source address
}

// NewDeviceRegistry creates an empty device registry.
func NewDeviceRegistry() *DeviceRegistry {
	return &DeviceRegistry{
		devices: make(map[uint8]*Device),
	}
}

// RecordPacket updates per-source packet statistics.
// Returns true if this is a previously unseen source address.
// Source 254 (Cannot Claim Address) and 255 (broadcast) are ignored
// since they are not real devices.
func (r *DeviceRegistry) RecordPacket(source uint8, ts time.Time, dataLen int) bool {
	if source >= 254 {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if dev, ok := r.devices[source]; ok {
		dev.LastSeen = ts
		dev.PacketCount++
		dev.ByteCount += uint64(dataLen)
		return false
	}

	r.devices[source] = &Device{
		Source:      source,
		FirstSeen:   ts,
		LastSeen:    ts,
		PacketCount: 1,
		ByteCount:   uint64(dataLen),
	}
	return true
}

// HandleAddressClaim processes a PGN 60928 ISO Address Claim.
// Returns the device if this is a new or changed device, nil otherwise.
// If a different source address previously held the same NAME, that old
// entry is evicted and its source address is returned in evictedSrc.
func (r *DeviceRegistry) HandleAddressClaim(source uint8, data []byte) (dev *Device, evictedSrc uint8, evicted bool) {
	if len(data) < 8 {
		return nil, 0, false
	}

	name := binary.LittleEndian.Uint64(data[0:8])

	r.mu.Lock()
	defer r.mu.Unlock()

	existing := r.devices[source]
	if existing != nil && existing.NAME == name {
		return nil, 0, false // no change
	}

	// Evict any *other* source that claimed this same NAME (device restarted
	// and grabbed a new address).
	for src, d := range r.devices {
		if src != source && d.NAME == name {
			delete(r.devices, src)
			evictedSrc = src
			evicted = true
			break // at most one prior holder
		}
	}

	dev = decodeNAME(name, source)

	// Preserve stats and product info from prior calls.
	if existing != nil {
		dev.FirstSeen = existing.FirstSeen
		dev.LastSeen = existing.LastSeen
		dev.PacketCount = existing.PacketCount
		dev.ByteCount = existing.ByteCount
	}

	r.devices[source] = dev
	return dev, evictedSrc, evicted
}

// HandleProductInfo processes a PGN 126996 Product Information response.
// Returns the device if fields changed, nil if source is unknown or unchanged.
func (r *DeviceRegistry) HandleProductInfo(source uint8, data []byte) *Device {
	if len(data) < 134 {
		return nil
	}

	productCode := binary.LittleEndian.Uint16(data[2:4])
	modelID := decodeFixedString(data[4:36])
	softwareVersion := decodeFixedString(data[36:76])
	modelVersion := decodeFixedString(data[76:100])
	modelSerial := decodeFixedString(data[100:132])

	r.mu.Lock()
	defer r.mu.Unlock()

	dev, ok := r.devices[source]
	if !ok {
		return nil
	}

	if dev.ProductCode == productCode &&
		dev.ModelID == modelID &&
		dev.SoftwareVersion == softwareVersion &&
		dev.ModelVersion == modelVersion &&
		dev.ModelSerial == modelSerial {
		return nil
	}

	dev.ProductCode = productCode
	dev.ModelID = modelID
	dev.SoftwareVersion = softwareVersion
	dev.ModelVersion = modelVersion
	dev.ModelSerial = modelSerial

	snapshot := *dev
	return &snapshot
}

// decodeFixedString extracts the ASCII string from a fixed-width field,
// terminating at the first null (0x00) or padding (0xFF) byte.
func decodeFixedString(data []byte) string {
	for i, b := range data {
		if b == 0x00 || b == 0xFF {
			return string(data[:i])
		}
	}
	return string(data)
}

// ExpireIdle removes all devices whose LastSeen is before cutoff.
// Returns the source addresses of evicted entries.
func (r *DeviceRegistry) ExpireIdle(cutoff time.Time) []uint8 {
	r.mu.Lock()
	defer r.mu.Unlock()

	var evicted []uint8
	for src, dev := range r.devices {
		if dev.LastSeen.Before(cutoff) {
			delete(r.devices, src)
			evicted = append(evicted, src)
		}
	}
	return evicted
}

// SourcesMissingProductInfo returns source addresses of devices that have a
// NAME (address claim received) but no product info (PGN 126996) yet.
func (r *DeviceRegistry) SourcesMissingProductInfo() []uint8 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var srcs []uint8
	for _, dev := range r.devices {
		if dev.NAME != 0 && dev.ModelID == "" && dev.ProductCode == 0 {
			srcs = append(srcs, dev.Source)
		}
	}
	return srcs
}

// Get returns a snapshot of the device at the given source address, or nil.
func (r *DeviceRegistry) Get(source uint8) *Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	dev, ok := r.devices[source]
	if !ok {
		return nil
	}
	snapshot := *dev
	return &snapshot
}

// Snapshot returns a copy of all known devices.
func (r *DeviceRegistry) Snapshot() []Device {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		result = append(result, *d)
	}
	return result
}

// SnapshotJSON returns the device list as pre-serialized JSON.
func (r *DeviceRegistry) SnapshotJSON() json.RawMessage {
	devices := r.Snapshot()
	b, _ := json.Marshal(devices)
	return b
}

// decodeNAME parses the 64-bit ISO NAME field from PGN 60928 and returns
// a Device populated with the decoded fields.
func decodeNAME(name uint64, source uint8) *Device {
	fields := canbus.DecodeNAME(name)
	return &Device{
		Source:           source,
		NAME:             name,
		NAMEHex:          fields.NAMEHex,
		Manufacturer:     fields.Manufacturer,
		ManufacturerCode: fields.ManufacturerCode,
		DeviceClass:      fields.DeviceClass,
		DeviceFunction:   fields.DeviceFunction,
		DeviceInstance:   fields.DeviceInstance,
		UniqueNumber:     fields.UniqueNumber,
	}
}

// SynthesizeFrames generates RxFrame slices for PGN 60928 (Address Claim) and
// PGN 126996 (Product Info) from all known devices. Used to seed a remote
// broker's device registry on live stream connect.
func (r *DeviceRegistry) SynthesizeFrames(ts time.Time) []RxFrame {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var frames []RxFrame
	for _, dev := range r.devices {
		if dev.NAME == 0 {
			continue
		}

		// PGN 60928: Address Claim (8 bytes, NAME as uint64 LE)
		claimData := make([]byte, 8)
		binary.LittleEndian.PutUint64(claimData, dev.NAME)
		frames = append(frames, RxFrame{
			Timestamp: ts,
			Header: CANHeader{
				Priority:    6,
				PGN:         60928,
				Source:      dev.Source,
				Destination: 255,
			},
			Data: claimData,
		})

		// PGN 126996: Product Info (134 bytes), only if we have product data.
		if dev.ProductCode == 0 && dev.ModelID == "" {
			continue
		}
		prodData := make([]byte, 134)
		// bytes 0-1: NMEA version (zero, we don't track it)
		binary.LittleEndian.PutUint16(prodData[2:4], dev.ProductCode)
		encodeFixedString(prodData[4:36], dev.ModelID)
		encodeFixedString(prodData[36:76], dev.SoftwareVersion)
		encodeFixedString(prodData[76:100], dev.ModelVersion)
		encodeFixedString(prodData[100:132], dev.ModelSerial)
		// bytes 132-133: cert level + load equiv (zero)
		frames = append(frames, RxFrame{
			Timestamp: ts,
			Header: CANHeader{
				Priority:    6,
				PGN:         126996,
				Source:      dev.Source,
				Destination: 255,
			},
			Data: prodData,
		})
	}
	return frames
}
