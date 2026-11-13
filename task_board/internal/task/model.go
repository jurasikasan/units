package task

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Status vocabulary. Mirrored by a CHECK constraint in internal/db/migrations —
// both exist on purpose: the CHECK is what no writer can route around, this is
// what gives a caller a 400 with a readable message instead of a 500 from
// Postgres.
//
// Review is two states, not one: QA checks the result against
// acceptance_criteria, UAT is acceptance by whoever asked for the work. There
// is no BLOCKED — being stuck is a change of assignee plus a conversation, not
// a state of the work.
const (
	StatusDraft      = "DRAFT"
	StatusTodo       = "TODO"
	StatusInProgress = "IN_PROGRESS"
	StatusQA         = "QA"
	StatusUAT        = "UAT"
	StatusDone       = "DONE"
	StatusCancelled  = "CANCELLED"
)

// transitions is the flow, written down in the one place that decides it.
// Validating only that a word is in the vocabulary, and not where it may be
// used, lets DRAFT → DONE go through without a sound.
//
// Backward edges are here deliberately: QA that fails sends work back to
// IN_PROGRESS, UAT that rejects does the same. A flow that only moves forward
// forces people to lie about where the work is.
var transitions = map[string][]string{
	StatusDraft: {StatusTodo, StatusCancelled},
	StatusTodo:  {StatusInProgress, StatusDraft, StatusCancelled},
	// IN_PROGRESS → TODO is not here on purpose. Putting work back
	// in the queue is only done by the service, never by a status write: Return
	// (which hands the task to its reporter and says why in the thread), and
	// parking (which SetDependencies performs when an open dependency lands
	// on work in progress). A bare move would leave the task TODO and still
	// assigned to whoever dropped it, and a dispatcher would hand it
	// straight back to them — for parked work that is the wanted behaviour,
	// which is exactly why the move stays closed to clients and is made by
	// these service paths instead.
	StatusInProgress: {StatusQA, StatusCancelled},
	// QA → DONE skips UAT on purpose: work whose acceptance criteria a machine
	// can check is closed on that evidence. UAT is for what only a person can
	// judge by looking; walking a task through it in the same second records a
	// review that never happened.
	StatusQA:  {StatusUAT, StatusDone, StatusInProgress, StatusCancelled},
	StatusUAT: {StatusDone, StatusInProgress, StatusCancelled},

	// Terminal. Undoing finished work is rewriting history rather than changing
	// a plan — the honest move is a new task that says what actually happened.
	StatusDone: {},

	// Not terminal: cancelling by mistake happens, and the alternative — a new
	// task — throws away the chat, which is where all the working context is.
	StatusCancelled: {StatusDraft},
}

// ValidStatus reports whether s is a status this service recognises.
func ValidStatus(s string) bool { _, ok := transitions[s]; return ok }

// CanTransition reports whether the flow allows from → to.
func CanTransition(from, to string) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// nextFrom lists the legal moves out of a status, sorted so the error message a
// caller reads is the same every time.
func nextFrom(from string) string {
	out := append([]string(nil), transitions[from]...)
	sort.Strings(out)
	if len(out) == 0 {
		return "nothing — it is terminal"
	}
	return strings.Join(out, ", ")
}

