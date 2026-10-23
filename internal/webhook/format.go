// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	// contentTypeJSON is the body type of every format except CloudEvents.
	contentTypeJSON = "application/json"
	// contentTypeCloudEvents is the CloudEvents 1.0 structured-mode JSON type.
	contentTypeCloudEvents = "application/cloudevents+json"

	// cloudEventsSpecVersion is the CloudEvents specification version emitted.
	cloudEventsSpecVersion = "1.0"
	// cloudEventsTypePrefix namespaces event names into CloudEvents types
	// (reverse-DNS, as the spec recommends): io.openartifact.artifact.uploaded.
	cloudEventsTypePrefix = "io.openartifact."
	// defaultEventSource is the CloudEvents source (and the Discord username
	// origin) when the server has no base URL configured.
	defaultEventSource = "urn:openartifact"

	// chatValueLimit caps one rendered value in the chat formats. The strictest
	// service limit is Discord's 1024 characters per embed field value; the
	// code-span quoting adds two.
	chatValueLimit = 1000
)

// PayloadFormats returns the accepted payload_format values.
func PayloadFormats() []string {
	return []string{
		models.PayloadFormatOpenArtifact,
		models.PayloadFormatCloudEvents,
		models.PayloadFormatSlack,
		models.PayloadFormatTeams,
		models.PayloadFormatDiscord,
	}
}

// IsPayloadFormat reports whether name is an accepted payload_format.
func IsPayloadFormat(name string) bool {
	switch name {
	case models.PayloadFormatOpenArtifact, models.PayloadFormatCloudEvents,
		models.PayloadFormatSlack, models.PayloadFormatTeams, models.PayloadFormatDiscord:
		return true
	default:
		return false
	}
}

var errUnknownPayloadFormat = errors.New("unknown payload format")

// renderBody encodes the delivery for the webhook's payload format and returns
// the request body with its Content-Type. The stored payload is always the
// native OpenArtifact one; the format is applied at every attempt, so changing
// a webhook's format also applies to its pending retries. source is the
// CloudEvents source (the server's base URL).
func renderBody(wh *models.Webhook, delivery *models.WebhookDelivery, source string) ([]byte, string, error) {
	switch wh.PayloadFormat {
	case "", models.PayloadFormatOpenArtifact:
		body, err := json.Marshal(delivery.Payload)
		return body, contentTypeJSON, wrapRender(err)
	case models.PayloadFormatCloudEvents:
		body, err := marshalNoHTMLEscape(cloudEvent(delivery, source))
		return body, contentTypeCloudEvents, wrapRender(err)
	case models.PayloadFormatSlack:
		body, err := marshalNoHTMLEscape(slackMessage(summarize(delivery)))
		return body, contentTypeJSON, wrapRender(err)
	case models.PayloadFormatTeams:
		body, err := marshalNoHTMLEscape(teamsMessage(summarize(delivery)))
		return body, contentTypeJSON, wrapRender(err)
	case models.PayloadFormatDiscord:
		body, err := marshalNoHTMLEscape(discordMessage(summarize(delivery)))
		return body, contentTypeJSON, wrapRender(err)
	default:
		return nil, "", fmt.Errorf("%w %q", errUnknownPayloadFormat, wh.PayloadFormat)
	}
}

func wrapRender(err error) error {
	if err != nil {
		return fmt.Errorf("render webhook body: %w", err)
	}

	return nil
}

// marshalNoHTMLEscape is json.Marshal without the \u003c-style escaping of
// <, > and &, which the chat services would otherwise receive for every angle
// bracket (valid JSON, but needlessly opaque in delivery logs).
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, err //nolint:wrapcheck // wrapped by renderBody
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ---- event view ------------------------------------------------------------

// fact is one labelled value shown by the chat formats.
type fact struct {
	name  string
	value string
}

// eventSummary is the format-neutral human view of one delivery.
type eventSummary struct {
	event    string
	title    string
	headline string // one-line plain text (notification fallback)
	facts    []fact
	at       time.Time
	severity severity
}

type severity int

const (
	severityInfo severity = iota
	severityGood
	severityBad
)

func eventTitle(event string) string {
	switch event {
	case EventArtifactUploaded:
		return "Artifact uploaded"
	case EventArtifactDeleted:
		return "Artifact deleted"
	case EventArtifactRestored:
		return "Artifact restored"
	case EventArtifactPurged:
		return "Artifact purged"
	case EventRepoCreated:
		return "Repository created"
	case EventRepoDeleted:
		return "Repository deleted"
	case EventRepoRestored:
		return "Repository restored"
	case EventRepoPurged:
		return "Repository purged"
	case EventBundlePromoted:
		return "Bundle promoted"
	case EventWebhookTest:
		return "Webhook test"
	default:
		return event
	}
}

