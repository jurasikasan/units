package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes is a function rather than a block inside main() so that the
// routing table can be tested. gorilla/mux matches in registration order: a
// literal such as `/api/v1/tasks/ready` registered after `/api/v1/tasks/{id}`
// is parsed as a task id, and the endpoint answers 400 to every caller without
// anything going red.
//
// Literal paths therefore come first here, and routes_test.go asserts it. Note
// that "what can be picked up right now" is a query parameter on the collection
// and not /tasks/ready — a filter has no route to be shadowed.
func RegisterRoutes(r *mux.Router, h *Handler) {
	r.HandleFunc("/api/v1/projects", h.CreateProject).Methods(http.MethodPost)
	r.HandleFunc("/api/v1/projects", h.ListProjects).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/projects/{key}", h.DeleteProject).Methods(http.MethodDelete)
	r.HandleFunc("/api/v1/projects/{key}/components", h.SetComponents).Methods(http.MethodPut)
	// A literal, and like the other literals it is registered before the
	// /tasks/{id} wildcard — and it must stay one level up from /tasks, because
	// "labels" inside /tasks would be the shadowed /tasks/ready all over again.
	r.HandleFunc("/api/v1/labels", h.ListLabels).Methods(http.MethodGet)

	r.HandleFunc("/api/v1/tasks", h.Create).Methods(http.MethodPost)
	r.HandleFunc("/api/v1/tasks", h.List).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/tasks/{id}", h.Get).Methods(http.MethodGet)
	// PATCH and never PUT on a whole task. A whole-task PUT nulls every field
	// a caller left out when it assembles a body for one field. Not offering
	// the verb is a cheaper fix than warning about it.
	r.HandleFunc("/api/v1/tasks/{id}", h.Patch).Methods(http.MethodPatch)
	r.HandleFunc("/api/v1/tasks/{id}", h.Delete).Methods(http.MethodDelete)
	// Sub-routes of the task rather than a PATCH of it, because neither is an
	// edit of a field: a status change is the one write with a rule about what
	// the previous value was, and a dependency set is a different table.
	r.HandleFunc("/api/v1/tasks/{id}/status", h.SetStatus).Methods(http.MethodPut)
	r.HandleFunc("/api/v1/tasks/{id}/return", h.Return).Methods(http.MethodPost)
	r.HandleFunc("/api/v1/tasks/{id}/dependencies", h.SetDependencies).Methods(http.MethodPut)
	r.HandleFunc("/api/v1/tasks/{id}/assignee", h.SetAssignee).Methods(http.MethodPut)
	r.HandleFunc("/api/v1/tasks/{id}/summary", h.SetSummary).Methods(http.MethodPut)
}

