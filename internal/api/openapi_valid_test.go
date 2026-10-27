// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// oas31Schemas holds the official OpenAPI 3.1 meta-schemas (schema-base,
// schema, the base dialect and its vocabulary), fetched from
// https://spec.openapis.org/oas/3.1/. They are vendored so the test is offline.
//
//go:embed testdata/oas31/*.json
var oas31Schemas embed.FS

func oas31Resources() map[string]string {
	return map[string]string{
		"https://spec.openapis.org/oas/3.1/schema-base/2022-10-07": "testdata/oas31/schema-base.json",
		"https://spec.openapis.org/oas/3.1/schema/2022-10-07":      "testdata/oas31/schema.json",
		"https://spec.openapis.org/oas/3.1/dialect/base":           "testdata/oas31/dialect-base.json",
		"https://spec.openapis.org/oas/3.1/meta/base":              "testdata/oas31/meta-base.json",
	}
}

// TestOpenAPISpecIsValid31 validates the shipped spec, including every schema
// object against the 3.1 JSON Schema dialect, so 3.0-only constructs such as
// `nullable` or boolean `exclusiveMinimum` are rejected.
func TestOpenAPISpecIsValid31(t *testing.T) {
	t.Parallel()

	c := jsonschema.NewCompiler()

	for url, file := range oas31Resources() {
		data, err := oas31Schemas.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}

		if err := c.AddResource(url, doc); err != nil {
			t.Fatal(err)
		}
	}

	sch, err := c.Compile("https://spec.openapis.org/oas/3.1/schema-base/2022-10-07")
	if err != nil {
		t.Fatal(err)
	}

	spec, err := jsonschema.UnmarshalJSON(bytes.NewReader(openapiSpec))
	if err != nil {
		t.Fatal(err)
	}

	if err := sch.Validate(spec); err != nil {
		t.Fatalf("openapi.json is not a valid OpenAPI 3.1 document: %v", err)
	}

	var head struct {
		OpenAPI string `json:"openapi"`
	}

	if err := json.Unmarshal(openapiSpec, &head); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(head.OpenAPI, "3.1.") {
		t.Errorf("openapi = %q, want 3.1.x", head.OpenAPI)
	}

	if bytes.Contains(openapiSpec, []byte(`"nullable"`)) {
		t.Error("openapi.json still uses the 3.0 `nullable` keyword")
	}
}
