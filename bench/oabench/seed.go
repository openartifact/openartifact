// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type seeder struct {
	base string
	hc   *http.Client
	tok  string
}

func (s *seeder) do(method, path, ctype string, body []byte, hdr map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(method, s.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}

	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}

	if s.tok != "" {
		req.Header.Set("Authorization", "Bearer "+s.tok)
	}

	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)

	return resp, b, nil
}

var errStatus = errors.New("unexpected status")

func (s *seeder) must(method, path, ctype string, body []byte, ok ...int) error {
	resp, b, err := s.do(method, path, ctype, body, nil)
	if err != nil {
		return err
	}

	for _, c := range ok {
		if resp.StatusCode == c {
			return nil
		}
	}

	return fmt.Errorf("%w: %s %s -> %d %s", errStatus, method, path, resp.StatusCode, strings.TrimSpace(string(b[:min(len(b), 200)])))
}

// parallel runs fn(i) for i in [0,n) on `workers` goroutines and returns the first error.
func parallel(n, workers int, fn func(i int) error) error {
	var (
		next   atomic.Int64
		wg     sync.WaitGroup
		mu     sync.Mutex
		first  error
		failed atomic.Int64
	)

	for range workers {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}

				if err := fn(i); err != nil {
					failed.Add(1)
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		})
	}

	wg.Wait()

	if failed.Load() > 0 {
		return fmt.Errorf("%d/%d failed, first: %w", failed.Load(), n, first)
	}

	return nil
}

// bootstrapToken logs in (changing the forced bootstrap password when
// needed) and mints an access token.
func (s *seeder) bootstrapToken(user, pass, newPass string) (string, error) {
	jar, _ := cookiejar.New(nil)
	hc := newHTTPClient(2)
	hc.Jar = jar
	s.hc = hc

	login := func(p string) (*http.Response, error) {
		b, _ := json.Marshal(map[string]string{"username": user, "password": p})
		resp, _, err := s.do("POST", "/api/v1/auth/login", "application/json", b, nil)

		return resp, err
	}

	resp, err := login(pass)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		resp, err = login(newPass)
		if err != nil {
			return "", err
		}

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%w: login -> %d", errStatus, resp.StatusCode)
		}
	}

	csrf := func() map[string]string {
		u, _ := http.NewRequest("GET", s.base, nil)
		for _, c := range jar.Cookies(u.URL) {
			if c.Name == "oa_csrf" {
				return map[string]string{"X-CSRF-Token": c.Value}
			}
		}

		return nil
	}

	// Forced password change on the bootstrap account; harmless otherwise.
	cp, _ := json.Marshal(map[string]string{"current_password": pass, "new_password": newPass})
	if r, _, err := s.do("POST", "/api/v1/auth/change-password", "application/json", cp, csrf()); err == nil && r.StatusCode == http.StatusOK {
		if _, err := login(newPass); err != nil {
			return "", err
		}
	}

	tb, _ := json.Marshal(map[string]any{"name": "bench-" + fmt.Sprint(time.Now().Unix()), "kind": "access"})

	r, body, err := s.do("POST", "/api/v1/auth/tokens", "application/json", tb, csrf())
	if err != nil {
		return "", err
	}

	if r.StatusCode != http.StatusCreated && r.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: create token -> %d %s", errStatus, r.StatusCode, body)
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("%w: token response %s", errStatus, body)
	}

	return out.Token, nil
}

func (s *seeder) createRepo(name, format string) error {
	b, _ := json.Marshal(map[string]any{"name": name, "format": format, "repo_type": "hosted"})

	return s.must("POST", "/api/v1/repositories", "application/json", b, 200, 201, 409)
}

