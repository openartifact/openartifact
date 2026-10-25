// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// updateGolden (UPDATE_GOLDEN=1) rewrites testdata/golden instead of comparing.
func updateGolden() bool { return os.Getenv("UPDATE_GOLDEN") != "" }

// hostile is an artifact name stuffed with every markup an attacker could use
// to ping a channel or inject links in the chat formats.
const hostile = "<!channel> <@U123> @everyone *bold* _it_ ~s~ `tick` [x](https://evil.example) <https://evil.example|click> & |pipe|"

func uploadedDelivery() *models.WebhookDelivery {
	return &models.WebhookDelivery{
		ID:        "5b1f3c1e-0000-4000-8000-00000000d001",
		EventType: EventArtifactUploaded,
		Payload: map[string]any{
			"event":      EventArtifactUploaded,
			"timestamp":  "2026-01-02T15:04:05Z",
			"repository": "npm-hosted",
			"format":     "npm",
			"artifact": map[string]any{
				"name":    "left-pad",
				"path":    "left-pad/-/left-pad-1.3.0.tgz",
				"version": "1.3.0",
				"sha256":  strings.Repeat("ab", 32),
				"size":    float64(4096),
			},
		},
	}
}

func hostileDelivery() *models.WebhookDelivery {
	d := uploadedDelivery()
	art := d.Payload["artifact"].(map[string]any)
	art["name"] = hostile
	art["path"] = hostile + "/" + strings.Repeat("p", 3000)
	d.Payload["repository"] = hostile

	return d
}

func repoDeletedDelivery() *models.WebhookDelivery {
	return &models.WebhookDelivery{
		ID:        "5b1f3c1e-0000-4000-8000-00000000d002",
		EventType: EventRepoDeleted,
		Payload: map[string]any{
			"event":      EventRepoDeleted,
			"timestamp":  "2026-01-02T15:04:05Z",
			"repository": "docker-proxy",
			"format":     "docker",
			"type":       "proxy",
		},
	}
}

func testEventDelivery() *models.WebhookDelivery {
	return &models.WebhookDelivery{
		ID:        "5b1f3c1e-0000-4000-8000-00000000d003",
		EventType: EventWebhookTest,
		Payload: map[string]any{
			"event":     EventWebhookTest,
			"message":   "This is a test webhook delivery",
			"timestamp": float64(1767366245), // unix seconds, as handleTest sends it
		},
	}
}

func fixtures() map[string]*models.WebhookDelivery {
	return map[string]*models.WebhookDelivery{
		"uploaded":     uploadedDelivery(),
		"repo_deleted": repoDeletedDelivery(),
		"test":         testEventDelivery(),
		"hostile":      hostileDelivery(),
	}
}

func render(t *testing.T, format string, d *models.WebhookDelivery) ([]byte, string) {
	t.Helper()

	body, ct, err := renderBody(&models.Webhook{PayloadFormat: format}, d, "https://oa.example.com")
	if err != nil {
		t.Fatalf("renderBody(%s): %v", format, err)
	}

	if !json.Valid(body) {
		t.Fatalf("%s body is not valid JSON: %s", format, body)
	}

	return body, ct
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}

	return m
}

func loadSchema(t *testing.T, file string) *jsonschema.Schema {
	t.Helper()

	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}

	c := jsonschema.NewCompiler()
	c.AssertFormat()

	if err := c.AddResource(file, doc); err != nil {
		t.Fatal(err)
	}

	s, err := c.Compile(file)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func validate(t *testing.T, s *jsonschema.Schema, v any) error {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	return s.Validate(inst)
}

// ---- openartifact ----------------------------------------------------------

func TestOpenArtifactFormatIsTheStoredPayload(t *testing.T) {
	for name, d := range fixtures() {
		for _, format := range []string{"", models.PayloadFormatOpenArtifact} {
			body, ct := render(t, format, d)

			want, _ := json.Marshal(d.Payload)
			if !bytes.Equal(body, want) || ct != "application/json" {
				t.Errorf("%s/%q: body or content type changed:\n%s\n%s (%s)", name, format, body, want, ct)
			}
		}
	}
}

