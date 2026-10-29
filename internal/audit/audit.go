// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package audit

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

//nolint:gochecknoglobals // process-lifetime singleton logger
var defaultLogger atomic.Pointer[Logger]

func SetDefault(l *Logger) {
	defaultLogger.Store(l)
}

func Default() *Logger {
	return defaultLogger.Load()
}

// Outcome values of AuditEvent.Outcome.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// AuditEvent is the canonical event type sent to the audit logger.
// Fields correspond to the audit_logs schema.
type AuditEvent struct {
	Action       string  // required; validated action name (e.g. auth.token_validate)
	ActorType    string  // "user", "system", "token", "sso"
	ActorID      *string // UUID of the user/token
	Actor        string  // human-readable identifier (username, token name) — stored in details_json
	RepositoryID *string
	ResourceType *string
	ResourceID   *string
	ProjectID    *string
	IPAddress    *string
	Details      string // free-text; stored in details_json as {"message": "..."}
	// Package and Version name the package version the event concerns and
	// Subject the display name of what it acted on, captured at emit time so
	// the entry stays readable after the subject is gone. They are stored in
	// details_json as "package", "version" and "subject"; the per-package
	// activity filter matches on the first two.
	Package string
	Version string
	Subject string
	// Outcome is OutcomeSuccess or OutcomeFailure. Log fills an empty one:
	// failure for a Status of 400 and above, success otherwise (an explicit
	// action records something that happened).
	Outcome string
	// Username is the login name of the authenticated user, TokenID the API
	// token the request authenticated with and RequestID its X-Request-ID.
	// Log fills empty ones from the Request attached to its context.
	Username  string
	TokenID   string
	RequestID string
	// Status is the HTTP status of the request, 0 when the event records none.
	// The five fields above are stored in details_json as "outcome",
	// "username", "token_id", "request_id" and "status", omitted when empty.
	Status int
	// Method and Route are the HTTP method and route pattern of the request,
	// set by the audit middleware on its own events. They are only streamed
	// (http.request.method, http.route): the middleware's message already
	// carries them in details_json.
	Method string
	Route  string
}

// Request identifies the HTTP request events are recorded under. The audit
// middleware attaches it to the request context (WithRequest) so the events
// handlers record carry it without each emitter filling it in.
type Request struct {
	ID       string
	Username string
	TokenID  string
}

type requestKey struct{}

// WithRequest returns ctx carrying req for Log.
func WithRequest(ctx context.Context, req Request) context.Context {
	return context.WithValue(ctx, requestKey{}, req)
}

// entry is an event as Log captured it: the row id, the event time and the
// span context of the caller, shared by the database row and the OTLP event.
type entry struct {
	AuditEvent

	id   string
	at   time.Time
	span trace.SpanContext
}

type Logger struct {
	ch     chan entry
	db     database.Database
	log    *logger.Logger
	reg    *metrics.Registry
	stream otellog.Logger // nil unless WithOTLP
	done   chan struct{}

	mu     sync.RWMutex
	closed bool
}

// Option configures a Logger.
type Option func(*Logger)

func NewLogger(db database.Database, log *logger.Logger, reg *metrics.Registry, bufferSize int, opts ...Option) *Logger {
	if bufferSize <= 0 {
		bufferSize = 1000
	}

	l := &Logger{
		ch:   make(chan entry, bufferSize),
		db:   db,
		log:  log,
		reg:  reg,
		done: make(chan struct{}),
	}

	for _, o := range opts {
		o(l)
	}

	go l.run()

	return l
}

func nilActorType(t string) *string {
	if t == "" {
		return nil
	}
	return &t
}

func (l *Logger) run() {
	defer func() {
		if r := recover(); r != nil {
			l.log.Errorw("audit worker panic", "error", r)
			go l.run()
		}
	}()

	for e := range l.ch {
		if err := l.insert(e); err != nil {
			l.log.Errorw("audit log insert failed", "action", e.Action, "id", e.id, "error", err)
		}
	}

	close(l.done)
}

