// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
)

// moduleDB answers the module-ownership queries of hosted ivy and sbt members:
// held lists "<member id>|<group>|<module>" triples; fail makes every query error.
type moduleDB struct {
	HandlerStore

	held []string
	fail bool
}

func (d *moduleDB) has(repoID, group, module string) (bool, error) {
	if d.fail {
		return false, errors.New("db down")
	}

	return slices.Contains(d.held, repoID+"|"+group+"|"+module), nil
}

func (d *moduleDB) IvyModuleExists(_ context.Context, repoID, org, module string) (bool, error) {
	return d.has(repoID, org, module)
}

func (d *moduleDB) SBTModuleExists(_ context.Context, repoID, group, module string) (bool, error) {
	return d.has(repoID, group, module)
}

func TestFileRepoRef(t *testing.T) {
	mavenLayout := []fileModule{{"com.example", "lib"}, {"com", "example"}}
	tests := []struct {
		name string
		path string
		want []fileModule
	}{
		{"ivy layout", "org.example/lib/1.0/jars/lib.jar", []fileModule{{"org.example", "lib"}}},
		{"maven layout", "com/example/lib/1.0/lib-1.0.jar", mavenLayout},
		{"maven layout checksum", "com/example/lib/1.0/lib-1.0.pom.sha1", mavenLayout},
		{"sbt plugin layout", "org/plug/scala_2.12/sbt_1.0/1.0/jars/plug.jar", []fileModule{{"org", "plug"}}},
		{"artifact metadata", "com/example/lib/maven-metadata.xml", mavenLayout},
		{"snapshot metadata checksum", "com/example/lib/1.0-SNAPSHOT/maven-metadata.xml.sha1", mavenLayout},
		{"short metadata", "org/lib/maven-metadata.xml", []fileModule{{"org", "lib"}}},
		{"too short", "org/lib.jar", nil},
		{"empty", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, ok := fileRepoRef(tt.path)
			if ok != (len(tt.want) > 0) {
				t.Fatalf("fileRepoRef(%q) ok = %v, want %v", tt.path, ok, len(tt.want) > 0)
			}

			if ok && !ref.file {
				t.Error("a request for a module's file is a file request")
			}

			for _, w := range tt.want {
				if !slices.Contains(ref.modules, w) {
					t.Errorf("fileRepoRef(%q).modules = %v, missing %v", tt.path, ref.modules, w)
				}
			}
		})
	}
}

// ivySbtFixture is a group of a hosted member and a proxy "mirror". The proxy
// holds lib 1.0 and lib 9.9.9 (the confusion attempt) and other 1.0; the hosted
// member holds the module org.example/lib at 1.0 only.
func ivySbtFixture(t *testing.T, format string) (*fixture, *moduleDB) {
	t.Helper()

	f := newFixture(t, format, member("hosted", format, "hosted"), member("mirror", format, "proxy"))
	f.grp.MergeProxyVersions = false

	db := &moduleDB{HandlerStore: f.h.db, held: []string{"id-hosted|org.example|lib"}}
	f.h.db = db

	f.set("hosted", "org.example/lib/1.0/jars/lib.jar", 200, "hosted-1.0")
	f.set("mirror", "org.example/lib/1.0/jars/lib.jar", 200, "mirror-1.0")
	f.set("mirror", "org.example/lib/9.9.9/jars/lib.jar", 200, "evil")
	f.set("mirror", "org.example/other/1.0/jars/other.jar", 200, "other")

	return f, db
}

func TestIvySbtGroup_HostedWins(t *testing.T) {
	for _, format := range []string{"ivy", "sbt"} {
		t.Run(format, func(t *testing.T) {
			f, _ := ivySbtFixture(t, format)

			if w := f.get(http.MethodGet, "org.example/lib/1.0/jars/lib.jar"); w.Code != 200 || w.Body.String() != "hosted-1.0" {
				t.Errorf("file both hold = %d %q, want the hosted one", w.Code, w.Body.String())
			}

			f.asked = nil

			if w := f.get(http.MethodGet, "org.example/lib/9.9.9/jars/lib.jar"); w.Code != http.StatusNotFound {
				t.Errorf("proxy-only version of an owned module = %d %q, want 404", w.Code, w.Body.String())
			}

			if slices.Contains(f.asked, "mirror") {
				t.Errorf("members asked = %v, the proxy must not be asked about an owned module", f.asked)
			}

			if w := f.get(http.MethodGet, "org.example/other/1.0/jars/other.jar"); w.Code != 200 || w.Body.String() != "other" {
				t.Errorf("module no hosted member holds = %d %q, want the proxy's", w.Code, w.Body.String())
			}
		})
	}
}

