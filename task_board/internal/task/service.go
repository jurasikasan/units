package task

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// repository is the boundary between the rules and the storage, written down as
// an interface because the one rule that depends on the graph — IN_PROGRESS
// refused while BlockedBy is non-empty — is exactly the one a pure-function test
// cannot reach: it only fires if the read actually filled BlockedBy. The fake
// in service_test.go stands in for Postgres so that the branch in SetStatus is
// tested the same way CanTransition is: against its inputs, and red the moment
// either the gate or the derivation goes away.
type repository interface {
	CreateProject(ctx context.Context, p Project) (*Project, error)
	ListProjects(ctx context.Context) ([]Project, error)
	DeleteProject(ctx context.Context, key string) error
	GetProject(ctx context.Context, key string) (*Project, error)
	SetComponents(ctx context.Context, key string, components []string) (*Project, error)
	Delete(ctx context.Context, id uuid.UUID) error
	Create(ctx context.Context, t *Task) (*Task, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Task, error)
	GetByHandle(ctx context.Context, h Handle) (*Task, error)
	SetStatus(ctx context.Context, id uuid.UUID, from, to string) (*Task, error)
	SetDependencies(ctx context.Context, id uuid.UUID, deps []uuid.UUID) (*Task, error)
	Park(ctx context.Context, id uuid.UUID, deps []uuid.UUID) (*Task, error)
	Patch(ctx context.Context, id uuid.UUID, p patch) (*Task, error)
	SetAssignee(ctx context.Context, id uuid.UUID, assignee *string) (*Task, error)
	SetSummary(ctx context.Context, id uuid.UUID, report string) (*Task, error)
	Return(ctx context.Context, id uuid.UUID) (*Task, error)
	List(ctx context.Context, f ListFilter) ([]Task, error)
	ListLabels(ctx context.Context, project string) ([]LabelCount, error)
}

// Service holds what the entity means, as opposed to how it is stored.
//
// The rules live here and only here — not in handlers, not in a proxy in front
// of the board.
//
// NewService keeps its concrete argument: production wiring (cmd/app/main.go)
// names the Postgres repository explicitly, and only the tests build the
// interface another way.
type Service struct {
	repo   repository
	events publisher
}

// NewService wires the board. events may be nil: then nothing is published.
func NewService(repo *Repository, events publisher) *Service {
	return &Service{repo: repo, events: events}
}

// CreateProject registers a line of work — a context with its own numbering,
// not necessarily a codebase.
func (s *Service) CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	// An explicit field list (not a Project(req) conversion) so a new field on
	// Project — Components, say — is a decision here, not a value a
	// create request silently carries.
	return s.repo.CreateProject(ctx, Project{Key: req.Key, Name: req.Name})
}

func (s *Service) ListProjects(ctx context.Context) ([]Project, error) {
	return s.repo.ListProjects(ctx)
}

// DeleteProject removes a line of work once nothing is left in it.
//
// Upper-cased like CreateProjectRequest.Validate does, and for the same reason:
// "abc" and "ABC" are a shouting convention, not two projects. Refusing the
// lower-case form here while accepting it on create would mean a project that
// can be made but not unmade by the same spelling.
func (s *Service) DeleteProject(ctx context.Context, key string) error {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !ValidProjectKey(key) {
		return fmt.Errorf("%w: %q is not a project key", ErrInvalid, key)
	}
	return s.repo.DeleteProject(ctx, key)
}

// Delete removes a task and its thread.
//
// No status gate. The temptation is to refuse DONE so the board keeps its
// history, and it would be theatre: the caller this exists for is a test
// runner, whose tasks reach DONE and CANCELLED by design, so the guard would
// have to be bypassable — and a bypassable guard is a convention, which is the
// thing this board exists to stop pretending about. What actually keeps real work from
// being erased is who may call this at all, and that is auth, deliberately not
// built yet. Written down so the next reader knows it was decided, not missed.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
}

