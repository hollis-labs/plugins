package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

type pin struct {
	ID        string  `json:"id"`
	SessionID *string `json:"session_id,omitempty"`
	Scope     string  `json:"scope"`
	ProjectID string  `json:"project_id,omitempty"`
	Content   string  `json:"content"`
	AgentID   string  `json:"agent_id"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func project(row []pluginapi.DataCell, index map[string]int) pin {
	get := func(name string) string { value, _ := cellString(row[index[name]]); return value }
	result := pin{ID: get("id"), Scope: get("scope"), ProjectID: get("project_id"), Content: get("content"), AgentID: get("agent_id"), CreatedAt: get("created_at"), UpdatedAt: get("updated_at")}
	if row[index["session_id"]].Kind != "null" {
		value := get("session_id")
		result.SessionID = &value
	}
	return result
}
func visible(row pin, session, projectID string) bool {
	return ((row.Scope == "session" || row.Scope == "turn") && row.SessionID != nil && *row.SessionID == session) || (row.Scope == "project" && projectID != "" && row.ProjectID == projectID)
}
func (db database) list(ctx context.Context, source, session, projectID string) ([]pin, error) {
	result := []pin{}
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, fmt.Errorf("committed core import required")
		}
		index, _ := columns(state.Snapshot)
		for _, row := range state.Snapshot.Rows {
			p := project(row, index)
			if visible(p, session, projectID) {
				result = append(result, p)
			}
		}
		slices.SortStableFunc(result, func(a, b pin) int {
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
func (db database) create(ctx context.Context, source string, p pin) (pin, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	p.ID = newID()
	p.CreatedAt = now
	p.UpdatedAt = now
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, fmt.Errorf("committed core import required")
		}
		index, _ := columns(state.Snapshot)
		row := make([]pluginapi.DataCell, len(state.Snapshot.Columns))
		for i := range row {
			row[i] = pluginapi.DataCell{Kind: "null"}
		}
		for key, value := range map[string]string{"id": p.ID, "scope": p.Scope, "content": p.Content, "agent_id": p.AgentID, "created_at": now, "updated_at": now} {
			row[index[key]] = pluginapi.DataCell{Kind: "text", Text: value}
		}
		if p.SessionID != nil {
			row[index["session_id"]] = pluginapi.DataCell{Kind: "text", Text: *p.SessionID}
		}
		if p.ProjectID != "" {
			row[index["project_id"]] = pluginapi.DataCell{Kind: "text", Text: p.ProjectID}
		}
		state.Snapshot.Rows = append(state.Snapshot.Rows, row)
		return true, nil
	})
	return p, err
}
func (db database) change(ctx context.Context, source, session, projectID, id, action, value string) error {
	return db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, errNotFound
		}
		index, _ := columns(state.Snapshot)
		for i, row := range state.Snapshot.Rows {
			p := project(row, index)
			if p.ID != id || !visible(p, session, projectID) {
				continue
			}
			switch action {
			case "delete":
				state.Snapshot.Rows = slices.Delete(state.Snapshot.Rows, i, i+1)
			case "content":
				if err := validateContent(value); err != nil {
					return false, err
				}
				row[index["content"]] = pluginapi.DataCell{Kind: "text", Text: value}
			case "scope":
				if value != "session" && value != "project" {
					return false, fmt.Errorf("invalid scope")
				}
				if value == "project" && projectID == "" {
					return false, fmt.Errorf("current session has no project")
				}
				if value == "session" && (p.SessionID == nil || *p.SessionID == "") {
					return false, fmt.Errorf("pin has no originating session")
				}
				row[index["scope"]] = pluginapi.DataCell{Kind: "text", Text: value}
				row[index["project_id"]] = pluginapi.DataCell{Kind: "null"}
				if value == "project" {
					row[index["project_id"]] = pluginapi.DataCell{Kind: "text", Text: projectID}
				}
			default:
				return false, fmt.Errorf("unknown pin mutation")
			}
			if action != "delete" {
				row[index["updated_at"]] = pluginapi.DataCell{Kind: "text", Text: time.Now().UTC().Format(time.RFC3339Nano)}
			}
			return true, nil
		}
		return false, errNotFound
	})
}
