// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"golang.org/x/term"
)

const uploadUsage = "Usage: oa artifact upload [--presigned [--path P] [--content-type T] [--multipart] [--part-size SIZE] [--parallel N]] <repo> <file>"

// maxSinglePutBytes is the largest object one presigned PUT can carry (the S3
// limit); anything bigger has to be uploaded in parts.
const maxSinglePutBytes int64 = 5 << 30

// uploadUsageError is the error for a malformed command line. It is a type
// rather than an errors.New value because its text is the capitalized usage
// line, which is shown to the user as is.
type uploadUsageError struct{}

func (uploadUsageError) Error() string { return uploadUsage }

var (
	errUploadUsage          error = uploadUsageError{}
	errUploadNeedsPresigned       = errors.New("--path, --content-type, --multipart, --part-size and --parallel require --presigned")
	errUploadParallel             = errors.New("--parallel must be between 1 and 16")
	errUploadTarget               = errors.New("server returned an incomplete upload target")
)

// uploadOptions is a parsed `oa artifact upload` command line.
type uploadOptions struct {
	repo, filePath string
	// presigned selects the direct-to-object-store upload; the options below
	// only apply to it.
	presigned   bool
	remotePath  string
	contentType string
	// multipart forces an upload in parts at any size; files above
	// maxSinglePutBytes and a non-zero partSize select it too.
	multipart bool
	// partSize is the requested part size in bytes; 0 leaves it to the server.
	partSize int64
	parallel int
}

// artifactUploadCommand runs `oa artifact upload`. Flags may appear before or
// after the positional arguments.
func artifactUploadCommand(client *Client, args []string) {
	opts, err := parseUploadArgs(args, flag.ExitOnError, nil)
	if err != nil {
		dief("%v", err)
	}

	opts, switched := routeUpload(opts, maxSinglePutBytes)
	if switched {
		fmt.Fprintf(os.Stderr, "note: %s is larger than 5 GiB; uploading it in parts through presigned URLs\n", opts.filePath)
	}

	if opts.presigned {
		artifactUploadPresigned(client, opts)
		return
	}

	artifactUpload(client, opts.repo, opts.filePath)
}

// parseUploadArgs parses the command line of `oa artifact upload`. output
// receives flag errors and usage (nil: stderr).
func parseUploadArgs(args []string, errorHandling flag.ErrorHandling, output io.Writer) (uploadOptions, error) {
	fs := flag.NewFlagSet("artifact upload", errorHandling)
	if output != nil {
		fs.SetOutput(output)
	}

	var opts uploadOptions

	fs.BoolVar(&opts.presigned, "presigned", false, "Upload straight to the object store through a presigned URL (raw repositories)")
	fs.StringVar(&opts.remotePath, "path", "", "Artifact path in the repository (--presigned; default: the file name)")
	fs.StringVar(&opts.contentType, "content-type", "", "Content type (--presigned; default: detected by the server)")
	fs.BoolVar(&opts.multipart, "multipart", false, "Upload in parts at any size (--presigned; automatic above 5 GiB)")
	fs.Func("part-size", "Part size such as 128M, 1G or a byte count, 5M to 4000M (--presigned; implies --multipart; default: chosen by the server)",
		func(s string) error {
			n, err := parsePartSize(s)
			opts.partSize = n

			return err
		})
	fs.IntVar(&opts.parallel, "parallel", defaultUploadParallel, "Parts uploaded at the same time, 1 to 16 (--presigned, multipart)")

	var positional []string

	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return uploadOptions{}, err
		}

		if fs.NArg() == 0 {
			break
		}

		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	if len(positional) != 2 {
		return uploadOptions{}, errUploadUsage
	}

	opts.repo, opts.filePath = positional[0], positional[1]

	if !opts.presigned {
		for name := range setFlags(fs) {
			if name != "presigned" {
				return uploadOptions{}, errUploadNeedsPresigned
			}
		}
	}

	if opts.parallel < 1 || opts.parallel > maxUploadParallel {
		return uploadOptions{}, errUploadParallel
	}

	return opts, nil
}

// routeUpload sends a plain upload of a file above limit bytes through the
// presigned multipart flow, the only one that can carry it; the bool reports
// that the route changed. A file that cannot be inspected stays on the plain
// route, which reports the problem.
func routeUpload(opts uploadOptions, limit int64) (uploadOptions, bool) {
	if opts.presigned {
		return opts, false
	}

	if info, err := os.Stat(opts.filePath); err != nil || !info.Mode().IsRegular() || info.Size() <= limit {
		return opts, false
	}

	opts.presigned = true

	return opts, true
}

