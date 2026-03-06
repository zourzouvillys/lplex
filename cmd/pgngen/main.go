// pgngen generates Go structs, Protocol Buffer definitions, and JSON Schema
// from NMEA 2000 PGN definition files written in the lplex PGN DSL.
//
// Usage:
//
//	pgngen -defs pgn/defs -go pgn -proto proto/pgn -jsonschema pgn/schema.json
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sixfathoms/lplex/pgngen"
)

func main() {
	defsDir := flag.String("defs", "pgn/defs", "directory containing .pgn definition files")
	goDir := flag.String("go", "", "output directory for generated Go code (empty to skip)")
	goPkg := flag.String("go-pkg", "pgn", "Go package name for generated code")
	protoDir := flag.String("proto", "", "output directory for generated .proto file (empty to skip)")
	protoPkg := flag.String("proto-pkg", "pgn.v1", "protobuf package name")
	jsonSchemaFile := flag.String("jsonschema", "", "output path for JSON Schema (empty to skip)")
	flag.Parse()

	if *goDir == "" && *protoDir == "" && *jsonSchemaFile == "" {
		fmt.Fprintln(os.Stderr, "at least one output flag (-go, -proto, -jsonschema) is required")
		flag.Usage()
		os.Exit(1)
	}

	// Read all .pgn files
	sources := map[string]string{}
	entries, err := os.ReadDir(*defsDir)
	if err != nil {
		log.Fatalf("reading defs directory: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pgn") {
			continue
		}
		path := filepath.Join(*defsDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("reading %s: %v", path, err)
		}
		sources[e.Name()] = string(data)
	}
	if len(sources) == 0 {
		log.Fatalf("no .pgn files found in %s", *defsDir)
	}

	schema, err := pgngen.ParseFiles(sources)
	if err != nil {
		log.Fatalf("parsing: %v", err)
	}

	fmt.Printf("parsed %d enums, %d lookups, %d PGN definitions\n", len(schema.Enums), len(schema.Lookups), len(schema.PGNs))

	// Generate Go
	if *goDir != "" {
		if err := os.MkdirAll(*goDir, 0o755); err != nil {
			log.Fatalf("creating Go output dir: %v", err)
		}
		// Helpers file
		helpers := pgngen.GenerateGoHelpers(*goPkg)
		if err := os.WriteFile(filepath.Join(*goDir, "helpers_gen.go"), []byte(helpers), 0o644); err != nil {
			log.Fatalf("writing helpers: %v", err)
		}
		// PGN definitions
		code := pgngen.GenerateGo(schema, *goPkg)
		if err := os.WriteFile(filepath.Join(*goDir, "pgn_gen.go"), []byte(code), 0o644); err != nil {
			log.Fatalf("writing Go code: %v", err)
		}
		fmt.Printf("wrote %s/pgn_gen.go (%d bytes)\n", *goDir, len(code))
		fmt.Printf("wrote %s/helpers_gen.go\n", *goDir)
	}

	// Generate Proto
	if *protoDir != "" {
		if err := os.MkdirAll(*protoDir, 0o755); err != nil {
			log.Fatalf("creating proto output dir: %v", err)
		}
		proto := pgngen.GenerateProto(schema, *protoPkg)
		path := filepath.Join(*protoDir, "pgn.proto")
		if err := os.WriteFile(path, []byte(proto), 0o644); err != nil {
			log.Fatalf("writing proto: %v", err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(proto))
	}

	// Generate JSON Schema
	if *jsonSchemaFile != "" {
		dir := filepath.Dir(*jsonSchemaFile)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("creating JSON Schema output dir: %v", err)
		}
		js := pgngen.GenerateJSONSchema(schema)
		if err := os.WriteFile(*jsonSchemaFile, []byte(js), 0o644); err != nil {
			log.Fatalf("writing JSON Schema: %v", err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", *jsonSchemaFile, len(js))
	}
}