// Create makes a task. A task is always born in DRAFT: nothing is dispatched by
// being written down. It is not a convention a caller could override: there is
// no status in CreateRequest.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Task, error) {
	p, err := req.Validate()
	if err != nil {
		return nil, err
	}
	// The component is checked here, not in the storage: the list it
	// must belong to lives on the project, and only this layer knows both.
	var component *string
	if req.Component != nil {
		c := strings.TrimSpace(*req.Component)
		if c != "" {
			proj, err := s.repo.GetProject(ctx, p.project)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil, fmt.Errorf("%w: project_key refers to a project "+
						"that does not exist — create it first", ErrUnknownRef)
				}
				return nil, err
			}
			if !containsString(proj.Components, c) {
				return nil, fmt.Errorf("%w: %q is not a component of project %s — the "+
					"allowed values are %s", ErrInvalid, c, proj.Key,
					allowedComponents(proj.Components))
			}
			component = &c
		}
		// Sent as "": same as absent — a task is born with no component.
	}
	return s.repo.Create(ctx, &Task{
		ID:                 uuid.New(),
		ProjectKey:         p.project,
		Goal:               req.Goal,
		Description:        req.Description,
		AcceptanceCriteria: req.AcceptanceCriteria,
		Status:             StatusDraft,
		Origin:             p.origin,
		Discussion:         p.discussion,
		Reporter:           p.reporter,
		Assignee:           p.assignee,
		ParentTaskID:       p.parent,
		Points:             req.Points,
		Labels:             normalizeLabels(req.Labels),
		Component:          component,
	})
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Task, error) {
	return s.repo.GetByID(ctx, id)
}

// GetByHandle finds a task by the name people say. Same task, second address —
// and the reason the handle is worth having at all: a name nobody can look up
// with is a decoration.
func (s *Service) GetByHandle(ctx context.Context, h Handle) (*Task, error) {
	return s.repo.GetByHandle(ctx, h)
}

// SetStatus moves a task through the flow. Three things are checked, and they
// are separate because they fail for different reasons: the word must be in the
// vocabulary, the move must be legal from where the task is, and — for starting
// work — nothing may still be in the way, and somebody must own it.
//
// With From the caller also says where it believes the task is, and the move is
// refused unless it is there. That is how a claim is made: two dispatchers
// taking the same TODO task both send from=TODO, and only one of them finds it
// there — the other gets a 409, not the idempotent 200 below, which would let
// both believe they had it.
func (s *Service) SetStatus(ctx context.Context, id uuid.UUID, req StatusRequest) (*Task, error) {
	to, err := req.ValidateStatus()
	if err != nil {
		return nil, err
	}
	from, err := req.ValidateFrom()
	if err != nil {
		return nil, err
	}
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if from != "" && current.Status != from {
		return nil, fmt.Errorf("%w: %s is %s, not %s as the request expected — "+
			"somebody moved it first", ErrIllegalTransition, current.Handle,
			current.Status, from)
	}
	// Idempotent on purpose: a retried delivery should not be an error. Redis
	// streams are at-least-once, so a consumer that answers 409 to its own
	// redelivery turns a normal event into an incident.
	if current.Status == to {
		return current, nil
	}
	if !CanTransition(current.Status, to) {
		return nil, fmt.Errorf("%w: %s cannot go to %s; from %s the task can go to %s",
			ErrIllegalTransition, current.Status, to, current.Status, nextFrom(current.Status))
	}

	// The gate stands on starting work, not on approval. A task may be both
	// approved and waiting — that is exactly the state the removed BLOCKED
	// status used to occupy badly, and it is derived here from the graph rather
	// than stored. Refusal names the blockers, because a refusal you have to go
	// and investigate is a refusal that gets worked around by hand.
	if to == StatusInProgress && len(current.BlockedBy) > 0 {
		return nil, fmt.Errorf("%w: %s is waiting on %s — close or cancel them, "+
			"or drop the dependency", ErrIllegalTransition, current.Handle,
			joinHandles(current.BlockedBy))
	}

	// The other half of "not ready": work with nobody on it. The board saying
	// "in progress" is a claim somebody owns, and a claim nobody owns is a lie
	// a dispatcher then acts on. The gate holds on every move into
	// IN_PROGRESS — from TODO, and back in from QA or UAT.
	if to == StatusInProgress && (current.Assignee == nil || *current.Assignee == "") {
		return nil, fmt.Errorf("%w: %s has no assignee — assign someone before "+
			"starting it", ErrIllegalTransition, current.Handle)
	}

	t, err := s.repo.SetStatus(ctx, id, current.Status, to)
	if err != nil {
		if errors.Is(err, ErrStaleStatus) {
			// The write saw zero rows and cannot say why: the row may have
			// moved, or it may be gone. Only a read can tell, and it is worth
			// paying because the two are fixed differently — a 409 tells the
			// caller to look at where the task is and try again, and retrying
			// is an action on a task that is no longer there. A read that fails
			// for some other reason falls through to the 409, which is the
			// safe answer: it does not claim the task is gone on evidence it
			// could not get.
			if _, gerr := s.repo.GetByID(ctx, id); errors.Is(gerr, ErrNotFound) {
				return nil, fmt.Errorf("%w: it was deleted while this request was "+
					"being handled", ErrNotFound)
			}
		}
		return nil, err
	}
	s.publish(ctx, Event{Type: EventStatus, TaskID: t.ID, Handle: t.Handle,
		From: current.Status, To: t.Status, Assignee: t.Assignee})
	return t, nil
}

