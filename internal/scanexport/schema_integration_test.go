// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package scanexport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/scanexport"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// sarifSchemaURL is the official OASIS SARIF 2.1.0 schema. It is fetched, not
// vendored: the repository's license terms (OASIS IPR policy, RF on RAND) are
// not an SPDX license we can redistribute under.
const sarifSchemaURL = "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"

func fetchSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sarifSchemaURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if os.Getenv("CI") == "" {
			t.Skipf("SARIF schema not reachable: %v", err)
		}

		t.Fatalf("fetch SARIF schema: %v", err)
	}

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource("sarif.json", doc); err != nil {
		t.Fatal(err)
	}

	schema, err := c.Compile("sarif.json")
	if err != nil {
		t.Fatal(err)
	}

	return schema
}

func TestSARIFMatchesOfficialSchema(t *testing.T) {
	schema := fetchSchema(t)

	vuln, _ := scanexport.VulnSARIF(vulnItems())
	empty, _ := scanexport.VulnSARIF(nil)
	secret, _ := scanexport.SecretsSARIF(secretItems())

	for name, doc := range map[string]*scanexport.Log{"vuln": vuln, "empty": empty, "secrets": secret} {
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}

		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}

		if err := schema.Validate(inst); err != nil {
			t.Errorf("%s SARIF does not match the 2.1.0 schema: %v", name, err)
		}
	}
}