func TestIvySbtGroup_MavenLayoutAndMetadata(t *testing.T) {
	f := newFixture(t, "sbt", member("hosted", "sbt", "hosted"), member("mirror", "sbt", "proxy"))
	f.grp.MergeProxyVersions = false
	f.h.db = &moduleDB{HandlerStore: f.h.db, held: []string{"id-hosted|com.example|lib_2.12"}}

	f.set("mirror", "com/example/lib_2.12/9.9.9/lib_2.12-9.9.9.jar", 200, "evil")
	f.set("mirror", "com/example/lib_2.12/maven-metadata.xml", 200, "<metadata/>")
	f.set("mirror", "com/example/other_2.12/1.0/other_2.12-1.0.jar", 200, "other")

	for _, p := range []string{
		"com/example/lib_2.12/9.9.9/lib_2.12-9.9.9.jar",
		"com/example/lib_2.12/maven-metadata.xml",
	} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d %q, want 404: the hosted member owns the artifact", p, w.Code, w.Body.String())
		}
	}

	if w := f.get(http.MethodGet, "com/example/other_2.12/1.0/other_2.12-1.0.jar"); w.Code != 200 {
		t.Errorf("artifact only the proxy holds = %d, want 200", w.Code)
	}
}

func TestIvySbtGroup_FailingOwnershipCheckFailsClosed(t *testing.T) {
	f, db := ivySbtFixture(t, "ivy")
	db.fail = true

	if w := f.get(http.MethodGet, "org.example/lib/9.9.9/jars/lib.jar"); w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 when ownership cannot be told", w.Code)
	}
}

func TestIvySbtGroup_MergeProxyVersionsKeepsTheUnion(t *testing.T) {
	f, _ := ivySbtFixture(t, "ivy")
	f.grp.MergeProxyVersions = true

	if w := f.get(http.MethodGet, "org.example/lib/9.9.9/jars/lib.jar"); w.Code != 200 || w.Body.String() != "evil" {
		t.Errorf("status = %d %q, want the proxy's file when the group merges proxy versions", w.Code, w.Body.String())
	}
}

func TestIvySbtGroup_ProxyOnlyGroupIsFirstHitAndReadOnly(t *testing.T) {
	f := newFixture(t, "sbt", member("a", "sbt", "proxy"), member("b", "sbt", "proxy"))
	f.grp.MergeProxyVersions = false
	f.set("b", "org/lib/1.0/lib-1.0.jar", 200, "from-b")

	if w := f.get(http.MethodGet, "org/lib/1.0/lib-1.0.jar"); w.Code != 200 || w.Body.String() != "from-b" {
		t.Errorf("status = %d %q, want the second proxy's file", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodPut, "org/lib/1.0/lib-1.0.jar"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT status = %d, want 405", w.Code)
	}
}

// FuzzFileRepoRef feeds arbitrary request paths to the ivy/sbt module reader:
// a reference it returns is a file request naming at least one module, none of
// whose coordinates is empty.
func FuzzFileRepoRef(f *testing.F) {
	for _, p := range []string{
		"org.example/lib/1.0/jars/lib.jar", "com/example/lib/1.0/lib-1.0.jar", "com/example/lib/maven-metadata.xml",
		"com/example/lib/1.0-SNAPSHOT/maven-metadata.xml.sha1", "org/plug/scala_2.12/sbt_1.0/1.0/jars/plug.jar",
		"/org/lib/maven-metadata.xml", "maven-metadata.xml", "a/maven-metadata.xml", "//", "../../x/y/z", "",
	} {
		f.Add(p)
	}

	f.Fuzz(func(t *testing.T, relPath string) {
		ref, ok := fileRepoRef(relPath)
		if !ok {
			return
		}

		if !ref.file || len(ref.modules) == 0 {
			t.Fatalf("fileRepoRef(%q) = %+v, want a file request with modules", relPath, ref)
		}

		for _, m := range ref.modules {
			if m.group == "" || m.module == "" {
				t.Fatalf("fileRepoRef(%q) module %+v has an empty coordinate", relPath, m)
			}
		}
	})
}