func TestRenderBodyRejectsUnknownFormat(t *testing.T) {
	if _, _, err := renderBody(&models.Webhook{PayloadFormat: "xml"}, uploadedDelivery(), ""); err == nil {
		t.Fatal("expected an error for an unknown payload format")
	}
}

func TestPayloadFormatsAreAccepted(t *testing.T) {
	for _, f := range PayloadFormats() {
		if !IsPayloadFormat(f) {
			t.Errorf("%s listed but not accepted", f)
		}
	}

	if IsPayloadFormat("") || IsPayloadFormat("xml") {
		t.Error("empty and unknown formats must be rejected")
	}
}

func TestArtifactDownloadedIsNotSubscribable(t *testing.T) {
	if IsSubscribableEvent("artifact.downloaded") {
		t.Fatal("artifact.downloaded is never emitted and must not be subscribable")
	}
}

// ---- CloudEvents -----------------------------------------------------------

func TestCloudEventsConformsToSpecSchema(t *testing.T) {
	schema := loadSchema(t, "cloudevents-schema-1.0.2.json")

	for name, d := range fixtures() {
		body, ct := render(t, models.PayloadFormatCloudEvents, d)
		if ct != "application/cloudevents+json" {
			t.Errorf("%s: content type = %q", name, ct)
		}

		ev := decode(t, body)
		if err := validate(t, schema, ev); err != nil {
			t.Errorf("%s: not a valid CloudEvents 1.0 JSON event: %v\n%s", name, err, body)
		}

		if ev["specversion"] != "1.0" || ev["id"] != d.ID || ev["datacontenttype"] != "application/json" {
			t.Errorf("%s: unexpected context attributes: %v", name, ev)
		}

		if ev["type"] != "io.openartifact."+d.EventType || ev["source"] != "https://oa.example.com" {
			t.Errorf("%s: type/source = %v / %v", name, ev["type"], ev["source"])
		}

		if _, err := time.Parse(time.RFC3339, ev["time"].(string)); err != nil {
			t.Errorf("%s: time is not RFC 3339: %v", name, err)
		}

		want, _ := json.Marshal(d.Payload)
		got, _ := json.Marshal(ev["data"])

		if !bytes.Equal(got, want) {
			t.Errorf("%s: data is not the native payload:\n%s\n%s", name, got, want)
		}
	}
}

func TestCloudEventsSchemaRejectsMalformedEvents(t *testing.T) {
	schema := loadSchema(t, "cloudevents-schema-1.0.2.json")
	good := cloudEvent(uploadedDelivery(), "https://oa.example.com")

	if err := validate(t, schema, good); err != nil {
		t.Fatalf("baseline event rejected: %v", err)
	}

	for _, attr := range []string{"id", "source", "specversion", "type"} {
		bad := map[string]any{}
		for k, v := range good {
			if k != attr {
				bad[k] = v
			}
		}

		if validate(t, schema, bad) == nil {
			t.Errorf("schema accepted an event without %s; the validator is not exercising the spec", attr)
		}
	}
}

func TestCloudEventsContextAttributes(t *testing.T) {
	t.Run("subject", func(t *testing.T) {
		if got := cloudEvent(uploadedDelivery(), "")["subject"]; got != "npm-hosted/left-pad/-/left-pad-1.3.0.tgz" {
			t.Errorf("artifact subject = %v", got)
		}

		if got := cloudEvent(repoDeletedDelivery(), "")["subject"]; got != "docker-proxy" {
			t.Errorf("repository subject = %v", got)
		}

		if _, ok := cloudEvent(testEventDelivery(), "")["subject"]; ok {
			t.Error("the test event has no subject")
		}
	})

	t.Run("source defaults when the server has no base URL", func(t *testing.T) {
		if got := cloudEvent(uploadedDelivery(), "")["source"]; got != "urn:openartifact" {
			t.Errorf("source = %v", got)
		}
	})

	t.Run("time", func(t *testing.T) {
		if got := cloudEvent(uploadedDelivery(), "")["time"]; got != "2026-01-02T15:04:05Z" {
			t.Errorf("artifact time = %v", got)
		}

		// webhook.test carries unix seconds: converted, not dropped.
		if got := cloudEvent(testEventDelivery(), "")["time"]; got != "2026-01-02T15:04:05Z" {
			t.Errorf("test event time = %v", got)
		}
	})

	t.Run("id is stable across retries", func(t *testing.T) {
		d := uploadedDelivery()
		if cloudEvent(d, "")["id"] != cloudEvent(d, "")["id"] {
			t.Error("id changed between attempts")
		}
	})

	t.Run("missing delivery id still yields a non-empty id", func(t *testing.T) {
		d := uploadedDelivery()
		d.ID = ""

		if id, _ := cloudEvent(d, "")["id"].(string); id == "" {
			t.Error("empty id")
		}
	})
}

