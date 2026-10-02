package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
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
	return document{get("id"), get("session_id"), get("name"), get("mime_type"), get("content"), size, nonzero(get("included")), nonzero(get("full_content")), get("summary"), get("created_at"), get("updated_at")}
}

func nonzero(s string) bool { v, err := strconv.ParseFloat(s, 64); return err == nil && v != 0 }

const maxSessionDocuments = 200
const maxSessionBytes = 8 << 20

func documentBytes(d document) int64 {
	return int64(len(d.Content) + len(d.Summary) + len(d.Name) + len(d.MimeType))
}
func sessionUsage(state table, session string) (int, int64) {
	index, _ := columns(state.Snapshot)
	count := 0
	var bytes int64
	for _, row := range state.Snapshot.Rows {
		d := project(row, index)
		if d.SessionID == session {
			count++
			bytes += documentBytes(d)
		}
	}
	return count, bytes
}
func checkSessionQuota(count int, bytes int64) error {
	if count > maxSessionDocuments {
		return fmt.Errorf("%w: session limit is 200 documents", errQuota)
	}
	if bytes > maxSessionBytes {
		return fmt.Errorf("%w: session text/metadata limit is 8 MiB", errQuota)
	}
	return nil
}
func documentOrder(a, b document) int {
	at, ae := time.Parse(time.RFC3339Nano, a.CreatedAt)
	bt, be := time.Parse(time.RFC3339Nano, b.CreatedAt)
	var order int
	switch {
	case ae == nil && be == nil:
		order = at.Compare(bt)
	case ae == nil:
		order = -1
	case be == nil:
		order = 1
	default:
		order = strings.Compare(a.CreatedAt, b.CreatedAt)
	}
	if order == 0 {
		order = strings.Compare(a.ID, b.ID)
	}
	return order
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
		slices.SortFunc(result, documentOrder)
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
	err := db.transactionWithGrowth(ctx, source, func(state *table) (bool, bool, error) {
		if state.ImportSHA256 == "" {
			return false, false, fmt.Errorf("committed import required")
		}
		count, bytes := sessionUsage(*state, d.SessionID)
		if err := checkSessionQuota(count+1, bytes+documentBytes(d)); err != nil {
			return false, false, err
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
		return true, true, nil
	})
	return d, err
}
func (db database) change(ctx context.Context, source, session, id string, patch patchRequest, remove bool) error {
	if !remove {
		if err := validatePatch(patch); err != nil {
			return err
		}
	}
	return db.transactionWithGrowth(ctx, source, func(state *table) (bool, bool, error) {
		if state.ImportSHA256 == "" {
			return false, false, errNotFound
		}
		index, _ := columns(state.Snapshot)
		growing := false
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
					count, bytes := sessionUsage(*state, session)
					growth := int64(len(*patch.Summary) - len(d.Summary))
					if growth > 0 {
						growing = true
						if err := checkSessionQuota(count, bytes+growth); err != nil {
							return false, false, err
						}
					}
					row[index["summary"]] = pluginapi.DataCell{Kind: "text", Text: *patch.Summary}
				}
				row[index["updated_at"]] = pluginapi.DataCell{Kind: "text", Text: time.Now().UTC().Format(time.RFC3339Nano)}
			}
			return true, growing, nil
		}
		return false, false, errNotFound
	})
}

type documentPage struct {
	Documents  []document `json:"documents"`
	More       bool       `json:"more"`
	NextOffset int        `json:"next_offset"`
}

func (db database) page(ctx context.Context, source, session string, offset int) (documentPage, error) {
	return db.pageVisible(ctx, source, session, offset, false)
}
func (db database) pageVisible(ctx context.Context, source, session string, offset int, includedOnly bool) (documentPage, error) {
	if offset < 0 || offset > pluginapi.MaxDataExportRows {
		return documentPage{}, fmt.Errorf("%w: invalid offset", errInvalid)
	}
	rows, err := db.list(ctx, source, session)
	if err != nil {
		return documentPage{}, err
	}
	if includedOnly {
		rows = slices.DeleteFunc(rows, func(row document) bool { return !row.Included })
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
		return p, fmt.Errorf("%w: legacy metadata exceeds limit", errInvalid)
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
	return db.readVisible(ctx, source, session, id, offset, limit, false)
}
func (db database) readVisible(ctx context.Context, source, session, id string, offset, limit int, includedOnly bool) (documentRead, error) {
	if offset < 0 || limit < 0 || limit > 65536 {
		return documentRead{}, fmt.Errorf("%w: invalid range", errInvalid)
	}
	if limit == 0 {
		limit = 65536
	}
	rows, err := db.list(ctx, source, session)
	if err != nil {
		return documentRead{}, err
	}
	for _, row := range rows {
		if id == "" || row.ID != id || (includedOnly && !row.Included) {
			continue
		}
		content := row.Content
		if !utf8.ValidString(content) {
			return documentRead{}, fmt.Errorf("%w: legacy content is not UTF-8; retained in typed storage", errInvalid)
		}
		if offset > len(content) || (offset < len(content) && !utf8.RuneStart(content[offset])) {
			return documentRead{}, fmt.Errorf("%w: offset must be a UTF-8 byte boundary", errInvalid)
		}
		end := min(offset+limit, len(content))
		for end > offset && end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		if end == offset && offset < len(content) {
			return documentRead{}, fmt.Errorf("%w: limit smaller than next rune", errInvalid)
		}
		row.Content = content[offset:end]
		result := documentRead{row, end < len(content), end, "utf-8"}
		raw, e := json.Marshal(result)
		if e != nil {
			return result, e
		}
		if len(raw) > maxResponseBytes {
			return documentRead{}, fmt.Errorf("%w: legacy metadata exceeds limit", errInvalid)
		}
		return result, nil
	}
	return documentRead{}, errNotFound
}