func joinHandles(hs []Handle) string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.String()
	}
	return strings.Join(out, ", ")
}

// SetDependencies replaces what a task waits on. Duplicates in the request are
// collapsed rather than refused — sending the same id twice is a clumsy way of
// saying something true, not a disagreement about the graph.
//
// On a task that is being worked, an open dependency among the new links
// parks the work rather than just recording it — the task goes back to TODO
// so a dispatcher stops handing it out, while the executor is left in
// place. This is the same move as Return with the opposite destination: Return
// says "this work fails, take it back", parking says "this work waits, and it
// resumes on the same executor when what it waits on closes". The thread line
// and the transition event keep the record, the way any transition does.
func (s *Service) SetDependencies(ctx context.Context, id uuid.UUID, req DependenciesRequest) (*Task, error) {
	// Checked before writing so that a dependency set aimed at a task that does
	// not exist answers 404 rather than reporting a foreign key problem about
	// the other side of the link.
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	seen := map[uuid.UUID]bool{}
	deps := make([]uuid.UUID, 0, len(req.DependsOn))
	for _, raw := range req.DependsOn {
		dep, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: %q in depends_on is not a valid UUID",
				ErrInvalid, raw)
		}
		if seen[dep] {
			continue
		}
		seen[dep] = true
		deps = append(deps, dep)
	}

	// A closed task holds nothing, so it is what the BlockedBy filter excludes:
	// only a dependency that is still open parks the work.
	if current.Status == StatusInProgress {
		var open []Handle
		for _, dep := range deps {
			d, err := s.repo.GetByID(ctx, dep)
			if err != nil {
				// The same answer the repository's foreign key would give when
				// the write lands: a missing dependency is a bad reference, not
				// "a task not found" about the task this request was aimed at.
				return nil, fmt.Errorf("%w: depends_on refers to a task that "+
					"does not exist", ErrUnknownRef)
			}
			if d.Status != StatusDone && d.Status != StatusCancelled {
				open = append(open, d.Handle)
			}
		}
		if len(open) > 0 {
			verb := "they close"
			if len(open) == 1 {
				verb = "it closes"
			}
			line := "Parked: waits for " + joinHandles(open) + "; resumes when " + verb

			// Said by the Scheduler, the system subject — not by the executor: in the executor's
			// history its own lines are read back as the model's prior words,
			// and "Parked: …" is not something it said.
			t, perr := s.repo.Park(ctx, id, deps)
			if errors.Is(perr, ErrStaleStatus) {
				// Zero rows, like in Return: the task moved or was deleted
				// between the read and the write, and the read tells which.
				current, gerr := s.repo.GetByID(ctx, id)
				if gerr != nil {
					return nil, gerr
				}
				return nil, fmt.Errorf("%w: %s is %s — only work IN_PROGRESS "+
					"can be parked", ErrIllegalTransition, current.Handle,
					current.Status)
			}
			if perr != nil {
				return nil, perr
			}
			s.publish(ctx, Event{Type: EventStatus, TaskID: t.ID, Handle: t.Handle,
				From: StatusInProgress, To: t.Status, Assignee: t.Assignee,
				Note: line, NoteBy: schedulerID.String()})
			return t, nil
		}
	}
	return s.repo.SetDependencies(ctx, id, deps)
}

