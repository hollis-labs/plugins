package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

type document struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	Name        string `json:"name"`
	MimeType    string `json:"mime_type"`
	Content     string `json:"content,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	Included    bool   `json:"included"`
	FullContent bool   `json:"full_content"`
	Summary     string `json:"summary"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func project(row []pluginapi.DataCell, index map[string]int) document {
	get := func(k string) string { v, _ := cellString(row[index[k]]); return v }
	size, _ := strconv.ParseInt(get("size_bytes"), 10, 64)
	return document{get("id"), get("session_id"), get("name"), get("mime_type"), get("content"), size, get("included") == "1", get("full_content") == "1", get("summary"), get("created_at"), get("updated_at")}
}
func (db database) list(ctx context.Context, source, session string) ([]document, error) {
	result := []document{}
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, fmt.Errorf("committed import required")
		}
		index, _ := columns(state.Snapshot)
		for _, row := range state.Snapshot.Rows {
			d := project(row, index)
			if d.SessionID == session {
				result = append(result, d)
			}
		}
		slices.SortStableFunc(result, func(a, b document) int {
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
func boolCell(b bool) pluginapi.DataCell {
	s := "0"
	if b {
		s = "1"
	}
	return pluginapi.DataCell{Kind: "integer", Text: s}
}
func (db database) create(ctx context.Context, source string, d document) (document, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	d.ID = newID()
	d.CreatedAt = now
	d.UpdatedAt = now
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, fmt.Errorf("committed import required")
		}
		index, _ := columns(state.Snapshot)
		row := make([]pluginapi.DataCell, len(state.Snapshot.Columns))
		for i := range row {
			row[i] = pluginapi.DataCell{Kind: "null"}
		}
		for k, v := range map[string]string{"id": d.ID, "session_id": d.SessionID, "name": d.Name, "mime_type": d.MimeType, "content": d.Content, "summary": d.Summary, "created_at": now, "updated_at": now} {
			row[index[k]] = pluginapi.DataCell{Kind: "text", Text: v}
		}
		row[index["size_bytes"]] = pluginapi.DataCell{Kind: "integer", Text: strconv.FormatInt(d.SizeBytes, 10)}
		row[index["included"]] = boolCell(d.Included)
		row[index["full_content"]] = boolCell(d.FullContent)
		state.Snapshot.Rows = append(state.Snapshot.Rows, row)
		return true, nil
	})
	return d, err
}
func (db database) change(ctx context.Context, source, session, id string, patch patchRequest, remove bool) error {
	if !remove {
		if err := validatePatch(patch); err != nil {
			return err
		}
	}
	return db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.ImportSHA256 == "" {
			return false, errNotFound
		}
		index, _ := columns(state.Snapshot)
		for i, row := range state.Snapshot.Rows {
			d := project(row, index)
			if id == "" || d.ID != id || d.SessionID != session {
				continue
			}
			if remove {
				state.Snapshot.Rows = slices.Delete(state.Snapshot.Rows, i, i+1)
			} else {
				if patch.Included != nil {
					row[index["included"]] = boolCell(*patch.Included)
				}
				if patch.FullContent != nil {
					row[index["full_content"]] = boolCell(*patch.FullContent)
				}
				if patch.Summary != nil {
					row[index["summary"]] = pluginapi.DataCell{Kind: "text", Text: *patch.Summary}
				}
				row[index["updated_at"]] = pluginapi.DataCell{Kind: "text", Text: time.Now().UTC().Format(time.RFC3339Nano)}
			}
			return true, nil
		}
		return false, errNotFound
	})
}

type documentPage struct {
	Documents  []document `json:"documents"`
	More       bool       `json:"more"`
	NextOffset int        `json:"next_offset"`
}

func (db database) page(ctx context.Context, source, session string, offset int) (documentPage, error) {
	if offset < 0 || offset > pluginapi.MaxDataExportRows {
		return documentPage{}, fmt.Errorf("invalid offset")
	}
	rows, err := db.list(ctx, source, session)
	if err != nil {
		return documentPage{}, err
	}
	slices.Reverse(rows)
	start := min(offset, len(rows))
	end := min(start+100, len(rows))
	p := documentPage{Documents: []document{}, NextOffset: start}
	for _, row := range rows[start:end] {
		row.Content = ""
		p.Documents = append(p.Documents, row)
		raw, e := json.Marshal(p)
		if e != nil {
			return p, e
		}
		if len(raw) > maxResponseBytes {
			p.Documents = p.Documents[:len(p.Documents)-1]
			break
		}
		p.NextOffset++
	}
	if p.NextOffset == start && start < len(rows) {
		return p, fmt.Errorf("legacy metadata exceeds limit")
	}
	p.More = p.NextOffset < len(rows)
	return p, nil
}

type documentRead struct {
	Document   document `json:"document"`
	More       bool     `json:"more"`
	NextOffset int      `json:"next_offset"`
	Encoding   string   `json:"encoding"`
}

func (db database) read(ctx context.Context, source, session, id string, offset, limit int) (documentRead, error) {
	if offset < 0 || limit < 0 || limit > 65536 {
		return documentRead{}, fmt.Errorf("invalid range")
	}
	if limit == 0 {
		limit = 65536
	}
	rows, err := db.list(ctx, source, session)
	if err != nil {
		return documentRead{}, err
	}
	for _, row := range rows {
		if id == "" || row.ID != id {
			continue
		}
		content := row.Content
		if !utf8.ValidString(content) {
			return documentRead{}, fmt.Errorf("legacy content is not UTF-8; retained in typed storage")
		}
		if offset > len(content) || (offset < len(content) && !utf8.RuneStart(content[offset])) {
			return documentRead{}, fmt.Errorf("offset must be a UTF-8 byte boundary")
		}
		end := min(offset+limit, len(content))
		for end > offset && end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		if end == offset && offset < len(content) {
			return documentRead{}, fmt.Errorf("limit smaller than next rune")
		}
		row.Content = content[offset:end]
		result := documentRead{row, end < len(content), end, "utf-8"}
		raw, e := json.Marshal(result)
		if e != nil {
			return result, e
		}
		if len(raw) > maxResponseBytes {
			return documentRead{}, fmt.Errorf("legacy metadata exceeds limit")
		}
		return result, nil
	}
	return documentRead{}, errNotFound
}
