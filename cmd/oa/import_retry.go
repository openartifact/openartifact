// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultImportRetries      = 3
	defaultImportRetryMaxWait = 30 * time.Second
	retryBaseWait             = 500 * time.Millisecond
)

// httpStatusError is a non-2xx response. It keeps the status and the
// Retry-After header so the retry logic can classify it; its text is the same
// message the importer has always reported.
type httpStatusError struct {
	status     int
	retryAfter string
	msg        string
}

func (e *httpStatusError) Error() string { return e.msg }

func (e *httpStatusError) Unwrap() error { return errHTTP }

func newStatusError(resp *http.Response, what, detail string) error {
	return &httpStatusError{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get("Retry-After"),
		msg:        fmt.Sprintf("%v: %s: HTTP %d: %s", errHTTP, what, resp.StatusCode, detail),
	}
}

// permanentError marks a failure that must not be retried even though its
// cause looks transient (an upload that is not safe to repeat).
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return &permanentError{err: err} }

// retrier re-runs an operation after transient failures, with exponential
// backoff plus jitter. A nil *retrier never retries. The clock, sleeper and
// jitter are fields so tests run without real waiting.
type retrier struct {
	max     int
	maxWait time.Duration
	sleep   func(ctx context.Context, d time.Duration) error
	jitter  func(d time.Duration) time.Duration
	now     func() time.Time
	// observe is told about every retry that is about to be waited out.
	observe func(what string, retry int, wait time.Duration, err error)
}

func newRetrier(maxRetries int, maxWait time.Duration) *retrier {
	return &retrier{max: maxRetries, maxWait: maxWait, sleep: sleepContext, jitter: halfJitter, now: time.Now}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// halfJitter spreads d over [d/2, d] so concurrent workers do not retry in step.
func halfJitter(d time.Duration) time.Duration {
	half := d / 2

	return half + rand.N(d-half+1) //nolint:gosec // jitter, not security
}

// do runs op until it succeeds, fails permanently, or the retries run out.
// op receives the zero-based attempt number.
func (r *retrier) do(ctx context.Context, what string, op func(attempt int) error) error {
	for attempt := 0; ; attempt++ {
		err := op(attempt)
		if err == nil {
			return nil
		}

		wait, ok := r.delay(attempt, err)
		if !ok || ctx.Err() != nil {
			return withRetryCount(err, attempt)
		}

		if r.observe != nil {
			r.observe(what, attempt+1, wait, err)
		}

		if r.sleep(ctx, wait) != nil {
			return withRetryCount(err, attempt)
		}
	}
}

func withRetryCount(err error, retries int) error {
	if retries == 0 {
		return err
	}

	return fmt.Errorf("%w (after %d retries)", err, retries)
}

// delay decides whether failed attempt number attempt is retried and how long
// to wait first. A Retry-After beyond maxWait is not waited out: the run fails
// fast and can be resumed later.
func (r *retrier) delay(attempt int, err error) (time.Duration, bool) {
	if r == nil || attempt >= r.max {
		return 0, false
	}

	if _, ok := errors.AsType[*permanentError](err); ok {
		return 0, false
	}

	retry, after := r.classify(err)
	if !retry {
		return 0, false
	}

	if after >= 0 {
		return after, after <= r.maxWait
	}

	return r.backoff(attempt), true
}

func (r *retrier) backoff(attempt int) time.Duration {
	d := retryBaseWait
	for range attempt {
		if d >= r.maxWait {
			break
		}

		d *= 2
	}

	return r.jitter(min(d, r.maxWait))
}

// classify reports whether err is transient and, for 429/503 carrying a usable
// Retry-After, how long the server asked to wait (-1 when it did not).
func (r *retrier) classify(err error) (bool, time.Duration) {
	if se, ok := errors.AsType[*httpStatusError](err); ok {
		return statusRetryable(se.status), r.retryAfter(se)
	}

	return transportRetryable(err), -1
}

func statusRetryable(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

func (r *retrier) retryAfter(se *httpStatusError) time.Duration {
	if se.status != http.StatusTooManyRequests && se.status != http.StatusServiceUnavailable {
		return -1
	}

	v := strings.TrimSpace(se.retryAfter)
	if v == "" {
		return -1
	}

	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 || secs > int64(24*time.Hour/time.Second) {
			return -1
		}

		return time.Duration(secs) * time.Second
	}

	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(r.now()), 0)
	}

	return -1
}

// transportRetryable reports connection-level failures: timeouts, resets,
// refused connections, and bodies that ended early. Cancellation, TLS
// certificate problems and redirect refusals are final.
func transportRetryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}

	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}

	var ne net.Error

	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return true
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ECONNABORTED), errors.Is(err, syscall.EPIPE):
		return true
	case errors.As(err, &ne):
		return true
	}

	return false
}
