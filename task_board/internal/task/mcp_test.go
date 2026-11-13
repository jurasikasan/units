package task

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// mcpFake is fakeRepo plus the writes the MCP tools reach and the service tests
// do not. It records what arrived instead of storing it: the adapter's job is
// to hand the service the right arguments, and that is what these tests read.
type mcpFake struct {
	*fakeRepo
	gotFilter ListFilter
	gotCreate *Task
}

// Create records what the adapter handed the service instead of storing it,
// like SetAssignee's override: the adapter's job is the right arguments.
func (f *mcpFake) Create(_ context.Context, t *Task) (*Task, error) {
	f.gotCreate = t
	return t, nil
}

func (f *mcpFake) List(_ context.Context, lf ListFilter) ([]Task, error) {
	f.gotFilter = lf
	var out []Task
	for _, t := range f.tasks {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle.Number < out[j].Handle.Number })
	return out, nil
}

func (f *mcpFake) SetAssignee(_ context.Context, id uuid.UUID, a *string) (*Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	t.Assignee = a
	return f.withDerived(id)
}

// connect runs the board's MCP server over an in-memory pipe, so the calls go
// through the SDK's schema derivation and output validation exactly as they do
// behind /mcp — the part a direct call to the handler methods would skip.
func connect(t *testing.T, f *mcpFake) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewMCPServer(&Service{repo: f}).Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err, "a tool's refusal must come back as a tool error, not a protocol one")
	var text []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text = append(text, tc.Text)
		}
	}
	return res, strings.Join(text, "\n")
}

func TestMCPToolSurface(t *testing.T) {
	cs := connect(t, &mcpFake{fakeRepo: newFakeRepo()})
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	require.Equal(t, []string{
		"append_to_description", "create_task", "edit_task", "get_task",
		"give_up", "list_tasks", "set_assignee", "set_status",
	}, names)
}

// A model speaks handles; the move must land on the task it named, and a move
// the flow forbids must reach the model as the service's own sentence, which
// says what was allowed instead.
func TestMCPSetStatusByHandle(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusTodo)
	// The test is about the handle lookup and the flow refusal, not the
	// assignee gate: the task has an owner, so IN_PROGRESS is legal for it.
	who := "alisa"
	f.fakeRepo.tasks[task.ID].Assignee = &who
	cs := connect(t, f)

	res, text := call(t, cs, "set_status", map[string]any{"task": "ABC-7", "status": StatusInProgress})
	require.False(t, res.IsError, text)
	require.Equal(t, StatusInProgress, f.tasks[task.ID].Status)
	var v TaskView
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	require.Equal(t, "ABC-7", v.Handle)
	require.Equal(t, task.ID.String(), v.ID)

	res, text = call(t, cs, "set_status", map[string]any{"task": "ABC-7", "status": StatusDone})
	require.True(t, res.IsError)
	require.Contains(t, text, "cannot go to DONE")
	require.Equal(t, StatusInProgress, f.tasks[task.ID].Status)
}

// from goes through to the service: a claim made by a model is refused the
// same way as one made over HTTP.
func TestMCPSetStatusWithFrom(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusInProgress)
	cs := connect(t, f)

	res, text := call(t, cs, "set_status", map[string]any{"task": "ABC-7", "status": StatusInProgress, "from": StatusTodo})
	require.True(t, res.IsError, text)
	require.Contains(t, text, "not TODO")

	res, text = call(t, cs, "set_status", map[string]any{"task": "ABC-7", "status": StatusQA, "from": StatusInProgress})
	require.False(t, res.IsError, text)
	require.Equal(t, StatusQA, f.tasks[task.ID].Status)
}

func TestMCPUnknownTask(t *testing.T) {
	cs := connect(t, &mcpFake{fakeRepo: newFakeRepo()})
	res, text := call(t, cs, "get_task", map[string]any{"task": "ABC-404"})
	require.True(t, res.IsError)
	require.Contains(t, text, "not found")

	res, text = call(t, cs, "get_task", map[string]any{"task": "four-oh-four"})
	require.True(t, res.IsError)
	require.Contains(t, text, "handle like ABC-12 or a UUID")
}