func eventSeverity(event string) severity {
	switch event {
	case EventArtifactUploaded, EventArtifactRestored, EventRepoCreated, EventRepoRestored, EventBundlePromoted:
		return severityGood
	case EventArtifactDeleted, EventArtifactPurged, EventRepoDeleted, EventRepoPurged:
		return severityBad
	default:
		return severityInfo
	}
}

func payloadString(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func payloadInt(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	default:
		return 0
	}
}

// eventTime reads the payload's "timestamp": an RFC 3339 string for artifact
// and repository events, unix seconds for webhook.test. It falls back to the
// delivery's creation time, then to now.
func eventTime(d *models.WebhookDelivery) time.Time {
	switch ts := d.Payload["timestamp"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			return t.UTC()
		}
	case float64:
		return time.Unix(int64(ts), 0).UTC()
	case int64:
		return time.Unix(ts, 0).UTC()
	case int:
		return time.Unix(int64(ts), 0).UTC()
	}

	if !d.CreatedAt.IsZero() {
		return d.CreatedAt.UTC()
	}

	return time.Now().UTC()
}

func addFact(facts []fact, name, value string) []fact {
	if value == "" {
		return facts
	}

	return append(facts, fact{name: name, value: value})
}

// summarize builds the chat view of a delivery from its native payload.
func summarize(d *models.WebhookDelivery) eventSummary {
	p := d.Payload
	event := d.EventType

	if event == "" {
		event = payloadString(p, "event")
	}

	title := eventTitle(event)

	s := eventSummary{event: event, title: title, at: eventTime(d), severity: eventSeverity(event), headline: title}
	repo := payloadString(p, "repository")

	var facts []fact

	if event == EventWebhookTest {
		facts = addFact(facts, "Message", payloadString(p, "message"))
	} else {
		facts = addFact(facts, "Repository", repo)
		facts = addFact(facts, "Format", payloadString(p, "format"))
		facts = addFact(facts, "Type", payloadString(p, "type"))
	}

	if art, ok := p["artifact"].(map[string]any); ok {
		name := payloadString(art, "name")
		version := payloadString(art, "version")
		facts = addFact(facts, "Artifact", name)
		facts = addFact(facts, "Version", version)
		facts = addFact(facts, "Path", payloadString(art, "path"))
		facts = addFact(facts, "SHA-256", payloadString(art, "sha256"))

		if size := payloadInt(art, "size"); size > 0 {
			facts = addFact(facts, "Size", humanSize(size))
		}

		s.headline = strings.TrimSpace(title + ": " + name + " " + version)
	} else if bundle, ok := p["bundle"].(map[string]any); ok {
		name := payloadString(bundle, "name")
		version := payloadString(bundle, "version")
		facts = addFact(facts, "Bundle", strings.TrimSpace(name+" "+version))
		facts = addFact(facts, "Stage", payloadString(p, "to_stage"))
		facts = addFact(facts, "Copied", payloadString(p, "copied"))
		facts = addFact(facts, "Already present", payloadString(p, "existing"))
		if replaced := payloadString(p, "replaced"); replaced != "" && replaced != "0" {
			facts = addFact(facts, "Replaced (forced)", replaced)
		}

		s.headline = strings.TrimSpace(title + ": " + name + " " + version + " to " + repo)
	} else if repo != "" {
		s.headline = title + ": " + repo
	}

	s.facts = facts

	return s
}

func humanSize(n int64) string {
	const unit = 1024

	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}

	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// truncate shortens s to at most limit runes, ending in an ellipsis.
func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}

	r := []rune(s)

	return string(r[:limit-1]) + "…"
}

// ---- CloudEvents -----------------------------------------------------------

// cloudEvent builds a CloudEvents 1.0 structured-mode JSON event: the native
// payload becomes data. The id is the delivery ID, so every retry of one
// delivery carries the same id and receivers can deduplicate.
func cloudEvent(d *models.WebhookDelivery, source string) map[string]any {
	s := summarize(d)

	id := d.ID
	if id == "" {
		id = uuid.NewString()
	}

	if source == "" {
		source = defaultEventSource
	}

	ev := map[string]any{
		"specversion":     cloudEventsSpecVersion,
		"id":              id,
		"source":          source,
		"type":            cloudEventsTypePrefix + s.event,
		"time":            s.at.Format(time.RFC3339),
		"datacontenttype": contentTypeJSON,
		"data":            d.Payload,
	}

	if subject := eventSubject(d.Payload); subject != "" {
		ev["subject"] = subject
	}

	return ev
}

// eventSubject is the CloudEvents subject: "<repository>/<path>" for artifact
// events, "<repository>" for repository events, empty for the test event.
func eventSubject(p map[string]any) string {
	repo := payloadString(p, "repository")
	if repo == "" {
		return ""
	}

	if art, ok := p["artifact"].(map[string]any); ok {
		if path := payloadString(art, "path"); path != "" {
			return repo + "/" + path
		}
	}

	return repo
}