// Delete removes a task and its thread. 204 and no body: there is nothing left
// to describe, and an echo of the deleted row invites a caller to keep using it.
//
// UUID only, unlike Get, which also takes ABC-12. Every mutating endpoint here
// does the same, and for a destructive one the reason is no longer only
// consistency: a mistyped handle that happens to parse resolves to a different
// real task, and this is the one verb where that is unrecoverable.
//
// Delete godoc
// @Summary      Delete a task
// @Description  Takes the task's thread with it. Refused, with 409 naming which,
// @Description  while the task has children or while tasks were born in its thread.
// @Tags         tasks
// @Param        id  path  string  true  "Task UUID"
// @Success      204  "Deleted; no body"
// @Failure      400  {object}  map[string]string  "invalid task id"
// @Failure      404  {object}  map[string]string  "no such task"
// @Failure      409  {object}  map[string]string  "still has children or tasks born in its thread"
// @Failure      500  {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id} [delete]
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteProject removes an empty line of work.
//
// DeleteProject godoc
// @Summary      Delete a project
// @Description  Refused while the project still holds tasks; it does not cascade.
// @Tags         projects
// @Param        key  path  string  true  "Project key"
// @Success      204  "Deleted; no body"
// @Failure      404  {object}  map[string]string  "no such project"
// @Failure      409  {object}  map[string]string  "the project still holds tasks"
// @Failure      500  {object}  map[string]string  "internal error"
// @Router       /api/v1/projects/{key} [delete]
func (h *Handler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteProject(r.Context(), mux.Vars(r)["key"]); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetComponents godoc
// @Summary      Replace a project's component list
// @Description  Replaces the whole list — that is also how a
// @Description  component is removed, and there is deliberately no additive
// @Description  form: a registry you can only grow is a label. Blanks and
// @Description  duplicates are normalised, the way a labels list is.
// @Description  Refused with 409 and the number of such tasks while a value the
// @Description  new list drops is still the component of one of the project's
// @Description  tasks; clear it on them first (PATCH with "component": "").
// @Tags         projects
// @Accept       json
// @Produce      json
// @Param        key         path  string            true  "Project key"
// @Param        components  body  ComponentsRequest true  "The new whole list"
// @Success      200         {object}  Project
// @Failure      400         {object}  map[string]string  "invalid key or request body"
// @Failure      404         {object}  map[string]string  "no such project"
// @Failure      409         {object}  map[string]string  "a value the list removes is still held by the project's tasks"
// @Failure      500         {object}  map[string]string  "internal error"
// @Router       /api/v1/projects/{key}/components [put]
func (h *Handler) SetComponents(w http.ResponseWriter, r *http.Request) {
	var req ComponentsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	p, err := h.svc.SetComponents(r.Context(), mux.Vars(r)["key"], req)
	if err != nil {
		writeErr(w, err)
		return
	}
	respond(w, http.StatusOK, p)
}

// Patch godoc
// @Summary      Patch a task
// @Description  Edits goal, description and acceptance_criteria. A field that
// @Description  was not sent cannot be cleared, and identity — project, handle,
// @Description  origin, reporter — is not offered at all. discussion sets the
// @Description  link to the chat the task is talked over in. labels, when
// @Description  sent, replaces the whole set; [] clears them, and labels not
// @Description  sent are left alone. component, when sent, must be in the
// @Description  project's list (400 naming the list otherwise); "" clears it,
// @Description  and not sending it leaves it alone.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        id     path  string        true  "Task UUID"
// @Param        patch  body  PatchRequest  true  "The fields to change"
// @Success      200    {object}  Task
// @Failure      400    {object}  map[string]string  "invalid request"
// @Failure      404    {object}  map[string]string  "no such task"
// @Failure      500    {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id} [patch]
func (h *Handler) Patch(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	t, err := h.svc.Patch(r.Context(), id, req)
	h.respondTask(w, t, err)
}

// SetAssignee godoc
// @Summary      Assign or unassign a task
// @Description  null hands the task back to nobody; "" is a 400 — omit the
// @Description  field or send null to mean none. Every successful write is
// @Description  published to the board_events stream.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        id        path  string           true  "Task UUID"
// @Param        assignee  body  AssigneeRequest  true  "assignee (a nick), or null to unassign"
// @Success      200       {object}  Task
// @Failure      400       {object}  map[string]string  "invalid request"
// @Failure      404       {object}  map[string]string  "no such task"
// @Failure      500       {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id}/assignee [put]
func (h *Handler) SetAssignee(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req AssigneeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	t, err := h.svc.SetAssignee(r.Context(), id, req)
	h.respondTask(w, t, err)
}

// SetSummary godoc
// @Summary      Set the task's summary
// @Description  What was done, why, and where the artefacts are. Blank is refused.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        id       path  string          true  "Task UUID"
// @Param        summary  body  SummaryRequest  true  "summary_report"
// @Success      200      {object}  Task
// @Failure      400      {object}  map[string]string  "blank summary"
// @Failure      404      {object}  map[string]string  "no such task"
// @Failure      500      {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id}/summary [put]
func (h *Handler) SetSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req SummaryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	t, err := h.svc.SetSummary(r.Context(), id, req)
	h.respondTask(w, t, err)
}

// CreateProject godoc
// @Summary      Create a project
// @Description  Registers a line of work with its own numbering. The key is
// @Description  upper-cased and must be an uppercase letter followed by 1 to 9
// @Description  more letters or digits; creating a project that already exists
// @Description  is a 400.
// @Tags         projects
// @Accept       json
// @Produce      json
// @Param        project  body  CreateProjectRequest  true  "Project key and name"
// @Success      201      {object}  Project
// @Failure      400      {object}  map[string]string  "invalid key or the project already exists"
// @Failure      500      {object}  map[string]string  "internal error"
// @Router       /api/v1/projects [post]
func (h *Handler) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	p, err := h.svc.CreateProject(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	respond(w, http.StatusCreated, p)
}

