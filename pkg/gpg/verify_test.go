// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpg

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// releaseFixture is a realistic Debian Release file (LF endings, dashed
// lines and a trailing newline) used to exercise every signature flavour.
const releaseFixture = "Origin: OpenArtifact\n" +
	"Label: Test\n" +
	"Suite: stable\n" +
	"Codename: bookworm\n" +
	"Date: Mon, 01 Jan 2024 00:00:00 UTC\n" +
	"Architectures: amd64 arm64\n" +
	"Components: main\n" +
	"- line starting with a dash\n" +
	"SHA256:\n" +
	" e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 0 main/binary-amd64/Packages\n"

// gpgEnv holds an isolated GNUPGHOME and the public key in both formats.
type gpgEnv struct {
	home    string
	pubASC  string // armored public key, for `gpg --import`
	pubKeyR string // binary keyring, for `gpgv --keyring`
}

func newGPGEnv(t *testing.T, s *LocalSigner) gpgEnv {
	t.Helper()

	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatalf("chmod GNUPGHOME: %v", err)
	}

	armored, err := s.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}

	env := gpgEnv{
		home:    home,
		pubASC:  filepath.Join(home, "pub.asc"),
		pubKeyR: filepath.Join(home, "pub.gpg"),
	}

	if err := os.WriteFile(env.pubASC, armored, 0o600); err != nil {
		t.Fatalf("write pub.asc: %v", err)
	}

	var bin bytes.Buffer
	if err := s.entity.Serialize(&bin); err != nil {
		t.Fatalf("serialize public key: %v", err)
	}

	if err := os.WriteFile(env.pubKeyR, bin.Bytes(), 0o600); err != nil {
		t.Fatalf("write pub.gpg: %v", err)
	}

	return env
}

// run executes bin with an isolated GNUPGHOME and returns combined output.
func (e gpgEnv) run(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+e.home, "LC_ALL=C")

	out, err := cmd.CombinedOutput()

	return string(out), err
}

func requireBin(t *testing.T, name string) string {
	t.Helper()

	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}

	return p
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()

	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}

	return p
}

// TestSignaturesVerifyWithRealClients signs with one freshly generated key
// (RSA-4096 generation is slow) and checks every signature flavour the server
// publishes with the binaries that real package clients use.
func TestSignaturesVerifyWithRealClients(t *testing.T) {
	entity, err := GenerateEntity("OpenArtifact Test", "", "test@example.com", AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity: %v", err)
	}

	s := &LocalSigner{entity: entity}

	t.Run("Release.gpg/gpgv", func(t *testing.T) { checkDetachedWithGPGV(t, s) })
	t.Run("InRelease/gpgv", func(t *testing.T) { checkCleartextWithGPGV(t, s) })
	t.Run("repomd.xml.asc/gpg", func(t *testing.T) { checkDetachedWithGPG(t, s) })
	t.Run("detached/openpgp", func(t *testing.T) { checkDetachedRoundTrip(t, s) })
}

// checkDetachedWithGPGV is the Release.gpg path: apt hands the armored
// detached signature to gpgv.
func checkDetachedWithGPGV(t *testing.T, s *LocalSigner) {
	t.Helper()

	gpgv := requireBin(t, "gpgv")
	env := newGPGEnv(t, s)
	dir := t.TempDir()

	sig, err := s.DetachedSign(strings.NewReader(releaseFixture))
	if err != nil {
		t.Fatalf("DetachedSign: %v", err)
	}

	release := writeFile(t, dir, "Release", []byte(releaseFixture))
	relGPG := writeFile(t, dir, "Release.gpg", sig)

	out, err := env.run(t, gpgv, "--keyring", env.pubKeyR, relGPG, release)
	if err != nil {
		t.Fatalf("gpgv rejected Release.gpg: %v\n%s", err, out)
	}

	if !strings.Contains(out, "Good signature") {
		t.Fatalf("gpgv did not report a good signature:\n%s", out)
	}

	// A tampered Release must be rejected, so the check above is meaningful.
	tampered := writeFile(t, dir, "Release.tampered", []byte(strings.Replace(releaseFixture, "stable", "evil", 1)))
	if out, err := env.run(t, gpgv, "--keyring", env.pubKeyR, relGPG, tampered); err == nil {
		t.Fatalf("gpgv accepted a tampered Release:\n%s", out)
	}
}

