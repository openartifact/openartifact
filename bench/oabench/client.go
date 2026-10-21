// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"net"
	"net/http"
	"time"
)

func newHTTPClient(conns int) *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			MaxIdleConns:        conns * 2,
			MaxIdleConnsPerHost: conns * 2,
			MaxConnsPerHost:     0,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
	}
}

func drain(resp *http.Response) int64 {
	n, _ := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return n
}
