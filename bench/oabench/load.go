// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// reqSpec is one request to issue.
type reqSpec struct {
	method string
	path   string
	hdr    map[string]string
	body   []byte
}

// scenario produces the n-th request of a run.
type scenario struct {
	name string
	// concurrency overrides the global -concurrency when > 0 (heavy uploads).
	concurrency int
	gen         func(n int64) reqSpec
}

// result is one scenario's measurements.
type result struct {
	Name        string  `json:"name"`
	Concurrency int     `json:"concurrency"`
	Requests    int64   `json:"requests"`
	Errors      int64   `json:"errors"`
	ErrorRate   float64 `json:"error_rate"`
	RPS         float64 `json:"rps"`
	MBps        float64 `json:"mb_per_s"`
	P50ms       float64 `json:"p50_ms"`
	P95ms       float64 `json:"p95_ms"`
	P99ms       float64 `json:"p99_ms"`
	MaxMs       float64 `json:"max_ms"`
	CPUPct      float64 `json:"server_cpu_pct"`
	RSSMB       float64 `json:"server_rss_mb"`
	HWMMB       float64 `json:"server_rss_hwm_mb"`
	AllocsPerOp float64 `json:"server_allocs_per_req"`
	BytesPerOp  float64 `json:"server_alloc_bytes_per_req"`
}

func buildScenarios(st *state, runID string) []scenario {
	rnd := func(n int64, mod int) int { return int(n*2654435761>>8) % mod }

	mavenJar := func(n int64) string {
		i := rnd(n, max(st.MavenCount, 1))
		group, art, ver := fmt.Sprintf("com/bench/g%d", i%50), fmt.Sprintf("art%d", i%300), fmt.Sprintf("1.%d.0", i/300)

		return "/repos/maven/" + st.MavenRepo + "/" + group + "/" + art + "/" + ver + "/" + art + "-" + ver + ".jar"
	}

	get := func(path string, hdr map[string]string) func(int64) reqSpec {
		return func(int64) reqSpec { return reqSpec{method: "GET", path: path, hdr: hdr} }
	}

	return []scenario{
		{name: "dl maven jar (8KB)", gen: func(n int64) reqSpec { return reqSpec{method: "GET", path: mavenJar(n)} }},
		{name: "dl maven jar (10MB)", concurrency: 8, gen: get("/repos/maven/"+st.MavenRepo+"/"+st.BigJar, nil)},
		{name: "dl npm tarball", gen: get("/repos/"+st.NPMRepo+"/"+st.NPMTarball, nil)},
		{name: "dl docker blob (8MB)", concurrency: 8, gen: get(st.DockerBlob, nil)},
		{name: "docker manifest", gen: get(st.DockerImage, map[string]string{"Accept": "application/vnd.docker.distribution.manifest.v2+json"})},
		{name: "meta maven-metadata.xml (200 ver)", gen: get("/repos/maven/"+st.MavenRepo+"/com/bench/hot/hot-lib/maven-metadata.xml", nil)},
		{name: "meta npm packument (100 ver)", gen: get("/repos/"+st.NPMRepo+"/hot-pkg", nil)},
		{name: "meta npm packument abbreviated", gen: get("/repos/"+st.NPMRepo+"/hot-pkg", map[string]string{"Accept": "application/vnd.npm.install-v1+json"})},
		{name: "meta deb Packages.gz", gen: get("/repos/"+st.DebRepo+"/dists/stable/main/binary-amd64/Packages.gz", nil)},
		{name: "meta deb Release", gen: get("/repos/"+st.DebRepo+"/dists/stable/Release", nil)},
		{name: "meta rpm repomd.xml", gen: get("/repos/"+st.RPMRepo+"/repodata/repomd.xml", nil)},
		{name: "api GET /auth/me", gen: get("/api/v1/auth/me", nil)},
		{name: "api GET /repositories", gen: get("/api/v1/repositories", nil)},
		{name: "api GET packages (maven, limit=100)", gen: func(n int64) reqSpec {
			return reqSpec{method: "GET", path: fmt.Sprintf("/api/v1/repositories/%s/packages?limit=100&page=%d", st.MavenRepo, 1+rnd(n, 20))}
		}},
		{name: "api GET packages (npm, limit=100)", gen: func(n int64) reqSpec {
			return reqSpec{method: "GET", path: fmt.Sprintf("/api/v1/repositories/%s/packages?limit=100&page=%d", st.NPMRepo, 1+rnd(n, 10))}
		}},
		{name: "search q=bench", gen: get("/api/v1/search?q=bench", nil)},
		{name: "search q=art1", gen: get("/api/v1/search?q=art1&limit=50", nil)},
		{name: "dashboard audit", gen: get("/api/v1/admin/audit?limit=50", nil)},
		{name: "dashboard system info", gen: get("/api/v1/admin/system/info", nil)},
		{name: "spa index.html", gen: get("/", nil)},
		{name: "ul maven PUT 10MB", concurrency: 4, gen: func(n int64) reqSpec {
			return reqSpec{method: "PUT", body: bigPayload, path: fmt.Sprintf("/repos/maven/%s/com/bench/up%s/up-lib/1.0.%d/up-lib-1.0.%d.jar", st.MavenRepo, runID, n, n)}
		}},
		{name: "ul npm publish", gen: func(n int64) reqSpec {
			b, _ := npmPublishBody(fmt.Sprintf("up-%s-%d", runID, n), []string{"1.0.0"})

			return reqSpec{method: "PUT", body: b, hdr: map[string]string{"Content-Type": "application/json"}, path: fmt.Sprintf("/repos/%s/up-%s-%d", st.NPMRepo, runID, n)}
		}},
		{name: "ul deb upload", gen: func(n int64) reqSpec {
			var buf bytes.Buffer

			mw := multipart.NewWriter(&buf)
			fw, _ := mw.CreateFormFile("file", "x.deb")
			_, _ = fw.Write(debPackage(fmt.Sprintf("up%s%d", runID, n), "1.0-1", "amd64"))
			_ = mw.WriteField("distribution", "stable")
			_ = mw.WriteField("component", "main")
			_ = mw.Close()

			return reqSpec{method: "POST", body: buf.Bytes(), hdr: map[string]string{"Content-Type": mw.FormDataContentType()}, path: "/api/v1/repositories/" + st.DebRepo + "/debs"}
		}},
	}
}

