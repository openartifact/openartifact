// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	configDirMode  = 0o700
	configFileMode = 0o600
	maxRedirects   = 10
)

var (
	errPasswordFlag   = errors.New("--password is no longer supported (it leaks into shell history and process lists); use --password-stdin or the interactive prompt")
	errRedirectLimit  = errors.New("stopped after too many redirects")
	errHTTPSDowngrade = errors.New("refusing redirect from https to http")
	errEmptyPassword  = errors.New("no password supplied on stdin")
)

// stdinReader is the single buffered reader over os.Stdin shared by every
// prompt, so that piped input is never split across competing buffers.
var stdinReader = bufio.NewReader(os.Stdin) //nolint:gochecknoglobals // one shared reader over the process stdin is the point

// readLine reads one line from r and trims the trailing line terminator.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("reading input: %w", err)
	}

	return strings.TrimRight(line, "\r\n"), nil
}

// promptLine prints prompt to stderr and reads a visible line from stdin.
func promptLine(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)

	line, err := readLine(stdinReader)
	if err != nil && !errors.Is(err, io.EOF) {
		dief("%v", err)
	}

	return strings.TrimSpace(line)
}

// readSecretFrom reads a secret: with fromStdin (or when stdin is not a
// terminal) it reads one line from r; otherwise it prompts without echo.
func readSecretFrom(r *bufio.Reader, fd int, interactive bool, prompt string) (string, error) {
	if interactive && term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)

		b, err := term.ReadPassword(fd)

		fmt.Fprintln(os.Stderr)

		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}

		return string(b), nil
	}

	return readLine(r)
}

// readPassword prompts for a secret without echo when stdin is a terminal,
// and otherwise reads one line from the shared stdin reader.
func readPassword(prompt string) string {
	s, err := readSecretFrom(stdinReader, int(os.Stdin.Fd()), true, prompt)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return ""
		}

		dief("%v", err)
	}

	return s
}

// readPasswordStdin reads the password for --password-stdin from stdin.
func readPasswordStdin() string {
	s, err := readLine(stdinReader)
	if err != nil {
		dief("%v: %v", errEmptyPassword, err)
	}

	if s == "" {
		dief("%v", errEmptyPassword)
	}

	return s
}

// newHTTPClient builds a client without a whole-request timeout (so long
// transfers work) but with connect/handshake/response-header timeouts.
func newHTTPClient() *http.Client {
	return newHTTPClientWithHeaderTimeout(60 * time.Second)
}

// newHTTPClientWithHeaderTimeout is newHTTPClient with a custom response-header
// timeout; 0 waits indefinitely for the response (the server is doing work,
// such as verifying a large upload, before it answers).
func newHTTPClientWithHeaderTimeout(headerTimeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
	}

	return &http.Client{Transport: transport, CheckRedirect: checkRedirect}
}

// checkRedirect refuses https->http downgrades and strips credentials when the
// redirect leaves the original host.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errRedirectLimit
	}

	prev := via[len(via)-1]
	if prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errHTTPSDowngrade
	}

	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
	}

	return nil
}

// writeFileAtomic writes data to path via temp file + rename with fileMode.
func writeFileAtomic(path string, data []byte, dirMode, fileMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}

	tmpName := tmp.Name()

	cleanup := func(e error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)

		return e
	}

	if err := tmp.Chmod(fileMode); err != nil {
		return cleanup(fmt.Errorf("chmod temp file: %w", err))
	}

	if _, err := tmp.Write(data); err != nil {
		return cleanup(fmt.Errorf("writing temp file: %w", err))
	}

	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("syncing temp file: %w", err))
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)

		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)

		return fmt.Errorf("replacing %s: %w", path, err)
	}

	return nil
}

// multipartStream returns a reader producing a multipart/form-data body with
// one file part (field "file") streamed from file, plus the matching Content-Type. Errors
// while reading the file propagate to the reader side via CloseWithError.
func multipartStream(file io.Reader, fileName string) (io.ReadCloser, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		part, err := mw.CreateFormFile("file", fileName)
		if err == nil {
			_, err = io.Copy(part, file)
		}

		if err == nil {
			err = mw.Close()
		}

		_ = pw.CloseWithError(err) // nil err => plain EOF
	}()

	return pr, mw.FormDataContentType()
}

// downloadTo streams the response body into a temp file beside dest and
// renames it onto dest only after a complete, synced write.
func downloadTo(resp *http.Response, dest string) (int64, error) {
	f, err := os.CreateTemp(filepath.Dir(dest), ".oa-download-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("creating temp file: %w", err)
	}

	tmpName := f.Name()

	fail := func(e error) (int64, error) {
		_ = f.Close()
		_ = os.Remove(tmpName)

		return 0, e
	}

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fail(fmt.Errorf("writing file: %w", err))
	}

	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fail(fmt.Errorf("%w: got %d of %d bytes", io.ErrUnexpectedEOF, n, resp.ContentLength))
	}

	if err := f.Chmod(0o644); err != nil { //nolint:mnd // regular downloaded file mode
		return fail(fmt.Errorf("chmod: %w", err))
	}

	if err := f.Sync(); err != nil {
		return fail(fmt.Errorf("syncing file: %w", err))
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)

		return 0, fmt.Errorf("closing file: %w", err)
	}

	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)

		return 0, fmt.Errorf("moving file into place: %w", err)
	}

	return n, nil
}
