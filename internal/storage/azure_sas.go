// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"maps"
	"net/url"
	"strings"
	"time"
)

// azureSASTimeFormat is the ISO 8601 UTC form of the st and se SAS fields.
const azureSASTimeFormat = "2006-01-02T15:04:05Z"

var _ Presigner = (*AzureBlobStore)(nil)

// azureSAS describes a service SAS for one blob.
type azureSAS struct {
	// blob is the blob name inside the container.
	blob string
	// permissions are the sp letters in the fixed order racwd ("r", "w", "cw").
	permissions string
	// expiry is when the SAS stops working (second precision).
	expiry time.Time
	// contentDisposition and contentType override the response headers (rscd, rsct).
	contentDisposition string
	contentType        string
}

// azureSASResource is the signed resource (sr) of a blob SAS.
const azureSASResource = "b"

// sasQuery returns the query parameters of a blob service SAS (sv 2020-10-02)
// signed with the account key. Only HTTPS is accepted (spr) when the endpoint
// is HTTPS.
//
// String to sign for sv >= 2018-11-09 and < 2020-12-06 ("Create a service SAS"):
//
//	sp \n st \n se \n canonicalizedResource \n si \n sip \n spr \n sv \n sr \n
//	snapshot \n rscc \n rscd \n rsce \n rscl \n rsct
//
// The canonicalized resource is /blob/<account>/<container>/<blob> whatever the
// addressing of the endpoint (Azurite's path-style account segment is not part
// of it, unlike for Shared Key). Fields are URL-decoded.
func (a *AzureBlobStore) sasQuery(s azureSAS) url.Values {
	expiry := s.expiry.UTC().Format(azureSASTimeFormat)

	protocol := ""
	if strings.HasPrefix(a.baseURL, "https://") {
		protocol = "https"
	}

	stringToSign := strings.Join([]string{
		s.permissions,
		"", // st: valid from now on
		expiry,
		"/blob/" + a.accountName + "/" + a.containerName + "/" + s.blob,
		"", // si
		"", // sip
		protocol,
		azureAPIVersion,
		azureSASResource,
		"",                   // snapshot
		"",                   // rscc
		s.contentDisposition, // rscd
		"",                   // rsce
		"",                   // rscl
		s.contentType,        // rsct
	}, "\n")

	q := url.Values{
		"sv":  {azureAPIVersion},
		"sr":  {azureSASResource},
		"sp":  {s.permissions},
		"se":  {expiry},
		"sig": {a.sign(stringToSign)},
	}

	if protocol != "" {
		q.Set("spr", protocol)
	}

	if s.contentDisposition != "" {
		q.Set("rscd", s.contentDisposition)
	}

	if s.contentType != "" {
		q.Set("rsct", s.contentType)
	}

	return q
}

// sasURL returns the URL of the blob with the SAS query and any extra request
// parameters (comp=block&blockid=...) appended.
func (a *AzureBlobStore) sasURL(s azureSAS, extra url.Values) string {
	q := a.sasQuery(s)
	maps.Copy(q, extra)

	return a.blobURL(s.blob) + "?" + q.Encode()
}

// PresignURL returns a SAS URL for direct blob download.
func (a *AzureBlobStore) PresignURL(ctx context.Context, sha256 string, ttl time.Duration) (string, error) {
	return a.PresignGet(ctx, sha256, ttl, PresignGetOptions{})
}

// PresignGet returns a read-only SAS URL for the blob with a clamped TTL (a
// TTL <= 0 selects the store's configured presign TTL). The response overrides
// in opts are part of the signature. Signing is local: no request is made.
func (a *AzureBlobStore) PresignGet(_ context.Context, sha256 string, ttl time.Duration, opts PresignGetOptions) (string, error) {
	if err := ValidateBlobID(sha256); err != nil {
		return "", err
	}

	if ttl <= 0 {
		ttl = a.presignTTL
	}

	return a.sasURL(azureSAS{
		blob:               azureBlobKey(sha256),
		permissions:        "r",
		expiry:             a.now().Add(clampTTL(ttl)),
		contentDisposition: opts.ContentDisposition,
		contentType:        opts.ContentType,
	}, nil), nil
}
