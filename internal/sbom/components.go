// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrUnreadable is returned by ReadComponents for a stored document that is not
// well-formed for its format.
var ErrUnreadable = errors.New("sbom: document is not readable")

// Component is one package a stored SBOM document names.
type Component struct {
	Name     string   `json:"name"`
	Version  string   `json:"version,omitempty"`
	PURL     string   `json:"purl,omitempty"`
	Licenses []string `json:"licenses,omitempty"`
}

// maxTextLine bounds one line of an SPDX tag-value document.
const maxTextLine = 1 << 20

const spdxNoLicense = "NOASSERTION"

// ReadComponents lists the packages of a stored SBOM document of the given
// format ("cyclonedx", "spdx-json", "spdx3-json" or "spdx", the formats of
// SBOMResult.Format), in document order. At most limit are returned; truncated
// says the document names more. The document is read as a stream, so only the
// returned components are held in memory, and reading stops as soon as the
// answer is known.
func ReadComponents(r io.Reader, format string, limit int) ([]Component, bool, error) {
	var (
		comps     []Component
		truncated bool
		err       error
	)

	switch format {
	case "cyclonedx":
		comps, truncated, err = readJSONArray(r, "components", limit, cdxEntry)
	case "spdx-json":
		comps, truncated, err = readJSONArray(r, "packages", limit, spdxEntry)
	case "spdx3-json":
		comps, truncated, err = readJSONArray(r, "@graph", limit, spdx3Entry)
	case "spdx":
		comps, truncated, err = readTagValue(r, limit)
	default:
		return nil, false, fmt.Errorf("%w: unknown format %q", ErrUnreadable, format)
	}

	if err != nil {
		return nil, false, err
	}

	return comps, truncated, nil
}

// readJSONArray streams the top-level object of a JSON document to the array
// stored under key and converts its elements with conv (which reports false for
// an element that is not a component).
func readJSONArray(
	r io.Reader, key string, limit int, conv func(json.RawMessage) (Component, bool, error),
) ([]Component, bool, error) {
	dec := json.NewDecoder(r)

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false, fmt.Errorf("%w: not a JSON object", ErrUnreadable)
	}

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrUnreadable, err)
		}

		if k, _ := tok.(string); k == key {
			return readArray(dec, limit, conv)
		}

		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrUnreadable, err)
		}
	}

	return nil, false, nil
}

func readArray(dec *json.Decoder, limit int, conv func(json.RawMessage) (Component, bool, error)) ([]Component, bool, error) {
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, false, fmt.Errorf("%w: component list is not an array", ErrUnreadable)
	}

	comps := []Component{}

	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrUnreadable, err)
		}

		c, ok, err := conv(raw)
		if err != nil {
			return nil, false, err
		}

		if !ok {
			continue
		}

		if len(comps) >= limit {
			return comps, true, nil
		}

		comps = append(comps, c)
	}

	return comps, false, nil
}

func cdxEntry(raw json.RawMessage) (Component, bool, error) {
	var e struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		PURL     string `json:"purl"`
		Licenses []struct {
			License *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"license"`
			Expression string `json:"expression"`
		} `json:"licenses"`
	}

	if err := json.Unmarshal(raw, &e); err != nil {
		return Component{}, false, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	c := Component{Name: e.Name, Version: e.Version, PURL: e.PURL}

	for _, l := range e.Licenses {
		switch {
		case l.Expression != "":
			c.Licenses = append(c.Licenses, l.Expression)
		case l.License != nil && l.License.ID != "":
			c.Licenses = append(c.Licenses, l.License.ID)
		case l.License != nil && l.License.Name != "":
			c.Licenses = append(c.Licenses, l.License.Name)
		}
	}

	return c, true, nil
}

func spdxEntry(raw json.RawMessage) (Component, bool, error) {
	//nolint:tagliatelle // the SPDX 2.3 JSON schema names its properties in camelCase
	var e struct {
		Name             string `json:"name"`
		VersionInfo      string `json:"versionInfo"`
		LicenseConcluded string `json:"licenseConcluded"`
		LicenseDeclared  string `json:"licenseDeclared"`
		ExternalRefs     []struct {
			Type    string `json:"referenceType"`
			Locator string `json:"referenceLocator"`
		} `json:"externalRefs"`
	}

	if err := json.Unmarshal(raw, &e); err != nil {
		return Component{}, false, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	c := Component{Name: e.Name, Version: e.VersionInfo}

	for _, ref := range e.ExternalRefs {
		if ref.Type == "purl" {
			c.PURL = ref.Locator
			break
		}
	}

	c.Licenses = spdxLicenses(e.LicenseConcluded, e.LicenseDeclared)

	return c, true, nil
}

// spdxLicenses is the concluded license, else the declared one; NOASSERTION and
// NONE state nothing.
func spdxLicenses(concluded, declared string) []string {
	for _, l := range []string{concluded, declared} {
		if l != "" && l != spdxNoLicense && l != "NONE" {
			return []string{l}
		}
	}

	return nil
}

func spdx3Entry(raw json.RawMessage) (Component, bool, error) {
	//nolint:tagliatelle // the SPDX 3.0 JSON-LD properties are named by the spec
	var e struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Version string `json:"software_packageVersion"`
		PURL    string `json:"software_packageUrl"`
	}

	if err := json.Unmarshal(raw, &e); err != nil {
		return Component{}, false, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	if e.Type != "software_Package" {
		return Component{}, false, nil
	}

	return Component{Name: e.Name, Version: e.Version, PURL: e.PURL}, true, nil
}

// readTagValue reads the packages of an SPDX tag-value document: each
// PackageName line starts one.
func readTagValue(r io.Reader, limit int) ([]Component, bool, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxTextLine)

	comps := []Component{}

	var (
		cur                 *Component
		concluded, declared string
		flush               = func() {
			if cur != nil {
				cur.Licenses = spdxLicenses(concluded, declared)
				comps = append(comps, *cur)
			}

			cur, concluded, declared = nil, "", ""
		}
	)

	for sc.Scan() {
		tag, value, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}

		value = strings.TrimSpace(value)

		switch tag {
		case "PackageName":
			flush()

			if len(comps) >= limit {
				return comps, true, nil
			}

			cur = &Component{Name: value}
		case "PackageVersion":
			if cur != nil {
				cur.Version = value
			}
		case "PackageLicenseConcluded":
			concluded = value
		case "PackageLicenseDeclared":
			declared = value
		case "ExternalRef":
			if fields := strings.Fields(value); cur != nil && len(fields) == 3 && fields[1] == "purl" {
				cur.PURL = fields[2]
			}
		}
	}

	if err := sc.Err(); err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	flush()

	return comps, false, nil
}
