// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package p2test builds p2 repository documents for tests.
package p2test

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/ulikunitz/xz"
)

// Artifacts is an artifacts.xml in the shape p2 writes it: repository
// properties with a mirror list and a statistics endpoint, one bundle with a
// checksum, and the usual download rule.
const Artifacts = `<?xml version='1.0' encoding='UTF-8'?>
<?artifactRepository version='1.1.0'?>
<repository name='Example' type='org.eclipse.equinox.p2.artifact.repository.simpleRepository' version='1'>
  <properties size='4'>
    <property name='p2.timestamp' value='1700000000000'/>
    <property name='p2.mirrorsURL' value='https://example.org/download.php?file=/rel&amp;format=xml'/>
    <property name='p2.statsURI' value='https://example.org/stats/rel'/>
    <property name='p2.compressed' value='true'/>
  </properties>
  <mappings size='1'>
    <rule filter='(&amp; (classifier=osgi.bundle))' output='${repoUrl}/plugins/${id}_${version}.jar'/>
  </mappings>
  <artifacts size='1'>
    <artifact classifier='osgi.bundle' id='org.example.core' version='1.2.3.v20240101'>
      <properties size='2'>
        <property name='artifact.size' value='10'/>
        <property name='p2.mirrorsURL' value='kept: this is an artifact property'/>
      </properties>
    </artifact>
  </artifacts>
</repository>
`

// Composite is a compositeArtifacts.xml with a relative child, a child that
// leaves the repository, an absolute child on the host of the test upstream
// and one on another host.
const Composite = `<?xml version='1.0' encoding='UTF-8'?>
<?compositeArtifactRepository version='1.0.0'?>
<repository name='Release' type='org.eclipse.equinox.internal.p2.artifact.repository.CompositeArtifactRepository' version='1.0.0'>
  <properties size='2'>
    <property name='p2.timestamp' value='1'/>
    <property name='p2.mirrorsURL' value='https://example.org/mirrors'/>
  </properties>
  <children size='4'>
    <child location='202412041000' />
    <child location='../../technology/epp/2024-12/'/>
    <child location="https://updates.example.org/releases/2024-12/202412041000"/>
    <child location='https://other.example.net/repo/'/>
  </children>
</repository>
`

// Jar packs name and body into a jar the way p2 does: one deflated entry.
func Jar(tb testing.TB, name, body string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create(name)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := w.Write([]byte(body)); err != nil {
		tb.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// XZ compresses body into one xz stream.
func XZ(tb testing.TB, body string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := xz.NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := w.Write([]byte(body)); err != nil {
		tb.Fatal(err)
	}

	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}
