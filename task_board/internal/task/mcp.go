package task

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The board's agent interface: the same Service the HTTP handlers call, offered
// as MCP tools so that an agent — behind an MCP aggregator or connected
// directly — can work the board without an HTTP client of its own.
// It is an adapter like handler.go and holds no rule of its own — every refusal
// a caller sees here is the service's sentence, because that sentence is the
// model's repair signal.
//
// Who acts is an argument (actor_id), not a header. An aggregator may forward
// every call over one shared session, so nothing about the original caller
// survives the hop except the arguments. The host running the agent is
// expected to overwrite actor_id with the subject whose turn it is, so a model
// cannot speak as somebody else by filling the field in. Only the tools that
// record a writer take it.
//
// Every output here is a view with plain strings rather than Task itself: the
// SDK derives an output schema from the Go type and validates the marshalled
// JSON against it, and Task's uuid.UUID and Handle marshal to strings while
// their Go shapes are a byte array and a struct.

// NewMCPServer builds the board's MCP server over svc.
func NewMCPServer(svc *Service) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "task_board", Version: "1.0.0"}, nil)
	b := &mcpBoard{svc: svc}

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_tasks",
		Description: "List tasks on the board, one line each: handle, status, goal, assignee, and what blocks it. " +
			"Without filters it returns the whole board, newest first. ready=true means approved (TODO), no open " +
			"dependency and no open subtask; it cannot be combined with status.",
	}, b.listTasks)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_task",
		Description: "Read one task in full: goal, description, acceptance criteria, status, people, dependencies.",
	}, b.getTask)
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_task",
		Description: "File a new task. acceptance_criteria is required: it is what QA checks the result against. " +
			"The new task starts in DRAFT. actor (the reporter, by nick) is filled in by the system for agents inside " +
			"it; outside the system, put in the subject you are speaking for. origin, the conversation the " +
			"task is born in, is optional.",
	}, b.createTask)
	mcp.AddTool(s, &mcp.Tool{
		Name: "give_up",
		Description: "Give up on a task you are working on: it goes back to TODO and to its reporter, and the reason " +
			"is written in its thread. Use it when you cannot finish — say what you tried and what stopped you. " +
			"Only for a task IN_PROGRESS. actor_id is filled in by the system for agents inside it; outside " +
			"the system, put in the subject who gives up.",
	}, b.giveUp)
	mcp.AddTool(s, &mcp.Tool{
		Name: "set_status",
		Description: "Move a task: DRAFT → TODO → IN_PROGRESS → QA → UAT → DONE, back from QA or UAT to IN_PROGRESS, " +
			"QA → DONE when a machine checked the criteria, CANCELLED from anywhere but DONE. " +
			"A refused move says which moves were allowed.",
	}, b.setStatus)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_assignee",
		Description: "Hand a task to a subject, by nick, or to nobody with \"none\".",
	}, b.setAssignee)
	mcp.AddTool(s, &mcp.Tool{
		Name: "edit_task",
		Description: "Replace a task's goal, description or acceptance criteria — each one you send " +
			"replaces the old text whole; to add to a description, use append_to_description.",
	}, b.editTask)
	mcp.AddTool(s, &mcp.Tool{
		Name: "append_to_description",
		Description: "Add a paragraph to a task's description, below what is there. Use it to record " +
			"what changed or what was found, instead of rewriting the description.",
	}, b.appendDescription)
	return s
}

// schedulerID is the fixed id of the system subject ("Scheduler") that the
// board's own lines are said by, such as the parking line.
var schedulerID = uuid.MustParse("00000000-0000-0000-0000-000000000003")

type mcpBoard struct{ svc *Service }

// TaskRef names one task, by handle (ABC-12) or UUID.
type TaskRef struct {
	Task string `json:"task" jsonschema:"the task's handle, like ABC-12, or its UUID"`
}

// resolve accepts a handle as well as a UUID, unlike the HTTP mutations, which
// take UUIDs only. There the reason is DELETE: a mistyped handle that parses
// lands on a different real task, unrecoverably. Nothing destructive is offered
// here, and handles are the names the board is discussed by.
func (b *mcpBoard) resolve(ctx context.Context, ref string) (*Task, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		return b.svc.Get(ctx, id)
	}
	h, err := ParseHandle(ref)
	if err != nil {
		return nil, fmt.Errorf("%w — task must be a handle like ABC-12 or a UUID", err)
	}
	return b.svc.GetByHandle(ctx, h)
}

