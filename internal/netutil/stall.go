// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// ErrBodyStalled is returned by a response body read that received no data
// for the configured stall timeout.
var ErrBodyStalled = errors.New("upstream response body stalled")

// stallTransport cancels a request whose response body stops making progress.
type stallTransport struct {
	base  http.RoundTripper
	stall time.Duration
}

func (t *stallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())

	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(err)
		return nil, err
	}

	// The timer is created stopped; it only runs while a Read is blocked.
	b := &stallBody{rc: resp.Body, ctx: ctx, cancel: cancel, stall: t.stall}
	b.timer = time.AfterFunc(t.stall, func() { cancel(ErrBodyStalled) })
	b.timer.Stop()
	resp.Body = b

	return resp, nil
}

type stallBody struct {
	rc     io.ReadCloser
	ctx    context.Context //nolint:containedctx // the request's own context, read for its cancel cause
	cancel context.CancelCauseFunc
	timer  *time.Timer
	stall  time.Duration
}

func (b *stallBody) Read(p []byte) (int, error) {
	b.timer.Reset(b.stall)
	n, err := b.rc.Read(p)
	b.timer.Stop()

	if err != nil && !errors.Is(err, io.EOF) && errors.Is(context.Cause(b.ctx), ErrBodyStalled) {
		err = ErrBodyStalled
	}

	return n, err
}

func (b *stallBody) Close() error {
	b.timer.Stop()
	err := b.rc.Close()
	b.cancel(nil)

	return err
}
