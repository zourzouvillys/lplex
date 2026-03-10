package pgn

import (
	"encoding/hex"
	"math"
	"testing"
)

func TestDecodeNavigationRouteWPInformation(t *testing.T) {
	// Real frame from Garmin chartplotter: 2 waypoints, destination "End"
	// near 48.1°N, 122.6°W. WP1 has all not-available fields.
	raw, _ := hex.DecodeString("ffff0200ffffffffe00201ffffff0201ffffff7fffffff7f01000501456e641f3eac1c85e0e6b6")

	m, err := DecodeNavigationRouteWPInformation(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if m.StartRps != 0xFFFF {
		t.Errorf("StartRPS = %d, want %d", m.StartRps, 0xFFFF)
	}
	if m.Items != 2 {
		t.Errorf("Items = %d, want 2", m.Items)
	}
	if m.DatabaseId != 0xFFFF {
		t.Errorf("DatabaseID = %d, want %d", m.DatabaseId, 0xFFFF)
	}
	if m.RouteId != 0xFFFF {
		t.Errorf("RouteID = %d, want %d", m.RouteId, 0xFFFF)
	}
	if m.NavigationDirection != 0 {
		t.Errorf("NavigationDirection = %d, want 0", m.NavigationDirection)
	}
	if m.SupplementaryData != 0 {
		t.Errorf("SupplementaryData = %d, want 0", m.SupplementaryData)
	}
	if m.RouteName != "" {
		t.Errorf("RouteName = %q, want empty", m.RouteName)
	}
	if len(m.Waypoints) != 2 {
		t.Fatalf("len(Waypoints) = %d, want 2", len(m.Waypoints))
	}

	// WP1: all not-available.
	wp1 := m.Waypoints[0]
	if wp1.Id != 0xFFFF {
		t.Errorf("wp[0].ID = %d, want %d", wp1.Id, 0xFFFF)
	}
	if wp1.Name != "" {
		t.Errorf("wp[0].Name = %q, want empty", wp1.Name)
	}
	// 0x7FFFFFFF * 1e-7 = 214.7483647 (NMEA "not available" for signed int32).
	if math.Abs(wp1.Latitude-214.7483647) > 1e-6 {
		t.Errorf("wp[0].Latitude = %f, want ~214.7483647", wp1.Latitude)
	}
	if math.Abs(wp1.Longitude-214.7483647) > 1e-6 {
		t.Errorf("wp[0].Longitude = %f, want ~214.7483647", wp1.Longitude)
	}

	// WP2: destination "End" near San Juan Islands.
	wp2 := m.Waypoints[1]
	if wp2.Id != 1 {
		t.Errorf("wp[1].ID = %d, want 1", wp2.Id)
	}
	if wp2.Name != "End" {
		t.Errorf("wp[1].Name = %q, want %q", wp2.Name, "End")
	}
	if math.Abs(wp2.Latitude-48.1050143) > 1e-6 {
		t.Errorf("wp[1].Latitude = %f, want ~48.1050143", wp2.Latitude)
	}
	if math.Abs(wp2.Longitude-(-122.6383227)) > 1e-6 {
		t.Errorf("wp[1].Longitude = %f, want ~-122.6383227", wp2.Longitude)
	}
}

func TestDecodeNavigationRouteWPInformationTooShort(t *testing.T) {
	_, err := DecodeNavigationRouteWPInformation([]byte{0x00, 0x01})
	if err == nil {
		t.Fatal("expected error for data shorter than header")
	}
}

func TestDecodeNavigationRouteWPInformationZeroItems(t *testing.T) {
	// Minimal valid packet: 0 waypoints, empty route name.
	raw, _ := hex.DecodeString("ffff0000ffffffffe00201ff")
	m, err := DecodeNavigationRouteWPInformation(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Items != 0 {
		t.Errorf("Items = %d, want 0", m.Items)
	}
	if len(m.Waypoints) != 0 {
		t.Errorf("len(Waypoints) = %d, want 0", len(m.Waypoints))
	}
}

func TestNavigationRouteWPInformationRegistry(t *testing.T) {
	info, ok := Registry[129285]
	if !ok {
		t.Fatal("PGN 129285 not in registry")
	}
	if info.Description != "Navigation Route WP Information" {
		t.Errorf("description = %q", info.Description)
	}
	if !info.FastPacket {
		t.Error("expected FastPacket: true")
	}
	if info.Decode == nil {
		t.Error("expected non-nil Decode")
	}
}