// Task is the whole entity, and it is short on purpose: a field earns its place
// by being required by a scenario, not by seeming useful.
//
// Reporter is a value, not a pointer. That is the same rule as the NOT NULL in
// the schema, expressed one layer up: what is representable in Go, in JSON and
// in the table eventually happens, so "a task with no reporter" is not
// representable. Assignee and ParentTaskID are pointers because their absence
// is a real state — "not staffed", "not a subtask" — rather than a gap.
type Task struct {
	ID                 uuid.UUID `json:"id"`
	Handle             Handle    `json:"handle"`
	ProjectKey         string    `json:"project_key"`
	Goal               string    `json:"goal"`
	Description        string    `json:"description"`
	AcceptanceCriteria string    `json:"acceptance_criteria"`
	Status             string    `json:"status"`
	// Origin is where the task was born, Discussion where it is talked over:
	// links the board keeps and never opens, and either may be absent.
	Origin       *string    `json:"origin"`
	Discussion   *string    `json:"discussion"`
	Reporter     string     `json:"reporter"`
	Assignee     *string    `json:"assignee"`
	ParentTaskID *uuid.UUID `json:"parent_task_id"`

	// Points is the estimate, like story points: nil is "not estimated", a
	// different fact from an estimate of 1.
	Points *int `json:"points"`

	// Labels are free-form tags: a fixed vocabulary would decide in
	// advance what may be said about the work. Always non-nil — the column is
	// NOT NULL DEFAULT '{}', and the service normalises what is written, so a
	// task with no labels answers [] and never null.
	Labels []string `json:"labels"`

	// Component is the one value this task draws from the project's list: the
	// list is the project's, the choice is the task's. nil is "no component" —
	// a task spanning several components has none, and they can be recorded as
	// labels instead — which is why this is nullable
	// where Labels is not: an absent component is a state, an absent label set
	// is just no labels.
	Component *string `json:"component"`

	// DependsOn is what this task waits on, in the order the links were made.
	DependsOn []uuid.UUID `json:"depends_on"`

	// BlockedBy is derived, never stored: the handles of the dependencies that
	// are not yet DONE or CANCELLED. This is the answer to the question a status
	// called OPEN or BLOCKED would have answered, and it cannot drift out of
	// agreement with the graph because there is nothing to keep in agreement.
	//
	// Handles rather than ids because the only thing anyone does with this is
	// read it or say it — "ABC-15 is waiting on ABC-12".
	BlockedBy []Handle `json:"blocked_by"`

	// Blocks is the other edge of the same graph, derived on every read and
	// never stored: the handles of the tasks waiting on this one. It is the
	// second half of the question a card should answer — "ABC-2 is in UAT, and
	// because of it ABC-4 stands" — and it inherits BlockedBy's guarantee:
	// there is nothing stored, so there is nothing to drift out of agreement
	// with the links table.
	//
	// The DONE or CANCELLED filter sits on this side, the reverse of where
	// BlockedBy puts it: a closed task holds nothing, so the subquery answers
	// empty for one. On the wrong side, a closed task would still claim to be
	// holding.
	Blocks []Handle `json:"blocks"`

	// OpenSubtasks is derived the same way: the handles of this task's
	// children that are not yet DONE or CANCELLED. A task with any is not
	// ready (repository.go, List), and this is where a reader sees why.
	OpenSubtasks []Handle `json:"open_subtasks"`

	// SummaryReport is what was done, why, and where the artefacts are — shaped
	// like a commit message. Nullable because a task that has not been worked
	// has nothing to report, and an empty string would claim otherwise.
	SummaryReport *string `json:"summary_report"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ClosedAt is when the task entered DONE or CANCELLED; null while open. A
	// trigger keeps it (migration 0004), whichever query moved the status.
	ClosedAt *time.Time `json:"closed_at"`
}

// TaskBrief is one line of GET /tasks (the list): the task without its three
// long texts. description, acceptance_criteria and summary_report are ABSENT
// from the JSON — not empty strings — because on a board of hundreds of tasks
// they are the whole payload and nobody reads them from the list; a client
// fetches one task with GET /tasks/{id} for them. Every
// other field is the task's, unchanged, which is why this is a projection of
// Task (brief) rather than a second read: a field added to Task belongs on the
// list the moment it is added, or it is dropped here deliberately.
type TaskBrief struct {
	ID           uuid.UUID   `json:"id"`
	Handle       Handle      `json:"handle"`
	ProjectKey   string      `json:"project_key"`
	Goal         string      `json:"goal"`
	Status       string      `json:"status"`
	Reporter     string      `json:"reporter"`
	Assignee     *string     `json:"assignee"`
	Points       *int        `json:"points"`
	ParentTaskID *uuid.UUID  `json:"parent_task_id"`
	Origin       *string     `json:"origin"`
	Discussion   *string     `json:"discussion"`
	Labels       []string    `json:"labels"`
	Component    *string     `json:"component"`
	DependsOn    []uuid.UUID `json:"depends_on"`
	Blocks       []Handle    `json:"blocks"`
	BlockedBy    []Handle    `json:"blocked_by"`
	OpenSubtasks []Handle    `json:"open_subtasks"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	ClosedAt     *time.Time  `json:"closed_at"`
}

