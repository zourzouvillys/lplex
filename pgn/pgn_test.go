package pgn

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestPositionRapidUpdateRoundTrip(t *testing.T) {
	// Encode known position: 47.6062° N, -122.3321° W (Seattle)
	orig := PositionRapidUpdate{
		Latitude:  47.6062,
		Longitude: -122.3321,
	}
	data := orig.Encode()
	if len(data) != 8 {
		t.Fatalf("encoded length = %d, want 8", len(data))
	}

	decoded, err := DecodePositionRapidUpdate(data)
	if err != nil {
		t.Fatal(err)
	}

	if math.Abs(decoded.Latitude-orig.Latitude) > 1e-6 {
		t.Errorf("latitude = %f, want ~%f", decoded.Latitude, orig.Latitude)
	}
	if math.Abs(decoded.Longitude-orig.Longitude) > 1e-6 {
		t.Errorf("longitude = %f, want ~%f", decoded.Longitude, orig.Longitude)
	}
}

func TestPositionRapidUpdateDecodeKnown(t *testing.T) {
	// 47.6062° = 476062000 raw, -122.3321° = -1223321000 raw
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], uint32(int32(476062000)))
	lonRaw := int32(-1223321000)
	binary.LittleEndian.PutUint32(data[4:8], uint32(lonRaw))

	pos, err := DecodePositionRapidUpdate(data)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(pos.Latitude-47.6062) > 1e-6 {
		t.Errorf("latitude = %f, want 47.6062", pos.Latitude)
	}
	if math.Abs(pos.Longitude-(-122.3321)) > 1e-6 {
		t.Errorf("longitude = %f, want -122.3321", pos.Longitude)
	}
}

