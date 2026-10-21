// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const clkTck = 100 // sysconf(_SC_CLK_TCK) on Linux

// procCPU returns the process's cumulative user+system CPU seconds, or 0.
func procCPU(pid int) float64 {
	if pid <= 0 {
		return 0
	}

	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}

	s := string(b)
	// fields after the ")" that closes the comm field
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
	if len(f) < 13 {
		return 0
	}

	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)

	return (ut + st) / clkTck
}

func procStatusKB(pid int, key string) float64 {
	if pid <= 0 {
		return 0
	}

	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}

	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key+":"); ok {
			n, _ := strconv.ParseFloat(strings.Fields(v)[0], 64)

			return n
		}
	}

	return 0
}

func procRSSMB(pid int) float64 { return procStatusKB(pid, "VmRSS") / 1024 }
func procHWMMB(pid int) float64 { return procStatusKB(pid, "VmHWM") / 1024 }

type goMem struct{ mallocs, allocBytes float64 }

// scrapeGo reads cumulative Go allocator counters from the Prometheus endpoint.
func scrapeGo(hc *http.Client, url, tok string) goMem {
	var m goMem

	if url == "" {
		return m
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return m
	}

	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return m
	}

	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)

	for line := range strings.SplitSeq(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "go_memstats_mallocs_total "):
			m.mallocs, _ = strconv.ParseFloat(strings.Fields(line)[1], 64)
		case strings.HasPrefix(line, "go_memstats_alloc_bytes_total "):
			m.allocBytes, _ = strconv.ParseFloat(strings.Fields(line)[1], 64)
		}
	}

	return m
}