func TestSetBaseURLTrimsTrailingSlash(t *testing.T) {
	d := NewDispatcher(nil, nil)
	d.SetBaseURL("https://oa.example.com/")

	if d.source != "https://oa.example.com" {
		t.Errorf("source = %q", d.source)
	}
}

// ---- Slack -----------------------------------------------------------------

// walkStrings calls fn for every string value in v.
func walkStrings(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case []any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	case map[string]any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	}
}

func stripSlackEntities(s string) string {
	return strings.NewReplacer("&amp;", "", "&lt;", "", "&gt;", "").Replace(s)
}

func TestSlackShape(t *testing.T) {
	for name, d := range fixtures() {
		body, ct := render(t, models.PayloadFormatSlack, d)
		msg := decode(t, body)

		if ct != "application/json" {
			t.Errorf("%s: content type %q", name, ct)
		}

		// Incoming webhooks: `text` is the notification fallback, `blocks` the layout.
		if text, _ := msg["text"].(string); text == "" || len(text) > 40000 {
			t.Errorf("%s: bad fallback text %q", name, text)
		}

		blocks, _ := msg["blocks"].([]any)
		if len(blocks) < 2 || len(blocks) > 50 {
			t.Fatalf("%s: %d blocks", name, len(blocks))
		}

		header := blocks[0].(map[string]any)
		htext := header["text"].(map[string]any)

		// header block: plain_text, at most 150 characters.
		if header["type"] != "header" || htext["type"] != "plain_text" || utf8.RuneCountInString(htext["text"].(string)) > 150 {
			t.Errorf("%s: bad header block %v", name, header)
		}

		for _, b := range blocks[1:] {
			sec := b.(map[string]any)
			fields, _ := sec["fields"].([]any)

			// section block: 1 to 10 fields of at most 2000 characters, mrkdwn or plain_text.
			if sec["type"] != "section" || len(fields) < 1 || len(fields) > 10 {
				t.Fatalf("%s: bad section %v", name, sec)
			}

			for _, f := range fields {
				fm := f.(map[string]any)
				txt := fm["text"].(string)

				if fm["type"] != "mrkdwn" || txt == "" || utf8.RuneCountInString(txt) > 2000 {
					t.Errorf("%s: bad field %v", name, fm)
				}
			}
		}

		// Slack treats a raw <, > or & as the start of a control sequence
		// (<!channel>, <@U123>, <url|label>): every one must be entity-escaped.
		walkStrings(msg, func(s string) {
			if strings.ContainsAny(stripSlackEntities(s), "<>&") {
				t.Errorf("%s: unescaped control character in %q", name, s)
			}
		})
	}
}

// ---- Discord ---------------------------------------------------------------

func TestDiscordShape(t *testing.T) {
	for name, d := range fixtures() {
		body, _ := render(t, models.PayloadFormatDiscord, d)
		msg := decode(t, body)

		// No mention may ever be parsed out of a value.
		am, _ := msg["allowed_mentions"].(map[string]any)
		if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
			t.Errorf("%s: allowed_mentions.parse must be an empty array, got %v", name, am)
		}

		embeds, _ := msg["embeds"].([]any)
		if len(embeds) != 1 {
			t.Fatalf("%s: %d embeds", name, len(embeds))
		}

		checkDiscordEmbed(t, name, embeds[0].(map[string]any))
	}
}