//nolint:gocyclo // linear seeding script
func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:8080", "server base URL")
	user := fs.String("user", "admin", "admin username")
	pass := fs.String("pass", "", "admin password (bootstrap password)")
	newPass := fs.String("new-pass", "Bench-Passw0rd-Strong!42", "password to set when a change is forced")
	token := fs.String("token", "", "existing access token (skips login)")
	out := fs.String("state", "bench-state.json", "state file to write")
	nMaven := fs.Int("maven", 3000, "maven artifacts (jar+pom)")
	nNPM := fs.Int("npm", 1500, "npm packages")
	nDeb := fs.Int("deb", 400, "deb packages")
	rpmFile := fs.String("rpm", "", "path to an .rpm to upload (e.g. pkg/rpm/testdata/weakdeps.rpm); enables the rpm scenarios")
	workers := fs.Int("workers", 16, "parallel uploads")
	_ = fs.Parse(args)

	s := &seeder{base: strings.TrimRight(*url, "/"), hc: newHTTPClient(*workers)}
	st := &state{
		URL: s.base, MavenRepo: "bench-maven", NPMRepo: "bench-npm", DebRepo: "bench-deb",
		RPMRepo: "bench-rpm", DockerRepo: "bench-docker",
		MavenCount: *nMaven, NPMCount: *nNPM, DebCount: *nDeb,
	}

	if *token != "" {
		s.tok = *token
	} else {
		t, err := s.bootstrapToken(*user, *pass, *newPass)
		if err != nil {
			return fmt.Errorf("auth: %w", err)
		}

		s.tok = t
		s.hc = newHTTPClient(*workers)
	}

	st.Token = s.tok

	for repo, format := range map[string]string{
		st.MavenRepo: "maven", st.NPMRepo: "npm", st.DebRepo: "deb", st.RPMRepo: "rpm", st.DockerRepo: "docker",
	} {
		if err := s.createRepo(repo, format); err != nil {
			return fmt.Errorf("create repo %s: %w", repo, err)
		}
	}

	start := time.Now()

	if err := s.seedMaven(st, *nMaven, *workers); err != nil {
		return fmt.Errorf("maven: %w", err)
	}

	fmt.Printf("maven: %d artifacts in %s\n", *nMaven, time.Since(start).Round(time.Millisecond))
	start = time.Now()

	if err := s.seedNPM(st, *nNPM, *workers); err != nil {
		return fmt.Errorf("npm: %w", err)
	}

	fmt.Printf("npm: %d packages in %s\n", *nNPM, time.Since(start).Round(time.Millisecond))
	start = time.Now()

	if err := s.seedDeb(st, *nDeb, *workers); err != nil {
		return fmt.Errorf("deb: %w", err)
	}

	fmt.Printf("deb: %d packages in %s\n", *nDeb, time.Since(start).Round(time.Millisecond))

	if *rpmFile != "" {
		if err := s.seedRPM(st, *rpmFile); err != nil {
			return fmt.Errorf("rpm: %w", err)
		}
	}

	if err := s.seedDocker(st); err != nil {
		_ = st.save(*out)

		return fmt.Errorf("docker: %w", err)
	}

	fmt.Println("docker: image seeded")

	return st.save(*out)
}

func (s *seeder) seedMaven(st *state, n, workers int) error {
	jar := randBytes(8 << 10)

	put := func(path string, data []byte) error {
		return s.must("PUT", "/repos/maven/"+st.MavenRepo+"/"+path, "application/octet-stream", data, 200, 201, 204)
	}

	// 300 artifacts with n/300 versions each, plus one hot artifact with 200 versions.
	perArtifact := max(n/300, 1)

	err := parallel(n, workers, func(i int) error {
		group, art, ver := fmt.Sprintf("com.bench.g%d", i%50), fmt.Sprintf("art%d", i%300), fmt.Sprintf("1.%d.0", i/300)
		base := strings.ReplaceAll(group, ".", "/") + "/" + art + "/" + ver + "/" + art + "-" + ver

		if err := put(base+".jar", jar); err != nil {
			return err
		}

		return put(base+".pom", pomXML(group, art, ver))
	})
	if err != nil {
		return err
	}

	_ = perArtifact

	err = parallel(200, workers, func(i int) error {
		ver := fmt.Sprintf("1.0.%d", i)
		base := "com/bench/hot/hot-lib/" + ver + "/hot-lib-" + ver

		if err := put(base+".jar", jar); err != nil {
			return err
		}

		return put(base+".pom", pomXML("com.bench.hot", "hot-lib", ver))
	})
	if err != nil {
		return err
	}

	// One 10 MB jar for download and upload-size scenarios.
	st.BigJar = "com/bench/big/big-lib/1.0.0/big-lib-1.0.0.jar"

	if err := put(st.BigJar, randBytes(10<<20)); err != nil {
		return err
	}

	return put("com/bench/big/big-lib/1.0.0/big-lib-1.0.0.pom", pomXML("com.bench.big", "big-lib", "1.0.0"))
}