// brief drops only the three long texts; everything else is copied through.
// The repository may or may not still select the columns — either way the list
// never serialises them.
func (t Task) brief() TaskBrief {
	return TaskBrief{
		ID:           t.ID,
		Handle:       t.Handle,
		ProjectKey:   t.ProjectKey,
		Goal:         t.Goal,
		Status:       t.Status,
		Reporter:     t.Reporter,
		Assignee:     t.Assignee,
		Points:       t.Points,
		ParentTaskID: t.ParentTaskID,
		Origin:       t.Origin,
		Discussion:   t.Discussion,
		Labels:       t.Labels,
		Component:    t.Component,
		DependsOn:    t.DependsOn,
		Blocks:       t.Blocks,
		BlockedBy:    t.BlockedBy,
		OpenSubtasks: t.OpenSubtasks,
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
		ClosedAt:     t.ClosedAt,
	}
}

// AssigneeRequest is the body of PUT /tasks/{id}/assignee. A pointer, because
// null is the request that hands a task back to nobody — "not staffed" is a
// state the board filters on, so it is this same field rather than a separate
// endpoint for unassigning.
type AssigneeRequest struct {
	Assignee *string `json:"assignee"`
}

// SummaryRequest is the body of PUT /tasks/{id}/summary.
type SummaryRequest struct {
	SummaryReport string `json:"summary_report"`
}

