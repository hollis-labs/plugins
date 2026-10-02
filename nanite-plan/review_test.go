package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func callMCP(t *testing.T, p *planPlugin, session, name string, args map[string]any) (bool, string) {
	t.Helper()
	reply, err := p.MCPCallTool(t.Context(), subprocess.MCPCallRequest{ToolName: name, SessionID: session, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var c []struct{ Text string }
	if err = json.Unmarshal(reply.Content, &c); err != nil || len(c) != 1 {
		t.Fatal(err, string(reply.Content))
	}
	return reply.IsError, c[0].Text
}
func httpCall(t *testing.T, p *planPlugin, method, path, body string) subprocess.HTTPResponse {
	t.Helper()
	r, e := p.HTTPHandle(t.Context(), subprocess.HTTPRequest{Method: method, Path: "/api/plugins/" + pluginID + "/" + path, RawQuery: "session_id=session-a", Body: []byte(body)})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func requireStatus(t *testing.T, r subprocess.HTTPResponse, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d expected %d: %s", r.Status, status, r.Body)
	}
}
func changeFixture(t *testing.T, db database, change func(*table)) {
	t.Helper()
	state := readState(t, db)
	change(&state)
	raw, e := json.Marshal(state)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(db.dir, sourceFile("workspace-a")), raw, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestCoreToolDefinitionGolden(t *testing.T) {
	raw, e := os.ReadFile("testdata/core-tools.json")
	if e != nil {
		t.Fatal(e)
	}
	var golden struct {
		Tools []struct {
			Name        string
			Description string
			InputSchema map[string]any `json:"input_schema"`
			Effect      string
			Annotations struct{ ReadOnlyHint, DestructiveHint, IdempotentHint, OpenWorldHint bool }
		}
	}
	if e = json.Unmarshal(raw, &golden); e != nil {
		t.Fatal(e)
	}
	got := agentTools()
	if len(got) != len(golden.Tools) {
		t.Fatal("nine core tools required")
	}
	for i, want := range golden.Tools {
		var schema map[string]any
		_ = json.Unmarshal(got[i].InputSchema, &schema)
		if got[i].Name != want.Name || got[i].Description != want.Description || got[i].Effect != want.Effect || !reflect.DeepEqual(schema, want.InputSchema) {
			t.Errorf("core definition drift: %s", want.Name)
		}
		if (got[i].Effect == "read") != want.Annotations.ReadOnlyHint || (got[i].Effect == "destructive") != want.Annotations.DestructiveHint || want.Annotations.OpenWorldHint {
			t.Errorf("annotation drift: %s", want.Name)
		} // IdempotentHint is captured, but unavailable in manifest effect vocabulary (0137).
		expectedIdempotent := map[string]bool{"todo_update": true, "plan_update": true, "todo_list": true, "plan_list": true, "plan_get": true}
		if want.Annotations.IdempotentHint != expectedIdempotent[want.Name] {
			t.Errorf("idempotency capture drift: %s", want.Name)
		}
	}
}

func TestOptionalArgumentsAndDeclaredAuthority(t *testing.T) {
	p, _ := pluginFixture(t, true)
	for _, name := range []string{"todo_create", "plan_create", "todo_list", "plan_list"} {
		keys := []string{"description", "scope", "scope_id", "project_id", "parent_id", "status", "priority", "labels"}
		if name == "plan_create" {
			keys = []string{"description", "scope_id", "steps"}
		}
		for _, key := range keys {
			for _, v := range []any{nil, ""} {
				args := map[string]any{key: v}
				if strings.HasSuffix(name, "create") {
					args["title"] = "optional"
				}
				if name == "plan_create" {
					args["scope"] = "session"
				}
				if bad, text := callMCP(t, p, "session-a", name, args); bad {
					t.Errorf("%s %s=%v: %s", name, key, v, text)
				}
			}
		}
	}
	bad, text := callMCP(t, p, "session-a", "todo_create", map[string]any{"title": "authority", "status": "done", "metadata": "{\"owned\":true}"})
	if bad || !strings.Contains(text, `"status":"pending"`) {
		t.Fatal(text)
	}
	bad, text = callMCP(t, p, "session-a", "plan_create", map[string]any{"title": "authority plan", "scope": "session", "status": "approved", "steps": "[{\"id\":\"s1\",\"title\":\"Keep\"}]"})
	if bad {
		t.Fatal(text)
	}
	var row record
	_ = json.Unmarshal([]byte(text[strings.Index(text, "\n")+1:]), &row)
	id := row["id"]
	if row["status"] != "proposed" {
		t.Fatal(row)
	}
	bad, text = callMCP(t, p, "session-a", "plan_update", map[string]any{"id": id, "steps": "[]", "description": "wiped"})
	if bad {
		t.Fatal(text)
	}
	bad, text = callMCP(t, p, "session-a", "plan_get", map[string]any{"id": id})
	if bad || !strings.Contains(text, "Keep") || strings.Contains(text, "wiped") {
		t.Fatal(text)
	}
}
func TestMCPExplicitCrossScopeAndSessionless(t *testing.T) {
	p, _ := pluginFixture(t, true)
	bad, text := callMCP(t, p, "session-b", "todo_update", map[string]any{"id": "legacy", "status": "done"})
	if bad || !strings.Contains(text, `"scope_id":"session-a"`) {
		t.Fatal(text)
	}
	bad, text = callMCP(t, p, "session-b", "todo_list", map[string]any{"scope": "session", "scope_id": "session-a"})
	if bad || !strings.Contains(text, "Old todo") {
		t.Fatal(text)
	}
	bad, text = callMCP(t, p, "", "todo_list", nil)
	if bad || !strings.Contains(text, "Old todo") {
		t.Fatal(text)
	}
	bad, _ = callMCP(t, p, "", "todo_create", map[string]any{"title": "no coordinates"})
	if !bad {
		t.Fatal("sessionless create accepted")
	}
	bad, text = callMCP(t, p, "", "todo_update", map[string]any{"id": "legacy", "title": ""})
	if bad || !strings.Contains(text, `"title":""`) {
		t.Fatal(text)
	}
	bad, _ = callMCP(t, p, "ghost", "plan_get", map[string]any{"id": "any"})
	if !bad {
		t.Fatal("unknown calling session accepted")
	}
}

func TestHTTPPlanRoutesScopesAndStaleSync(t *testing.T) {
	p, _ := pluginFixture(t, true)
	r := httpCall(t, p, "POST", "work/sync", `{"todos_checked":["missing","legacy"],"plan_steps_checked":[{"plan_id":"gone","step_id":"s1"}]}`)
	requireStatus(t, r, 200)
	if !strings.Contains(string(r.Body), "skipped") {
		t.Fatal(string(r.Body))
	}
	requireStatus(t, httpCall(t, p, "GET", "todos/legacy", ""), 200)
	requireStatus(t, httpCall(t, p, "PUT", "todos/legacy", `{"status":""}`), 400)
	r = httpCall(t, p, "PATCH", "todos/legacy/scope", `{"scope":"project"}`)
	requireStatus(t, r, 200)
	r = httpCall(t, p, "PATCH", "todos/legacy/scope", `{"scope":"session"}`)
	requireStatus(t, r, 200)
	if strings.Contains(string(r.Body), "project_id") {
		t.Fatal("demote retained project", string(r.Body))
	}
	requireStatus(t, httpCall(t, p, "POST", "work/reorder", `{"items":[{"id":"legacy","sort_order":2}]}`), 200)
	r = httpCall(t, p, "POST", "plans", `{"title":"HTTP plan","scope":"session","steps":[{"id":"s1","title":"Step"}]}`)
	requireStatus(t, r, 201)
	var row record
	_ = json.Unmarshal(r.Body, &row)
	path := "plans/" + row["id"].(string)
	requireStatus(t, httpCall(t, p, "PUT", path+"/steps/s1", `{"status":"done"}`), 200)
	requireStatus(t, httpCall(t, p, "POST", path+"/steps", `{"steps":[{"title":"Next"}]}`), 200)
	requireStatus(t, httpCall(t, p, "GET", path, ""), 200)
	requireStatus(t, httpCall(t, p, "PUT", path, `{"title":"Renamed"}`), 200)
	requireStatus(t, httpCall(t, p, "POST", path+"/approve", ""), 200)
	requireStatus(t, httpCall(t, p, "DELETE", path, ""), 200)
	requireStatus(t, httpCall(t, p, "PUT", "plans/missing", `{"title":"x"}`), 404)
	requireStatus(t, httpCall(t, p, "POST", "plans/missing/approve", ""), 404)
	p.db.syncDirectory = func(*os.File) error { return fmt.Errorf("private /secret/path") }
	r = httpCall(t, p, "PUT", "todos/legacy", `{"title":"fails"}`)
	requireStatus(t, r, 500)
	if strings.Contains(string(r.Body), "secret") || strings.Contains(string(r.Body), "fsync") {
		t.Fatal("private error escaped", string(r.Body))
	}
}
func TestPrecisionAndMalformedLegacy(t *testing.T) {
	p, _ := pluginFixture(t, true)
	if _, err := p.ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := operation(t, p.db, "plan_create", map[string]any{"title": "numbers", "scope": "session", "steps": `[{"id":"s1","title":"step","status":"pending","big":9007199254740993,"exp":1e2,"decimal":1.50}]`}).(record)["id"].(string)
	operation(t, p.db, "plan_update", map[string]any{"id": id, "step_id": "s1", "status": "done"})
	state := readState(t, p.db)
	snap := state.Tables["plans"].Snapshot
	i, idx, _ := find(snap, id)
	raw := getCell(snap.Rows[i], idx, "steps")
	for _, n := range []string{"9007199254740993", "1e2", "1.50"} {
		if !strings.Contains(raw, n) {
			t.Fatal("number changed", raw)
		}
	}
	changeFixture(t, p.db, func(s *table) {
		v := s.Tables["todos"]
		idx, _ := columns(v.Snapshot)
		setCell(v.Snapshot.Rows[0], idx, "metadata", `{"big":9007199254740993,"exp":1e2}`)
		s.Tables["todos"] = v
		v = s.Tables["plans"]
		i, idx, _ := find(v.Snapshot, id)
		setCell(v.Snapshot.Rows[i], idx, "steps", "broken")
		s.Tables["plans"] = v
	})
	requireStatus(t, httpCall(t, p, "POST", "work/reorder", `{"items":[{"id":"legacy","sort_order":1}]}`), 200)
	state = readState(t, p.db)
	snap = state.Tables["todos"].Snapshot
	idx, _ = columns(snap)
	raw = getCell(snap.Rows[0], idx, "metadata")
	if !strings.Contains(raw, "9007199254740993") || !strings.Contains(raw, "1e2") {
		t.Fatal(raw)
	}
	requireStatus(t, httpCall(t, p, "POST", "plans/"+id+"/approve", ""), 200)
	changeFixture(t, p.db, func(s *table) {
		v := s.Tables["todos"]
		idx, _ := columns(v.Snapshot)
		setCell(v.Snapshot.Rows[0], idx, "metadata", "broken")
		s.Tables["todos"] = v
	})
	requireStatus(t, httpCall(t, p, "POST", "work/reorder", `{"items":[{"id":"legacy","sort_order":3}]}`), 200)
	id = operation(t, p.db, "plan_create", map[string]any{"title": "empty workspace", "scope": "workspace"}).(record)["id"].(string)
	requireStatus(t, httpCall(t, p, "POST", "plans/"+id+"/approve", `{"create_todos":true}`), 200)
}

func TestWriterChild(t *testing.T) {
	dir := os.Getenv("PLAN_WRITER_DIR")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "writer-ready-"+os.Getenv("PLAN_WRITER_ID")), nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "writer-start")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer start barrier timed out")
		}
		time.Sleep(time.Millisecond)
	}
	db := newDatabase(dir)
	for i := 0; i < 8; i++ {
		_, err := db.operate(context.Background(), "workspace-a", "todo_create", map[string]any{"title": fmt.Sprintf("process %s row %d", os.Getenv("PLAN_WRITER_ID"), i)}, "session-a", "project-a", "agent")
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestTwoProcessWriters(t *testing.T) {
	db, _ := databaseFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	children := []*exec.Cmd{}
	for i := 0; i < 2; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWriterChild$")
		cmd.Env = append(os.Environ(), "PLAN_WRITER_DIR="+db.dir, fmt.Sprintf("PLAN_WRITER_ID=%d", i), "GORACE=atexit_sleep_ms=0")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
	}
	for i := 0; i < 2; i++ {
		for {
			if _, err := os.Stat(filepath.Join(db.dir, fmt.Sprintf("writer-ready-%d", i))); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("writers did not reach start barrier")
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(db.dir, "writer-start"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	rows := operation(t, db, "todo_list", nil).([]record)
	if len(rows) != 17 {
		t.Fatal("lost cross-process writes", len(rows))
	}
}
func TestCrashTemporaryCleanupAndSymlinkRefusal(t *testing.T) {
	db, _ := databaseFixture(t)
	name := sourceFile("workspace-a")
	crash := filepath.Join(db.dir, name+"."+strings.Repeat("A", 26)+".tmp")
	unrelated := filepath.Join(db.dir, name+".unrelated.tmp")
	for _, path := range []string{crash, unrelated} {
		if e := os.WriteFile(path, []byte("crash"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	operation(t, newDatabase(db.dir), "todo_list", nil)
	if _, e := os.Stat(crash); !os.IsNotExist(e) {
		t.Fatal("crash file retained", e)
	}
	if _, e := os.Stat(unrelated); e != nil {
		t.Fatal("unrelated removed", e)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if e := os.WriteFile(target, []byte("sentinel"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(db.dir, name)); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, filepath.Join(db.dir, name)); e != nil {
		t.Fatal(e)
	}
	if _, e := newDatabase(db.dir).operate(t.Context(), "workspace-a", "todo_list", nil, "session-a", "project-a", "agent"); e == nil {
		t.Fatal("symlink accepted")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "sentinel" {
		t.Fatal("symlink target changed")
	}
}

func TestHandlerQuotasAndLegacyEdits(t *testing.T) {
	p, _ := pluginFixture(t, true)
	// New field growth is capped across text and JSON mutation routes.
	big := strings.Repeat("x", 65537)
	for _, body := range []map[string]any{{"description": big}, {"labels": []any{big}}, {"metadata": map[string]any{"value": big}}} {
		raw, _ := json.Marshal(body)
		requireStatus(t, httpCall(t, p, "PUT", "todos/legacy", string(raw)), 413)
	}
	raw, _ := json.Marshal(map[string]any{"title": big})
	requireStatus(t, httpCall(t, p, "POST", "todos", string(raw)), 413)
	requireStatus(t, httpCall(t, p, "POST", "todos", strings.Repeat(" ", 512<<10)+`{}`), 413)
	id := operation(t, p.db, "plan_create", map[string]any{"title": "Legacy large", "scope": "session"}).(record)["id"].(string)
	changeFixture(t, p.db, func(s *table) {
		v := s.Tables["todos"]
		idx, _ := columns(v.Snapshot)
		setCell(v.Snapshot.Rows[0], idx, "description", big)
		s.Tables["todos"] = v
		v = s.Tables["plans"]
		i, idx, _ := find(v.Snapshot, id)
		steps := `[{"id":"s1","title":"Large","status":"pending","notes":"` + strings.Repeat("x", 69000) + `"}]`
		setCell(v.Snapshot.Rows[i], idx, "steps", steps)
		s.Tables["plans"] = v
	})
	raw, _ = json.Marshal(map[string]any{"title": "rename", "description": big})
	requireStatus(t, httpCall(t, p, "PUT", "todos/legacy", string(raw)), 200)
	raw, _ = json.Marshal(map[string]any{"description": big[:65536]})
	requireStatus(t, httpCall(t, p, "PUT", "todos/legacy", string(raw)), 200)
	requireStatus(t, httpCall(t, p, "PUT", "plans/"+id+"/steps/s1", `{"status":"done"}`), 200)
	requireStatus(t, httpCall(t, p, "DELETE", "plans/"+id, ""), 200)
	// A valid imported table over the native row cap still permits edits/deletes.
	changeFixture(t, p.db, func(s *table) {
		v := s.Tables["todos"]
		idx, _ := columns(v.Snapshot)
		base := append([]pluginapi.DataCell{}, v.Snapshot.Rows[0]...)
		setCell(base, idx, "description", "")
		for i := len(v.Snapshot.Rows); i < 10000; i++ {
			row := append([]pluginapi.DataCell{}, base...)
			setCell(row, idx, "id", fmt.Sprintf("quota-%d", i))
			v.Snapshot.Rows = append(v.Snapshot.Rows, row)
		}
		s.Tables["todos"] = v
	})
	requireStatus(t, httpCall(t, p, "POST", "todos", `{"title":"over row cap"}`), 413)
	requireStatus(t, httpCall(t, p, "PUT", "todos/legacy", `{"status":"done"}`), 200)
	requireStatus(t, httpCall(t, p, "DELETE", "todos/legacy", ""), 200)
	// Public lists pack whole records below the 2 MiB response cap; MCP instead
	// reports the bound and asks the caller to narrow its filters.
	changeFixture(t, p.db, func(s *table) {
		v := s.Tables["todos"]
		v.Snapshot.Rows = v.Snapshot.Rows[:100]
		idx, _ := columns(v.Snapshot)
		for _, row := range v.Snapshot.Rows {
			setCell(row, idx, "description", strings.Repeat("x", 30000))
		}
		s.Tables["todos"] = v
	})
	r := httpCall(t, p, "GET", "todos", "")
	requireStatus(t, r, 200)
	if len(r.Body) > maxResponseBytes || !strings.Contains(string(r.Body), `"more":true`) {
		t.Fatal("response not bounded", len(r.Body))
	}
	bad, text := callMCP(t, p, "session-a", "todo_list", nil)
	if !bad || !strings.Contains(text, "2 MiB") {
		t.Fatal("MCP response limit", bad, len(text))
	}
}

// Expected text was captured by running CURRENT core's handlers and services,
// rather than constructed from this plugin. Only documented transport/storage
// differences are normalized: IDs, timestamps, project attribution and parsed
// JSON fields outside todo_update. Errors and human text remain exact.
func normalizeGolden(t *testing.T, name, text string) string {
	t.Helper()
	start := strings.Index(text, "\n{")
	prefix := ""
	if strings.HasPrefix(text, "{") {
		start = 0
	} else if start >= 0 {
		prefix = text[:start+1]
		start++
	} else {
		return text
	}
	var row map[string]any
	if err := decodeNumbers(text[start:], &row); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"created_at", "updated_at", "project_id"} {
		delete(row, key)
	}
	if name != "todo_update" {
		for _, key := range []string{"labels", "metadata", "steps"} {
			if s, ok := row[key].(string); ok {
				var value any
				if err := decodeNumbers(s, &value); err != nil {
					t.Fatal(err)
				}
				row[key] = value
			}
		}
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return prefix + string(raw)
}
func TestCoreOutputGolden(t *testing.T) {
	raw, e := os.ReadFile("testdata/core-outputs.json")
	if e != nil {
		t.Fatal(e)
	}
	var samples []struct {
		Name    string
		Args    map[string]any
		Text    string
		IsError bool `json:"is_error"`
	}
	if e = json.Unmarshal(raw, &samples); e != nil {
		t.Fatal(e)
	}
	p, _ := pluginFixture(t, true)
	if _, e = p.ready(t.Context()); e != nil {
		t.Fatal(e)
	}
	operation(t, p.db, "todo_delete", map[string]any{"id": "legacy"})
	ids := map[string]string{}
	for _, sample := range samples {
		args := map[string]any{}
		for key, value := range sample.Args {
			if s, ok := value.(string); ok && ids[s] != "" {
				value = ids[s]
			}
			args[key] = value
		}
		bad, text := callMCP(t, p, "session-a", sample.Name, args)
		if strings.HasSuffix(sample.Name, "_create") && !bad {
			var row record
			_ = json.Unmarshal([]byte(text[strings.Index(text, "\n")+1:]), &row)
			token := "$todo"
			if sample.Name == "plan_create" {
				token = "$plan"
			}
			ids[token] = row["id"].(string)
		}
		for token, id := range ids {
			text = strings.ReplaceAll(text, id, token)
		}
		if bad != sample.IsError || normalizeGolden(t, sample.Name, text) != normalizeGolden(t, sample.Name, sample.Text) {
			t.Errorf("core output drift %s args=%v\ngot %s\nwant %s", sample.Name, sample.Args, text, sample.Text)
		}
	}
}
