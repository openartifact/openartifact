// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"net/http"
	"regexp"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

// azureAccountName is the shape Azure gives a storage account: it becomes the
// host label in https://<account>.blob.core.windows.net, so anything else
// (a slash, a dot, an IP) would point the client somewhere else.
var azureAccountName = regexp.MustCompile(`^[a-z0-9]{3,24}$`)

// ValidAzureAccountName reports whether name is a well-formed Azure storage
// account name (3-24 lowercase letters and digits).
func ValidAzureAccountName(name string) bool { return azureAccountName.MatchString(name) }

// dialGuardedTransport returns the transport of a REST backend (Azure, OCI)
// that refuses to connect to blocked addresses, or nil when the config is not
// SafeDial and the default streaming transport applies.
func (c StorageConfig) dialGuardedTransport() http.RoundTripper {
	if !c.SafeDial {
		return nil
	}

	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   storageTLSHandshakeTimeout,
		ResponseHeaderTimeout: storageResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	netutil.GuardTransport(tr)

	return tr
}
