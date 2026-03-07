package pgn

import (
	"encoding/hex"
	"math"
	"testing"
)

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	data, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAISClassAPositionReportDecode(t *testing.T) {
	// Real AIS Class A frame from src=43
	data := decodeHex(t, "031c44e91500de19b760e5951c5dae584103b012084b59191a00fe")

	m, err := DecodeAISClassAPositionReport(data)
	if err != nil {
		t.Fatal(err)
	}

	if m.MessageId != 3 {
		t.Errorf("message_id = %d, want 3", m.MessageId)
	}
	if m.RepeatIndicator != 0 {
		t.Errorf("repeat_indicator = %d, want 0", m.RepeatIndicator)
	}
	if m.UserId != 367608860 {
		t.Errorf("user_id = %d, want 367608860", m.UserId)
	}
	if math.Abs(m.Longitude-(-122.3042)) > 0.001 {
		t.Errorf("longitude = %f, want ~-122.3042", m.Longitude)
	}
	if math.Abs(m.Latitude-47.9586) > 0.001 {
		t.Errorf("latitude = %f, want ~47.9586", m.Latitude)
	}
	if m.PositionAccuracy != 1 {
		t.Errorf("position_accuracy = %d, want 1", m.PositionAccuracy)
	}
	if m.Raim != 0 {
		t.Errorf("raim = %d, want 0", m.Raim)
	}
	if m.TimeStamp != 23 {
		t.Errorf("time_stamp = %d, want 23", m.TimeStamp)
	}
	if math.Abs(m.Cog-2.2702) > 0.001 {
		t.Errorf("cog = %f, want ~2.2702", m.Cog)
	}
	if math.Abs(m.Sog-8.33) > 0.01 {
		t.Errorf("sog = %f, want ~8.33", m.Sog)
	}
	if m.NavStatus != NavStatusUnderWayUsingEngine {
		t.Errorf("nav_status = %d, want %d (under_way_using_engine)", m.NavStatus, NavStatusUnderWayUsingEngine)
	}
	if m.Sid != 0xfe {
		t.Errorf("sid = 0x%02x, want 0xfe", m.Sid)
	}
}

