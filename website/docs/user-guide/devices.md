---
sidebar_position: 6
title: Devices
---

# Device Discovery

lplex automatically discovers and tracks all devices on the NMEA 2000 bus. It builds a device registry from two PGN types:

- **PGN 60928** (ISO Address Claim): provides the 64-bit CAN NAME, manufacturer code, device class, function, and instance
- **PGN 126996** (Product Information): provides model ID, software version, and serial number

## How discovery works

1. When lplex sees a frame from an unknown source address, it sends an **ISO Request** (PGN 59904) to that source asking for an address claim
2. The device responds with PGN 60928, which populates the basic device info
3. lplex then requests PGN 126996 for product details
4. The registry updates continuously as new claims and product info arrive

This is transparent. You don't need to configure anything.

## Device table fields

| Field | Source | Description |
|---|---|---|
| `src` | CAN header | Current source address (0-253) |
| `name` | PGN 60928 | 64-bit ISO 11783 NAME (hex string) |
| `manufacturer` | PGN 60928 | Manufacturer name (resolved from code) |
| `manufacturer_code` | PGN 60928 | Raw manufacturer code |
| `device_class` | PGN 60928 | Device class number |
| `device_function` | PGN 60928 | Device function number |
| `device_instance` | PGN 60928 | Device instance (0-252) |
| `unique_number` | PGN 60928 | 21-bit unique number from NAME |
| `product_code` | PGN 126996 | Product code |
| `model_id` | PGN 126996 | Model name string |
| `software_version` | PGN 126996 | Software version string |
| `model_version` | PGN 126996 | Hardware/model version string |
| `model_serial` | PGN 126996 | Serial number string |
| `first_seen` | lplex | RFC 3339 timestamp of first frame |
| `last_seen` | lplex | RFC 3339 timestamp of last frame |
| `packet_count` | lplex | Total frames received from this device |
| `byte_count` | lplex | Total bytes received from this device |

## API

### List all devices

```bash
curl http://inuc1.local:8089/devices | jq
```

```json
[
  {
    "src": 10,
    "name": "0x00A1B2C3D4E5F600",
    "manufacturer": "Garmin",
    "manufacturer_code": 229,
    "device_class": 25,
    "device_function": 130,
    "device_instance": 0,
    "unique_number": 123456,
    "product_code": 1234,
    "model_id": "GPS 19x HVS",
    "software_version": "5.60",
    "model_version": "1",
    "model_serial": "ABC123",
    "first_seen": "2026-03-06T10:00:00Z",
    "last_seen": "2026-03-06T10:15:32Z",
    "packet_count": 45023,
    "byte_count": 360184
  }
]
```

### Filter by device

Use device-based filters on the `/events` or `/values` endpoints:

```bash
# All frames from Garmin devices
curl -N "http://inuc1.local:8089/events?manufacturer=Garmin"

# Last values from device instance 0
curl "http://inuc1.local:8089/values?instance=0"
```

## Device table in journals

Journal files include a device table snapshot in each block. This allows journal readers to resolve source addresses to device names without needing a live connection. The device table includes manufacturer, model ID, software version, and product code.

## Cloud devices

The cloud server exposes the same device API per instance:

```bash
curl https://lplex.dockwise.app/instances/boat-001/devices | jq
```

Device info is carried over the replication stream. The replica broker maintains its own DeviceRegistry populated from replicated frames.