func checkDiscordEmbed(t *testing.T, name string, e map[string]any) {
	t.Helper()

	// Documented embed limits: title 256, 25 fields, name 256, value 1024, 6000 total.
	total := utf8.RuneCountInString(e["title"].(string))
	if total == 0 || total > 256 {
		t.Errorf("%s: title length %d", name, total)
	}

	if _, err := time.Parse(time.RFC3339, e["timestamp"].(string)); err != nil {
		t.Errorf("%s: embed timestamp: %v", name, err)
	}

	if c, ok := e["color"].(float64); !ok || c < 0 || c > 0xFFFFFF {
		t.Errorf("%s: color %v", name, e["color"])
	}

	fields, _ := e["fields"].([]any)
	if len(fields) > 25 {
		t.Errorf("%s: %d fields", name, len(fields))
	}

	for _, f := range fields {
		fm := f.(map[string]any)
		n, v := fm["name"].(string), fm["value"].(string)

		if n == "" || v == "" || utf8.RuneCountInString(n) > 256 || utf8.RuneCountInString(v) > 1024 {
			t.Errorf("%s: bad field %v", name, fm)
		}

		if _, ok := fm["inline"].(bool); !ok {
			t.Errorf("%s: inline must be a boolean: %v", name, fm)
		}

		// Values are code spans: markdown and mentions inside are literal.
		if !strings.HasPrefix(v, "`") || !strings.HasSuffix(v, "`") || strings.Contains(v[1:len(v)-1], "`") {
			t.Errorf("%s: value is not a safe code span: %q", name, v)
		}

		total += utf8.RuneCountInString(n) + utf8.RuneCountInString(v)
	}

	if total > 6000 {
		t.Errorf("%s: embed totals %d characters", name, total)
	}
}

// ---- Microsoft Teams -------------------------------------------------------

func TestTeamsAdaptiveCardConformsToSchema(t *testing.T) {
	schema := loadSchema(t, "adaptive-card-schema-1.5.json")

	for name, d := range fixtures() {
		body, _ := render(t, models.PayloadFormatTeams, d)
		msg := decode(t, body)

		// Workflows webhook envelope.
		atts, _ := msg["attachments"].([]any)
		if msg["type"] != "message" || len(atts) != 1 {
			t.Fatalf("%s: bad envelope %v", name, msg)
		}

		att := atts[0].(map[string]any)
		if att["contentType"] != "application/vnd.microsoft.card.adaptive" {
			t.Errorf("%s: contentType %v", name, att["contentType"])
		}

		card := att["content"].(map[string]any)
		if card["type"] != "AdaptiveCard" || card["$schema"] != "http://adaptivecards.io/schemas/adaptive-card.json" {
			t.Errorf("%s: bad card header %v", name, card)
		}

		if err := validate(t, schema, card); err != nil {
			t.Errorf("%s: card is not a valid Adaptive Card: %v\n%s", name, err, body)
		}
	}
}

func TestTeamsEscapesMarkdown(t *testing.T) {
	body, _ := render(t, models.PayloadFormatTeams, hostileDelivery())

	// The raw link and mention syntax must not survive as live markdown.
	for _, live := range []string{"[x](", "<!channel>", "<@U123>", "<https://evil.example|click>", "*bold*", "`tick`"} {
		if strings.Contains(string(body), live) {
			t.Errorf("unescaped %q in %s", live, body)
		}
	}

	if got := teamsEscape("a*b_c[d](e)<f>|g~h`i\\"); got != "a\\*b\\_c\\[d\\]\\(e\\)\\<f\\>\\|g\\~h\\`i\\\\" {
		t.Errorf("teamsEscape = %q", got)
	}
}