// checkCleartextWithGPGV is the InRelease path.
func checkCleartextWithGPGV(t *testing.T, s *LocalSigner) {
	t.Helper()

	gpgv := requireBin(t, "gpgv")
	env := newGPGEnv(t, s)
	dir := t.TempDir()

	signed, err := s.CleartextSign(strings.NewReader(releaseFixture))
	if err != nil {
		t.Fatalf("CleartextSign: %v", err)
	}

	data, err := io.ReadAll(signed)
	if err != nil {
		t.Fatalf("read InRelease: %v", err)
	}

	inRelease := writeFile(t, dir, "InRelease", data)

	out, err := env.run(t, gpgv, "--keyring", env.pubKeyR, inRelease)
	if err != nil {
		t.Fatalf("gpgv rejected InRelease: %v\n%s", err, out)
	}

	if !strings.Contains(out, "Good signature") {
		t.Fatalf("gpgv did not report a good signature:\n%s", out)
	}
}

// checkDetachedWithGPG is the repomd.xml.asc path: dnf's repo_gpgcheck hands
// the armored signature to gpg after importing the key.
func checkDetachedWithGPG(t *testing.T, s *LocalSigner) {
	t.Helper()

	gpg := requireBin(t, "gpg")
	env := newGPGEnv(t, s)
	dir := t.TempDir()

	repomd := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<repomd xmlns=\"http://linux.duke.edu/metadata/repo\"><revision>1</revision></repomd>\n")

	sig, err := s.DetachedSign(bytes.NewReader(repomd))
	if err != nil {
		t.Fatalf("DetachedSign: %v", err)
	}

	repomdPath := writeFile(t, dir, "repomd.xml", repomd)
	ascPath := writeFile(t, dir, "repomd.xml.asc", sig)

	if out, err := env.run(t, gpg, "--batch", "--import", env.pubASC); err != nil {
		t.Fatalf("gpg --import: %v\n%s", err, out)
	}

	out, err := env.run(t, gpg, "--batch", "--verify", ascPath, repomdPath)
	if err != nil {
		t.Fatalf("gpg rejected repomd.xml.asc: %v\n%s", err, out)
	}

	if !strings.Contains(out, "Good signature") {
		t.Fatalf("gpg did not report a good signature:\n%s", out)
	}
}

// checkDetachedRoundTrip verifies the signature in-process and asserts it is
// a single armor layer (the original defect double-armored it).
func checkDetachedRoundTrip(t *testing.T, s *LocalSigner) {
	t.Helper()

	sig, err := s.DetachedSign(strings.NewReader(releaseFixture))
	if err != nil {
		t.Fatalf("DetachedSign: %v", err)
	}

	if n := bytes.Count(sig, []byte("-----BEGIN PGP SIGNATURE-----")); n != 1 {
		t.Fatalf("want exactly one armor header, got %d:\n%s", n, sig)
	}

	if bytes.Contains(sig, []byte("LS0tLS1CRUdJTi")) { // base64("-----BEGIN")
		t.Fatalf("signature body is itself an armor block (double armor):\n%s", sig)
	}

	_, err = openpgp.CheckArmoredDetachedSignature(openpgp.EntityList{s.entity}, strings.NewReader(releaseFixture), bytes.NewReader(sig), nil)
	if err != nil {
		t.Fatalf("CheckArmoredDetachedSignature: %v", err)
	}

	_, err = openpgp.CheckArmoredDetachedSignature(openpgp.EntityList{s.entity}, strings.NewReader(releaseFixture+"x"), bytes.NewReader(sig), nil)
	if err == nil {
		t.Fatal("signature verified against modified data")
	}
}
