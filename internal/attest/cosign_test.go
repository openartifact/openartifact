// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCosignVerifiesBundles proves interoperability: `cosign verify-blob-attestation`
// (COSIGN_BIN, or cosign on PATH; skipped when absent) accepts bundles signed by
// every supported key type for every predicate type, and rejects a modified blob.
func TestCosignVerifiesBundles(t *testing.T) {
	bin := os.Getenv("COSIGN_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("cosign"); err != nil {
			t.Skip("cosign not installed")
		}
	}

	cases := []struct{ predicate, cosignType string }{
		{PredicateCycloneDX, "cyclonedx"},
		{PredicateSPDX, "spdxjson"},
		{PredicateOpenVEX, "openvex"},
		{PredicateSPDX3, PredicateSPDX3},
	}

	for name, key := range signersUnderTest(t) {
		s, err := NewSigner(key)
		if err != nil {
			t.Fatal(err)
		}

		dir := t.TempDir()
		blob := filepath.Join(dir, "package.tgz")

		if err := os.WriteFile(blob, []byte("package bytes"), 0o600); err != nil {
			t.Fatal(err)
		}

		pub := filepath.Join(dir, "pub.pem")
		if err := os.WriteFile(pub, s.PublicKeyPEM(), 0o600); err != nil {
			t.Fatal(err)
		}

		sum := sha256.Sum256([]byte("package bytes"))

		for _, c := range cases {
			env, err := s.Attest(c.predicate, []byte(`{"k":"v"}`), []Subject{SHA256Subject("pkg", hex.EncodeToString(sum[:]))})
			if err != nil {
				t.Fatal(err)
			}

			body, _, err := s.Encode(env, true)
			if err != nil {
				t.Fatal(err)
			}

			bundle := filepath.Join(dir, "att.sigstore.json")
			if err := os.WriteFile(bundle, body, 0o600); err != nil {
				t.Fatal(err)
			}

			args := []string{"verify-blob-attestation", "--bundle", bundle, "--key", pub, "--type", c.cosignType, "--insecure-ignore-tlog"}

			if out, err := exec.CommandContext(context.Background(), bin, append(args, blob)...).CombinedOutput(); err != nil {
				t.Errorf("%s/%s: cosign rejected the attestation: %v\n%s", name, c.cosignType, err, out)
			}

			tampered := filepath.Join(dir, "tampered.tgz")
			if err := os.WriteFile(tampered, []byte("other bytes"), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := exec.CommandContext(context.Background(), bin, append(args, tampered)...).CombinedOutput(); err == nil {
				t.Errorf("%s/%s: cosign accepted a modified blob", name, c.cosignType)
			}
		}
	}
}
