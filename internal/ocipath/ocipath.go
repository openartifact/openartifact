// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ocipath parses OCI distribution /v2/ request paths. The RBAC
// middleware and the registry handler both use it, so the repository RBAC
// authorizes is always the repository the handler serves.
//
// A distribution name such as "reg/org/team/app" addresses an OpenArtifact
// repository (its first segment, "reg") and an image inside it (the rest,
// "org/team/app"). A single-segment name addresses the repository's default
// image, whose name is empty.
package ocipath

import (
	"regexp"
	"strings"
)

// Kind identifies the registry resource a /v2/ path addresses.
type Kind int

const (
	// KindUnknown is a path that matches none of the registry endpoints.
	KindUnknown Kind = iota
	// KindManifest is /v2/<name>/manifests/<reference>.
	KindManifest
	// KindBlob is /v2/<name>/blobs/<digest>.
	KindBlob
	// KindUploadStart is /v2/<name>/blobs/uploads/ (POST).
	KindUploadStart
	// KindUpload is /v2/<name>/blobs/uploads/<uuid>.
	KindUpload
	// KindTags is /v2/<name>/tags/list.
	KindTags
	// KindReferrers is /v2/<name>/referrers/<digest>.
	KindReferrers
)

// Request is a parsed /v2/ path.
type Request struct {
	Kind Kind
	// Name is the full distribution name, e.g. "reg/org/team/app".
	Name string
	// Ref is the trailing reference: manifest reference, digest, or upload UUID.
	// Empty for KindUploadStart and KindTags.
	Ref string
}

// Parse splits rest, the part of a /v2/… URL after the "/v2/" prefix, into the
// distribution name and the sub-resource. The sub-resource is matched from the
// right, so a name may itself contain a literal marker segment (the spec's
// name grammar allows a component called "manifests").
func Parse(rest string) Request {
	segs := strings.Split(rest, "/")
	n := len(segs)

	join := func(end int) string { return strings.Join(segs[:end], "/") }

	switch {
	case n >= 4 && segs[n-3] == "blobs" && segs[n-2] == "uploads":
		if segs[n-1] == "" {
			return Request{Kind: KindUploadStart, Name: join(n - 3)}
		}

		return Request{Kind: KindUpload, Name: join(n - 3), Ref: segs[n-1]}
	case n >= 3 && segs[n-2] == "blobs" && segs[n-1] == "uploads":
		return Request{Kind: KindUploadStart, Name: join(n - 2)}
	case n >= 3 && segs[n-2] == "tags" && segs[n-1] == "list":
		return Request{Kind: KindTags, Name: join(n - 2)}
	case n >= 3 && segs[n-1] != "":
		switch segs[n-2] {
		case "manifests":
			return Request{Kind: KindManifest, Name: join(n - 2), Ref: segs[n-1]}
		case "blobs":
			return Request{Kind: KindBlob, Name: join(n - 2), Ref: segs[n-1]}
		case "referrers":
			return Request{Kind: KindReferrers, Name: join(n - 2), Ref: segs[n-1]}
		}
	}

	return Request{Name: rest}
}

// SplitName splits a distribution name into the OpenArtifact repository (the
// first segment) and the image within it (the remaining segments, "" for the
// repository's default image).
func SplitName(name string) (string, string) {
	repo, image, _ := strings.Cut(name, "/")
	return repo, image
}

// Resource returns the artifact path RBAC scopes against: the image, when
// there is one, followed by the sub-resource — e.g. "org/team/app/manifests/1".
// rest is the full path after "/v2/"; name is its distribution-name prefix.
func Resource(rest, name string) string {
	_, image := SplitName(name)
	sub := strings.TrimPrefix(strings.TrimPrefix(rest, name), "/")

	switch {
	case image == "":
		return sub
	case sub == "":
		return image
	default:
		return image + "/" + sub
	}
}

// imageRE is the distribution-spec name grammar for one name; an image name is
// the path components after the OpenArtifact repository.
var imageRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*$`)

// ValidImage reports whether image is empty (the default image) or matches the
// distribution-spec name grammar.
func ValidImage(image string) bool {
	return image == "" || imageRE.MatchString(image)
}
