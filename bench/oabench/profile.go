// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// profiler pulls pprof profiles from a server running with pprof enabled.
type profiler struct {
	base, tok, dir, name string
}

func (p *profiler) fetch(url, kind string) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}

	if p.tok != "" {
		req.Header.Set("Authorization", "Bearer "+p.tok)
	}

	hc := &http.Client{Timeout: 3 * time.Minute}

	resp, err := hc.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pprof:", err)
		return
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "pprof: status", resp.StatusCode, url)
		return
	}

	f, err := os.Create(p.path(kind))
	if err != nil {
		return
	}

	defer func() { _ = f.Close() }()

	_, _ = io.Copy(f, resp.Body)
}

// cpu records a CPU profile covering most of the measured window.
func (p *profiler) cpu(d time.Duration) {
	secs := max(int(d.Seconds())-1, 1)
	p.fetch(fmt.Sprintf("%s/profile?seconds=%d", p.base, secs), "cpu")
}

// snapshots grabs the point-in-time profiles after the window.
func (p *profiler) snapshots() {
	for _, k := range []string{"heap", "allocs", "mutex", "block", "goroutine"} {
		p.fetch(p.base+"/"+k, k)
	}
}
