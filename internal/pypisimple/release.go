// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"strings"
	"time"
)

// archiveSuffixes are the extensions a distribution file name can end in.
var archiveSuffixes = []string{ //nolint:gochecknoglobals // immutable lookup table
	".tar.gz", ".tar.bz2", ".tar.xz", ".tar.z", ".tar.lz", ".tgz", ".tbz", ".tar", ".zip", ".whl", ".egg", ".exe", ".msi", ".rpm",
}

// Uploaded returns the file's PEP 700 upload-time, the zero time when the
// index did not state one (HTML pages never do) or it is not a timestamp.
func (f *File) Uploaded() time.Time {
	if f.UploadTime == "" {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339, f.UploadTime)
	if err != nil {
		return time.Time{}
	}

	return t
}

// FileVersion returns the version a distribution file of project (a PEP 503
// normalized name) belongs to, in a canonical spelling so that "1.0" and
// "1.0.0" name one release. It is "" for a name that carries no version.
func FileVersion(project, filename string) string {
	return canonicalVersion(SpelledVersion(project, filename))
}

// SpelledVersion returns the version a distribution file of project belongs to
// as the file name spells it ("1.0.0", "2.0rc1", "1.0-1"), "" for a name that
// carries no version. Curation rules match this spelling.
func SpelledVersion(project, filename string) string {
	stem := strings.TrimSuffix(filename, MetadataSuffix)
	lower := strings.ToLower(stem)

	for _, suffix := range archiveSuffixes {
		if strings.HasSuffix(lower, suffix) {
			stem = stem[:len(stem)-len(suffix)]

			if suffix == ".whl" {
				// {name}-{version}-{python}-{abi}-{platform}: the name has no "-".
				parts := strings.SplitN(stem, "-", 3)
				if len(parts) < 2 {
					return ""
				}

				return parts[1]
			}

			break
		}
	}

	return versionAfterName(project, stem)
}

// versionAfterName returns the part of an sdist-style stem after the project
// name; when the stem does not start with the project's name, after the last
// "-".
func versionAfterName(project, stem string) string {
	for i := range len(stem) {
		if stem[i] == '-' && NormalizeName(stem[:i]) == project {
			return stem[i+1:]
		}
	}

	if _, v, ok := strings.CutLast(stem, "-"); ok {
		return v
	}

	return ""
}

// canonicalVersion lowercases a version and drops a leading "v", turns the
// "-" and "_" separators into "." and removes trailing ".0" components, so
// "1.0", "1.0.0" and "V1_0" are one spelling.
func canonicalVersion(v string) string {
	v = strings.TrimPrefix(strings.ToLower(v), "v")
	v = strings.NewReplacer("-", ".", "_", ".").Replace(v)

	parts := strings.Split(v, ".")
	for len(parts) > 1 && parts[len(parts)-1] == "0" {
		parts = parts[:len(parts)-1]
	}

	return strings.Join(parts, ".")
}

// Releases maps a release (the files of one version) to the time its first
// file was uploaded.
type Releases map[string]time.Time

// PublishedVersion returns when the release spelled version (any spelling that
// canonicalizes alike: "1.0" and "1.0.0") was first uploaded, the zero time when
// unknown.
func (r Releases) PublishedVersion(version string) time.Time {
	return r["v:"+canonicalVersion(version)]
}

// releaseKey names the release a file belongs to; a file whose name carries no
// version is a release of its own.
func releaseKey(project, filename string) string {
	if v := FileVersion(project, filename); v != "" {
		return "v:" + v
	}

	return "f:" + filename
}

// NewReleases groups the files of project by version and records the earliest
// upload-time of each. A release none of whose files states a time has no
// entry. Grouping by the earliest file means a wheel added to an old release
// later does not make the release look new again.
func NewReleases(project string, files []File) Releases {
	out := make(Releases)

	for i := range files {
		up := files[i].Uploaded()
		if up.IsZero() {
			continue
		}

		key := releaseKey(project, files[i].Filename)
		if first, ok := out[key]; !ok || up.Before(first) {
			out[key] = up
		}
	}

	return out
}

// Published returns when the release of filename was first uploaded, the zero
// time when unknown.
func (r Releases) Published(project, filename string) time.Time {
	return r[releaseKey(project, filename)]
}

// Filter returns the files for which keep reports true, given the version each
// is spelled with (SpelledVersion) and the first upload time of the release it
// belongs to (zero when unknown). The input is not modified.
func (r Releases) Filter(project string, files []File, keep func(version string, published time.Time) bool) []File {
	out := make([]File, 0, len(files))

	for i := range files {
		if keep(SpelledVersion(project, files[i].Filename), r.Published(project, files[i].Filename)) {
			out = append(out, files[i])
		}
	}

	return out
}