// normalizeLabels is the spelling of a label set, applied on every write so
// that the board holds one form of each label. Trimmed, empties
// dropped, duplicates collapsed, order as sent: the first spelling wins, so a
// reader can rely on what they sent and a filter can rely on the stored form.
// Always returns a non-nil slice — the column is NOT NULL DEFAULT '{}', and a
// reader of a task with no labels gets [] rather than null.
func normalizeLabels(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		l := strings.TrimSpace(raw)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// PatchRequest edits what somebody decided about a task, as opposed to what the
// task is. Project, handle, origin and reporter are absent on
// purpose: those are the task's identity, and an endpoint that could change
// them would be an endpoint that could move a task into another project's
// numbering or re-attribute who asked for it.
//
// PATCH and not PUT, and the fields are pointers: a full PUT writes every
// column from a body assembled by a caller who only meant to change one, and
// nulls the rest. Here a field that was not sent cannot be cleared, because
// "not sent" and "sent as empty" are different values.
//
// The need was found by running work, not by reasoning: QA sent work back and
// sharpened the definition of done in a comment, because there was no way to
// change the field. The next run received two contradictory closes — the stale
// one from the column and the real one from the thread. A specification that
// cannot be corrected stops being a specification the first time review touches
// it.
// Labels, when sent, replaces the whole set — like a full PUT of one field:
// PATCH never merges a label list. A pointer, for the same reason as the other
// fields: "not sent" must leave the labels alone, while "sent as []" is the
// request that clears them.
type PatchRequest struct {
	Goal               *string   `json:"goal"`
	Description        *string   `json:"description"`
	AcceptanceCriteria *string   `json:"acceptance_criteria"`
	Points             *int      `json:"points"`
	Labels             *[]string `json:"labels"`

	// Component, when sent, must be in the project's list or the patch is a
	// 400 naming the list; sent as "" it clears the value, which is the only
	// endpoint for unsetting it — "not sent" must leave it alone.
	Component *string `json:"component"`

	// Discussion links the chat the task is talked over in, set by whoever
	// opens it.
	Discussion *string `json:"discussion"`
}

// patch is PatchRequest after validation: the fields actually being changed.
type patch struct {
	goal        *string
	description *string
	acceptance  *string
	points      *int
	labels      *[]string
	component   *string
	discussion  *string
}

// validPoints refuses an estimate below one. There is no way to clear an
// estimate once made: re-estimating is the move, and "unestimated again" is
// not a state anybody has asked for.
func validPoints(p *int) error {
	if p != nil && *p < 1 {
		return fmt.Errorf("%w: points is %d — an estimate is at least 1; leave "+
			"it out if the task is not estimated", ErrInvalid, *p)
	}
	return nil
}

// Validate refuses a blank where the schema refuses one, so the caller gets a
// sentence instead of a CHECK violation. Description is exempt: empty is a legal
// value there, and clearing it is a real thing to want.
func (r PatchRequest) Validate() (patch, error) {
	var out patch
	if r.Goal != nil {
		if strings.TrimSpace(*r.Goal) == "" {
			return out, fmt.Errorf("%w: goal cannot be blank", ErrInvalid)
		}
		out.goal = r.Goal
	}
	if r.AcceptanceCriteria != nil {
		if strings.TrimSpace(*r.AcceptanceCriteria) == "" {
			return out, fmt.Errorf("%w: acceptance_criteria cannot be blank — it "+
				"is what QA checks against", ErrInvalid)
		}
		out.acceptance = r.AcceptanceCriteria
	}
	out.description = r.Description
	if err := validPoints(r.Points); err != nil {
		return out, err
	}
	out.points = r.Points
	// Normalised rather than stored as sent: the board holds the trimmed,
	// de-duplicated set, and a set that is all empties is the clear request,
	// not a 400 — there is no separate endpoint for "remove every label".
	if r.Labels != nil {
		normalized := normalizeLabels(*r.Labels)
		out.labels = &normalized
	}
	// Sent, even as "": the service checks it against the project's list,
	// and the empty string is the clear request, so both are a
	// patch and not an empty one.
	if r.Component != nil {
		out.component = r.Component
	}

	var err error
	if out.discussion, err = optionalLink("discussion", r.Discussion); err != nil {
		return out, err
	}

	if out.goal == nil && out.description == nil && out.acceptance == nil &&
		out.points == nil && out.labels == nil && out.component == nil &&
		out.discussion == nil {
		// An empty patch is almost always a caller sending a field this endpoint
		// does not accept — project_key, say — and answering 200 would let them
		// believe it happened.
		return out, fmt.Errorf("%w: nothing to change — this endpoint accepts "+
			"goal, description, acceptance_criteria, points, labels, "+
			"component and discussion; a task's project, handle, origin and reporter are "+
			"what it is, not what was decided about it, and do not change",
			ErrInvalid)
	}
	return out, nil
}

// CreateRequest is what a caller may set. Separate from Task so that id, status
// and timestamps cannot be supplied from outside: accepting a whole Task on
// POST would let a client pick its own id.
//
// The optional references are *string rather than string so that "the field was
// not sent" and "the field was sent as an empty string" stay distinguishable —
// the second is a client bug worth a 400, and collapsing them into one hides it.
type CreateRequest struct {
	ProjectKey         string  `json:"project_key"`
	Goal               string  `json:"goal"`
	Description        string  `json:"description"`
	AcceptanceCriteria string  `json:"acceptance_criteria"`
	Origin             *string `json:"origin"`
	Discussion         *string `json:"discussion"`
	Reporter           string  `json:"reporter"`
	Assignee           *string `json:"assignee"`
	ParentTaskID       *string `json:"parent_task_id"`
	Points             *int    `json:"points"`

	// Labels are free-form tags; absent or empty is an ordinary
	// task, answered back as []. The service normalises them, so a caller
	// sending " bug ", "bug" and "" stores the single label "bug".
	Labels []string `json:"labels"`

	// Component is one of the project's components: absent means the
	// task has none, and a value outside the project's list is a 400 naming
	// the list. Unlike labels there is a registry, because it is the project's
	// property.
	Component *string `json:"component"`
}

// LabelCount is one line of GET /api/v1/labels: a label that exists on a
// project's tasks, and how many of the project's open tasks carry it — the
// list a caller chooses from when tagging, busiest first.
type LabelCount struct {
	Label     string `json:"label"`
	OpenTasks int    `json:"open_tasks"`
}

// StatusRequest is the body of PUT /tasks/{id}/status. From is optional: the
// status the caller expects the task to be in. Absent, the move is checked
// against wherever the task is; present, a task found anywhere else is a 409.
type StatusRequest struct {
	Status string  `json:"status"`
	From   *string `json:"from,omitempty"`
}

// ReturnRequest is the body of POST /tasks/{id}/return: who puts the work
// back and why. Both are required — the reason is the whole record of the
// failure, and it is what the reporter reads before deciding what next.
type ReturnRequest struct {
	ActorID string `json:"actor_id"`
	Reason  string `json:"reason"`
}

func (r ReturnRequest) validate() (uuid.UUID, string, error) {
	reason := strings.TrimSpace(r.Reason)
	if reason == "" {
		return uuid.Nil, "", fmt.Errorf("%w: reason is required — it is what "+
			"the reporter reads to decide what to do with the task", ErrInvalid)
	}
	actor, err := requiredUUID("actor_id", r.ActorID,
		" — the line in the thread is said by somebody")
	return actor, reason, err
}

// DependenciesRequest replaces a task's whole dependency set rather than adding
// to it. That is the shape because of a consequence recorded with the decision
// to refuse rather than warn: if removing a dependency is more work than adding
// one, the refusal gets routed around by editing status in the database. An
// empty list is an ordinary request here, not a special case.
type DependenciesRequest struct {
	DependsOn []string `json:"depends_on"`
}

var (
	// ErrNotFound is returned when a task id matches nothing.
	ErrNotFound = errors.New("task not found")

	// ErrInvalid wraps everything the caller could have sent correctly, so the
	// handler answers 400 by asking errors.Is one question instead of knowing
	// the list of possible messages.
	ErrInvalid = errors.New("invalid request")

	// ErrIllegalTransition is a well-formed request that the task's current
	// state forbids. It is separate from ErrInvalid because the two are fixed by
	// different people: a word outside the vocabulary is the caller's typo, a
	// legal word in an illegal place is a disagreement about where the work is.
	ErrIllegalTransition = errors.New("illegal status transition")

	// ErrInUse is a removal refused because something else still points at the
	// row. Answered as 409, same as an illegal transition, but kept apart
	// because reusing that name for "the project still has tasks" would make the
	// error text lie about what happened.
	ErrInUse = errors.New("still in use")
)

// parsed is CreateRequest after validation — the same values in the types that
// make an invalid combination unrepresentable downstream.
type parsed struct {
	project    string
	origin     *string
	discussion *string
	reporter   string
	assignee   *string
	parent     *uuid.UUID
}

// Validate checks what can be checked before touching the database. It does not
// verify that the chat or the parent exist — only Postgres can
// answer that without a race, and the foreign keys do.
func (r CreateRequest) Validate() (parsed, error) {
	var out parsed

	// Upper-cased rather than refused: "abc" and "ABC" are a shouting
	// convention, not two projects. Whether the project exists is the foreign
	// key's question.
	out.project = strings.ToUpper(strings.TrimSpace(r.ProjectKey))
	if out.project == "" {
		return out, fmt.Errorf("%w: project_key is required — a task is numbered "+
			"inside a project, and there is deliberately no default one", ErrInvalid)
	}
	if !ValidProjectKey(out.project) {
		return out, fmt.Errorf("%w: project_key %q is not a key — uppercase "+
			"letters and digits, 2 to 10 characters", ErrInvalid, out.project)
	}

	if strings.TrimSpace(r.Goal) == "" {
		return out, fmt.Errorf("%w: goal is required and cannot be blank", ErrInvalid)
	}
	if strings.TrimSpace(r.AcceptanceCriteria) == "" {
		return out, fmt.Errorf("%w: acceptance_criteria is required and cannot be "+
			"blank — it is what QA checks the result against, and a task without "+
			"one can only be closed by someone deciding it looks done", ErrInvalid)
	}

	var err error
	if out.origin, err = optionalLink("origin", r.Origin); err != nil {
		return out, err
	}
	if out.discussion, err = optionalLink("discussion", r.Discussion); err != nil {
		return out, err
	}
	if out.reporter, err = nick("reporter", r.Reporter); err != nil {
		return out, err
	}
	if out.assignee, err = optionalNick("assignee", r.Assignee); err != nil {
		return out, err
	}
	if out.parent, err = optionalUUID("parent_task_id", r.ParentTaskID); err != nil {
		return out, err
	}
	if err := validPoints(r.Points); err != nil {
		return out, err
	}
	return out, nil
}

func requiredUUID(field, raw, why string) (uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return uuid.Nil, fmt.Errorf("%w: %s is required%s", ErrInvalid, field, why)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %s is not a valid UUID", ErrInvalid, field)
	}
	return id, nil
}

