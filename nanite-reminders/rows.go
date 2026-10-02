package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
)

type reminder struct {
	ID          string  `json:"id"`
	SessionID   string  `json:"session_id"`
	Scope       string  `json:"scope"`
	ProjectID   string  `json:"project_id,omitempty"`
	Text        string  `json:"text"`
	TriggerJSON string  `json:"trigger_json"`
	FiredAt     *string `json:"fired_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}
type trigger struct {
	Type string `json:"type"`
	At   string `json:"at,omitempty"`
	N    int    `json:"n,omitempty"`
}

func parseTrigger(raw string) (trigger, error) {
	var t trigger
	if err := manifest.DecodeExtension([]byte(raw), &t); err != nil {
		return t, err
	}
	switch t.Type {
	case "time":
		if t.N != 0 {
			return t, fmt.Errorf("time trigger cannot carry a turn count")
		}
		if _, err := time.Parse(time.RFC3339, t.At); err != nil {
			return t, fmt.Errorf("time trigger requires RFC3339 at")
		}
	case "turn_count":
		if t.At != "" || t.N < 1 || t.N > 1000000 {
			return t, fmt.Errorf("turn_count requires n between 1 and 1000000")
		}
	default:
		return t, fmt.Errorf("unknown trigger type")
	}
	return t, nil
}
func project(row []pluginapi.DataCell, index map[string]int) reminder {
	get := func(name string) string { value, _ := cellString(row[index[name]]); return value }
	result := reminder{ID: get("id"), SessionID: get("session_id"), Scope: get("scope"), ProjectID: get("project_id"), Text: get("text"), TriggerJSON: get("trigger_json"), CreatedAt: get("created_at"), UpdatedAt: get("updated_at")}
	if row[index["fired_at"]].Kind != "null" {
		value := get("fired_at")
		result.FiredAt = &value
	}
	return result
}
func visible(r reminder, session, projectID string) bool {
	return ((r.Scope == "session" || r.Scope == "turn") && r.SessionID == session) || (r.Scope == "project" && projectID != "" && r.ProjectID == projectID)
}
func (db database) list(ctx context.Context, source, session, projectID string) ([]reminder, error) {
	result := []reminder{}
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, fmt.Errorf("committed core import required")
		}
		index, _ := columns(state.Snapshot)
		for _, row := range state.Snapshot.Rows {
			r := project(row, index)
			if visible(r, session, projectID) && r.FiredAt == nil {
				result = append(result, r)
			}
		}
		slices.SortStableFunc(result, func(a, b reminder) int {
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			if a.CreatedAt > b.CreatedAt {
				return 1
			}
			return 0
		})
		return false, nil
	})
	return result, err
}
func (db database) create(ctx context.Context, source string, r reminder, creationCount int) (reminder, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.ID = newID()
	r.CreatedAt = now
	r.UpdatedAt = now
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, fmt.Errorf("committed core import required")
		}
		index, _ := columns(state.Snapshot)
		row := make([]pluginapi.DataCell, len(state.Snapshot.Columns))
		for i := range row {
			row[i] = pluginapi.DataCell{Kind: "null"}
		}
		for key, value := range map[string]string{"id": r.ID, "session_id": r.SessionID, "scope": r.Scope, "project_id": r.ProjectID, "text": r.Text, "trigger_json": r.TriggerJSON, "created_at": now, "updated_at": now} {
			row[index[key]] = pluginapi.DataCell{Kind: "text", Text: value}
		}
		if r.ProjectID == "" {
			row[index["project_id"]] = pluginapi.DataCell{Kind: "null"}
		}
		state.Snapshot.Rows = append(state.Snapshot.Rows, row)
		if state.CreationCounts == nil {
			state.CreationCounts = map[string]int{}
		}
		state.CreationCounts[r.ID] = creationCount
		return true, nil
	})
	return r, err
}

// Acknowledgment is explicit. Context reads never consume or mutate reminders.
func (db database) change(ctx context.Context, source, session, projectID, id, action, scope string) error {
	return db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, errNotFound
		}
		index, _ := columns(state.Snapshot)
		for i, row := range state.Snapshot.Rows {
			r := project(row, index)
			if r.ID != id || !visible(r, session, projectID) {
				continue
			}
			switch action {
			case "delete":
				state.Snapshot.Rows = slices.Delete(state.Snapshot.Rows, i, i+1)
				delete(state.CreationCounts, id)
			case "ack":
				if r.FiredAt != nil {
					return false, nil
				}
				row[index["fired_at"]] = pluginapi.DataCell{Kind: "text", Text: time.Now().UTC().Format(time.RFC3339Nano)}
			case "scope":
				if scope != "session" && scope != "project" {
					return false, fmt.Errorf("scope must be session or project")
				}
				if scope == "project" && projectID == "" {
					return false, fmt.Errorf("current session has no project")
				}
				// Demotion returns a shared reminder to its originating session.
				row[index["scope"]] = pluginapi.DataCell{Kind: "text", Text: scope}
				row[index["project_id"]] = pluginapi.DataCell{Kind: "null"}
				if scope == "project" {
					row[index["project_id"]] = pluginapi.DataCell{Kind: "text", Text: projectID}
				}
			default:
				return false, fmt.Errorf("unknown mutation")
			}
			if action != "delete" {
				row[index["updated_at"]] = pluginapi.DataCell{Kind: "text", Text: time.Now().UTC().Format(time.RFC3339Nano)}
			}
			return true, nil
		}
		return false, errNotFound
	})
}
func (db database) due(ctx context.Context, source, session, projectID string, count int, now time.Time) ([]reminder, error) {
	result := []reminder{}
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, fmt.Errorf("committed core import required")
		}
		index, _ := columns(state.Snapshot)
		for _, row := range state.Snapshot.Rows {
			r := project(row, index)
			if !visible(r, session, projectID) || r.FiredAt != nil {
				continue
			}
			var t trigger
			// Legacy trigger bytes remain untouched, including old unknown fields.
			if json.Unmarshal([]byte(r.TriggerJSON), &t) != nil {
				continue
			}
			fire := false
			switch t.Type {
			case "time":
				at, err := time.Parse(time.RFC3339, t.At)
				fire = err == nil && !now.Before(at)
			case "turn_count":
				fire = t.N > 0 && count >= state.CreationCounts[r.ID]+t.N
			}
			if fire {
				result = append(result, r)
			}
		}
		slices.SortStableFunc(result, func(a, b reminder) int {
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			if a.CreatedAt > b.CreatedAt {
				return 1
			}
			return 0
		})
		return false, nil
	})
	return result, err
}
