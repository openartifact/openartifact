// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apktest builds .apk packages and checks signed indexes for tests,
// on top of the apk package: real files in the layout abuild writes.
package apktest

import (
	"bytes"
	"crypto/rsa"
	"io/fs"
	"path"
	"slices"
	"testing"
	"time"

	"github.com/M0Rf30/pkgformat/apk"
)

const (
	defaultVersion     = "1.0-r0"
	defaultArch        = "x86_64"
	defaultDescription = "test package"
	defaultLicense     = "MIT"
)

// Options describes the package [Build] makes. Only Name is required.
type Options struct {
	// Name is the package name (pkgname).
	Name string
	// Version defaults to "1.0-r0".
	Version string
	// Arch defaults to "x86_64".
	Arch string
	// Description defaults to "test package".
	Description string
	// License defaults to "MIT".
	License string
	// Origin defaults to Name.
	Origin string
	// URL is left out of the package when empty.
	URL string
	// Depends and Provides become one depend and provides line per entry.
	Depends, Provides []string
	// Files maps an install path (relative, no leading slash) to its content.
	// The default is one file, usr/share/<Name>/README. The parent directories
	// are added to the package.
	Files map[string]string
	// Sign, when set, puts a signature segment in front of the control
	// segment.
	Sign *apk.Signer
}

// Build returns the bytes of a .apk for o. The output is a function of o
// alone. It fails the test when the options do not make a package that
// [apk.ParsePackage] accepts.
func Build(tb testing.TB, o Options) []byte {
	tb.Helper()

	if o.Name == "" {
		tb.Fatal("apktest: Options.Name is required")
	}

	files := o.Files
	if files == nil {
		version := o.Version
		if version == "" {
			version = defaultVersion
		}

		files = map[string]string{"usr/share/" + o.Name + "/README": o.Name + " " + version + "\n"}
	}

	spec := &apk.Spec{
		Info: apk.Info{
			Name:        o.Name,
			Version:     or(o.Version, defaultVersion),
			Arch:        or(o.Arch, defaultArch),
			Description: or(o.Description, defaultDescription),
			License:     or(o.License, defaultLicense),
			Origin:      or(o.Origin, o.Name),
			URL:         o.URL,
			Packager:    "apktest",
			BuildDate:   time.Unix(1700000000, 0),
			Depends:     o.Depends,
			Provides:    o.Provides,
		},
		Files: dataFiles(files),
	}

	var buf bytes.Buffer
	if _, err := apk.WritePackage(&buf, spec, apk.WriteOptions{Signer: o.Sign, TempDir: tb.TempDir()}); err != nil {
		tb.Fatalf("apktest: writing the package: %v", err)
	}

	out := buf.Bytes()
	if _, err := apk.ParsePackage(bytes.NewReader(out), int64(len(out))); err != nil {
		tb.Fatalf("apktest: the package is not valid: %v", err)
	}

	return out
}

// VerifyIndex checks that data is an APKINDEX.tar.gz signed with SHA-256 by the
// key trusted under name.
func VerifyIndex(data []byte, name string, key *rsa.PublicKey) error {
	kr := apk.NewKeyring()
	if err := kr.Add(name, key); err != nil {
		return err
	}

	_, err := apk.VerifyIndex(data, apk.VerifyOptions{Keys: kr, Algorithms: []apk.SigAlgorithm{apk.SigRSASHA256}})

	return err
}

func or(s, def string) string {
	if s == "" {
		return def
	}

	return s
}

// dataFiles lists the parent directories first, as abuild does, then the files.
func dataFiles(files map[string]string) []apk.File {
	dirs := map[string]bool{}
	names := make([]string, 0, len(files))

	for name := range files {
		names = append(names, name)
	}

	slices.Sort(names)

	for _, name := range names {
		for dir := path.Dir(name); dir != "." && dir != "/" && !dirs[dir]; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}

	dirNames := make([]string, 0, len(dirs))
	for dir := range dirs {
		dirNames = append(dirNames, dir)
	}

	slices.Sort(dirNames)

	out := make([]apk.File, 0, len(dirNames)+len(names))
	for _, dir := range dirNames {
		out = append(out, apk.File{Name: dir + "/", Mode: fs.ModeDir | 0o755})
	}

	for _, name := range names {
		out = append(out, apk.MemFile(name, 0o644, []byte(files[name])))
	}

	return out
}