// Patch edits what was decided about a task — its goal, its context, its
// definition of done. Not what the task is: project, handle, origin chat and
// reporter have no endpoint, deliberately.
func (s *Service) Patch(ctx context.Context, id uuid.UUID, req PatchRequest) (*Task, error) {
	p, err := req.Validate()
	if err != nil {
		return nil, err
	}
	if p.component != nil {
		c := strings.TrimSpace(*p.component)
		if c != "" {
			current, err := s.repo.GetByID(ctx, id)
			if err != nil {
				return nil, err
			}
			proj, err := s.repo.GetProject(ctx, current.ProjectKey)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil, fmt.Errorf("%w: project_key refers to a project "+
						"that does not exist — create it first", ErrUnknownRef)
				}
				return nil, err
			}
			if !containsString(proj.Components, c) {
				return nil, fmt.Errorf("%w: %q is not a component of project %s — the "+
					"allowed values are %s", ErrInvalid, c, proj.Key,
					allowedComponents(proj.Components))
			}
			p.component = &c // stored trimmed, the way labels are stored
		}
		// c == "": left as-is; the storage NULLIF's it to the clear.
	}
	return s.repo.Patch(ctx, id, p)
}

// SetAssignee hands a task to somebody, or to nobody. The empty string is
// refused rather than read as nobody: a client that sends "" means null but has
// no way to say so, and accepting it hides the bug in the client.
func (s *Service) SetAssignee(ctx context.Context, id uuid.UUID, req AssigneeRequest) (*Task, error) {
	assignee, err := optionalNick("assignee", req.Assignee)
	if err != nil {
		return nil, err
	}
	t, err := s.repo.SetAssignee(ctx, id, assignee)
	if err != nil {
		return nil, err
	}
	// Published even when the assignee did not change: knowing that would cost
	// a read on every write, and a consumer has to survive a repeated entry
	// anyway, because the stream delivers at least once.
	s.publish(ctx, Event{Type: EventAssignee, TaskID: t.ID, Handle: t.Handle,
		From: t.Status, To: t.Status, Assignee: t.Assignee})
	return t, nil
}

// Return puts a task that is being worked back in the queue, assigned to its
// reporter, with the reason in the event, for the task's discussion. It is the
// only way from IN_PROGRESS to TODO. Two kinds of caller: the executor giving
// up on its own, and a watcher whose budget ran out when an estimate did not
// hold. The line in the
// thread is the record of the failure — the reporter reads it before deciding
// whether to re-estimate, split, or hand the work to somebody else.
//
// Not idempotent, unlike SetStatus: a second return of a task already in TODO
// is a 409, because answering 200 would claim a reason was recorded that was not.
func (s *Service) Return(ctx context.Context, id uuid.UUID, req ReturnRequest) (*Task, error) {
	actor, reason, err := req.validate()
	if err != nil {
		return nil, err
	}
	t, err := s.repo.Return(ctx, id)
	if errors.Is(err, ErrStaleStatus) {
		current, gerr := s.repo.GetByID(ctx, id)
		if gerr != nil {
			return nil, gerr
		}
		return nil, fmt.Errorf("%w: %s is %s — only work IN_PROGRESS can be "+
			"returned", ErrIllegalTransition, current.Handle, current.Status)
	}
	if err != nil {
		return nil, err
	}
	s.publish(ctx, Event{Type: EventStatus, TaskID: t.ID, Handle: t.Handle,
		From: StatusInProgress, To: t.Status, Assignee: t.Assignee,
		Note: "Returned to the reporter: " + reason, NoteBy: actor.String()})
	return t, nil
}

