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
}

type Logger struct {
	ch   chan AuditEvent
	db   database.Database
	log  *logger.Logger
	reg  *metrics.Registry
	done chan struct{}

	mu     sync.RWMutex
	closed bool
}

func NewLogger(db database.Database, log *logger.Logger, reg *metrics.Registry, bufferSize int) *Logger {
	if bufferSize <= 0 {
		bufferSize = 1000
	}

	l := &Logger{
		ch:   make(chan AuditEvent, bufferSize),
		db:   db,
		log:  log,
		reg:  reg,
		done: make(chan struct{}),
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

	for event := range l.ch {
		detailsJSON := "{}"
		if event.Details != "" || event.Actor != "" {
			detailsJSON = buildDetailsJSON(event.Details, event.Actor)
		}

		entry := &models.AuditLog{
			Action:       event.Action,
			ActorID:      event.ActorID,
			ActorType:    nilActorType(event.ActorType),
			DetailsJSON:  json.RawMessage(detailsJSON),
			ResourceType: event.ResourceType,
			ResourceID:   event.ResourceID,
			RepositoryID: event.RepositoryID,
			ProjectID:    event.ProjectID,
			IPAddress:    event.IPAddress,
		}
		ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
		err := l.db.InsertAuditLog(ctx, entry)

		cancel()

		if err != nil {
			l.log.Errorw("audit log insert failed", "action", event.Action, "error", err)
		}
	}

	close(l.done)
}

// insertTimeout bounds one audit insert so a stalled database cannot wedge the
// single writer (and with it every Log caller and Shutdown).
const insertTimeout = 10 * time.Second

// buildDetailsJSON always yields valid JSON regardless of input bytes.
func buildDetailsJSON(message, actor string) string {
	m := make(map[string]string, 2)
	if message != "" {
		m["message"] = message
	}
	if actor != "" {
		m["actor"] = actor
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Log enqueues an event. It never panics, even after Shutdown. If the buffer is
// full the event is written synchronously (bounded wait) rather than dropped.
func (l *Logger) Log(_ context.Context, event AuditEvent) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.closed {
		l.dropped(event.Action, "audit event after shutdown dropped")
		return
	}

	select {
	case l.ch <- event:
		return
	default:
	}

	// Buffer full: block briefly instead of silently dropping security events.
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case l.ch <- event:
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