// ListProjects godoc
// @Summary      List projects
// @Tags         projects
// @Produce      json
// @Success      200 {object}  []Project
// @Failure      500 {object}  map[string]string  "internal error"
// @Router       /api/v1/projects [get]
func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.svc.ListProjects(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	respond(w, http.StatusOK, projects)
}

// ListLabels godoc
// @Summary      List a project's labels with their open-task counts
// @Description  The labels that exist on the project's tasks — closed ones
// @Description  included, which is why a label can answer with 0 — ordered by
// @Description  the number of open tasks carrying them, busiest first. This is
// @Description  the list a caller chooses from when tagging, and a label not
// @Description  here is added by writing it on a task: there is no registry.
// @Tags         labels
// @Produce      json
// @Param        project  query  string  true  "Project key, like ABC"
// @Success      200      {array}   LabelCount  "Existing labels, busiest first"
// @Failure      400      {object}  map[string]string  "missing or invalid project key"
// @Failure      500      {object}  map[string]string  "internal error"
// @Router       /api/v1/labels [get]
func (h *Handler) ListLabels(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.Labels(r.Context(), r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}

// Create godoc
// @Summary      Create a task
// @Description  The task is always born DRAFT. project_key, goal,
// @Description  acceptance_criteria and reporter (a nick) are required;
// @Description  origin and discussion are optional links the board does not open.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        task  body  CreateRequest  true  "The task to create"
// @Success      201   {object}  Task
// @Failure      400   {object}  map[string]string  "invalid request or unknown chat/reporter"
// @Failure      500   {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks [post]
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	t, err := h.svc.Create(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	respond(w, http.StatusCreated, t)
}

// Get accepts either address: the uuid, or the name a person would say. One
// route rather than two, because they identify the same thing and a second
// route would be a second place to forget a change.
//
// Get godoc
// @Summary      Get a task by UUID or spoken handle
// @Description  The id segment takes the UUID or the spoken handle ABC-12;
// @Description  they identify the same row.
// @Tags         tasks
// @Produce      json
// @Param        id  path  string  true  "Task UUID or a handle like ABC-12"
// @Success      200 {object}  Task
// @Failure      400 {object}  map[string]string  "neither a UUID nor a handle"
// @Failure      404 {object}  map[string]string  "no such task"
// @Failure      500 {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id} [get]
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	raw := mux.Vars(r)["id"]
	if id, err := uuid.Parse(raw); err == nil {
		t, err := h.svc.Get(r.Context(), id)
		h.respondTask(w, t, err)
		return
	}
	handle, err := ParseHandle(raw)
	if err != nil {
		// Reported as "neither", because a caller who mistyped a uuid should not
		// be told their project key is malformed.
		badRequest(w, "task id must be a UUID or a handle like ABC-12; "+
			strings.TrimPrefix(err.Error(), "invalid request: "))
		return
	}
	t, err := h.svc.GetByHandle(r.Context(), handle)
	h.respondTask(w, t, err)
}

// SetStatus godoc
// @Summary      Move a task through the flow
// @Description  The move must be legal from the task's current status; setting
// @Description  the status it already has is a no-op 200. The vocabulary is
// @Description  DRAFT, TODO, IN_PROGRESS, QA, UAT, DONE, CANCELLED; there is
// @Description  no REVIEW and no BLOCKED. With from, the task must
// @Description  also be in that status, or the answer is 409: this is how a task
// @Description  is claimed, since two claimants sending from=TODO cannot both win.
// @Description  A successful move is published to the board_events stream.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        id      path  string         true  "Task UUID"
// @Param        status  body  StatusRequest  true  "The new status"
// @Success      200     {object}  Task
// @Failure      400     {object}  map[string]string  "not a status"
// @Failure      404     {object}  map[string]string  "no such task"
// @Failure      409     {object}  map[string]string  "illegal transition from the current status, or the task is not in from"
// @Failure      500     {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id}/status [put]
func (h *Handler) SetStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req StatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	t, err := h.svc.SetStatus(r.Context(), id, req)
	h.respondTask(w, t, err)
}