// SetSummary writes the report. Blank is refused: writing over a report is an
// ordinary thing to do, erasing one by accident is not, and nothing today needs
// to clear it deliberately.
func (s *Service) SetSummary(ctx context.Context, id uuid.UUID, req SummaryRequest) (*Task, error) {
	report := strings.TrimSpace(req.SummaryReport)
	if report == "" {
		return nil, fmt.Errorf("%w: summary_report is required and cannot be "+
			"blank — it is what the notification reads when the task closes",
			ErrInvalid)
	}
	return s.repo.SetSummary(ctx, id, report)
}

// List returns tasks, narrowed by whatever the caller asked for.
//
// ready and status are refused together rather than silently ANDed. ready
// already hard-codes status='TODO' plus nothing blocking (repository.go), so a
// status list that disagreed would come back as an empty result with no word
// about why, and one that agreed would just be TODO said twice without the
// caller knowing it.
//
// open and status are refused the same way: open already says "every status
// but DONE and CANCELLED", so a status list combined with it either silently
// ANDs down to empty the moment they disagree, or just repeats what open
// already said. open with ready is not refused: ready is a subset of open.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Task, error) {
	if f.Ready && len(f.Statuses) > 0 {
		return nil, fmt.Errorf("%w: ready=true already means status=TODO (and "+
			"nothing blocking) — combine the two and either they silently AND "+
			"down to empty the moment they disagree, or status repeats what "+
			"ready already said; drop one of them", ErrInvalid)
	}
	if f.Open && len(f.Statuses) > 0 {
		return nil, fmt.Errorf("%w: open=true already means status is not DONE "+
			"or CANCELLED — combine the two and either they silently AND down "+
			"to empty the moment they disagree, or status repeats what open "+
			"already said; drop one of them", ErrInvalid)
	}
	return s.repo.List(ctx, f)
}

// Labels is the project's existing labels with their open-task counts, busiest
// first — the list a caller chooses from when tagging.
//
// Upper-cased and key-checked like DeleteProject: "abc" and "ABC" are a
// shouting convention, not two projects, and a label only exists inside one
// project's numbering.
func (s *Service) Labels(ctx context.Context, project string) ([]LabelCount, error) {
	key := strings.ToUpper(strings.TrimSpace(project))
	if key == "" {
		return nil, fmt.Errorf("%w: project is required — a label exists inside "+
			"one project's numbering, and there is deliberately no default one",
			ErrInvalid)
	}
	if !ValidProjectKey(key) {
		return nil, fmt.Errorf("%w: %q is not a project key", ErrInvalid, key)
	}
	return s.repo.ListLabels(ctx, key)
}

// SetComponents replaces the project's component list in full — the
// same replace semantics a labels list has, and the way a component is
// removed. A removal the project's tasks still hold is the storage's 409
// (repository.go): the rule is about two tables, the wording about the count.
func (s *Service) SetComponents(ctx context.Context, key string, req ComponentsRequest) (*Project, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		return nil, fmt.Errorf("%w: project is required — a component exists "+
			"inside one project's list, and there is deliberately no default one",
			ErrInvalid)
	}
	if !ValidProjectKey(key) {
		return nil, fmt.Errorf("%w: %q is not a project key", ErrInvalid, key)
	}
	// Blanks and duplicates are the same spelling problem as with labels, so
	// the list is normalised before it becomes the registry.
	return s.repo.SetComponents(ctx, key, normalizeLabels(req.Components))
}

// containsString is membership for one value in a project's component list —
// the check behind both the 400 on create/patch and the 409 on a list change.
func containsString(have []string, want string) bool {
	for _, s := range have {
		if s == want {
			return true
		}
	}
	return false
}

// allowedComponents is what the 400 shows a caller who chose outside the list:
// the list itself, or a pointer to the endpoint that fills it when it is
// still empty.
func allowedComponents(components []string) string {
	if len(components) == 0 {
		return "(none yet — set them with PUT /api/v1/projects/{key}/components)"
	}
	return strings.Join(components, ", ")
}