func (s *seeder) seedNPM(st *state, n, workers int) error {
	pub := func(name string, versions []string) error {
		b, err := npmPublishBody(name, versions)
		if err != nil {
			return err
		}

		return s.must("PUT", "/repos/"+st.NPMRepo+"/"+strings.ReplaceAll(name, "/", "%2f"), "application/json", b, 200, 201, 409)
	}

	if err := parallel(n, workers, func(i int) error {
		return pub(fmt.Sprintf("bench-pkg-%d", i), []string{"1.0.0"})
	}); err != nil {
		return err
	}

	vers := make([]string, 100)
	for i := range vers {
		vers[i] = fmt.Sprintf("1.0.%d", i)
	}

	if err := pub("hot-pkg", vers); err != nil {
		return err
	}

	st.NPMTarball = "hot-pkg/-/hot-pkg-1.0.50.tgz"

	return nil
}

func (s *seeder) seedDeb(st *state, n, workers int) error {
	return parallel(n, workers, func(i int) error {
		data := debPackage(fmt.Sprintf("benchdeb%d", i), "1.0-1", "amd64")

		var buf bytes.Buffer

		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", fmt.Sprintf("benchdeb%d_1.0-1_amd64.deb", i))
		_, _ = fw.Write(data)
		_ = mw.WriteField("distribution", "stable")
		_ = mw.WriteField("component", "main")
		_ = mw.Close()

		return s.must("POST", "/api/v1/repositories/"+st.DebRepo+"/debs", mw.FormDataContentType(), buf.Bytes(), 200, 201, 409)
	})
}

func (s *seeder) seedDocker(st *state) error {
	// Multi-segment image names do not route today ({name:*} matches one
	// segment), so the repository name is the whole image name.
	name := st.DockerRepo
	pushBlob := func(data []byte) (string, error) {
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])

		// Re-pushing an existing blob answers 500 today; clients HEAD first.
		if resp, _, err := s.do("HEAD", "/v2/"+name+"/blobs/"+digest, "", nil, nil); err == nil && resp.StatusCode == http.StatusOK {
			return digest, nil
		}

		resp, _, err := s.do("POST", "/v2/"+name+"/blobs/uploads/", "", nil, nil)
		if err != nil {
			return "", err
		}

		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusAccepted || loc == "" {
			return "", fmt.Errorf("%w: blob upload start -> %d", errStatus, resp.StatusCode)
		}

		sep := "?"
		if strings.Contains(loc, "?") {
			sep = "&"
		}

		return digest, s.must("PUT", strings.TrimPrefix(loc, s.base)+sep+"digest="+digest, "application/octet-stream", data, 201)
	}

	layer, err := pushBlob(randBytes(8 << 20))
	if err != nil {
		return err
	}

	cfgData := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["` + layer + `"]}}`)

	cfg, err := pushBlob(cfgData)
	if err != nil {
		return err
	}

	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.docker.distribution.manifest.v2+json",
		"config":        map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "size": len(cfgData), "digest": cfg},
		"layers":        []any{map[string]any{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "size": 8 << 20, "digest": layer}},
	})

	// Re-pushing an identical manifest answers 500 today; skip when present.
	if resp, _, err := s.do("HEAD", "/v2/"+name+"/manifests/latest", "", nil, nil); err != nil || resp.StatusCode != http.StatusOK {
		if err := s.must("PUT", "/v2/"+name+"/manifests/latest", "application/vnd.docker.distribution.manifest.v2+json", manifest, 201); err != nil {
			return err
		}
	}

	st.DockerBlob = "/v2/" + name + "/blobs/" + layer
	st.DockerImage = "/v2/" + name + "/manifests/latest"

	return nil
}

func (s *seeder) seedRPM(st *state, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filepath.Base(path))
	_, _ = fw.Write(data)
	_ = mw.Close()

	return s.must("POST", "/api/v1/repositories/"+st.RPMRepo+"/packages", mw.FormDataContentType(), buf.Bytes(), 200, 201, 409)
}
