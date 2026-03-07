---
sidebar_position: 3
title: Enums & Lookups
---

# Enums and Lookup Tables

The DSL provides two mechanisms for named values: **enums** and **lookups**. They serve different purposes.

## Enums

Enums define a new Go type with a `String()` method. Use them when the field has a small, dense set of possible values.

### Definition

```
enum WindReference {
  0 = "true_north"
  1 = "magnetic_north"
  2 = "apparent"
  3 = "true_boat"
  4 = "true_water"
}
```

### Generated Go code

```go
type WindReference uint8

const (
    WindReferenceTrueNorth    WindReference = 0
    WindReferenceMagneticNorth WindReference = 1
    WindReferenceApparent     WindReference = 2
    WindReferenceTrueBoat     WindReference = 3
    WindReferenceTrueWater    WindReference = 4
)

func (v WindReference) String() string {
    switch v {
    case 0: return "true_north"
    case 1: return "magnetic_north"
    case 2: return "apparent"
    case 3: return "true_boat"
    case 4: return "true_water"
    default: return "unknown"
    }
}
```

### Usage in PGN

```
pgn 130306 "Wind Data" {
  wind_reference  WindReference  :3
}
```

The Go struct field becomes `WindReference WindReference`, and JSON serialization uses the string value.

### When to use enums

- The value set is small (fits in the bit width)
- Values are dense (0, 1, 2, ... with few gaps)
- You want type safety (a distinct Go type)
- You want `String()` for human-readable output

## Lookup tables

Lookups map sparse integer keys to human-readable names without creating a new type. Use them when the key space is large and sparse.

### Definition

```
lookup VictronRegister uint16 {
  0x0100 = "Product ID"
  0x0200 = "Device Mode"
  0x0201 = "Device State"
  0xED8D = "DC Channel 1 Voltage"
  0xED8E = "DC Channel 1 Power"
  0xEDD3 = "Yield Today"
}
```

The type after the name (`uint16`) specifies the key type.

### Generated Go code

```go
var victronRegisterNames = map[uint16]string{
    0x0100: "Product ID",
    0x0200: "Device Mode",
    0x0201: "Device State",
    0xED8D: "DC Channel 1 Voltage",
    0xED8E: "DC Channel 1 Power",
    0xEDD3: "Yield Today",
}

// RegisterName returns the human-readable name, or empty if unknown.
func (m VictronBatteryRegister) RegisterName() string {
    return victronRegisterNames[m.Register]
}

// LookupFields returns JSON field name -> resolved name for display code.
func (m VictronBatteryRegister) LookupFields() map[string]string {
    return map[string]string{
        "register": victronRegisterNames[m.Register],
    }
}
```

### Usage in PGN

```
pgn 61184 "Victron Battery Register" {
  register  uint16  :16  lookup=VictronRegister
}
```

The Go struct field stays `uint16` (no new type), but gains a `RegisterName() string` helper method and a `LookupFields()` method for display code.

Display tools like `lplexdump` use `LookupFields()` to wrap lookup fields as `{"id": <raw>, "name": "..."}` objects in JSON output. Unknown values omit the `name` field.

### When to use lookups

- The key space is large or sparse (e.g., 0x0100, 0xED8D)
- You want to keep the raw integer type for arithmetic
- You're mapping register IDs, product codes, or other identifiers
- Adding new entries shouldn't require a new Go constant

## Comparison

| | Enum | Lookup |
|---|---|---|
| Go type | New named type | Original integer type |
| `String()` | Yes (on the type) | `Name()` function |
| Type safety | Yes | No |
| Key space | Dense (0, 1, 2, ...) | Sparse (any values) |
| JSON output | String value | `{"id": <raw>, "name": "..."}` object |
| Use case | Small finite sets | Large/sparse mappings |