var bigPayload = randBytes(10 << 20)

//nolint:gocyclo // flag plumbing
func runLoad(args []string) error {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	statePath := fs.String("state", "bench-state.json", "state file written by seed")
	dur := fs.Duration("duration", 10*time.Second, "duration per scenario")
	conc := fs.Int("concurrency", 32, "concurrent clients per scenario")
	only := fs.String("only", "", "comma-separated substrings; run matching scenarios only")
	pid := fs.Int("pid", 0, "server PID for RSS/CPU sampling")
	metricsURL := fs.String("metrics", "", "server /metrics URL for allocs/request (needs anonymous metrics or -metrics-token)")
	metricsTok := fs.String("metrics-token", "", "bearer token for /metrics")
	pprofURL := fs.String("pprof", "", "server pprof base URL (e.g. http://127.0.0.1:8080/debug/pprof); needs -pprof-token")
	pprofTok := fs.String("pprof-token", "", "bearer token for pprof")
	profDir := fs.String("profile-dir", "", "write per-scenario CPU/heap/alloc/mutex/block/goroutine profiles here")
	jsonOut := fs.String("json", "", "write results as JSON to this file")
	label := fs.String("label", "run", "label stored in JSON output")
	warm := fs.Duration("warmup", 1*time.Second, "warmup per scenario (not measured)")
	_ = fs.Parse(args)

	st, err := loadState(*statePath)
	if err != nil {
		return fmt.Errorf("read state (run seed first): %w", err)
	}

	runID := fmt.Sprint(time.Now().Unix() % 1_000_000)
	scenarios := buildScenarios(st, runID)

	var results []result

	for _, sc := range scenarios {
		if *only != "" && !matchAny(sc.name, *only) {
			continue
		}

		c := *conc
		if sc.concurrency > 0 {
			c = min(sc.concurrency, *conc)
		}

		h := &harness{st: st, pid: *pid, metricsURL: *metricsURL, metricsTok: *metricsTok}
		h.hc = newHTTPClient(c)

		if *warm > 0 {
			h.run(sc, c, *warm, nil, nil) // not recorded
		}

		var prof *profiler
		if *pprofURL != "" && *profDir != "" {
			prof = &profiler{base: *pprofURL, tok: *pprofTok, dir: *profDir, name: sc.name}
		}

		r := h.run(sc, c, *dur, prof, nil)
		results = append(results, r)

		fmt.Printf("%-40s c=%-3d n=%-7d err=%-5.1f%% rps=%-9.1f p50=%-8.2f p95=%-8.2f p99=%-8.2f MB/s=%-7.1f cpu=%-5.0f%% rss=%-6.0fMB allocs/req=%-8.0f B/req=%.0f\n",
			r.Name, r.Concurrency, r.Requests, r.ErrorRate*100, r.RPS, r.P50ms, r.P95ms, r.P99ms, r.MBps, r.CPUPct, r.RSSMB, r.AllocsPerOp, r.BytesPerOp)
	}

	if *jsonOut != "" {
		b, _ := json.MarshalIndent(map[string]any{"label": *label, "duration": dur.String(), "concurrency": *conc, "results": results}, "", "  ")

		return os.WriteFile(*jsonOut, b, 0o600)
	}

	return nil
}