// Return godoc
// @Summary      Put work in progress back in the queue
// @Description  The only way from IN_PROGRESS to TODO: the task also goes to
// @Description  its reporter, and the reason becomes a line in its thread from
// @Description  actor_id — all in one write. For an executor giving up and for
// @Description  a watcher whose budget ran out. Published to board_events as
// @Description  an ordinary move. A task anywhere but IN_PROGRESS is a 409.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        id      path  string         true  "Task UUID"
// @Param        return  body  ReturnRequest  true  "Who returns it and why"
// @Success      200     {object}  Task
// @Failure      400     {object}  map[string]string  "missing actor_id or reason"
// @Failure      404     {object}  map[string]string  "no such task"
// @Failure      409     {object}  map[string]string  "the task is not IN_PROGRESS"
// @Failure      500     {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id}/return [post]
func (h *Handler) Return(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req ReturnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	t, err := h.svc.Return(r.Context(), id, req)
	h.respondTask(w, t, err)
}

// SetDependencies godoc
// @Summary      Replace a task's dependency set
// @Description  Replaces the whole set; an empty list clears it. A link to an
// @Description  unknown task, to the task itself, or into a longer cycle is a 400.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        id          path  string               true  "Task UUID"
// @Param        depends_on  body  DependenciesRequest  true  "The new whole set"
// @Success      200         {object}  Task
// @Failure      400         {object}  map[string]string  "unknown task, self-link or cycle"
// @Failure      404         {object}  map[string]string  "no such task"
// @Failure      500         {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks/{id}/dependencies [put]
func (h *Handler) SetDependencies(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req DependenciesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	t, err := h.svc.SetDependencies(r.Context(), id, req)
	h.respondTask(w, t, err)
}

// List godoc
// @Summary      List tasks
// @Description  Every item is the task without its long texts: description,
// @Description  acceptance_criteria and summary_report are absent from the
// @Description  answer, and GET /tasks/{id} is the one that answers them.
// @Description  All filters are optional. status takes a comma-separated list
// @Description  from the flow's vocabulary; ready=true and open=true are each
// @Description  refused together with status (open=true and ready=true is
// @Description  allowed — ready is already open); order is number, updated or
// @Description  created (the default). label is repeated for more than one:
// @Description  every named label must be on the task, so ?label=a&label=b ANDs.
// @Tags         tasks
// @Produce      json
// @Param        origin          query  string  false  "Only tasks born here (the origin link, as stored)"
// @Param        project_key     query  string  false  "Only tasks of this project"
// @Param        assignee        query  string  false  "Only tasks assigned to this nick; none: only unassigned ones"
// @Param        q               query  string  false  "A handle (ABC-45, any case) finds that task, a bare number that number in every project; anything else is a case-insensitive piece of the goal"
// @Param        status          query  string  false  "Comma-separated statuses, e.g. QA,UAT"
// @Param        label           query  string  false  "Only tasks carrying this label; repeat the parameter for tasks carrying all of them"
// @Param        component       query  string  false  "Only tasks with this component — one of the project's components"
// @Param        ready           query  string  false  "true: only TODO tasks with no open dependency and no open subtask"
// @Param        open            query  string  false  "true: only tasks that are not DONE or CANCELLED"
// @Param        order           query  string  false  "number, updated or created (default created)"
// @Success      200             {object}  []TaskBrief
// @Failure      400             {object}  map[string]string  "invalid filter"
// @Failure      500             {object}  map[string]string  "internal error"
// @Router       /api/v1/tasks [get]
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f ListFilter

	f.Origin = strings.TrimSpace(q.Get("origin"))
	if raw := q.Get("project_key"); raw != "" {
		f.Project = strings.ToUpper(strings.TrimSpace(raw))
		if !ValidProjectKey(f.Project) {
			badRequest(w, "invalid project_key")
			return
		}
	}
	if raw := q.Get("assignee"); raw == "none" {
		f.Unassigned = true
	} else if raw != "" {
		n, err := nick("assignee", raw)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		f.Assignee = n
	}
	if raw := q.Get("status"); raw != "" {
		statuses, err := parseStatusList(raw)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		f.Statuses = statuses
	}
	// Repeated on purpose: ?label=bug&label=ui means "has both", which q.Get
	// cannot express because it keeps only the first value. A blank parameter
	// is refused like status names nothing: a filter that would match every
	// task is the filter that was not sent, and the two are different requests.
	if labels := q["label"]; len(labels) > 0 {
		for _, raw := range labels {
			label := strings.TrimSpace(raw)
			if label == "" {
				badRequest(w, "label is present but blank — drop it to leave "+
					"the filter off")
				return
			}
			f.Labels = append(f.Labels, label)
		}
	}
	// A component is one value, so there is nothing to repeat — but a present
	// blank is still a request, and gets the same refusal as a blank label:
	// a filter that matches everything is the filter that was not sent.
	if raws := q["component"]; len(raws) > 0 {
		c := strings.TrimSpace(raws[0])
		if c == "" {
			badRequest(w, "component is present but blank — drop it to leave "+
				"the filter off")
			return
		}
		f.Component = c
	}
	f.Handle, f.GoalLike = parseSearch(q.Get("q"))

	// Only "true" turns it on. Treating any present value as true is how
	// ?ready=false ends up meaning ready.
	f.Ready = q.Get("ready") == "true"

	// Unlike ready, any present value but "true" is refused, empty included:
	// "open=false" means nothing on this board, and answering it with the
	// whole board would teach callers the other way.
	if q.Has("open") {
		if q.Get("open") != "true" {
			badRequest(w, "open accepts only true — drop the parameter to list "+
				"every status")
			return
		}
		f.Open = true
	}

	f.Order = OrderCreated
	if raw := q.Get("order"); raw != "" {
		if !ValidOrder(raw) {
			badRequest(w, fmt.Sprintf("%q is not an order — use number, updated, or created", raw))
			return
		}
		f.Order = raw
	}

	tasks, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The list carries no long texts: on a full board they are most of the
	// payload, and the keys are absent rather than empty — "not here", not
	// "here and blank". The task itself, on every other route, is whole.
	out := make([]TaskBrief, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.brief())
	}
	respond(w, http.StatusOK, out)
}

