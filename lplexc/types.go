// Package lplexc provides a Go client for lplex, a CAN bus HTTP bridge for NMEA 2000.
package lplexc

// Frame represents a single CAN frame received from the lplex server.
type Frame struct {
	Seq  uint64 `json:"seq"`
	Ts   string `json:"ts"`
	Prio uint8  `json:"prio"`
	PGN  uint32 `json:"pgn"`
	Src  uint8  `json:"src"`
	Dst  uint8  `json:"dst"`
	Data string `json:"data"`
}

// Device represents an NMEA 2000 device discovered on the bus.
type Device struct {
	Src              uint8  `json:"src"`
	Name             string `json:"name"`
	Manufacturer     string `json:"manufacturer"`
	ManufacturerCode uint16 `json:"manufacturer_code"`
	DeviceClass      uint8  `json:"device_class"`
	DeviceFunction   uint8  `json:"device_function"`
	DeviceInstance   uint8  `json:"device_instance"`
	UniqueNumber     uint32 `json:"unique_number"`
	ModelID          string `json:"model_id"`
	SoftwareVersion  string `json:"software_version"`
	ModelVersion     string `json:"model_version"`
	ModelSerial      string `json:"model_serial"`
	ProductCode      uint16 `json:"product_code"`
	FirstSeen        string `json:"first_seen"`
	LastSeen         string `json:"last_seen"`
	PacketCount      uint64 `json:"packet_count"`
	ByteCount        uint64 `json:"byte_count"`
}

// Filter specifies which CAN frames to receive.
// Categories are AND'd, values within a category are OR'd.
type Filter struct {
	PGNs          []uint32 `json:"pgn,omitempty"`
	ExcludePGNs   []uint32 `json:"exclude_pgn,omitempty"`
	Manufacturers []string `json:"manufacturer,omitempty"`
	Instances     []uint8  `json:"instance,omitempty"`
	Names         []string `json:"name,omitempty"`
	ExcludeNames  []string `json:"exclude_name,omitempty"`
}

// IsEmpty returns true if no filter criteria are set.
func (f *Filter) IsEmpty() bool {
	return f == nil || (len(f.PGNs) == 0 && len(f.ExcludePGNs) == 0 &&
		len(f.Manufacturers) == 0 && len(f.Instances) == 0 &&
		len(f.Names) == 0 && len(f.ExcludeNames) == 0)
}

// PGNValue is a single PGN's last-known value.
type PGNValue struct {
	PGN  uint32 `json:"pgn"`
	Ts   string `json:"ts"`
	Data string `json:"data"`
	Seq  uint64 `json:"seq"`
}

// DeviceValues groups PGN values by device.
type DeviceValues struct {
	Name         string     `json:"name"`
	Source       uint8      `json:"src"`
	Manufacturer string     `json:"manufacturer,omitempty"`
	ModelID      string     `json:"model_id,omitempty"`
	Values       []PGNValue `json:"values"`
}

// DecodedPGNValue is a single PGN's last-known value decoded into named fields.
type DecodedPGNValue struct {
	PGN         uint32 `json:"pgn"`
	Description string `json:"description"`
	Ts          string `json:"ts"`
	Seq         uint64 `json:"seq"`
	Fields      any    `json:"fields"`
}

// DecodedDeviceValues groups decoded PGN values by device.
type DecodedDeviceValues struct {
	Name         string            `json:"name"`
	Source       uint8             `json:"src"`
	Manufacturer string            `json:"manufacturer,omitempty"`
	ModelID      string            `json:"model_id,omitempty"`
	Values       []DecodedPGNValue `json:"values"`
}

// Event is a message received from an lplex SSE stream.
// Exactly one of Frame or Device will be non-nil.
type Event struct {
	Frame  *Frame
	Device *Device
}
