package pgn

import (
	"encoding/binary"
	"fmt"
)

// GNSSSatsInView represents PGN 129540 — GNSS Satellites in View.
// Variable-length: 3-byte header + 12 bytes per satellite.
type GNSSSatsInView struct {
	SID               uint8              `json:"sid"`
	RangeResidualMode uint8              `json:"range_residual_mode"`
	SatsInView        uint8              `json:"sats_in_view"`
	Satellites        []SatelliteInView  `json:"satellites,omitempty"`
}

// SatelliteInView describes a single satellite entry in PGN 129540.
type SatelliteInView struct {
	PRN            uint8   `json:"prn"`
	Elevation      float64 `json:"elevation"`       // rad
	Azimuth        float64 `json:"azimuth"`          // rad
	SNR            float64 `json:"snr"`              // dB
	RangeResiduals float64 `json:"range_residuals"`  // m
	Status         uint8   `json:"status"`
}

func (GNSSSatsInView) PGN() uint32 { return 129540 }

// DecodeGNSSSatsInView decodes PGN 129540 from raw bytes.
//
//	Header (3 bytes):
//	  [0]     SID
//	  [1]     bits 0-1: Range Residual Mode, bits 2-7: reserved
//	  [2]     Sats in View (N)
//
//	Per satellite (12 bytes each, repeated N times):
//	  [0]     PRN
//	  [1:3]   Elevation   (int16,  scale 0.0001 rad)
//	  [3:5]   Azimuth     (uint16, scale 0.0001 rad)
//	  [5:7]   SNR         (int16,  scale 0.01 dB)
//	  [7:11]  Range Residuals (int32, scale 0.00001 m)
//	  [11]    bits 0-3: Status, bits 4-7: reserved
func DecodeGNSSSatsInView(data []byte) (GNSSSatsInView, error) {
	if len(data) < 3 {
		return GNSSSatsInView{}, fmt.Errorf("pgn 129540: need at least 3 bytes, got %d", len(data))
	}
	var m GNSSSatsInView
	m.SID = data[0]
	m.RangeResidualMode = data[1] & 0x03
	m.SatsInView = data[2]

	n := int(m.SatsInView)
	available := (len(data) - 3) / 12
	if n > available {
		n = available
	}

	m.Satellites = make([]SatelliteInView, n)
	for i := range n {
		off := 3 + i*12
		s := &m.Satellites[i]
		s.PRN = data[off]
		s.Elevation = float64(int16(binary.LittleEndian.Uint16(data[off+1:off+3]))) * 0.0001
		s.Azimuth = float64(binary.LittleEndian.Uint16(data[off+3:off+5])) * 0.0001
		s.SNR = float64(int16(binary.LittleEndian.Uint16(data[off+5:off+7]))) * 0.01
		s.RangeResiduals = float64(int32(binary.LittleEndian.Uint32(data[off+7:off+11]))) * 0.00001
		s.Status = data[off+11] & 0x0F
	}
	return m, nil
}

func init() {
	Registry[129540] = PGNInfo{
		PGN:         129540,
		Description: "GNSS Sats in View",
		Decode:      func(data []byte) (any, error) { return DecodeGNSSSatsInView(data) },
	}
}
