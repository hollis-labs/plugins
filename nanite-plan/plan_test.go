package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func exportedFixture(t *testing.T, dir, feature string, rows [][]pluginapi.DataCell) pluginapi.DataExportReceipt {
	t.Helper()
	cols := append(append([]string{}, requiredColumns[feature]...), "future")
	snap := pluginapi.DataSnapshot{Protocol: 1, PluginID: pluginID, Feature: feature, SourceID: "workspace-a", Columns: cols, Rows: rows}
	raw, err := pluginapi.EncodeDataExport(snap)
	if err != nil {
		t.Fatal(err)
	}
	path := feature + ".jsonl"
	if err = os.WriteFile(filepath.Join(dir, path), raw, 0600); err != nil {
		t.Fatal(err)
	}
	decoded, err := pluginapi.DecodeDataExport(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return pluginapi.DataExportReceipt{PluginID: pluginID, Feature: feature, SourceID: snap.SourceID, Path: path, SHA256: decoded.SHA256, RowCount: len(rows)}
}
func legacyTodo() [][]pluginapi.DataCell {
	row := []pluginapi.DataCell{}
	values := map[string]string{"id": "legacy", "scope": "session", "scope_id": "session-a", "title": "Old todo", "description": "retained", "status": "pending", "priority": "high", "labels": "[\"old\"]", "metadata": "{\"sort_order\":4}", "created_by": "original-agent", "created_at": "2020-01-01", "updated_at": "original-time"}
	for _, name := range requiredColumns["todos"] {
		if name == "parent_id" || name == "project_id" {
			row = append(row, pluginapi.DataCell{Kind: "null"})
		} else {
			row = append(row, pluginapi.DataCell{Kind: "text", Text: values[name]})
		}
	}
	row = append(row, pluginapi.DataCell{Kind: "integer", Text: "9223372036854775807"})
	return [][]pluginapi.DataCell{row}
}
func databaseFixture(t *testing.T) (database, []pluginapi.DataExportReceipt) {
	t.Helper()
	db := newDatabase(t.TempDir())
	receipts := []pluginapi.DataExportReceipt{exportedFixture(t, db.dir, "todos", legacyTodo()), exportedFixture(t, db.dir, "plans", [][]pluginapi.DataCell{})}
	if _, err := db.importReceipts(t.Context(), receipts); err != nil {
		t.Fatal(err)
	}
	return db, receipts
}
func readState(t *testing.T, db database) table {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(db.dir, sourceFile("workspace-a")))
	if err != nil {
		t.Fatal(err)
	}
	var state table
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func operation(t *testing.T, db database, name string, args map[string]any) any {
	t.Helper()
	value, err := db.operate(t.Context(), "workspace-a", name, args, "session-a", "project-a", "agent")
	if err != nil {
		t.Fatal(name, err)
	}
	return value
}

func TestTypedImportAtomicPairAndReplay(t *testing.T) {
	db, receipts := databaseFixture(t)
	original := legacyTodo()
	if !reflect.DeepEqual(readState(t, db).Tables["todos"].Snapshot.Rows, original) {
		t.Fatal("typed import changed cells")
	}
	operation(t, db, "todo_update", map[string]any{"id": "legacy", "title": "edited"})
	state := readState(t, db)
	row := state.Tables["todos"].Snapshot.Rows[0]
	if row[len(row)-1] != original[0][len(row)-1] || row[11] != original[0][11] {
		t.Fatal("unknown fields or provenance changed")
	}
	operation(t, db, "todo_delete", map[string]any{"id": "legacy"})
	after := readState(t, db)
	for _, receipt := range receipts {
		if err := os.Remove(filepath.Join(db.dir, receipt.Path)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.importReceipts(t.Context(), receipts); err != nil {
		t.Fatal("matching replay needs no export", err)
	}
	if !reflect.DeepEqual(after, readState(t, db)) {
		t.Fatal("replay resurrected deletion")
	}
	conflict := append([]pluginapi.DataExportReceipt{}, receipts...)
	conflict[0].SHA256 = strings.Repeat("a", 64)
	if _, err := db.importReceipts(t.Context(), conflict); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	fresh := newDatabase(t.TempDir())
	only := []pluginapi.DataExportReceipt{exportedFixture(t, fresh.dir, "todos", legacyTodo())}
	if _, err := fresh.importReceipts(t.Context(), only); err == nil {
		t.Fatal("half import accepted")
	}
	if _, err := os.Stat(filepath.Join(fresh.dir, sourceFile("workspace-a"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("half state persisted")
	}
}
func TestImportFailureDoesNotCommitOrPoisonCache(t *testing.T) {
	db := newDatabase(t.TempDir())
	todo := exportedFixture(t, db.dir, "todos", legacyTodo())
	plan := exportedFixture(t, db.dir, "plans", [][]pluginapi.DataCell{})
	plan.SHA256 = strings.Repeat("a", 64)
	if _, err := db.importReceipts(t.Context(), []pluginapi.DataExportReceipt{todo, plan}); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if _, err := os.Stat(filepath.Join(db.dir, sourceFile("workspace-a"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial receipt committed")
	}
	plan = exportedFixture(t, db.dir, "plans", [][]pluginapi.DataCell{})
	if _, err := db.importReceipts(t.Context(), []pluginapi.DataExportReceipt{todo, plan}); err != nil {
		t.Fatal(err)
	}
	before := readState(t, db)
	_, err := db.operate(t.Context(), "workspace-a", "work_sync", map[string]any{"todos_checked": []any{"legacy"}, "plan_steps_checked": "invalid batch"}, "session-a", "project-a", "user")
	if err == nil {
		t.Fatal("failed batch accepted")
	}
	if !reflect.DeepEqual(before, readState(t, db)) {
		t.Fatal("partial batch persisted")
	}
	rows := operation(t, db, "todo_list", map[string]any{}).([]record)
	if rows[0]["status"] != "pending" {
		t.Fatal("failed batch poisoned cache")
	}
}
func TestScopesParentsAndCascadingDeletion(t *testing.T) {
	db, _ := databaseFixture(t)
	project := operation(t, db, "todo_create", map[string]any{"title": "Project", "scope": "project"}).(record)
	if project["scope_id"] != "project-a" || project["project_id"] != "project-a" || project["created_by"] != "agent" {
		t.Fatal(project)
	}
	turn := operation(t, db, "todo_create", map[string]any{"title": "Turn", "scope": "turn"}).(record)
	if turn["scope_id"] != "session-a" {
		t.Fatal(turn)
	}
	child := operation(t, db, "todo_create", map[string]any{"title": "Child", "parent_id": turn["id"]}).(record)
	rows := operation(t, db, "todo_list", map[string]any{"scope": "project"}).([]record)
	if len(rows) != 1 || rows[0]["id"] != project["id"] {
		t.Fatal(rows)
	}
	operation(t, db, "todo_scope", map[string]any{"id": turn["id"], "scope": "project"})
	operation(t, db, "todo_delete", map[string]any{"id": turn["id"]})
	if _, err := db.operate(t.Context(), "workspace-a", "todo_get", map[string]any{"id": child["id"]}, "session-a", "project-a", "agent"); !errors.Is(err, errNotFound) {
		t.Fatal("child survived cascade", err)
	}
	explicit := operation(t, db, "todo_create", map[string]any{"title": "Explicit", "scope": "session", "scope_id": "session-b"}).(record)
	if explicit["scope_id"] != "session-b" {
		t.Fatal("explicit core coordinate narrowed")
	}
}
func TestPlanAppendStepIdentityAndApproval(t *testing.T) {
	db, _ := databaseFixture(t)
	plan := operation(t, db, "plan_create", map[string]any{"title": "Plan", "scope": "session", "steps": "[{\"id\":\"s1\",\"title\":\"First\",\"status\":\"pending\",\"future\":{\"keep\":true}}]"}).(record)
	id := plan["id"]
	appended := operation(t, db, "plan_step_add", map[string]any{"plan_id": id, "steps": []any{map[string]any{"title": "Second", "depends_on": []any{"s1"}, "acceptance": "verified"}}}).(map[string]any)
	if appended["appended"].([]record)[0]["id"] != "s2" {
		t.Fatal(appended)
	}
	operation(t, db, "plan_update", map[string]any{"id": id, "step_id": "s1", "status": "done", "notes": "retained"})
	got := operation(t, db, "plan_get", map[string]any{"id": id}).(record)
	steps := got["steps"].([]any)
	if steps[0].(map[string]any)["future"].(map[string]any)["keep"] != true {
		t.Fatal("step edit lost unknown properties")
	}
	before := readState(t, db)
	if _, err := db.operate(t.Context(), "workspace-a", "plan_step_add", map[string]any{"plan_id": id, "steps": "[{\"id\":\"s2\",\"title\":\"collision\"}]"}, "session-a", "project-a", "agent"); err == nil {
		t.Fatal("collision accepted")
	}
	if !reflect.DeepEqual(before, readState(t, db)) {
		t.Fatal("collision changed state")
	}
	approved := operation(t, db, "plan_approve", map[string]any{"id": id, "create_todos": true}).(record)
	if approved["status"] != "approved" {
		t.Fatal(approved)
	}
	for _, step := range approved["steps"].([]any) {
		todoID := step.(map[string]any)["todo_id"]
		if todoID == nil {
			t.Fatal("missing link")
		}
		todo := operation(t, db, "todo_get", map[string]any{"id": todoID}).(record)
		if todo["scope_id"] != "session-a" {
			t.Fatal(todo)
		}
	}
	workspace := operation(t, db, "plan_create", map[string]any{"title": "Workspace", "scope": "workspace", "steps": `[{"title":"Cannot create workspace todo"}]`}).(record)
	before = readState(t, db)
	if _, err := db.operate(t.Context(), "workspace-a", "plan_approve", map[string]any{"id": workspace["id"], "create_todos": true}, "session-a", "project-a", "agent"); err == nil {
		t.Fatal("retired workspace todos accepted")
	}
	if !reflect.DeepEqual(before, readState(t, db)) {
		t.Fatal("failed approval changed tables")
	}
}

func pluginFixture(t *testing.T, committed bool, capture ...*subprocess.InitParams) (*planPlugin, []pluginapi.DataExportReceipt) {
	t.Helper()
	dir := t.TempDir()
	receipts := []pluginapi.DataExportReceipt{exportedFixture(t, dir, "todos", legacyTodo()), exportedFixture(t, dir, "plans", [][]pluginapi.DataCell{})}
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resource := pluginapi.QueryResource(strings.TrimPrefix(r.URL.Path, "/api/plugin-host/query/"))
		session := r.URL.Query().Get("session_id")
		var data any
		switch resource {
		case pluginapi.QueryDataExports:
			values := []pluginapi.DataExportReceipt{}
			if committed {
				values = receipts
			}
			data = pluginapi.QueryDataExportsData{Exports: values}
		case pluginapi.QuerySessions:
			if session != "session-a" && session != "session-b" {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			data = pluginapi.QuerySessionsData{Sessions: []pluginapi.QuerySession{{ID: session, ProjectID: "project-a"}}}
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		raw, _ := json.Marshal(data)
		_ = json.NewEncoder(w).Encode(pluginapi.QueryResponse{Protocol: 1, Resource: resource, SessionID: session, Data: raw})
	}))
	t.Cleanup(server.Close)
	grant := pluginapi.QueryGrant{Protocol: 1, PluginID: pluginID, HostURL: server.URL, Token: token, Scope: pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QuerySessions, pluginapi.QueryDataExports}, AllSessions: true}}
	identity, _ := json.Marshal(map[string]any{"nanite_host_query": grant})
	p := &planPlugin{}
	params := subprocess.InitParams{DataDir: dir, Identity: identity}
	if len(capture) > 0 {
		*capture[0] = params
	}
	if _, err := p.Init(t.Context(), params); err != nil {
		t.Fatal(err)
	}
	return p, receipts
}
func mcpCall(t *testing.T, p *planPlugin, name string, args map[string]any) string {
	t.Helper()
	reply, err := p.MCPCallTool(t.Context(), subprocess.MCPCallRequest{ToolName: name, SessionID: "session-a", Arguments: args})
	if err != nil || reply.IsError {
		t.Fatal(name, string(reply.Content), err)
	}
	var content []struct{ Text string }
	if err = json.Unmarshal(reply.Content, &content); err != nil {
		t.Fatal(err)
	}
	return content[0].Text
}
func TestNineToolsAndCoreEnvelopeSemantics(t *testing.T) {
	p, _ := pluginFixture(t, true)
	todo := mcpCall(t, p, "todo_create", map[string]any{"title": "From tool"})
	if !strings.Contains(todo, "Created todo") {
		t.Fatal(todo)
	}
	rows, err := p.db.operate(t.Context(), "workspace-a", "todo_list", map[string]any{}, "session-a", "project-a", "agent")
	if err != nil {
		t.Fatal(err)
	}
	id := rows.([]record)[0]["id"]
	mcpCall(t, p, "todo_update", map[string]any{"id": id, "status": "blocked", "labels": "[\"plugin\"]"})
	list := mcpCall(t, p, "todo_list", map[string]any{"scope": "session"})
	if !strings.Contains(list, "<!--ENVELOPE_DATA:") || !strings.Contains(list, `"kind":"todos"`) {
		t.Fatal(list)
	}
	plan := mcpCall(t, p, "plan_create", map[string]any{"title": "Tool plan", "scope": "session", "steps": "[{\"id\":\"s1\",\"title\":\"Start\"}]"})
	var row record
	if err = json.Unmarshal([]byte(plan[strings.Index(plan, "\n")+1:]), &row); err != nil {
		t.Fatal(err)
	}
	mcpCall(t, p, "plan_update", map[string]any{"id": row["id"], "step_id": "s1", "status": "done"})
	added := mcpCall(t, p, "plan_step_add", map[string]any{"plan_id": row["id"], "steps": "[{\"title\":\"Next\"}]"})
	if !strings.Contains(added, `"id":"s2"`) {
		t.Fatal(added)
	}
	if got := mcpCall(t, p, "plan_get", map[string]any{"id": row["id"]}); !strings.Contains(got, `"status":"done"`) {
		t.Fatal(got)
	}
	if got := mcpCall(t, p, "plan_list", map[string]any{"scope": "session"}); !strings.Contains(got, "Tool plan") {
		t.Fatal(got)
	}
	mcpCall(t, p, "todo_create", map[string]any{"title": "Explicit orphan coordinate", "scope_id": "missing-session"})
	mcpCall(t, p, "todo_list", map[string]any{"scope": "session", "scope_id": "missing-session"})
	mcpCall(t, p, "plan_update", map[string]any{"id": row["id"], "step_id": "s1", "title": "Ignored title", "notes": "edited note"})
	got := mcpCall(t, p, "plan_get", map[string]any{"id": row["id"]})
	if strings.Contains(got, "Ignored title") || !strings.Contains(got, "edited note") {
		t.Fatal("step branch violated core semantics", got)
	}
	mcpCall(t, p, "plan_delete", map[string]any{"id": row["id"]})
	if err = p.Unload(t.Context()); err != nil {
		t.Fatal(err)
	}
	reply, err := p.MCPCallTool(t.Context(), subprocess.MCPCallRequest{ToolName: "todo_list"})
	if err != nil || !reply.IsError {
		t.Fatal("unloaded call succeeded")
	}
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range m.Tools {
		if tool.Name == "plan_delete" && tool.Effect != pluginapi.ToolEffectDestructive {
			t.Fatal("lost core destructive permission hint")
		}
	}
	block, err := pluginapi.DecodeBlock(m.Nanite)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Registers.Slots) != 0 || block.Registers.Panels[0].ID != "work" || !block.Registers.Panels[0].DefaultVisible {
		t.Fatal("Plan must be an E5 panel")
	}
}
func TestReadinessAndHTTPValidation(t *testing.T) {
	blocked, _ := pluginFixture(t, false)
	reply, err := blocked.MCPCallTool(t.Context(), subprocess.MCPCallRequest{ToolName: "todo_create", SessionID: "session-a", Arguments: map[string]any{"title": "Forbidden"}})
	if err != nil || !reply.IsError {
		t.Fatal("orphan export authorized writes")
	}
	p, _ := pluginFixture(t, true)
	request := func(method, path, query, body string) subprocess.HTTPResponse {
		t.Helper()
		reply, err := p.HTTPHandle(t.Context(), subprocess.HTTPRequest{Method: method, Path: "/api/plugins/" + pluginID + "/" + path, RawQuery: query, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	if reply := request("POST", "todos", "session_id=session-a", `{"title":"Via HTTP"}`); reply.Status != 201 {
		t.Fatal(reply.Status, string(reply.Body))
	}
	for _, body := range []string{`null`, `{"title":"x","title":"y"}`, `{"title":"x","future":1}`, `{"title":null}`, `{"title":"x"} {}`} {
		if reply := request("POST", "todos", "session_id=session-a", body); reply.Status != 400 {
			t.Fatal(body, reply.Status)
		}
	}
	if reply := request("GET", "todos", "session_id=session-a&session_id=session-b", ""); reply.Status != 400 {
		t.Fatal(reply.Status)
	}
	if reply := request("GET", "todos", "session_id=session-a&offset=-1", ""); reply.Status != 400 {
		t.Fatal(reply.Status)
	}
	if reply := request("GET", "todos/legacy/children", "session_id=session-a", ""); reply.Status != 200 {
		t.Fatal(reply.Status, string(reply.Body))
	}
	if reply := request("GET", "todos", "session_id=session-a", ""); reply.Status != 200 || !strings.Contains(string(reply.Body), "Via HTTP") {
		t.Fatal(reply.Status, string(reply.Body))
	}
}

func TestConcurrentConnectionsCancellationAndCache(t *testing.T) {
	db, _ := databaseFixture(t)
	other := newDatabase(db.dir)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			writer := db
			if i%2 == 0 {
				writer = other
			}
			_, err := writer.operate(context.Background(), "workspace-a", "todo_create", map[string]any{"title": fmt.Sprintf("Concurrent %d", i)}, "session-a", "project-a", "agent")
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	rows := operation(t, db, "todo_list", map[string]any{}).([]record)
	if len(rows) != 13 {
		t.Fatal("lost concurrent write", len(rows))
	}
	lock, err := os.OpenFile(filepath.Join(db.dir, sourceFile("workspace-a")+".lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close() //nolint:errcheck
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err = db.operate(ctx, "workspace-a", "todo_list", nil, "session-a", "project-a", "agent")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock ignores cancellation", err)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}
func TestQuotaAndUncertainCommit(t *testing.T) {
	db, _ := databaseFixture(t)
	db.nativeGrowthCap = 1
	before := readState(t, db)
	_, err := db.operate(t.Context(), "workspace-a", "todo_create", map[string]any{"title": "Quota"}, "session-a", "project-a", "agent")
	if !errors.Is(err, errQuota) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, readState(t, db)) {
		t.Fatal("quota failure changed state")
	}
	operation(t, db, "todo_update", map[string]any{"id": "legacy", "status": "done"})
	db.nativeGrowthCap = 0
	db.syncDirectory = func(*os.File) error { return errors.New("fsync failed") }
	_, err = db.operate(t.Context(), "workspace-a", "todo_create", map[string]any{"title": "Uncertain"}, "session-a", "project-a", "agent")
	if !errors.Is(err, errCommitUncertain) {
		t.Fatal(err)
	}
	rows := operation(t, newDatabase(db.dir), "todo_list", map[string]any{}).([]record)
	if len(rows) != 2 {
		t.Fatal("uncertain commit not visible")
	}
}