func TestWindDataRoundTrip(t *testing.T) {
	orig := WindData{
		Sid:           1,
		WindSpeed:     5.5,
		WindAngle:     1.2345,
		WindReference: WindReferenceApparent,
	}
	data := orig.Encode()
	decoded, err := DecodeWindData(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Sid != 1 {
		t.Errorf("sid = %d, want 1", decoded.Sid)
	}
	if math.Abs(decoded.WindSpeed-5.5) > 0.01 {
		t.Errorf("wind_speed = %f, want ~5.5", decoded.WindSpeed)
	}
	if math.Abs(decoded.WindAngle-1.2345) > 0.0001 {
		t.Errorf("wind_angle = %f, want ~1.2345", decoded.WindAngle)
	}
	if decoded.WindReference != WindReferenceApparent {
		t.Errorf("wind_reference = %d, want Apparent (%d)", decoded.WindReference, WindReferenceApparent)
	}
}

func TestBatteryStatusRoundTrip(t *testing.T) {
	orig := BatteryStatus{
		Instance:    0,
		Voltage:     12.85,
		Current:     -5.3,
		Temperature: 293.15,
		Sid:         42,
	}
	data := orig.Encode()
	decoded, err := DecodeBatteryStatus(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Instance != 0 {
		t.Errorf("instance = %d, want 0", decoded.Instance)
	}
	if math.Abs(decoded.Voltage-12.85) > 0.01 {
		t.Errorf("voltage = %f, want ~12.85", decoded.Voltage)
	}
	if math.Abs(decoded.Current-(-5.3)) > 0.1 {
		t.Errorf("current = %f, want ~-5.3", decoded.Current)
	}
	if math.Abs(decoded.Temperature-293.15) > 0.01 {
		t.Errorf("temperature = %f, want ~293.15", decoded.Temperature)
	}
	if decoded.Sid != 42 {
		t.Errorf("sid = %d, want 42", decoded.Sid)
	}
}

func TestVesselHeadingRoundTrip(t *testing.T) {
	orig := VesselHeading{
		Sid:               0,
		Heading:           3.14,
		Deviation:         -0.05,
		Variation:         0.1,
		HeadingReference:  HeadingReferenceMagnetic,
	}
	data := orig.Encode()
	decoded, err := DecodeVesselHeading(data)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(decoded.Heading-3.14) > 0.0001 {
		t.Errorf("heading = %f, want ~3.14", decoded.Heading)
	}
	if math.Abs(decoded.Deviation-(-0.05)) > 0.0001 {
		t.Errorf("deviation = %f, want ~-0.05", decoded.Deviation)
	}
	if decoded.HeadingReference != HeadingReferenceMagnetic {
		t.Errorf("heading_reference = %d, want Magnetic", decoded.HeadingReference)
	}
}

func TestWaterDepthRoundTrip(t *testing.T) {
	orig := WaterDepth{
		Sid:    0,
		Depth:  15.25,
		Offset: -0.5,
		Range:  100.0,
	}
	data := orig.Encode()
	if len(data) != 8 {
		t.Fatalf("encoded length = %d, want 8", len(data))
	}
	decoded, err := DecodeWaterDepth(data)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(decoded.Depth-15.25) > 0.01 {
		t.Errorf("depth = %f, want ~15.25", decoded.Depth)
	}
	if math.Abs(decoded.Offset-(-0.5)) > 0.001 {
		t.Errorf("offset = %f, want ~-0.5", decoded.Offset)
	}
	if math.Abs(decoded.Range-100.0) > 10 {
		t.Errorf("range = %f, want ~100.0", decoded.Range)
	}
}

func TestWaterDepthDecodeAirmarFrame(t *testing.T) {
	// Real Airmar frame: ff3d020000a5fa0e
	data := []byte{0xff, 0x3d, 0x02, 0x00, 0x00, 0xa5, 0xfa, 0x0e}
	decoded, err := DecodeWaterDepth(data)
	if err != nil {
		t.Fatal(err)
	}
	// sid = 0xff
	if decoded.Sid != 0xff {
		t.Errorf("sid = %d, want 255", decoded.Sid)
	}
	// depth = 0x0000023d = 573 -> 573 * 0.01 = 5.73m
	if math.Abs(decoded.Depth-5.73) > 0.01 {
		t.Errorf("depth = %f, want ~5.73", decoded.Depth)
	}
	// offset = 0xfaa5 as int16 = -1371 -> -1371 * 0.001 = -1.371m
	if math.Abs(decoded.Offset-(-1.371)) > 0.001 {
		t.Errorf("offset = %f, want ~-1.371", decoded.Offset)
	}
	// range = 0x0e = 14 -> 14 * 10 = 140m
	if math.Abs(decoded.Range-140.0) > 0.01 {
		t.Errorf("range = %f, want 140.0", decoded.Range)
	}
}

func TestProductInformationRoundTrip(t *testing.T) {
	orig := ProductInformation{
		NmeaVersion:     2100,
		ProductCode:     1234,
		ModelId:         "GPS 200",
		SoftwareVersion: "v3.1.0",
		ModelVersion:    "Rev B",
		ModelSerial:     "ABC12345",
		CertLevel:       1,
		LoadEquiv:       2,
	}
	data := orig.Encode()
	decoded, err := DecodeProductInformation(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProductCode != 1234 {
		t.Errorf("product_code = %d, want 1234", decoded.ProductCode)
	}
	if decoded.ModelId != "GPS 200" {
		t.Errorf("model_id = %q, want GPS 200", decoded.ModelId)
	}
	if decoded.SoftwareVersion != "v3.1.0" {
		t.Errorf("software_version = %q, want v3.1.0", decoded.SoftwareVersion)
	}
	if decoded.ModelSerial != "ABC12345" {
		t.Errorf("model_serial = %q, want ABC12345", decoded.ModelSerial)
	}
}

func TestDecodeShortDataPadded(t *testing.T) {
	// NMEA 2000 devices may omit trailing fields. Short data should be
	// padded with 0xFF ("not available") rather than returning an error.
	m, err := DecodePositionRapidUpdate([]byte{0, 1, 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Remaining bytes were padded with 0xFF, so longitude should be the
	// "not available" sentinel (all bits set = -1 as int32).
	wantLon := float64(int32(-1)) * 1e-7
	if m.Longitude != wantLon {
		t.Errorf("longitude = %v, want %v (not-available sentinel)", m.Longitude, wantLon)
	}
}

func TestRegistry(t *testing.T) {
	info, ok := Registry[129025]
	if !ok {
		t.Fatal("PGN 129025 not in registry")
	}
	if info.Description != "Position Rapid Update" {
		t.Errorf("description = %q", info.Description)
	}

	// Decode through registry
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], uint32(int32(100000000)))
	lonRaw := int32(-200000000)
	binary.LittleEndian.PutUint32(data[4:8], uint32(lonRaw))

	v, err := info.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	pos, ok := v.(PositionRapidUpdate)
	if !ok {
		t.Fatalf("expected PositionRapidUpdate, got %T", v)
	}
	if math.Abs(pos.Latitude-10.0) > 1e-6 {
		t.Errorf("latitude = %f, want 10.0", pos.Latitude)
	}
}

func TestEnumString(t *testing.T) {
	if WindReferenceApparent.String() != "apparent" {
		t.Errorf("WindReferenceApparent.String() = %q", WindReferenceApparent.String())
	}
	if HeadingReferenceMagnetic.String() != "magnetic" {
		t.Errorf("HeadingReferenceMagnetic.String() = %q", HeadingReferenceMagnetic.String())
	}
}

func TestPGNMethod(t *testing.T) {
	var p PositionRapidUpdate
	if p.PGN() != 129025 {
		t.Errorf("PGN() = %d, want 129025", p.PGN())
	}
	var w WindData
	if w.PGN() != 130306 {
		t.Errorf("PGN() = %d, want 130306", w.PGN())
	}
}

func TestFluidLevelBitFields(t *testing.T) {
	// Fluid Level has 4-bit instance and 4-bit fluid_type in the first byte
	orig := FluidLevel{
		Instance:  3,
		FluidType: 5,
		Level:     75.0,
		Capacity:  200.0,
	}
	data := orig.Encode()
	decoded, err := DecodeFluidLevel(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Instance != 3 {
		t.Errorf("instance = %d, want 3", decoded.Instance)
	}
	if decoded.FluidType != 5 {
		t.Errorf("fluid_type = %d, want 5", decoded.FluidType)
	}
	if math.Abs(decoded.Level-75.0) > 0.01 {
		t.Errorf("level = %f, want ~75.0", decoded.Level)
	}
}
