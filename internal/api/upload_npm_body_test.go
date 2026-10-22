// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

func npmBody(name, version, data string, length int, order string) string {
	att := fmt.Sprintf(`"content_type":"application/octet-stream","data":"%s","length":%d`, data, length)
	if order == "length-first" {
		att = fmt.Sprintf(`"length":%d,"content_type":"application/octet-stream","data":"%s"`, length, data)
	}

	return fmt.Sprintf(`{"_id":"%[1]s","name":"%[1]s","dist-tags":{"latest":"%[2]s"},`+
		`"versions":{"%[2]s":{"name":"%[1]s","version":"%[2]s","nested":{"a":[1,2,"}"]}}},`+
		`"_attachments":{"%[1]s-%[2]s.tgz":{%[3]s}}}`, name, version, att)
}

func TestParseNPMPublishBody(t *testing.T) {
	payload := bytes.Repeat([]byte("tarball-bytes\x00\xff"), 5000)
	b64 := base64.StdEncoding.EncodeToString(payload)

	for _, order := range []string{"data-middle", "length-first"} {
		pub, cleanup, err := parseNPMPublishBody(strings.NewReader(npmBody("@s/pkg", "1.0.0", b64, len(payload), order)))
		if err != nil {
			t.Fatalf("%s: %v", order, err)
		}

		if pub.Name != "@s/pkg" || pub.DistTags["latest"] != "1.0.0" || len(pub.Versions) != 1 {
			t.Fatalf("%s: small fields wrong: %+v", order, pub)
		}

		att, ok := pub.attachment("1.0.0")
		if !ok {
			t.Fatalf("%s: attachment not found", order)
		}

		if err := att.validate(); err != nil {
			t.Fatalf("%s: validate: %v", order, err)
		}

		got, err := os.ReadFile(att.path)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s: spooled bytes differ (err=%v)", order, err)
		}

		path := att.path

		cleanup()

		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: spool file not removed", order)
		}
	}
}

func TestParseNPMPublishBodyEscapedSlash(t *testing.T) {
	payload := []byte{0xfb, 0xff, 0xbf, 0xfb, 0xef} // base64 contains '+' and '/'
	b64 := strings.ReplaceAll(base64.StdEncoding.EncodeToString(payload), "/", `\/`)

	pub, cleanup, err := parseNPMPublishBody(strings.NewReader(npmBody("p", "1.0.0", b64, len(payload), "")))
	if err != nil {
		t.Fatal(err)
	}

	defer cleanup()

	att, _ := pub.attachment("1.0.0")
	if got, _ := os.ReadFile(att.path); !bytes.Equal(got, payload) {
		t.Errorf("escaped base64 decoded to %x, want %x", got, payload)
	}
}

func TestNPMAttachmentValidateFailures(t *testing.T) {
	for name, tc := range map[string]struct{ data string }{
		"bad base64": {"!!!!"},
	} {
		pub, cleanup, err := parseNPMPublishBody(strings.NewReader(npmBody("p", "1.0.0", tc.data, 3, "")))
		if err != nil {
			t.Fatalf("%s: parse should defer data errors to validate, got %v", name, err)
		}

		att, _ := pub.attachment("1.0.0")
		if err := att.validate(); !errors.Is(err, pkgnpm.ErrInvalidAttachment) {
			t.Errorf("%s: validate = %v, want ErrInvalidAttachment", name, err)
		}

		cleanup()
	}

	pub, cleanup, err := parseNPMPublishBody(strings.NewReader(npmBody("p", "1.0.0", "aGVsbG8=", 99, "")))
	if err != nil {
		t.Fatal(err)
	}

	defer cleanup()

	att, _ := pub.attachment("1.0.0")
	if err := att.validate(); !errors.Is(err, pkgnpm.ErrInvalidAttachment) {
		t.Errorf("length mismatch: validate = %v", err)
	}
}

func TestParseNPMPublishBodyMalformed(t *testing.T) {
	good := npmBody("p", "1.0.0", "aGVsbG8=", 5, "")

	for name, body := range map[string]string{
		"truncated":      good[:len(good)-20],
		"not object":     `[1,2]`,
		"empty":          ``,
		"bad versions":   `{"name":"p","versions":5}`,
		"trailing comma": `{"name":"p",}`,
	} {
		_, cleanup, err := parseNPMPublishBody(strings.NewReader(body))
		if err == nil {
			cleanup()
			t.Errorf("%s: expected error", name)

			continue
		}

		if !errors.Is(err, errNPMBodySyntax) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%s: error %v not mapped to a syntax error", name, err)
		}
	}
}

func TestParseNPMPublishBodyNullAttachments(t *testing.T) {
	pub, cleanup, err := parseNPMPublishBody(strings.NewReader(`{"name":"p","versions":{},"_attachments":null}`))
	if err != nil {
		t.Fatal(err)
	}

	defer cleanup()

	if len(pub.Attachments) != 0 {
		t.Errorf("attachments = %d, want 0", len(pub.Attachments))
	}
}

// BenchmarkNPMPublishBody measures parsing a publish body carrying a 50 MiB
// tarball.
func BenchmarkNPMPublishBody(b *testing.B) {
	payload := make([]byte, 50<<20)
	for i := range payload {
		payload[i] = byte(i * 7)
	}

	body := npmBody("pkg", "1.0.0", base64.StdEncoding.EncodeToString(payload), len(payload), "")

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))

	for b.Loop() {
		pub, cleanup, err := parseNPMPublishBody(strings.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}

		att, _ := pub.attachment("1.0.0")
		if _, _, _, err := att.integrity(); err != nil {
			b.Fatal(err)
		}

		cleanup()
	}
}
