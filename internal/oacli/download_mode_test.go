// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// download runs downloadTo on a complete response carrying body.
func download(body, dest string) (int64, error) {
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: int64(len(body)),
		Body:          io.NopCloser(strings.NewReader(body)),
	}
	defer func() { _ = resp.Body.Close() }()

	return downloadTo(resp, dest)
}

func TestDownloadToModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}

	t.Run("new file is owner-only", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "out")
		if _, err := download("x", dest); err != nil {
			t.Fatal(err)
		}

		if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	})

	t.Run("existing mode is kept", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "out")
		if err := os.WriteFile(dest, []byte("old"), 0o640); err != nil {
			t.Fatal(err)
		}

		if err := os.Chmod(dest, 0o640); err != nil {
			t.Fatal(err)
		}

		if _, err := download("new", dest); err != nil {
			t.Fatal(err)
		}

		if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v, want 0640", fi.Mode().Perm())
		}
	})

	t.Run("non-regular destination is written in place", func(t *testing.T) {
		n, err := download("abc", os.DevNull)
		if err != nil || n != 3 {
			t.Fatalf("n=%d err=%v", n, err)
		}

		if fi, err := os.Stat(os.DevNull); err != nil || fi.Mode().IsRegular() {
			t.Fatalf("/dev/null was replaced: %v %v", fi, err)
		}
	})
}

func TestSameServer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"http://localhost:8080", "http://localhost:8080/", true},
		{"HTTPS://Example.com", "https://example.com:443", true},
		{"https://a.example.com", "https://b.example.com", false},
		{"http://x.com", "https://x.com", false},
	}

	for _, tt := range tests {
		if got := sameServer(tt.a, tt.b); got != tt.want {
			t.Errorf("sameServer(%q,%q) = %v", tt.a, tt.b, got)
		}
	}
}

func TestSavedTokenOnlyForSavedServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OA_SERVER", "")
	t.Setenv("OA_TOKEN", "")

	if err := saveConfig(&Config{Server: "https://saved.example.com", Token: "secret"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		args      []string
		wantToken string
	}{
		{[]string{"--server", "https://other.example.com"}, ""},
		{[]string{"--server", "https://saved.example.com/"}, "secret"},
		{[]string{"--server", "https://other.example.com", "--token", "explicit"}, "explicit"},
	}

	for _, tt := range tests {
		a := &app{}
		root := &cobra.Command{Use: "oa"}
		pf := root.PersistentFlags()
		pf.StringVar(&a.server, "server", "", "")
		pf.StringVar(&a.token, "token", "", "")
		pf.StringVar(&a.format, "format", formatTable, "")

		child := &cobra.Command{Use: "x"}
		root.AddCommand(child)

		if err := pf.Parse(tt.args); err != nil {
			t.Fatal(err)
		}

		if err := a.preRun(child, nil); err != nil {
			t.Fatal(err)
		}

		if a.token != tt.wantToken {
			t.Errorf("args %v: token = %q, want %q", tt.args, a.token, tt.wantToken)
		}
	}
}

func TestAuthLoginRejectsBlankToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	for _, stdin := range []string{"", "\n", "   \n"} {
		res := runOA(t, srv, stdin, "auth", "login")
		if res.code == 0 || strings.Contains(res.stdout, "Credentials saved") {
			t.Errorf("stdin %q: blank token accepted: %+v", stdin, res)
		}
	}
}