// presignedResult is what a finished presigned upload reports.
type presignedResult struct {
	remotePath string
	size       int64
	sha256     any
}

// artifactUploadPresigned runs a presigned upload and prints its result.
func artifactUploadPresigned(client *Client, opts uploadOptions) {
	res, err := newPresignedUploader(client).upload(opts)
	if err != nil {
		dief("%v", err)
	}

	fmt.Printf("Uploaded '%s' to '%s' as '%s' (%d bytes, sha256 %v).\n", filepath.Base(opts.filePath), opts.repo, res.remotePath, res.size, res.sha256)
}

// presignedUploader uploads files straight to the object store. The knobs are
// fields so tests can lower the size threshold and skip real backoff waits.
type presignedUploader struct {
	client *Client
	// singlePutLimit is the largest file sent with one PUT; larger ones go in parts.
	singlePutLimit int64
	// retry paces the retries of a failed part.
	retry *retrier
	// progress, when set, is told after every uploaded part.
	progress func(done, total int)
}

func newPresignedUploader(client *Client) *presignedUploader {
	return &presignedUploader{
		client:         client,
		singlePutLimit: maxSinglePutBytes,
		retry:          newRetrier(partRetries, partRetryMaxWait),
		progress:       stderrProgress(),
	}
}

// stderrProgress reports the uploaded parts on a terminal and stays silent
// otherwise, so scripts see only the final result.
func stderrProgress() func(done, total int) {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return nil
	}

	return func(done, total int) {
		fmt.Fprintf(os.Stderr, "\rUploaded %d/%d parts", done, total)

		if done == total {
			fmt.Fprintln(os.Stderr)
		}
	}
}

// uploadTarget is the answer to the upload request: a presigned URL for one PUT,
// or the part plan of a multipart upload.
type uploadTarget struct {
	ID        string         `json:"id"`
	UploadURL string         `json:"upload_url"`
	Headers   map[string]any `json:"headers"`
	Multipart bool           `json:"multipart"`
	PartSize  int64          `json:"part_size"`
	PartCount int            `json:"part_count"`
}

// upload uploads a file in three steps: ask the server for a presigned target
// (declaring size and sha256), send the bytes straight to the object store (one
// PUT, or the parts of a multipart upload for big files), and ask the server to
// verify and record the artifact. The upload itself bypasses the OpenArtifact
// server; verification does not: the server reads the object back from the
// store to check size and sha256, which can take a while for a large file.
func (u *presignedUploader) upload(opts uploadOptions) (presignedResult, error) {
	file, err := os.Open(opts.filePath)
	if err != nil {
		return presignedResult{}, fmt.Errorf("opening file: %w", err)
	}
	defer func() { _ = file.Close() }()

	sum, size, err := hashFile(file)
	if err != nil {
		return presignedResult{}, err
	}

	if opts.remotePath == "" {
		opts.remotePath = filepath.Base(opts.filePath)
	}

	multipart := opts.multipart || opts.partSize > 0 || size > u.singlePutLimit

	request := map[string]any{
		"path":         opts.remotePath,
		"size":         size,
		"sha256":       sum,
		"content_type": opts.contentType,
	}

	if multipart {
		if err := checkPartPlan(size, opts.partSize); err != nil {
			return presignedResult{}, err
		}

		request["multipart"] = true

		if opts.partSize > 0 {
			request["part_size"] = opts.partSize
		}
	}

	base := "/api/v1/repositories/" + escape(opts.repo) + "/uploads"

	var target uploadTarget
	if err := postJSON(u.client, base, request, &target, http.StatusCreated); err != nil {
		return presignedResult{}, err
	}

	if target.ID == "" {
		return presignedResult{}, errUploadTarget
	}

	if multipart {
		err = u.uploadParts(file, size, base, target, opts.parallel)
	} else {
		err = u.uploadSingle(file, size, target)
	}

	if err != nil {
		return presignedResult{}, err
	}

	done, err := completePresigned(u.client, base+"/"+escape(target.ID)+"/complete")
	if err != nil {
		return presignedResult{}, err
	}

	return presignedResult{remotePath: opts.remotePath, size: size, sha256: done["sha256"]}, nil
}

