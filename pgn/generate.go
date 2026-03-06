// Package pgn provides generated Go types for decoding and encoding NMEA 2000 PGN messages.
//
// The types in this package are generated from PGN definition files (.pgn DSL)
// in the pgn/defs/ directory using the pgngen tool. Do not edit the generated
// files directly; modify the .pgn definitions and regenerate.
//
//go:generate go run ../cmd/pgngen -defs defs -go . -proto proto -jsonschema schema.json
package pgn