// parseSearch splits the search box in two. Something shaped like a handle is
// a handle: someone typing "acc-45" wants that task, not every goal that quotes
// it. Everything else is matched against the goal only — descriptions here run
// to pages, and a word found somewhere in one says little about the task.
func parseSearch(raw string) (*Handle, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ""
	}
	if h, err := ParseHandle(strings.ToUpper(raw)); err == nil {
		return &h, ""
	}
	// A bare number is the half of a handle people actually remember. It
	// matches that number in every project — ABC-287 and XYZ-287 both — and the
	// project filter, when set, narrows it to one.
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
		return &Handle{Number: n}, ""
	}
	return nil, raw
}

// taskID is for the routes that act on a task rather than read it. They take the
// uuid only: a write addressed by spoken name is a write where a typo lands on
// somebody else's task.
func taskID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		badRequest(w, "invalid task id")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) respondTask(w http.ResponseWriter, t *Task, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	respond(w, http.StatusOK, t)
}

// writeErr maps domain errors to status codes. Two of them matter.
//
// ErrUnknownRef: a foreign key violation is the caller's bad input, not a broken
// service, and answering 500 to it would send whoever is integrating off to read
// our logs instead of their payload.
//
// ErrIllegalTransition is 409 and not 400, and the difference is not pedantry —
// the two are fixed by different people. A word outside the vocabulary is the
// caller's typo; a legal word in an illegal place means the caller and the board
// disagree about where the work is, which is resolved by looking at the task.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		respond(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrIllegalTransition), errors.Is(err, ErrInUse):
		respond(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownRef), errors.Is(err, ErrInvalid):
		badRequest(w, err.Error())
	default:
		log.Printf("task: %v", err)
		respond(w, http.StatusInternalServerError,
			map[string]string{"error": "internal error"})
	}
}

func badRequest(w http.ResponseWriter, msg string) {
	respond(w, http.StatusBadRequest, map[string]string{"error": msg})
}

// respond exists only to keep the call sites short; the writing itself is
// shared/http's, which also redacts what it logs.
func respond(w http.ResponseWriter, code int, body any) {
	httpHelper.RespondWithJSON(w, code, body)
}
