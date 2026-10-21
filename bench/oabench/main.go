// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command oabench seeds a running OpenArtifact instance with thousands of
// packages and drives HTTP load scenarios against it, reporting latency
// percentiles, throughput, error rate and server RSS/CPU.
//
//	oabench seed -url http://127.0.0.1:8080 -user admin -pass '...' -state state.json
//	oabench load -state state.json -duration 15s -concurrency 32 -pid 1234
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error

	switch os.Args[1] {
	case "seed":
		err = runSeed(os.Args[2:])
	case "load":
		err = runLoad(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "oabench:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: oabench seed|load [flags]")
}