func matchAny(name, csv string) bool {
	for p := range strings.SplitSeq(csv, ",") {
		if p != "" && strings.Contains(strings.ToLower(name), strings.ToLower(p)) {
			return true
		}
	}

	return false
}

type harness struct {
	st         *state
	hc         *http.Client
	pid        int
	metricsURL string
	metricsTok string
}

//nolint:gocyclo // measurement loop
func (h *harness) run(sc scenario, conc int, d time.Duration, prof *profiler, _ any) result {
	var (
		counter  atomic.Int64
		errs     atomic.Int64
		firstErr atomic.Bool
		bytesRx  atomic.Int64
		wg       sync.WaitGroup
		mu       sync.Mutex
		all      []time.Duration
	)

	cpu0, wall0 := procCPU(h.pid), time.Now()
	m0 := scrapeGo(h.hc, h.metricsURL, h.metricsTok)

	stopSample := make(chan struct{})
	rssMax := make(chan float64, 1)

	go func() {
		var peak float64

		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()

		for {
			select {
			case <-stopSample:
				rssMax <- peak
				return
			case <-t.C:
				peak = max(peak, procRSSMB(h.pid))
			}
		}
	}()

	if prof != nil {
		go prof.cpu(d)
	}

	deadline := wall0.Add(d)

	for range conc {
		wg.Go(func() {
			local := make([]time.Duration, 0, 4096)

			for time.Now().Before(deadline) {
				n := counter.Add(1) - 1
				spec := sc.gen(n)

				var body io.Reader
				if spec.body != nil {
					body = bytes.NewReader(spec.body)
				}

				req, err := http.NewRequest(spec.method, h.st.URL+spec.path, body)
				if err != nil {
					errs.Add(1)
					continue
				}

				req.Header.Set("Authorization", "Bearer "+h.st.Token)

				for k, v := range spec.hdr {
					req.Header.Set(k, v)
				}

				t0 := time.Now()
				resp, err := h.hc.Do(req)

				if err != nil {
					errs.Add(1)
					continue
				}

				if resp.StatusCode >= 400 {
					errs.Add(1)

					if firstErr.CompareAndSwap(false, true) {
						snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
						fmt.Fprintf(os.Stderr, "  first error in %q: HTTP %d %s\n", sc.name, resp.StatusCode, snippet)
					}
				}

				bytesRx.Add(drain(resp))
				local = append(local, time.Since(t0))
			}

			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
		})
	}

	wg.Wait()

	wall := time.Since(wall0)

	close(stopSample)

	peak := <-rssMax
	cpu1 := procCPU(h.pid)
	m1 := scrapeGo(h.hc, h.metricsURL, h.metricsTok)

	if prof != nil {
		prof.snapshots()
	}

	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	pct := func(p float64) float64 {
		if len(all) == 0 {
			return 0
		}

		return float64(all[min(int(float64(len(all))*p), len(all)-1)].Microseconds()) / 1000
	}

	total := int64(len(all))
	r := result{
		Name: sc.name, Concurrency: conc, Requests: total, Errors: errs.Load(),
		RPS:   float64(total) / wall.Seconds(),
		MBps:  float64(bytesRx.Load()) / wall.Seconds() / (1 << 20),
		P50ms: pct(0.50), P95ms: pct(0.95), P99ms: pct(0.99),
		RSSMB: peak, HWMMB: procHWMMB(h.pid),
	}

	if total > 0 {
		r.ErrorRate = float64(errs.Load()) / float64(total+errs.Load())
	}

	if len(all) > 0 {
		r.MaxMs = float64(all[len(all)-1].Microseconds()) / 1000
	}

	if cpu1 > 0 {
		r.CPUPct = (cpu1 - cpu0) / wall.Seconds() * 100
	}

	if total > 0 && m1.mallocs > 0 {
		r.AllocsPerOp = (m1.mallocs - m0.mallocs) / float64(total)
		r.BytesPerOp = (m1.allocBytes - m0.allocBytes) / float64(total)
	}

	return r
}

func ensureDir(p string) { _ = os.MkdirAll(p, 0o750) }

func (p *profiler) path(kind string) string {
	ensureDir(p.dir)

	safe := strings.NewReplacer(" ", "_", "/", "_", "(", "", ")", "", "=", "", ",", "").Replace(p.name)

	return filepath.Join(p.dir, safe+"."+kind+".pb.gz")
}
