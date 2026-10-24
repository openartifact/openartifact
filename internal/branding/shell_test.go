// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

const testIndex = `<!DOCTYPE html><html><head>
<link rel="icon" type="image/svg+xml" href="/favicon.svg" />
<link rel="icon" type="image/png" sizes="32x32" href="/favicon-32.png" />
<link rel="apple-touch-icon" href="/icons/apple-touch-icon.png" />
<meta name="apple-mobile-web-app-title" content="OpenArtifact" />
<title>OpenArtifact</title>
</head><body><div id="app"></div></body></html>`

func snapshotWith(t *testing.T, u Update, imgs map[Kind]*Image) *Snapshot {
	t.Helper()

	m := NewManager(newMemStore(), Defaults{Images: imgs}, nil)

	snap, err := m.Update(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}

	return snap
}

func TestPatchIndex_Default(t *testing.T) {
	snap := NewManager(nil, Defaults{}, nil).Snapshot(context.Background())
	out := string(PatchIndex([]byte(testIndex), snap))

	for _, want := range []string{
		"<title>OpenArtifact</title>", `content="OpenArtifact"`,
		`href="/favicon.svg"`, `href="/favicon-32.png"`, `href="/icons/apple-touch-icon.png"`,
		`<script type="application/json" id="oa-branding">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	if strings.Contains(out, "oa-brand-style") {
		t.Error("no custom colour: no style override expected")
	}

	if i, j := strings.Index(out, "oa-branding"), strings.Index(out, "</head>"); i < 0 || i > j {
		t.Error("payload must sit inside <head>")
	}
}

func TestPatchIndex_Hostile(t *testing.T) {
	name := `</title><script>alert(1)</script>"><img src=x onerror=alert(1)>`
	snap := snapshotWith(t, Update{
		ProductName:  str(name),
		LoginMessage: str(`</script><script>alert(2)</script><!-- <script>`),
		PrimaryColor: str("#0f766e"),
	}, nil)

	out := string(PatchIndex([]byte(testIndex), snap))

	if strings.Contains(out, "<script>alert") || strings.Contains(out, "<img") || strings.Contains(out, "</title><script") {
		t.Fatalf("hostile input broke out of its context:\n%s", out)
	}

	// The payload still decodes to exactly the stored values.
	m := regexp.MustCompile(`(?s)<script type="application/json" id="oa-branding">(.*?)</script>`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("payload missing")
	}

	var pub Public
	if err := json.Unmarshal([]byte(m[1]), &pub); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}

	if pub.ProductName != name || pub.Theme == nil {
		t.Errorf("payload = %+v", pub)
	}

	if !strings.Contains(out, `<style id="oa-brand-style">:root{--color-primary:`) || !strings.Contains(out, ".dark{--color-primary:") {
		t.Errorf("theme variables missing:\n%s", out)
	}
}

func TestPatchIndex_Favicon(t *testing.T) {
	pngImg, err := NewImage("", testPNG(t, 64, 64))
	if err != nil {
		t.Fatal(err)
	}

	svgImg, err := NewImage("", []byte(cleanSVG))
	if err != nil {
		t.Fatal(err)
	}

	pngOut := string(PatchIndex([]byte(testIndex), snapshotWith(t, Update{}, map[Kind]*Image{Favicon: pngImg})))
	if n := strings.Count(pngOut, `rel="icon"`); n != 1 {
		t.Errorf("%d icon links, want exactly the custom one:\n%s", n, pngOut)
	}

	if !strings.Contains(pngOut, `type="image/png" sizes="64x64" href="/api/v1/branding/favicon?v=`) ||
		!strings.Contains(pngOut, `rel="apple-touch-icon" href="/api/v1/branding/favicon?v=`) ||
		strings.Contains(pngOut, "/favicon.svg") || strings.Contains(pngOut, "/icons/apple-touch-icon.png") {
		t.Errorf("png favicon not applied:\n%s", pngOut)
	}

	svgOut := string(PatchIndex([]byte(testIndex), snapshotWith(t, Update{}, map[Kind]*Image{Favicon: svgImg})))
	if !strings.Contains(svgOut, `type="image/svg+xml" href="/api/v1/branding/favicon?v=`) || strings.Contains(svgOut, "apple-touch-icon") {
		t.Errorf("svg favicon: apple-touch-icon must be dropped, not left on the default brand:\n%s", svgOut)
	}
}

func TestManifest(t *testing.T) {
	var got struct {
		Name      string         `json:"name"`
		ShortName string         `json:"short_name"`
		StartURL  string         `json:"start_url"`
		Icons     []manifestIcon `json:"icons"`
	}

	snap := NewManager(nil, Defaults{}, nil).Snapshot(context.Background())
	if err := json.Unmarshal(Manifest(snap), &got); err != nil {
		t.Fatal(err)
	}

	if got.Name != "OpenArtifact" || got.ShortName != "OpenArtifact" || got.StartURL != "/" || len(got.Icons) != 3 ||
		got.Icons[0].Src != "/icons/icon-192.png" || got.Icons[2].Purpose != "maskable" {
		t.Errorf("default manifest = %+v", got)
	}

	fav, _ := NewImage("", testPNG(t, 512, 512))

	custom := snapshotWith(t, Update{ProductName: str("Acme Artifact Hub Internal")}, map[Kind]*Image{Favicon: fav})
	if err := json.Unmarshal(Manifest(custom), &got); err != nil {
		t.Fatal(err)
	}

	if got.Name != "Acme Artifact Hub Internal" || len([]rune(got.ShortName)) > maxShortNameRunes ||
		len(got.Icons) != 1 || got.Icons[0].Sizes != "512x512" || got.Icons[0].Type != ContentTypePNG ||
		!strings.HasPrefix(got.Icons[0].Src, "/api/v1/branding/favicon?v=") {
		t.Errorf("custom manifest = %+v", got)
	}
}