// nick is a subject's address as the board keeps it: the board does not
// resolve it. Case and a leading @ are how a nick gets typed. A UUID is
// refused so that a client still sending ids fails instead of storing one.
func nick(field, raw string) (string, error) {
	n := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	if n == "" {
		return "", fmt.Errorf("%w: %s is required — a subject's nick", ErrInvalid, field)
	}
	if _, err := uuid.Parse(n); err == nil {
		return "", fmt.Errorf("%w: %s is a UUID — the board names subjects by nick", ErrInvalid, field)
	}
	return n, nil
}

// optionalNick is nick for a field where absent and null both mean nobody.
func optionalNick(field string, raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	if strings.TrimSpace(*raw) == "" {
		return nil, fmt.Errorf("%w: %s is empty — omit the field or send null to "+
			"mean nobody", ErrInvalid, field)
	}
	n, err := nick(field, *raw)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// optionalLink is a link the board stores and does not open: absent or null
// is none, blank is a client bug.
func optionalLink(field string, raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	l := strings.TrimSpace(*raw)
	if l == "" {
		return nil, fmt.Errorf("%w: %s is empty — omit the field or send null to "+
			"mean none", ErrInvalid, field)
	}
	return &l, nil
}

// optionalUUID treats an absent field and an explicit null as the same thing,
// and an empty string as an error: a client that sends "" means "none" but has
// no way to say so correctly, and answering 400 tells it which.
func optionalUUID(field string, raw *string) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	if strings.TrimSpace(*raw) == "" {
		return nil, fmt.Errorf("%w: %s is empty — omit the field or send null to "+
			"mean none", ErrInvalid, field)
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a valid UUID", ErrInvalid, field)
	}
	return &id, nil
}