func TestChatFormatsTruncateLongValues(t *testing.T) {
	d := hostileDelivery()

	for _, format := range []string{models.PayloadFormatSlack, models.PayloadFormatDiscord, models.PayloadFormatTeams} {
		body, _ := render(t, format, d)
		if bytes.Contains(body, bytes.Repeat([]byte("p"), 1100)) {
			t.Errorf("%s: an over-long value was not truncated", format)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[int64]string{1: "1 B", 1023: "1023 B", 1024: "1.0 KiB", 4096: "4.0 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

// ---- golden fixtures -------------------------------------------------------

// TestGoldenBodies compares every format's body for the fixture events with
// the reviewed files in testdata/golden. Regenerate with UPDATE_GOLDEN=1 after an
// intentional change and review the diff.
func TestGoldenBodies(t *testing.T) {
	for _, format := range PayloadFormats() {
		for _, name := range []string{"uploaded", "repo_deleted", "test"} {
			body, _ := render(t, format, fixtures()[name])

			var pretty bytes.Buffer
			if err := json.Indent(&pretty, body, "", "  "); err != nil {
				t.Fatal(err)
			}

			pretty.WriteByte('\n')

			path := filepath.Join("testdata", "golden", format+"_"+name+".json")

			if updateGolden() {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(path, pretty.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}

				continue
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(pretty.Bytes(), want) {
				t.Errorf("%s differs from golden file:\n%s", path, pretty.String())
			}
		}
	}
}

// ---- delivery --------------------------------------------------------------

// TestDeliverUsesPayloadFormat sends one delivery per format through the real
// dispatcher and checks what the receiver observes: body, Content-Type and the
// HMAC signature over exactly those bytes.
func TestDeliverUsesPayloadFormat(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	schema := loadSchema(t, "cloudevents-schema-1.0.2.json")

	for _, format := range PayloadFormats() {
		t.Run(format, func(t *testing.T) {
			var (
				gotBody []byte
				gotCT   string
				gotSig  string
			)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				gotCT = r.Header.Get("Content-Type")
				gotSig = r.Header.Get("X-Hub-Signature-256")
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(srv.Close)

			db := &deliveryTestDB{BaseMockDB: &databasetest.BaseMockDB{}}
			d := NewDispatcher(db, log)
			d.SetBaseURL("https://oa.example.com")

			delivery := uploadedDelivery()
			wh := &models.Webhook{
				ID: "w", URL: srv.URL, SecretEnc: "s3cret", ContentType: "application/json",
				PayloadFormat: format, MaxRetries: 1, TimeoutSeconds: 5,
			}

			d.deliver(wh, delivery)

			if delivery.Status != "success" {
				t.Fatalf("status = %q", delivery.Status)
			}

			wantBody, wantCT := render(t, format, delivery)
			if !bytes.Equal(gotBody, wantBody) || gotCT != wantCT {
				t.Errorf("receiver got %q (%s), want %q (%s)", gotBody, gotCT, wantBody, wantCT)
			}

			if want := "sha256=" + computeHMAC(gotBody, []byte("s3cret")); gotSig != want {
				t.Errorf("signature = %q, want %q (over the format's body)", gotSig, want)
			}

			if format == models.PayloadFormatCloudEvents {
				if gotCT != "application/cloudevents+json" {
					t.Errorf("content type = %q", gotCT)
				}

				if err := validate(t, schema, decode(t, gotBody)); err != nil {
					t.Errorf("delivered body is not a CloudEvent: %v", err)
				}
			}
		})
	}
}

// TestUnknownPayloadFormatFailsPermanently: a delivery that can never be
// rendered must not be retried forever.
func TestUnknownPayloadFormatFailsPermanently(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &deliveryTestDB{BaseMockDB: &databasetest.BaseMockDB{}}
	delivery := uploadedDelivery()

	NewDispatcher(db, log).deliver(&models.Webhook{
		ID: "w", URL: "http://127.0.0.1:1/", PayloadFormat: "xml", MaxRetries: 3, TimeoutSeconds: 1,
	}, delivery)

	if delivery.Status != "failed" || delivery.NextRetryAt != nil {
		t.Fatalf("status = %q, next retry = %v", delivery.Status, delivery.NextRetryAt)
	}
}