// hashFile returns the hex sha256 and the size of file, leaving it rewound.
func hashFile(file *os.File) (string, int64, error) {
	hasher := sha256.New()

	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, fmt.Errorf("hashing file: %w", err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, fmt.Errorf("rewinding file: %w", err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

// uploadSingle sends the whole file to the presigned URL of target.
func (u *presignedUploader) uploadSingle(file io.Reader, size int64, target uploadTarget) error {
	if target.UploadURL == "" {
		return errUploadTarget
	}

	return putObject(context.Background(), u.client, target.UploadURL, target.Headers, file, size)
}

// maxCompleteWait bounds how long the CLI keeps retrying a complete call that
// answers "in progress" (the server gives a verification 30 minutes).
const maxCompleteWait = 35 * time.Minute

// completePresigned asks the server to verify and record the upload and returns
// the artifact. The server hashes the whole object before answering, so there
// is no response-header timeout here, and a 409 upload_in_progress (an earlier
// attempt is still verifying) is retried until it yields the result. Any other
// failure, such as 409 upload_incomplete (a part is missing), is returned with
// the server's message.
func completePresigned(client *Client, path string) (map[string]any, error) {
	slow := *client
	slow.httpClient = newHTTPClientWithHeaderTimeout(0)

	deadline := time.Now().Add(maxCompleteWait)

	for {
		resp, err := slow.doJSON(http.MethodPost, path, nil)
		if err != nil {
			return nil, fmt.Errorf("completing upload: %w", err)
		}

		if resp.StatusCode == http.StatusConflict && time.Now().Before(deadline) {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			var e struct {
				Code string `json:"code"`
			}

			if json.Unmarshal(body, &e) == nil && e.Code == "upload_in_progress" {
				time.Sleep(retryAfter(resp))
				continue
			}

			resp.Body = io.NopCloser(bytes.NewReader(body))
		}

		var out map[string]any
		err = readAPIResponse(resp, &out, http.StatusCreated, http.StatusOK)
		_ = resp.Body.Close()

		if err != nil {
			return nil, err
		}

		return out, nil
	}
}

// retryAfter reads a Retry-After header in seconds (default 2s).
func retryAfter(resp *http.Response) time.Duration {
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}

	return 2 * time.Second
}

// postJSON POSTs body to the OpenArtifact API and decodes the answer into out.
// A status other than expected is an *httpStatusError carrying the server's
// message.
func postJSON(client *Client, path string, body, out any, expected ...int) error {
	resp, err := client.doJSON(http.MethodPost, path, body)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	return readAPIResponse(resp, out, expected...)
}

// readAPIResponse decodes the JSON body of resp into out when the status is
// one of expected; otherwise it returns an *httpStatusError. The caller closes
// the body.
func readAPIResponse(resp *http.Response, out any, expected ...int) error {
	if !slices.Contains(expected, resp.StatusCode) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

		return &httpStatusError{
			status:     resp.StatusCode,
			retryAfter: resp.Header.Get("Retry-After"),
			msg:        fmt.Sprintf("%s (HTTP %d)", errorMessage(body), resp.StatusCode),
		}
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

// putObject sends size bytes of body to the presigned URL. The OpenArtifact
// token is deliberately not attached: the URL itself is the credential, and
// errors never print it. A non-2xx answer is an *httpStatusError.
func putObject(ctx context.Context, client *Client, uploadURL string, headers map[string]any, body io.Reader, size int64) error {
	if size == 0 {
		body = http.NoBody
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		return fmt.Errorf("building upload request: %w", redactURLError(err))
	}

	req.ContentLength = size

	for k, v := range headers {
		if s, ok := v.(string); ok {
			req.Header.Set(k, s)
		}
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("uploading to object store: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return &httpStatusError{
			status:     resp.StatusCode,
			retryAfter: resp.Header.Get("Retry-After"),
			msg:        fmt.Sprintf("object store rejected the upload: HTTP %d %s", resp.StatusCode, msg),
		}
	}

	// Let the connection be reused by the next part.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	return nil
}

// redactURLError drops the URL (whose query string is a credential) from a
// net/http error.
func redactURLError(err error) error {
	if ue, ok := err.(*url.Error); ok { //nolint:errorlint // url.Error is returned unwrapped by net/http
		return ue.Err
	}

	return err
}
