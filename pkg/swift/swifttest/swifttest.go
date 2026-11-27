// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package swifttest builds Swift package source archives for tests.
package swifttest

import (
	"archive/zip"
	"bytes"
	"maps"
	"slices"
	"strconv"
)

// TB is the part of testing.TB the builders use, which fuzz targets satisfy too.
type TB interface {
	Helper()
	Fatal(args ...any)
}

// Manifest is a minimal Package.swift declaring the given tools version.
func Manifest(toolsVersion, name string) string {
	q := strconv.Quote(name)

	return "// swift-tools-version:" + toolsVersion + "\nimport PackageDescription\n\n" +
		"let package = Package(name: " + q + ", products: [.library(name: " + q + ", targets: [" + q + "])], " +
		"targets: [.target(name: " + q + ")])\n"
}

// Archive builds a zip with the given files (path to content), written in path
// order.
func Archive(tb TB, files map[string]string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, name := range slices.Sorted(maps.Keys(files)) {
		w, err := zw.Create(name)
		if err != nil {
			tb.Fatal(err)
		}

		if _, err := w.Write([]byte(files[name])); err != nil {
			tb.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// Package builds the source archive of a library named name: Package.swift at
// the root (or under dir/ when dir is not empty), one source file and the
// extra files.
func Package(tb TB, dir, name string, extra map[string]string) []byte {
	tb.Helper()

	files := map[string]string{
		"Package.swift": Manifest("5.9", name),
		"Sources/" + name + "/" + name + ".swift": "public struct " + name + " {}\n",
	}

	maps.Copy(files, extra)

	if dir != "" {
		prefixed := make(map[string]string, len(files))
		for k, v := range files {
			prefixed[dir+"/"+k] = v
		}

		files = prefixed
	}

	return Archive(tb, files)
}