// ValidateStatus checks the word alone — whether it may be used from where the
// task currently is, is the service's question.
func (r StatusRequest) ValidateStatus() (string, error) {
	s := strings.TrimSpace(r.Status)
	if s == "" {
		return "", fmt.Errorf("%w: status is required", ErrInvalid)
	}
	if !ValidStatus(s) {
		return "", fmt.Errorf("%w: %q is not a status — the vocabulary is "+
			"DRAFT, TODO, IN_PROGRESS, QA, UAT, DONE, CANCELLED; there is no "+
			"REVIEW and no BLOCKED", ErrInvalid, s)
	}
	return s, nil
}

// ValidateFrom checks the expected status, if one was sent; "" means none.
// An empty string sent explicitly is refused like a missing status is: a
// client that means "no expectation" leaves the field out.
func (r StatusRequest) ValidateFrom() (string, error) {
	if r.From == nil {
		return "", nil
	}
	s := strings.TrimSpace(*r.From)
	if !ValidStatus(s) {
		return "", fmt.Errorf("%w: from %q is not a status — leave it out to "+
			"move the task from wherever it is", ErrInvalid, *r.From)
	}
	return s, nil
}

// parseStatusList reads ?status=QA,UAT. Comma-separated because the spec names
// it as one query value and not a repeated key. Every entry must be in the
// vocabulary or the whole filter is refused — a typo silently dropped would
// come back as a board missing a column with no word about why.
func parseStatusList(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		if !ValidStatus(s) {
			return nil, fmt.Errorf("%w: %q in status is not a status — the "+
				"vocabulary is DRAFT, TODO, IN_PROGRESS, QA, UAT, DONE, CANCELLED",
				ErrInvalid, s)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: status is present but names nothing", ErrInvalid)
	}
	return out, nil
}

// Order is the vocabulary for ?order. OrderCreated is the original, unstated
// default — newest first — kept as an explicit word rather than only the empty
// string so a caller can say it. OrderNumber and OrderUpdated exist because
// this task asked for them: see repository.go's List for why number is free.
const (
	OrderCreated = "created"
	OrderNumber  = "number"
	OrderUpdated = "updated"
)

// ValidOrder reports whether o is a sort order this service recognises.
func ValidOrder(o string) bool {
	switch o {
	case OrderCreated, OrderNumber, OrderUpdated:
		return true
	}
	return false
}