func TestAISClassAPositionReportRoundTrip(t *testing.T) {
	orig := AISClassAPositionReport{
		MessageId:          1,
		UserId:             366468000,
		Longitude:          -122.1864,
		Latitude:           48.0348,
		PositionAccuracy:   1,
		TimeStamp:          22,
		Cog:                2.9158,
		Heading:            4.4148,
		AisTransceiverInfo: AISTransceiverChannelBVdl,
		NavStatus:          NavStatusUnderWayUsingEngine,
		Sid:                0xfe,
	}
	data := orig.Encode()
	decoded, err := DecodeAISClassAPositionReport(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.MessageId != 1 {
		t.Errorf("message_id = %d, want 1", decoded.MessageId)
	}
	if decoded.UserId != 366468000 {
		t.Errorf("user_id = %d, want 366468000", decoded.UserId)
	}
	if math.Abs(decoded.Longitude-orig.Longitude) > 1e-6 {
		t.Errorf("longitude = %f, want ~%f", decoded.Longitude, orig.Longitude)
	}
	if math.Abs(decoded.Latitude-orig.Latitude) > 1e-6 {
		t.Errorf("latitude = %f, want ~%f", decoded.Latitude, orig.Latitude)
	}
	if math.Abs(decoded.Cog-orig.Cog) > 0.001 {
		t.Errorf("cog = %f, want ~%f", decoded.Cog, orig.Cog)
	}
	if math.Abs(decoded.Heading-orig.Heading) > 0.001 {
		t.Errorf("heading = %f, want ~%f", decoded.Heading, orig.Heading)
	}
	if decoded.NavStatus != NavStatusUnderWayUsingEngine {
		t.Errorf("nav_status = %d, want 0", decoded.NavStatus)
	}
}

func TestAISClassBPositionReportDecode(t *testing.T) {
	// Real AIS Class B frame from src=43
	data := decodeHex(t, "1212fe2714803326b7a0969b1c5cd51c000006000effff0074ff")

	m, err := DecodeAISClassBPositionReport(data)
	if err != nil {
		t.Fatal(err)
	}

	if m.MessageId != 18 {
		t.Errorf("message_id = %d, want 18", m.MessageId)
	}
	if m.UserId != 338165266 {
		t.Errorf("user_id = %d, want 338165266", m.UserId)
	}
	if math.Abs(m.Longitude-(-122.2233)) > 0.001 {
		t.Errorf("longitude = %f, want ~-122.2233", m.Longitude)
	}
	if math.Abs(m.Latitude-47.9959) > 0.001 {
		t.Errorf("latitude = %f, want ~47.9959", m.Latitude)
	}
	if m.TimeStamp != 23 {
		t.Errorf("time_stamp = %d, want 23", m.TimeStamp)
	}
	if math.Abs(m.Cog-0.7381) > 0.001 {
		t.Errorf("cog = %f, want ~0.7381", m.Cog)
	}
	if math.Abs(m.Sog) > 0.01 {
		t.Errorf("sog = %f, want ~0", m.Sog)
	}
}

func TestAISClassBPositionReportRoundTrip(t *testing.T) {
	orig := AISClassBPositionReport{
		MessageId:          18,
		UserId:             338165266,
		Longitude:          -122.2233,
		Latitude:           47.9959,
		TimeStamp:          23,
		Cog:                0.7381,
		AisTransceiverInfo: AISTransceiverChannelBVdl,
	}
	data := orig.Encode()
	decoded, err := DecodeAISClassBPositionReport(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.MessageId != 18 {
		t.Errorf("message_id = %d, want 18", decoded.MessageId)
	}
	if decoded.UserId != 338165266 {
		t.Errorf("user_id = %d, want 338165266", decoded.UserId)
	}
	if math.Abs(decoded.Longitude-orig.Longitude) > 1e-6 {
		t.Errorf("longitude = %f, want ~%f", decoded.Longitude, orig.Longitude)
	}
}

func TestAISAidsToNavigationReportDecode(t *testing.T) {
	// Real AIS AtoN frame from src=43
	data := decodeHex(t, "1576893a3b0060e3b60079ad1cf4ffffffff0000000053ee00e016015342202020202020202020202020202020202020404040404c4340304c434d365443")

	m, err := DecodeAISAidsToNavigationReport(data)
	if err != nil {
		t.Fatal(err)
	}

	if m.MessageId != 21 {
		t.Errorf("message_id = %d, want 21", m.MessageId)
	}
	if m.RepeatIndicator != 0 {
		t.Errorf("repeat_indicator = %d, want 0", m.RepeatIndicator)
	}
	if m.UserId != 993692022 {
		t.Errorf("user_id = %d, want 993692022", m.UserId)
	}
	if math.Abs(m.Longitude-(-122.6613)) > 0.001 {
		t.Errorf("longitude = %f, want ~-122.6613", m.Longitude)
	}
	if math.Abs(m.Latitude-48.1131) > 0.001 {
		t.Errorf("latitude = %f, want ~48.1131", m.Latitude)
	}
	if m.TimeStamp != 61 {
		t.Errorf("time_stamp = %d, want 61", m.TimeStamp)
	}
	if m.VirtualAtonFlag != 1 {
		t.Errorf("virtual_aton_flag = %d, want 1", m.VirtualAtonFlag)
	}
	if m.AtonType != 19 {
		t.Errorf("aton_type = %d, want 19", m.AtonType)
	}
	if m.AtonName != "SB" {
		t.Errorf("aton_name = %q, want %q", m.AtonName, "SB")
	}
}

func TestAISUTCAndDateReportDecode(t *testing.T) {
	// Real AIS UTC/Date frame from src=43
	data := decodeHex(t, "04a1fc370000bce8b6e07be11cfff0130501a784090b50ff00fc")

	m, err := DecodeAISUTCAndDateReport(data)
	if err != nil {
		t.Fatal(err)
	}

	if m.MessageId != 4 {
		t.Errorf("message_id = %d, want 4", m.MessageId)
	}
	if m.UserId != 3669153 {
		t.Errorf("user_id = %d, want 3669153", m.UserId)
	}
	if math.Abs(m.Longitude-(-122.6262)) > 0.001 {
		t.Errorf("longitude = %f, want ~-122.6262", m.Longitude)
	}
	if math.Abs(m.Latitude-48.4539) > 0.001 {
		t.Errorf("latitude = %f, want ~48.4539", m.Latitude)
	}
	if m.PositionAccuracy != 1 {
		t.Errorf("position_accuracy = %d, want 1", m.PositionAccuracy)
	}
	if m.Raim != 1 {
		t.Errorf("raim = %d, want 1", m.Raim)
	}
	// position_time: 0x010513f0 = 17110000 * 0.0001 = 1711.0 seconds
	if math.Abs(m.PositionTime-1711.0) > 0.1 {
		t.Errorf("position_time = %f, want ~1711.0", m.PositionTime)
	}
	// position_date: days since 1970-01-01 = 20491 = 2026-02-07
	if m.PositionDate != 20491 {
		t.Errorf("position_date = %d, want 20491", m.PositionDate)
	}
}

func TestAISClassBStaticDataPartADecode(t *testing.T) {
	// Real AIS Class B Static Data Part A frame
	data := decodeHex(t, "18f4d4e915414c494345204d41524945404040404040404040")

	m, err := DecodeAISClassBCSStaticDataPartA(data)
	if err != nil {
		t.Fatal(err)
	}

	if m.MessageId != 24 {
		t.Errorf("message_id = %d, want 24", m.MessageId)
	}
	if m.UserId != 367645940 {
		t.Errorf("user_id = %d, want 367645940", m.UserId)
	}
	want := "ALICE MARIE"
	if m.Name != want {
		t.Errorf("name = %q, want %q", m.Name, want)
	}
}

func TestAISClassBStaticDataPartBDecode(t *testing.T) {
	// Real AIS Class B Static Data Part B frame
	data := decodeHex(t, "1849112c142556535040554840404040404040405a0014000a001e00ffffffff03")

	m, err := DecodeAISClassBCSStaticDataPartB(data)
	if err != nil {
		t.Fatal(err)
	}

	if m.MessageId != 24 {
		t.Errorf("message_id = %d, want 24", m.MessageId)
	}
	if m.UserId != 338432329 {
		t.Errorf("user_id = %d, want 338432329", m.UserId)
	}
	if m.ShipType != 37 {
		t.Errorf("ship_type = %d, want 37", m.ShipType)
	}
	// ship_length: 90 * 0.1 = 9.0m
	if math.Abs(m.ShipLength-9.0) > 0.1 {
		t.Errorf("ship_length = %f, want 9.0", m.ShipLength)
	}
	// ship_beam: 20 * 0.1 = 2.0m
	if math.Abs(m.ShipBeam-2.0) > 0.1 {
		t.Errorf("ship_beam = %f, want 2.0", m.ShipBeam)
	}
	// position_ref_starboard: 10 * 0.1 = 1.0m
	if math.Abs(m.PositionRefStarboard-1.0) > 0.1 {
		t.Errorf("position_ref_starboard = %f, want 1.0", m.PositionRefStarboard)
	}
	// position_ref_bow: 30 * 0.1 = 3.0m
	if math.Abs(m.PositionRefBow-3.0) > 0.1 {
		t.Errorf("position_ref_bow = %f, want 3.0", m.PositionRefBow)
	}
	// mothership_mmsi: 0xFFFFFFFF = not available
	if m.MothershipMmsi != 0xFFFFFFFF {
		t.Errorf("mothership_mmsi = %d, want 4294967295", m.MothershipMmsi)
	}
}

func TestAISClassARegistryEntry(t *testing.T) {
	info, ok := Registry[129038]
	if !ok {
		t.Fatal("PGN 129038 not in registry")
	}
	if info.Decode == nil {
		t.Fatal("PGN 129038 Decode is nil")
	}
	if !info.FastPacket {
		t.Error("PGN 129038 should be fast_packet")
	}
	if info.Description != "AIS Class A Position Report" {
		t.Errorf("description = %q", info.Description)
	}

	data := decodeHex(t, "031c44e91500de19b760e5951c5dae584103b012084b59191a00fe")
	v, err := info.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(AISClassAPositionReport)
	if !ok {
		t.Fatalf("expected AISClassAPositionReport, got %T", v)
	}
	if m.UserId != 367608860 {
		t.Errorf("user_id = %d, want 367608860", m.UserId)
	}
}

func TestAISNameOnlyPGNsInRegistry(t *testing.T) {
	// Verify that name-only AIS PGNs are still in the registry
	nameOnlyPGNs := []uint32{129040, 129792, 129795, 129796, 129797, 129798, 129799, 129800, 129801, 129802, 129803, 129804, 129805, 129806, 129807, 129808}
	for _, pgn := range nameOnlyPGNs {
		info, ok := Registry[pgn]
		if !ok {
			t.Errorf("PGN %d not in registry", pgn)
			continue
		}
		if info.Decode != nil {
			t.Errorf("PGN %d should have nil Decode (name-only)", pgn)
		}
		if !info.FastPacket {
			t.Errorf("PGN %d should be fast_packet", pgn)
		}
	}
}