func TestMCPListTasksFilters(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	seed(f.fakeRepo, "ABC-1", StatusTodo)
	seed(f.fakeRepo, "ABC-2", StatusQA)
	cs := connect(t, f)

	res, text := call(t, cs, "list_tasks", map[string]any{"project": "abc", "assignee": "none", "ready": true})
	require.False(t, res.IsError, text)
	require.Equal(t, "ABC", f.gotFilter.Project)
	require.True(t, f.gotFilter.Unassigned)
	require.True(t, f.gotFilter.Ready)

	var list TaskList
	require.NoError(t, json.Unmarshal([]byte(text), &list))
	require.Len(t, list.Tasks, 2)
	require.Equal(t, "ABC-1", list.Tasks[0].Handle)

	_, _ = call(t, cs, "list_tasks", map[string]any{"query": "ABC-2"})
	require.NotNil(t, f.gotFilter.Handle, "a handle in query is a lookup, not a goal search")

	res, text = call(t, cs, "list_tasks", map[string]any{"ready": true, "status": "QA"})
	require.True(t, res.IsError)
	require.Contains(t, text, "drop one of them")
}

// origin is optional: an outside caller has no conversation to point at, and
// a task born without one has none — there is no default chat.
func TestMCPCreateTaskOriginIsOptional(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	cs := connect(t, f)

	res, text := call(t, cs, "create_task", map[string]any{
		"project": "ABC", "goal": "g", "acceptance_criteria": "a", "actor": "skill",
	})
	require.False(t, res.IsError, text)
	require.NotNil(t, f.gotCreate)
	require.Nil(t, f.gotCreate.Origin)

	res, text = call(t, cs, "create_task", map[string]any{
		"project": "ABC", "goal": "g", "acceptance_criteria": "a", "actor": "skill",
		"origin": "chat-1",
	})
	require.False(t, res.IsError, text)
	require.Equal(t, "chat-1", *f.gotCreate.Origin)
}

// tools/list is what an outside agent reads before it calls: the schema must
// not demand origin, while actor stays required — who speaks is a
// choice the caller makes, inside and outside the system alike. And a field
// that is "filled in by the system" says so only for the inside; the outside
// caller must see the rest in the same sentence.
func TestMCPCreateTaskSchemaForOutsideCallers(t *testing.T) {
	cs := connect(t, &mcpFake{fakeRepo: newFakeRepo()})
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var create *mcp.Tool
	for _, tl := range res.Tools {
		if tl.Name == "create_task" {
			create = tl
		}
	}
	require.NotNil(t, create)

	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	data, err := json.Marshal(create.InputSchema)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &schema))
	for _, r := range schema.Required {
		require.NotEqual(t, "origin", r)
	}
	require.Contains(t, schema.Required, "actor")

	for _, field := range []string{"actor", "origin"} {
		prop, ok := schema.Properties[field].(map[string]any)
		require.True(t, ok, field)
		desc, _ := prop["description"].(string)
		require.Contains(t, desc, "Filled in by the system", field)
		require.Contains(t, desc, "outside the system", field)
	}
}

func TestMCPSetAssignee(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusTodo)
	cs := connect(t, f)
	res, text := call(t, cs, "set_assignee", map[string]any{"task": "ABC-7", "assignee": "alisa"})
	require.False(t, res.IsError, text)
	require.Equal(t, "alisa", *f.tasks[task.ID].Assignee)

	res, text = call(t, cs, "set_assignee", map[string]any{"task": "ABC-7", "assignee": "none"})
	require.False(t, res.IsError, text)
	require.Nil(t, f.tasks[task.ID].Assignee)
}

// edit_task replaces whole, per field: the fields sent are the new text, the
// fields not sent are left alone — the model must not have to retype what is
// already on the task.
func TestMCPEditTask(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusTodo)
	f.tasks[task.ID].Description = "keep me"
	f.tasks[task.ID].AcceptanceCriteria = "old criteria"
	cs := connect(t, f)

	res, text := call(t, cs, "edit_task", map[string]any{"task": "ABC-7", "goal": "new goal"})
	require.False(t, res.IsError, text)
	require.Equal(t, "new goal", f.tasks[task.ID].Goal)
	require.Equal(t, "keep me", f.tasks[task.ID].Description)
	require.Equal(t, "old criteria", f.tasks[task.ID].AcceptanceCriteria)

	var v TaskView
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	require.Equal(t, "new goal", v.Goal)
	require.Equal(t, "keep me", v.Description)

	// All three at once, by UUID: still per-field replacement.
	desc := "new description"
	accept := "new criteria"
	res, text = call(t, cs, "edit_task", map[string]any{
		"task": task.ID.String(), "goal": "g2", "description": desc, "acceptance_criteria": accept,
	})
	require.False(t, res.IsError, text)
	require.Equal(t, "g2", f.tasks[task.ID].Goal)
	require.Equal(t, desc, f.tasks[task.ID].Description)
	require.Equal(t, accept, f.tasks[task.ID].AcceptanceCriteria)
}