// TaskLine is one row of list_tasks.
type TaskLine struct {
	Handle    string   `json:"handle"`
	Status    string   `json:"status"`
	Goal      string   `json:"goal"`
	Assignee  string   `json:"assignee,omitempty"`
	BlockedBy []string `json:"blocked_by,omitempty"`
	// OpenSubtasks keep a parent out of ready, like BlockedBy does.
	OpenSubtasks []string `json:"open_subtasks,omitempty"`
}

// TaskView is a whole task as get_task and the writes return it.
type TaskView struct {
	ID                 string   `json:"id"`
	Handle             string   `json:"handle"`
	Status             string   `json:"status"`
	Goal               string   `json:"goal"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	Reporter           string   `json:"reporter"`
	Assignee           string   `json:"assignee,omitempty"`
	ParentTaskID       string   `json:"parent_task_id,omitempty"`
	Points             int      `json:"points,omitempty"`
	Discussion         string   `json:"discussion,omitempty"`
	BlockedBy          []string `json:"blocked_by,omitempty"`
	OpenSubtasks       []string `json:"open_subtasks,omitempty"`
	SummaryReport      string   `json:"summary_report,omitempty"`
}

func optID(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func optNick(n *string) string {
	if n == nil {
		return ""
	}
	return *n
}

func handles(hs []Handle) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.String())
	}
	return out
}

func viewOf(t *Task) TaskView {
	v := TaskView{
		ID:                 t.ID.String(),
		Handle:             t.Handle.String(),
		Status:             t.Status,
		Goal:               t.Goal,
		Description:        t.Description,
		AcceptanceCriteria: t.AcceptanceCriteria,
		Reporter:           t.Reporter,
		Assignee:           optNick(t.Assignee),
		ParentTaskID:       optID(t.ParentTaskID),
		Discussion:         optNick(t.Discussion),
		BlockedBy:          handles(t.BlockedBy),
		OpenSubtasks:       handles(t.OpenSubtasks),
	}
	if t.SummaryReport != nil {
		v.SummaryReport = *t.SummaryReport
	}
	if t.Points != nil {
		v.Points = *t.Points
	}
	return v
}

// ListTasksParams narrows list_tasks. Every field is optional.
type ListTasksParams struct {
	Project  string `json:"project,omitempty" jsonschema:"project key, like ABC"`
	Status   string `json:"status,omitempty" jsonschema:"comma-separated statuses, like QA,UAT"`
	Assignee string `json:"assignee,omitempty" jsonschema:"a subject's nick, or none for unassigned tasks"`
	Ready    bool   `json:"ready,omitempty" jsonschema:"only TODO tasks with no open dependency and no open subtask"`
	Query    string `json:"query,omitempty" jsonschema:"a handle, or words to find in goals"`
}

// TaskList is list_tasks' answer. An object rather than a bare array because
// MCP structured output must be one.
type TaskList struct {
	Tasks []TaskLine `json:"tasks"`
}

func (b *mcpBoard) listTasks(ctx context.Context, _ *mcp.CallToolRequest, in ListTasksParams) (*mcp.CallToolResult, TaskList, error) {
	f := ListFilter{Order: OrderCreated, Ready: in.Ready}
	if p := strings.TrimSpace(in.Project); p != "" {
		f.Project = strings.ToUpper(p)
		if !ValidProjectKey(f.Project) {
			return nil, TaskList{}, fmt.Errorf("%w: %q is not a project key", ErrInvalid, p)
		}
	}
	if s := strings.TrimSpace(in.Status); s != "" {
		statuses, err := parseStatusList(s)
		if err != nil {
			return nil, TaskList{}, err
		}
		f.Statuses = statuses
	}
	switch a := strings.TrimSpace(in.Assignee); a {
	case "":
	case "none":
		f.Unassigned = true
	default:
		n, err := nick("assignee", a)
		if err != nil {
			return nil, TaskList{}, err
		}
		f.Assignee = n
	}
	f.Handle, f.GoalLike = parseSearch(in.Query)

	tasks, err := b.svc.List(ctx, f)
	if err != nil {
		return nil, TaskList{}, err
	}
	out := TaskList{Tasks: make([]TaskLine, 0, len(tasks))}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, TaskLine{
			Handle:       t.Handle.String(),
			Status:       t.Status,
			Goal:         t.Goal,
			Assignee:     optNick(t.Assignee),
			BlockedBy:    handles(t.BlockedBy),
			OpenSubtasks: handles(t.OpenSubtasks),
		})
	}
	return nil, out, nil
}

func (b *mcpBoard) getTask(ctx context.Context, _ *mcp.CallToolRequest, in TaskRef) (*mcp.CallToolResult, TaskView, error) {
	t, err := b.resolve(ctx, in.Task)
	if err != nil {
		return nil, TaskView{}, err
	}
	return nil, viewOf(t), nil
}

// CreateTaskParams is create_task's input. actor becomes the reporter.
type CreateTaskParams struct {
	Project            string `json:"project" jsonschema:"project key, like ABC"`
	Goal               string `json:"goal" jsonschema:"one line: what the task achieves"`
	AcceptanceCriteria string `json:"acceptance_criteria" jsonschema:"what QA checks the result against"`
	Description        string `json:"description,omitempty" jsonschema:"context, addresses in the code, what was already found"`
	Parent             string `json:"parent,omitempty" jsonschema:"parent task, handle or UUID"`
	Assignee           string `json:"assignee,omitempty" jsonschema:"the subject to hand it to, by nick"`
	Points             int    `json:"points,omitempty" jsonschema:"the estimate, like story points: 1 is a small job; leave out if not estimated"`
	Origin             string `json:"origin,omitempty" jsonschema:"the conversation the task is born in. Filled in by the system for agents inside it; outside the system, leave it out."`
	Actor              string `json:"actor" jsonschema:"who files it, by nick. Filled in by the system for agents inside it; outside the system, the subject you are speaking for."`
}

func (b *mcpBoard) createTask(ctx context.Context, _ *mcp.CallToolRequest, in CreateTaskParams) (*mcp.CallToolResult, TaskView, error) {
	req := CreateRequest{
		ProjectKey:         in.Project,
		Goal:               in.Goal,
		Description:        in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria,
		Reporter:           in.Actor,
	}
	if o := strings.TrimSpace(in.Origin); o != "" {
		req.Origin = &o
	}
	if in.Points != 0 {
		req.Points = &in.Points
	}
	if a := strings.TrimSpace(in.Assignee); a != "" {
		req.Assignee = &a
	}
	if p := strings.TrimSpace(in.Parent); p != "" {
		parent, err := b.resolve(ctx, p)
		if err != nil {
			return nil, TaskView{}, fmt.Errorf("parent: %w", err)
		}
		id := parent.ID.String()
		req.ParentTaskID = &id
	}
	t, err := b.svc.Create(ctx, req)
	if err != nil {
		return nil, TaskView{}, err
	}
	return nil, viewOf(t), nil
}

// SetStatusParams is set_status's input.
type SetStatusParams struct {
	Task   string `json:"task" jsonschema:"the task's handle, like ABC-12, or its UUID"`
	Status string `json:"status" jsonschema:"DRAFT, TODO, IN_PROGRESS, QA, UAT, DONE or CANCELLED"`
	From   string `json:"from,omitempty" jsonschema:"optional: the status you expect the task to be in now; the move is refused if it is elsewhere"`
}

func (b *mcpBoard) setStatus(ctx context.Context, _ *mcp.CallToolRequest, in SetStatusParams) (*mcp.CallToolResult, TaskView, error) {
	t, err := b.resolve(ctx, in.Task)
	if err != nil {
		return nil, TaskView{}, err
	}
	req := StatusRequest{Status: in.Status}
	if in.From != "" {
		req.From = &in.From
	}
	t, err = b.svc.SetStatus(ctx, t.ID, req)
	if err != nil {
		return nil, TaskView{}, err
	}
	return nil, viewOf(t), nil
}

// GiveUpParams is give_up's input.
type GiveUpParams struct {
	Task    string `json:"task" jsonschema:"the task's handle, like ABC-12, or its UUID"`
	Reason  string `json:"reason" jsonschema:"what you tried and what stopped you"`
	ActorID string `json:"actor_id" jsonschema:"who gives up. Filled in by the system for agents inside it; outside the system, the subject who gives up."`
}

func (b *mcpBoard) giveUp(ctx context.Context, _ *mcp.CallToolRequest, in GiveUpParams) (*mcp.CallToolResult, TaskView, error) {
	t, err := b.resolve(ctx, in.Task)
	if err != nil {
		return nil, TaskView{}, err
	}
	t, err = b.svc.Return(ctx, t.ID, ReturnRequest{ActorID: in.ActorID, Reason: in.Reason})
	if err != nil {
		return nil, TaskView{}, err
	}
	return nil, viewOf(t), nil
}

// SetAssigneeParams is set_assignee's input.
type SetAssigneeParams struct {
	Task     string `json:"task" jsonschema:"the task's handle, like ABC-12, or its UUID"`
	Assignee string `json:"assignee" jsonschema:"a subject's nick, or none to unassign"`
}

// EditTaskParams is edit_task's input. A pointer means "sent": each field that
// is present replaces the old text whole, and one that is absent is left alone.
// Points, labels, component and discussion stay out of the agent's reach;
// status and assignee have their own tools.
type EditTaskParams struct {
	Task               string  `json:"task" jsonschema:"the task's handle, like ABC-12, or its UUID"`
	Goal               *string `json:"goal,omitempty" jsonschema:"the new goal, replacing the current one"`
	Description        *string `json:"description,omitempty" jsonschema:"the new description, replacing the current one"`
	AcceptanceCriteria *string `json:"acceptance_criteria,omitempty" jsonschema:"the new acceptance criteria, replacing the current one"`
}

func (b *mcpBoard) editTask(ctx context.Context, _ *mcp.CallToolRequest, in EditTaskParams) (*mcp.CallToolResult, TaskView, error) {
	t, err := b.resolve(ctx, in.Task)
	if err != nil {
		return nil, TaskView{}, err
	}
	// The fields not sent stay nil, and Service.Patch's empty-patch refusal is
	// the sentence that names what this endpoint accepts — the same sentence
	// the HTTP endpoint gives over a blank body.
	t, err = b.svc.Patch(ctx, t.ID, PatchRequest{
		Goal:               in.Goal,
		Description:        in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria,
	})
	if err != nil {
		return nil, TaskView{}, err
	}
	return nil, viewOf(t), nil
}

// AppendDescriptionParams is append_to_description's input.
type AppendDescriptionParams struct {
	Task string `json:"task" jsonschema:"the task's handle, like ABC-12, or its UUID"`
	Text string `json:"text" jsonschema:"the paragraph to add, below what is there"`
}

// Descriptions on this board grow in dated layers, and the edit endpoint
// replaces whole: a model asked to add a paragraph must not retype the layers
// above it and lose the tail without noticing. So the append is read, trimmed,
// and written back through the same Patch that HTTP uses.
func (b *mcpBoard) appendDescription(ctx context.Context, _ *mcp.CallToolRequest, in AppendDescriptionParams) (*mcp.CallToolResult, TaskView, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, TaskView{}, fmt.Errorf("%w: text is empty — there is no paragraph to add", ErrInvalid)
	}
	t, err := b.resolve(ctx, in.Task)
	if err != nil {
		return nil, TaskView{}, err
	}
	next := strings.TrimRight(t.Description, " \t\r\n")
	if next != "" {
		next = next + "\n\n" + text
	} else {
		next = text
	}
	t, err = b.svc.Patch(ctx, t.ID, PatchRequest{Description: &next})
	if err != nil {
		return nil, TaskView{}, err
	}
	return nil, viewOf(t), nil
}

func (b *mcpBoard) setAssignee(ctx context.Context, _ *mcp.CallToolRequest, in SetAssigneeParams) (*mcp.CallToolResult, TaskView, error) {
	t, err := b.resolve(ctx, in.Task)
	if err != nil {
		return nil, TaskView{}, err
	}
	var req AssigneeRequest
	if a := strings.TrimSpace(in.Assignee); a != "none" {
		req.Assignee = &a
	}
	t, err = b.svc.SetAssignee(ctx, t.ID, req)
	if err != nil {
		return nil, TaskView{}, err
	}
	return nil, viewOf(t), nil
}
