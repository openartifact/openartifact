// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package blockreason is the contract of a policy refusal on the download path
// of proxy and group repositories.
//
// A download the repository's policy refuses, whatever the format, answers 403
// and names the policy that refused it twice, so a build tool or a CI task can
// tell "blocked on purpose" from a transport failure without parsing prose:
//
//   - the X-OA-Block-Reason response header carries the policy;
//   - the JSON body is {"error", "code": "BLOCKED_BY_POLICY", "policy",
//     "package", "version"} ("package" and "version" are empty when the refused
//     file does not name them).
//
// Docker clients only understand the distribution-spec error body, so for the
// registry API the body stays {"errors":[{"code":"DENIED",...}]} and the same
// facts move into its "detail" member; the header is the same.
package blockreason

import (
	"encoding/json"
	"net/http"

	"github.com/openartifact/openartifact/internal/ocipath"
)

// Header is the response header that carries the Policy of a refusal. Header
// names are case-insensitive: it is documented as X-OA-Block-Reason, and this is
// its canonical form in Go.
const Header = "X-Oa-Block-Reason"

// Code is the "code" of the JSON body of every policy refusal.
const Code = "BLOCKED_BY_POLICY"

// Policy is the kind of policy that refused a download.
type Policy string

// The policies that refuse downloads.
const (
	// Quarantine is a vulnerability or secret scan that quarantined the artifact.
	Quarantine Policy = "quarantine"
	// Curation is the repository's package denylist or allowlist.
	Curation Policy = "curation"
	// MinAge is the repository's minimum age of a published version.
	MinAge Policy = "min_age"
	// License is a license policy that blocks the artifact.
	License Policy = "license"
)

// Block is one refusal.
type Block struct {
	Policy Policy
	// Package and Version identify what was refused; either may be empty.
	Package, Version string
	// Message is the human-readable reason.
	Message string
}

type body struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Policy  Policy `json:"policy"`
	Package string `json:"package"`
	Version string `json:"version"`
}

// Write answers the request with the refusal: 403, the header and the JSON body.
func (b Block) Write(w http.ResponseWriter) {
	w.Header().Set(Header, string(b.Policy))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)

	out, err := json.Marshal(body{Error: b.Message, Code: Code, Policy: b.Policy, Package: b.Package, Version: b.Version})
	if err == nil {
		_, _ = w.Write(append(out, '\n'))
	}
}

// WriteOCI is Write for the registry API: the distribution-spec error DENIED
// with the policy, the package (the image) and the version (the reference) in
// its detail.
func (b Block) WriteOCI(w http.ResponseWriter) {
	w.Header().Set(Header, string(b.Policy))

	ocipath.WriteErrorDetail(w, http.StatusForbidden, "DENIED", b.Message, map[string]string{
		"code":    Code,
		"policy":  string(b.Policy),
		"package": b.Package,
		"version": b.Version,
	})
}
