// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpmsign_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	rpmutils "github.com/M0Rf30/go-rpmutils"
	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/rpm"
	"github.com/openartifact/openartifact/pkg/rpmsign"
)

func fixture(t *testing.T) []byte {
	t.Helper()

	return fixtureNamed(t, "hello.rpm")
}

func fixtureNamed(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// formats lists one fixture per rpm package format with the signatures Sign
// must produce: format 4 gets header and header+payload signatures, format 6
// a single OPENPGP header signature.
func formats() []struct {
	fixture  string
	wantSigs int
} {
	return []struct {
		fixture  string
		wantSigs int
	}{
		{"hello.rpm", 2},
		{"hello-v6.rpm", 1},
	}
}

func newSigner(t *testing.T, alg gpg.Algorithm) (*gpg.LocalSigner, *openpgp.Entity) {
	t.Helper()

	entity, err := gpg.GenerateEntity("rpmsign test", "", "rpmsign@example.com", alg)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := gpg.NewLocalSignerFromEntity(entity)
	if err != nil {
		t.Fatal(err)
	}

	return signer, entity
}

func TestSignProducesAVerifiablePackage(t *testing.T) {
	for _, alg := range []gpg.Algorithm{gpg.AlgorithmED25519, gpg.AlgorithmRSA4096} {
		for _, f := range formats() {
			t.Run(string(alg)+"/"+f.fixture, func(t *testing.T) {
				signer, entity := newSigner(t, alg)
				orig := fixtureNamed(t, f.fixture)

				var signed bytes.Buffer
				if err := rpmsign.Sign(bytes.NewReader(orig), &signed, signer); err != nil {
					t.Fatalf("Sign: %v", err)
				}

				if sha256.Sum256(orig) == sha256.Sum256(signed.Bytes()) {
					t.Fatal("signed package is byte-identical to the input")
				}

				// The key verifies every signature of the format.
				hdr, sigs, err := rpmutils.Verify(bytes.NewReader(signed.Bytes()), openpgp.EntityList{entity})
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}

				if len(sigs) != f.wantSigs {
					t.Fatalf("signatures = %d, want %d", len(sigs), f.wantSigs)
				}

				if f.wantSigs == 1 && (hdr.Format() != 6 || !sigs[0].HeaderOnly) {
					t.Fatalf("format %d, header-only %v: want a v6 header signature", hdr.Format(), sigs[0].HeaderOnly)
				}

				// The package still parses and keeps its identity and metadata.
				before, err := rpm.ParseRPM(bytes.NewReader(orig))
				if err != nil {
					t.Fatal(err)
				}

				after, err := rpm.ParseRPM(bytes.NewReader(signed.Bytes()))
				if err != nil {
					t.Fatalf("ParseRPM(signed): %v", err)
				}

				if before.Name != after.Name || before.Version != after.Version || before.Arch != after.Arch {
					t.Errorf(
						"identity changed: %s-%s.%s -> %s-%s.%s",
						before.Name,
						before.Version,
						before.Arch,
						after.Name,
						after.Version,
						after.Arch,
					)
				}

				if after.HeaderStart == 0 || after.HeaderEnd <= after.HeaderStart {
					t.Errorf("header range = %d-%d", after.HeaderStart, after.HeaderEnd)
				}
			})
		}
	}
}

func TestSignedPackageIsRejectedByAnotherKey(t *testing.T) {
	signer, _ := newSigner(t, gpg.AlgorithmED25519)
	_, other := newSigner(t, gpg.AlgorithmED25519)

	var signed bytes.Buffer
	if err := rpmsign.Sign(bytes.NewReader(fixture(t)), &signed, signer); err != nil {
		t.Fatal(err)
	}

	if _, _, err := rpmutils.Verify(bytes.NewReader(signed.Bytes()), openpgp.EntityList{other}); err == nil {
		t.Error("package verified against a key that did not sign it")
	}
}