// An edit with nothing to change is the service's empty-patch refusal, naming
// the fields this endpoint accepts — and nothing on the task may move.
func TestMCPEditTaskRefusesEmptyEdit(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusTodo)
	f.tasks[task.ID].Goal = "unchanged goal"
	f.tasks[task.ID].Description = "unchanged description"
	cs := connect(t, f)

	res, text := call(t, cs, "edit_task", map[string]any{"task": "ABC-7"})
	require.True(t, res.IsError)
	require.Contains(t, text, "goal, description, acceptance_criteria")

	res, text = call(t, cs, "edit_task", map[string]any{
		"task": "ABC-7", "goal": "  ", "description": "nope",
	})
	require.True(t, res.IsError, "a blank goal is refused, not stored")
	require.Contains(t, text, "goal cannot be blank")

	require.Equal(t, "unchanged goal", f.tasks[task.ID].Goal)
	require.Equal(t, "unchanged description", f.tasks[task.ID].Description)
}

// Descriptions grow in dated layers: append_to_description adds a paragraph
// below what is there, trimmed, instead of the model retyping the tail.
func TestMCPAppendToDescription(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusTodo)
	cs := connect(t, f)

	f.tasks[task.ID].Description = "A"
	res, text := call(t, cs, "append_to_description", map[string]any{"task": "ABC-7", "text": "B"})
	require.False(t, res.IsError, text)
	require.Equal(t, "A\n\nB", f.tasks[task.ID].Description)
	var v TaskView
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	require.Equal(t, "A\n\nB", v.Description)

	// Trailing whitespace on the old text and padding on the new one stay out.
	f.tasks[task.ID].Description = "A\n\n"
	res, text = call(t, cs, "append_to_description", map[string]any{"task": "ABC-7", "text": "  B  "})
	require.False(t, res.IsError, text)
	require.Equal(t, "A\n\nB", f.tasks[task.ID].Description)

	// An empty description is just the new text.
	f.tasks[task.ID].Description = ""
	res, text = call(t, cs, "append_to_description", map[string]any{"task": "ABC-7", "text": "B"})
	require.False(t, res.IsError, text)
	require.Equal(t, "B", f.tasks[task.ID].Description)

	// Appending onto a UUID-named task works too.
	res, text = call(t, cs, "append_to_description", map[string]any{"task": task.ID.String(), "text": "C"})
	require.False(t, res.IsError, text)
	require.Equal(t, "B\n\nC", f.tasks[task.ID].Description)
}

// Blank text is an error, not a blank paragraph: nothing must be written.
func TestMCPAppendToDescriptionRefusesBlankText(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusTodo)
	f.tasks[task.ID].Description = "A"
	cs := connect(t, f)

	res, _ := call(t, cs, "append_to_description", map[string]any{"task": "ABC-7", "text": "   "})
	require.True(t, res.IsError)
	require.Equal(t, "A", f.tasks[task.ID].Description)

	res, _ = call(t, cs, "append_to_description", map[string]any{"task": "ABC-7", "text": ""})
	require.True(t, res.IsError)
	require.Equal(t, "A", f.tasks[task.ID].Description)
}

// The board keeps the nick as typed, folded: case and a leading @ are how a
// nick gets typed. A UUID is refused, so a caller still sending ids fails.
func TestMCPAssigneeByNick(t *testing.T) {
	f := &mcpFake{fakeRepo: newFakeRepo()}
	task := seed(f.fakeRepo, "ABC-7", StatusTodo)
	cs := connect(t, f)

	res, text := call(t, cs, "list_tasks", map[string]any{"assignee": "@Skill"})
	require.False(t, res.IsError, text)
	require.Equal(t, "skill", f.gotFilter.Assignee)

	for _, typed := range []string{"SKILL", "@skill", " Skill "} {
		f.tasks[task.ID].Assignee = nil
		res, text = call(t, cs, "set_assignee", map[string]any{"task": "ABC-7", "assignee": typed})
		require.False(t, res.IsError, text)
		require.Equal(t, "skill", *f.tasks[task.ID].Assignee, typed)
	}

	res, text = call(t, cs, "set_assignee", map[string]any{"task": "ABC-7", "assignee": uuid.NewString()})
	require.True(t, res.IsError)
	require.Contains(t, text, "names subjects by nick")
}