// ---- Slack -----------------------------------------------------------------

// slackEscape escapes the three control characters Slack reserves (&, <, >), so
// a value can never form a <!channel> / <@user> / <url|label> token.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// codeSpan renders a value as an inline code span in Slack and Discord
// markdown: formatting characters inside it are literal. Backticks cannot be
// escaped inside a span, so they become apostrophes.
func codeSpan(s string) string {
	return "`" + strings.ReplaceAll(truncate(s, chatValueLimit), "`", "'") + "`"
}

// slackMessage builds a Slack incoming-webhook body: a fallback `text` plus a
// header block and a section block of mrkdwn fields.
func slackMessage(s eventSummary) map[string]any {
	blocks := []any{
		map[string]any{
			"type": "header",
			"text": map[string]any{"type": "plain_text", "text": truncate(s.title, 150)},
		},
	}

	// A section carries at most 10 fields of at most 2000 characters each.
	for start := 0; start < len(s.facts); start += 10 {
		fields := make([]any, 0, 10)

		for _, f := range s.facts[start:min(start+10, len(s.facts))] {
			fields = append(fields, map[string]any{
				"type": "mrkdwn",
				"text": "*" + f.name + "*\n" + slackEscape(codeSpan(f.value)),
			})
		}

		blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
	}

	return map[string]any{
		"text":   slackEscape(truncate(s.headline, chatValueLimit)),
		"blocks": blocks,
	}
}

// ---- Discord ---------------------------------------------------------------

const (
	discordColorGood = 0x2ECC71
	discordColorBad  = 0xE74C3C
	discordColorInfo = 0x3498DB
	discordMaxFields = 25
)

// discordMessage builds a Discord webhook body with one embed. allowed_mentions
// is empty so no value can ping @everyone, a role or a user.
func discordMessage(s eventSummary) map[string]any {
	fields := make([]any, 0, len(s.facts))

	for _, f := range s.facts[:min(len(s.facts), discordMaxFields)] {
		fields = append(fields, map[string]any{
			"name":   f.name,
			"value":  codeSpan(f.value),
			"inline": len(f.value) <= 40,
		})
	}

	color := discordColorInfo

	switch s.severity {
	case severityGood:
		color = discordColorGood
	case severityBad:
		color = discordColorBad
	case severityInfo:
	}

	return map[string]any{
		"username":         "OpenArtifact",
		"allowed_mentions": map[string]any{"parse": []string{}},
		"embeds": []any{
			map[string]any{
				"title":     truncate(s.title, 256),
				"color":     color,
				"timestamp": s.at.Format(time.RFC3339),
				"fields":    fields,
			},
		},
	}
}

// ---- Microsoft Teams -------------------------------------------------------

const (
	adaptiveCardSchema  = "http://adaptivecards.io/schemas/adaptive-card.json"
	adaptiveCardVersion = "1.4"
)

// teamsEscape backslash-escapes the characters Teams' Adaptive Card markdown
// treats specially, so a value renders literally (no links, emphasis or
// mentions).
func teamsEscape(s string) string {
	var b strings.Builder

	for _, r := range truncate(s, chatValueLimit) {
		if strings.ContainsRune("\\`*_~[]()<>|", r) {
			b.WriteByte('\\')
		}

		b.WriteRune(r)
	}

	return b.String()
}

// teamsMessage builds the Adaptive Card message accepted by Teams Workflows
// ("When a Teams webhook request is received") and legacy incoming webhooks.
func teamsMessage(s eventSummary) map[string]any {
	color := "Accent"

	switch s.severity {
	case severityGood:
		color = "Good"
	case severityBad:
		color = "Attention"
	case severityInfo:
	}

	facts := make([]any, 0, len(s.facts))
	for _, f := range s.facts {
		facts = append(facts, map[string]any{"title": f.name, "value": teamsEscape(f.value)})
	}

	body := []any{
		map[string]any{
			"type":   "TextBlock",
			"text":   s.title,
			"weight": "Bolder",
			"size":   "Medium",
			"color":  color,
			"wrap":   true,
		},
	}

	if len(facts) > 0 {
		body = append(body, map[string]any{"type": "FactSet", "facts": facts})
	}

	body = append(body, map[string]any{
		"type":     "TextBlock",
		"text":     "{{DATE(" + s.at.Format("2006-01-02T15:04:05Z") + ", SHORT)}} {{TIME(" + s.at.Format("2006-01-02T15:04:05Z") + ")}}",
		"isSubtle": true,
		"size":     "Small",
		"wrap":     true,
	})

	return map[string]any{
		"type": "message",
		"attachments": []any{
			map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"contentUrl":  nil,
				"content": map[string]any{
					"$schema": adaptiveCardSchema,
					"type":    "AdaptiveCard",
					"version": adaptiveCardVersion,
					"body":    body,
				},
			},
		},
	}
}
