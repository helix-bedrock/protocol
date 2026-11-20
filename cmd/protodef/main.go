// Command protodef writes a ProtoDef schema of every packet (the protocol.json format of
// PrismarineJS minecraft-data), generated from the Go source of this module.
//
//	go run ./cmd/protodef -o protocol.json
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/helix-bedrock/protocol/internal/protodef"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1) //nolint:forbidigo // a command reports failure through its exit status
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("protodef", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "root of the protocol module")
	out := fs.String("o", "", "file to write the schema to (default: standard output)")
	indent := fs.Bool("indent", true, "indent the JSON")
	allowIncomplete := fs.Bool("allow-incomplete", false, "write the schema even if some code could not be converted")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("protodef: unexpected arguments %q (name the output file with -o)", fs.Args())
	}
	s, err := protodef.Generate(*dir)
	if err != nil {
		return err
	}
	for _, d := range s.Diagnostics {
		_, _ = fmt.Fprintln(stderr, d)
	}
	if len(s.Diagnostics) > 0 && !*allowIncomplete {
		return fmt.Errorf("protodef: %d constructs could not be converted (see above); use -allow-incomplete to write the schema anyway", len(s.Diagnostics))
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("protodef: encode schema: %w", err)
	}
	if *indent {
		var buf bytes.Buffer
		if err := json.Indent(&buf, data, "", "  "); err != nil {
			return fmt.Errorf("protodef: indent schema: %w", err)
		}
		data = buf.Bytes()
	}
	data = append(data, '\n')
	if *out == "" {
		if _, err := stdout.Write(data); err != nil {
			return fmt.Errorf("protodef: write schema: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil { //nolint:gosec // the schema is meant to be shared
		return fmt.Errorf("protodef: write schema: %w", err)
	}
	return nil
}