func TestSigningTwiceReplacesTheSignature(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.fixture, func(t *testing.T) {
			first, firstEntity := newSigner(t, gpg.AlgorithmED25519)
			second, secondEntity := newSigner(t, gpg.AlgorithmRSA4096)

			var once, twice bytes.Buffer
			if err := rpmsign.Sign(bytes.NewReader(fixtureNamed(t, f.fixture)), &once, first); err != nil {
				t.Fatal(err)
			}

			if err := rpmsign.Sign(bytes.NewReader(once.Bytes()), &twice, second); err != nil {
				t.Fatal(err)
			}

			_, sigs, err := rpmutils.Verify(bytes.NewReader(twice.Bytes()), openpgp.EntityList{secondEntity})
			if err != nil {
				t.Errorf("re-signed package does not verify with the new key: %v", err)
			}

			if len(sigs) != f.wantSigs {
				t.Errorf("signatures = %d, want %d (signatures accumulated?)", len(sigs), f.wantSigs)
			}

			if _, _, err := rpmutils.Verify(bytes.NewReader(twice.Bytes()), openpgp.EntityList{firstEntity}); err == nil {
				t.Error("old signature survived re-signing")
			}
		})
	}
}

func TestSignRejectsNonRPMAndForeignSigners(t *testing.T) {
	signer, _ := newSigner(t, gpg.AlgorithmED25519)

	var out bytes.Buffer
	if err := rpmsign.Sign(bytes.NewReader([]byte("not an rpm at all, just text")), &out, signer); err == nil {
		t.Error("signing garbage succeeded")
	}

	if err := rpmsign.Sign(bytes.NewReader(fixture(t)), &out, foreignSigner{}); !errors.Is(err, rpmsign.ErrKeyNotSupported) {
		t.Errorf("foreign signer: err = %v, want ErrKeyNotSupported", err)
	}
}

// TestSignedPackagePassesRpmCheck cross-checks with the real rpm tool when installed.
func TestSignedPackagePassesRpmCheck(t *testing.T) {
	rpmBin, err := exec.LookPath("rpm")
	if err != nil {
		t.Skip("rpm not installed")
	}

	for _, f := range formats() {
		t.Run(f.fixture, func(t *testing.T) {
			if f.wantSigs == 1 && rpmMajorVersion(t, rpmBin) < 6 {
				t.Skip("format-6 packages need rpm 6.0+")
			}

			checkWithRpm(t, rpmBin, fixtureNamed(t, f.fixture))
		})
	}
}

// rpmMajorVersion returns the major version of the rpm tool ("RPM version 6.0.1").
func rpmMajorVersion(t *testing.T, rpmBin string) int {
	t.Helper()

	out, err := exec.CommandContext(context.Background(), rpmBin, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}

	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatalf("rpm --version: %q", out)
	}

	major, _, _ := strings.Cut(fields[len(fields)-1], ".")

	n, err := strconv.Atoi(major)
	if err != nil {
		t.Fatalf("rpm --version: %q", out)
	}

	return n
}

// checkWithRpm signs data and requires `rpm -K` to accept the signature.
func checkWithRpm(t *testing.T, rpmBin string, data []byte) {
	t.Helper()

	signer, _ := newSigner(t, gpg.AlgorithmRSA4096)

	dir := t.TempDir()
	pkg := filepath.Join(dir, "p.rpm")
	pub := filepath.Join(dir, "pub.asc")

	var signed bytes.Buffer
	if err := rpmsign.Sign(bytes.NewReader(data), &signed, signer); err != nil {
		t.Fatal(err)
	}

	key, err := signer.PublicKey()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(pkg, signed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(pub, key, 0o600); err != nil {
		t.Fatal(err)
	}

	db := filepath.Join(dir, "db")

	for _, args := range [][]string{{"--dbpath", db, "--import", pub}, {"--dbpath", db, "-K", pkg}} {
		cmd := exec.CommandContext(context.Background(), rpmBin, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("rpm %v: %v\n%s", args, err, out)
		}

		if args[2] == "-K" && !bytes.Contains(out, []byte("signatures OK")) {
			t.Errorf("rpm -K: %s", out)
		}
	}
}

type foreignSigner struct{ gpg.Signer }
