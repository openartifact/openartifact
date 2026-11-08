// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import (
	"encoding/json"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	maxNotificationTitleLen = 255
	maxNotificationKeyLen   = 64
)

// notificationRequest accepts the event configuration under the key Quay
// uses (event_config) and the key the image-controller sends (eventConfig).
type notificationRequest struct {
	Title            string          `json:"title"`
	Event            string          `json:"event"`
	Method           string          `json:"method"`
	Config           json.RawMessage `json:"config"`
	EventConfig      json.RawMessage `json:"eventConfig"`
	EventConfigSnake json.RawMessage `json:"event_config"`
}

type notificationJSON struct {
	UUID        string          `json:"uuid"`
	Title       string          `json:"title"`
	Event       string          `json:"event"`
	Method      string          `json:"method"`
	Config      json.RawMessage `json:"config"`
	EventConfig json.RawMessage `json:"eventConfig"`
}

func notificationView(n *models.QuayNotification) notificationJSON {
	return notificationJSON{
		UUID: n.ID, Title: n.Title, Event: n.Event, Method: n.Method,
		Config: n.Config, EventConfig: n.EventConfig,
	}
}

// notificationMethods routes the sub-resources of /repository/{org}/{repo}/notification:
// the collection and notification/{uuid}. It returns nil for any other shape.
func notificationMethods(rest []string) func(*repoCall) methods {
	switch len(rest) {
	case 0:
		return func(rc *repoCall) methods {
			return methods{http.MethodGet: rc.listNotifications, http.MethodPost: rc.createNotification}
		}
	case 1:
		return func(rc *repoCall) methods {
			return methods{
				http.MethodGet:    func() { rc.getNotification(rest[0]) },
				http.MethodPut:    func() { rc.updateNotification(rest[0]) },
				http.MethodDelete: func() { rc.deleteNotification(rest[0]) },
			}
		}
	}

	return nil
}

// isJSONObject reports whether raw is empty or a JSON object.
func isJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}

	var obj map[string]json.RawMessage

	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

// parseNotification reads and validates a notification body.
func (rc *repoCall) parseNotification() (*models.QuayNotification, bool) {
	var req notificationRequest
	if !rc.decode(&req) {
		return nil, false
	}

	eventConfig := req.EventConfig
	if len(eventConfig) == 0 {
		eventConfig = req.EventConfigSnake
	}

	switch {
	case req.Event == "" || len(req.Event) > maxNotificationKeyLen,
		req.Method == "" || len(req.Method) > maxNotificationKeyLen,
		len(req.Title) > maxNotificationTitleLen,
		!isJSONObject(req.Config), !isJSONObject(eventConfig):
		writeError(rc.w, http.StatusBadRequest, "Invalid notification")
		return nil, false
	}

	return &models.QuayNotification{
		QuayRepositoryID: rc.q.ID,
		Title:            req.Title,
		Event:            req.Event,
		Method:           req.Method,
		Config:           req.Config,
		EventConfig:      eventConfig,
	}, true
}

func (rc *repoCall) createNotification() {
	n, ok := rc.parseNotification()
	if !ok {
		return
	}

	created, err := rc.h.db.CreateQuayNotification(rc.r.Context(), n)
	if err != nil {
		rc.internalError("create notification", err)
		return
	}

	writeJSON(rc.w, http.StatusCreated, notificationView(created))
}

func (rc *repoCall) listNotifications() {
	list, err := rc.h.db.ListQuayNotifications(rc.r.Context(), rc.q.ID)
	if err != nil {
		rc.internalError("list notifications", err)
		return
	}

	out := make([]notificationJSON, 0, len(list))
	for _, n := range list {
		out = append(out, notificationView(n))
	}

	writeJSON(rc.w, http.StatusOK, map[string]any{"notifications": out, "page": 1, "has_additional": false})
}

func (rc *repoCall) getNotification(id string) {
	n, err := rc.h.db.GetQuayNotification(rc.r.Context(), rc.q.ID, id)
	if err != nil {
		rc.internalError("get notification", err)
		return
	}

	if n == nil {
		writeError(rc.w, http.StatusNotFound, "Notification not found")
		return
	}

	writeJSON(rc.w, http.StatusOK, notificationView(n))
}

func (rc *repoCall) updateNotification(id string) {
	n, ok := rc.parseNotification()
	if !ok {
		return
	}

	n.ID = id

	updated, err := rc.h.db.UpdateQuayNotification(rc.r.Context(), n)
	if err != nil {
		rc.internalError("update notification", err)
		return
	}

	if updated == nil {
		writeError(rc.w, http.StatusNotFound, "Notification not found")
		return
	}

	writeJSON(rc.w, http.StatusOK, notificationView(updated))
}

func (rc *repoCall) deleteNotification(id string) {
	deleted, err := rc.h.db.DeleteQuayNotification(rc.r.Context(), rc.q.ID, id)
	if err != nil {
		rc.internalError("delete notification", err)
		return
	}

	if !deleted {
		writeError(rc.w, http.StatusNotFound, "Notification not found")
		return
	}

	noContent(rc.w)
}
