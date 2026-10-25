// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import "testing"

func TestChartPathSegments(t *testing.T) {
	for name, want := range map[string]bool{
		"nginx/Chart.yaml": true, "./nginx/Chart.yaml": true, "..x/Chart.yaml": true,
		"../Chart.yaml": false, "./Chart.yaml": false, "/Chart.yaml": false, "nginx/sub/Chart.yaml": false, "a\\b/Chart.yaml": false,
	} {
		if got := isRootChartYAML(name); got != want {
			t.Errorf("isRootChartYAML(%q) = %v, want %v", name, got, want)
		}
	}

	for p, want := range map[string]bool{
		"/charts/nginx-1.0.tgz": true, "nginx-1.0.tgz": true,
		"/charts/": false, "/charts/..": false, "/charts/.": false, "/charts/a%5Cb": true,
	} {
		_, got := fileOfPath(p)
		if got != want {
			t.Errorf("fileOfPath(%q) ok = %v, want %v", p, got, want)
		}
	}
}
