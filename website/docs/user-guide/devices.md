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

### NAME-based deduplication

If a device restarts and claims a **new source address** while keeping the same 64-bit NAME, lplex automatically evicts the old entry. This prevents stale phantom devices from accumulating in the registry. The old source's values are also cleaned up, and a `device_removed` event is sent to SSE subscribers.

### Idle expiry

Devices that haven't sent any frames within the idle timeout (default 5 minutes) are automatically removed. This covers both NAME-bearing devices and stats-only entries. Configure with `-device-idle-timeout` (or HOCON `device.idle-timeout`). Set to `0` to disable.

## Virtual device

When lplex-server sends CAN frames (via `/send` or `/query`), it needs a claimed source address to be a compliant NMEA 2000 participant. Without this, frames are sent from the unclaimed address 254, and some devices will ignore them.

Enable a virtual device to make lplex-server claim an address on the bus:

```bash
lplex-server -virtual-device -virtual-device-name 00e0170001000004
```

Or in HOCON:

```hocon
virtual-device {
  enabled = true
  name = "00e0170001000004"
}
```

The virtual device:

- **Auto-selects** a source address (starting at 252, counting down to avoid real hardware)
- **Claims** the address via PGN 60928, with a 250ms holdoff per the NMEA 2000 spec
- **Resolves conflicts** automatically (lower NAME wins; if we lose, we pick a new address)
- **Responds** to ISO requests for address claim (PGN 60928) and product info (PGN 126996)
- **Heartbeats** periodically, re-broadcasting address claims (default every 60s) and product info (default every 5m) to keep the bus aware of our presence
- **Appears** in the device table like any other device, with full product info

The NAME must be a 64-bit hex value. Lower values have higher priority in address conflicts. See [ISO 11783-5](https://en.wikipedia.org/wiki/ISO_11783) for the NAME field encoding.

Heartbeat intervals are configurable via `-virtual-device-claim-heartbeat` (default `60s`) and `-virtual-device-product-info-heartbeat` (default `5m`). See [Configuration](../getting-started/configuration.md) for all options.

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