func (l *Logger) insert(e entry) error {
	row := &models.AuditLog{
		ID:           e.id,
		CreatedAt:    e.at,
		Action:       e.Action,
		ActorID:      e.ActorID,
		ActorType:    nilActorType(e.ActorType),
		DetailsJSON:  json.RawMessage(buildDetailsJSON(e.AuditEvent)),
		ResourceType: e.ResourceType,
		ResourceID:   e.ResourceID,
		RepositoryID: e.RepositoryID,
		ProjectID:    e.ProjectID,
		IPAddress:    e.IPAddress,
	}

	ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()

	return l.db.InsertAuditLog(ctx, row) //nolint:wrapcheck // logged by the caller with the action and id
}

// insertTimeout bounds one audit insert so a stalled database cannot wedge the
// single writer (and with it every Log caller and Shutdown).
const insertTimeout = 10 * time.Second

// buildDetailsJSON always yields valid JSON regardless of input bytes.
func buildDetailsJSON(event AuditEvent) string {
	m := make(map[string]any, 10) //nolint:mnd // one slot per key below

	for k, v := range map[string]string{
		"message": event.Details, "actor": event.Actor,
		"package": event.Package, "version": event.Version, "subject": event.Subject,
		"outcome": event.Outcome, "username": event.Username, "token_id": event.TokenID, "request_id": event.RequestID,
	} {
		if v != "" {
			m[k] = v
		}
	}

	if event.Status != 0 {
		m["status"] = event.Status
	}

	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}

	return string(b)
}

// complete fills the request fields event leaves empty from the Request on
// ctx, and its outcome.
func complete(ctx context.Context, event AuditEvent) AuditEvent {
	if req, ok := ctx.Value(requestKey{}).(Request); ok {
		if event.RequestID == "" {
			event.RequestID = req.ID
		}

		if event.Username == "" {
			event.Username = req.Username
		}

		if event.TokenID == "" {
			event.TokenID = req.TokenID
		}
	}

	if event.Outcome == "" {
		event.Outcome = OutcomeSuccess
		if event.Status >= 400 { //nolint:mnd // HTTP client and server errors
			event.Outcome = OutcomeFailure
		}
	}

	return event
}

// capture completes event and stamps it: a new row id, the event time (to the
// microsecond the database keeps, so the row and the OTLP event agree) and the
// caller's span context.
func capture(ctx context.Context, event AuditEvent) entry {
	return entry{
		AuditEvent: complete(ctx, event),
		id:         uuid.NewString(),
		at:         time.Now().Truncate(time.Microsecond),
		span:       trace.SpanContextFromContext(ctx),
	}
}

// Log streams an event and enqueues it for the database. It never panics, even
// after Shutdown. If the buffer is full it waits briefly for room rather than
// dropping the row at once.
func (l *Logger) Log(ctx context.Context, event AuditEvent) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.closed {
		l.dropped(event.Action, "audit event after shutdown dropped")
		return
	}

	e := capture(ctx, event)

	// Streamed before the queue, so a slow or failing database (a full
	// buffer, a refused insert) loses no event from the stream. Emit never
	// waits for the collector.
	l.emit(e)

	select {
	case l.ch <- e:
		return
	default:
	}

	// Buffer full: block briefly instead of silently dropping security events.
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case l.ch <- e:
	case <-t.C:
		l.dropped(event.Action, "audit buffer full; event dropped")
	}
}

// Shutdown stops accepting events, drains the buffer and waits for the writer.
// Safe to call more than once.
func (l *Logger) Shutdown(ctx context.Context) error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	l.mu.Unlock()

	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Logger) dropped(action, msg string) {
	if l.reg != nil {
		l.reg.AuditDroppedTotal.Inc()
	}
	if l.log != nil {
		l.log.Warnw(msg, "action", action)
	}
}
