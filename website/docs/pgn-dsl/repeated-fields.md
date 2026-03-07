---
sidebar_position: 5
title: Repeated Fields
---

# Repeated Fields

The `repeat=N` attribute generates array or map types for fields that appear multiple times in a PGN.

## Basic repetition

Use `repeat=N` to generate a slice of N elements:

```
pgn 127501 "Binary Switch Bank Status" {
  instance    uint8   :8
  indicator   uint8   :2  repeat=28
}
```

This generates:

```go
type BinarySwitchBankStatus struct {
    Instance  uint8
    Indicator [28]uint8
}
```

Each element occupies the specified bit width (`:2` in this case). The 28 indicators are packed into 56 bits (7 bytes).

## Map groups

Use `group="map"` with `repeat=` to generate a map keyed by the iteration index:

```
indicator  uint8  :2  repeat=28  group="map"
```

This generates a `map[int]uint8` instead of an array. Only non-null entries are included in the map, which is useful for sparse data.

## Custom naming

Use `as="name"` to customize the Go field name:

```
indicator  uint8  :2  repeat=28  as="switches"
```

Generates field `Switches [28]uint8` instead of `Indicator [28]uint8`.

## Bit layout

Repeated fields are packed sequentially in the bit stream. For `repeat=28` with `:2` bits each:

```
Bit:  0-7     8-9   10-11  12-13  ...  62-63
      instance  [0]    [1]    [2]  ...  [27]
```

## Combining with other fields

Repeated fields can be mixed with regular fields:

```
pgn 127501 "Binary Switch Bank Status" {
  instance    uint8   :8           # regular field
  indicator   uint8   :2  repeat=28  # 28 x 2 bits = 56 bits
}
```

Total: 8 + 56 = 64 bits = 8 bytes.
