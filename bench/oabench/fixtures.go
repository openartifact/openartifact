// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // npm shasum
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
)

// randBytes returns n pseudo-random (incompressible) bytes.
func randBytes(n int) []byte {
	b := make([]byte, n)
	r := rand.NewChaCha8([32]byte{1, 2, 3})

	_, _ = r.Read(b)

	return b
}

func tarGz(files map[string][]byte) []byte {
	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, data := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
		_, _ = tw.Write(data)
	}

	_ = tw.Close()
	_ = gz.Close()

	return buf.Bytes()
}

// npmPublishBody builds the JSON document `npm publish` PUTs.
func npmPublishBody(name string, versions []string) ([]byte, error) {
	doc := map[string]any{
		"_id":       name,
		"name":      name,
		"dist-tags": map[string]string{"latest": versions[len(versions)-1]},
	}
	vers := map[string]any{}
	atts := map[string]any{}

	for _, v := range versions {
		pj, _ := json.Marshal(map[string]any{
			"name": name, "version": v, "description": "bench package " + name,
			"license": "MIT", "main": "index.js",
			"dependencies": map[string]string{"left-pad": "^1.0.0"},
		})
		tgz := tarGz(map[string][]byte{
			"package/package.json": pj,
			"package/index.js":     []byte("module.exports = 1\n"),
			"package/payload.bin":  randBytes(2048),
		})
		h512 := sha512.Sum512(tgz)
		h1 := sha1.Sum(tgz) //nolint:gosec // protocol
		file := fmt.Sprintf("%s-%s.tgz", baseName(name), v)

		vers[v] = map[string]any{
			"name": name, "version": v, "description": "bench package " + name,
			"license": "MIT", "main": "index.js",
			"dependencies": map[string]string{"left-pad": "^1.0.0"},
			"dist": map[string]string{
				"shasum":    hex.EncodeToString(h1[:]),
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(h512[:]),
				"tarball":   "http://localhost/" + name + "/-/" + file,
			},
		}
		atts[file] = map[string]any{
			"content_type": "application/octet-stream",
			"data":         base64.StdEncoding.EncodeToString(tgz),
			"length":       len(tgz),
		}
	}

	doc["versions"] = vers
	doc["_attachments"] = atts

	return json.Marshal(doc)
}

func baseName(n string) string {
	for i := len(n) - 1; i >= 0; i-- {
		if n[i] == '/' {
			return n[i+1:]
		}
	}

	return n
}

func arMember(buf *bytes.Buffer, name string, data []byte) {
	fmt.Fprintf(buf, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, 0, 0, 0, "100644", len(data))
	buf.Write(data)

	if len(data)%2 == 1 {
		buf.WriteByte('\n')
	}
}

// debPackage builds a minimal but valid .deb.
func debPackage(name, version, arch string) []byte {
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Bench <bench@example.com>\n"+
		"Installed-Size: 4\nDepends: libc6 (>= 2.31)\nSection: misc\nPriority: optional\nDescription: bench package %s\n long description line\n",
		name, version, arch, name)

	var buf bytes.Buffer

	buf.WriteString("!<arch>\n")
	arMember(&buf, "debian-binary", []byte("2.0\n"))
	arMember(&buf, "control.tar.gz", tarGz(map[string][]byte{"./control": []byte(control)}))
	arMember(&buf, "data.tar.gz", tarGz(map[string][]byte{"./usr/share/" + name + "/payload": randBytes(4096)}))

	return buf.Bytes()
}

func pomXML(group, artifact, version string) []byte {
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%s</groupId>
  <artifactId>%s</artifactId>
  <version>%s</version>
  <name>%s</name>
  <dependencies><dependency><groupId>org.slf4j</groupId><artifactId>slf4j-api</artifactId>
  <version>2.0.9</version></dependency></dependencies>
</project>
`, group, artifact, version, artifact)
}
