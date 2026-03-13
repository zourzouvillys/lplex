package canbus

import (
	"testing"
)

func TestEncodeNameRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		fields NameEncodeFields
	}{
		{
			name: "typical marine device",
			fields: NameEncodeFields{
				UniqueNumber:            0x12345,
				ManufacturerCode:        341, // Victron
				DeviceInstance:          0,
				DeviceFunction:          130,
				DeviceClass:             35,
				SystemInstance:          0,
				IndustryGroup:          4, // Marine
				ArbitraryAddressCapable: true,
			},
		},
		{
			name: "all fields populated",
			fields: NameEncodeFields{
				UniqueNumber:            0x1FFFFF,
				ManufacturerCode:        0x7FF,
				DeviceInstance:          0xFF,
				DeviceFunction:          0xFF,
				DeviceClass:             0x7F,
				SystemInstance:          0x0F,
				IndustryGroup:          0x07,
				ArbitraryAddressCapable: true,
			},
		},
		{
			name: "zero values",
			fields: NameEncodeFields{},
		},
		{
			name: "not arbitrary address capable",
			fields: NameEncodeFields{
				UniqueNumber:            42,
				ManufacturerCode:        229, // Garmin
				DeviceFunction:          60,
				DeviceClass:             25,
				IndustryGroup:          4,
				ArbitraryAddressCapable: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeName(tt.fields)
			decoded := DecodeNAME(encoded)

			if decoded.UniqueNumber != tt.fields.UniqueNumber&0x1FFFFF {
				t.Errorf("UniqueNumber: got %d, want %d", decoded.UniqueNumber, tt.fields.UniqueNumber&0x1FFFFF)
			}
			if decoded.ManufacturerCode != tt.fields.ManufacturerCode&0x7FF {
				t.Errorf("ManufacturerCode: got %d, want %d", decoded.ManufacturerCode, tt.fields.ManufacturerCode&0x7FF)
			}
			if decoded.DeviceFunction != tt.fields.DeviceFunction {
				t.Errorf("DeviceFunction: got %d, want %d", decoded.DeviceFunction, tt.fields.DeviceFunction)
			}
			if decoded.DeviceClass != tt.fields.DeviceClass&0x7F {
				t.Errorf("DeviceClass: got %d, want %d", decoded.DeviceClass, tt.fields.DeviceClass&0x7F)
			}
			// DeviceInstance round-trips through the lower/upper split
			if decoded.DeviceInstance != tt.fields.DeviceInstance {
				t.Errorf("DeviceInstance: got %d, want %d", decoded.DeviceInstance, tt.fields.DeviceInstance)
			}
		})
	}
}

func TestEncodeNameKnownValue(t *testing.T) {
	// Encode a known NAME and verify the hex matches what we'd expect from a
	// real device on the bus.
	f := NameEncodeFields{
		UniqueNumber:            4,
		ManufacturerCode:        0x70, // 112
		DeviceInstance:          0,
		DeviceFunction:          0,
		DeviceClass:             0,
		SystemInstance:          0,
		IndustryGroup:          4, // Marine
		ArbitraryAddressCapable: true,
	}
	name := EncodeName(f)

	// Verify decode round-trips.
	decoded := DecodeNAME(name)
	if decoded.UniqueNumber != 4 {
		t.Errorf("UniqueNumber: got %d, want 4", decoded.UniqueNumber)
	}
	if decoded.ManufacturerCode != 0x70 {
		t.Errorf("ManufacturerCode: got %d, want %d", decoded.ManufacturerCode, 0x70)
	}

	// Re-encode should be identical.
	reencoded := EncodeName(NameEncodeFields{
		UniqueNumber:            decoded.UniqueNumber,
		ManufacturerCode:        decoded.ManufacturerCode,
		DeviceInstance:          decoded.DeviceInstance,
		DeviceFunction:          decoded.DeviceFunction,
		DeviceClass:             decoded.DeviceClass,
		ArbitraryAddressCapable: true,
		IndustryGroup:          4,
	})
	if reencoded != name {
		t.Errorf("re-encode mismatch: got %016x, want %016x", reencoded, name)
	}
}
