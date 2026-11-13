package task

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// fakeRepo is the graph as Postgres would hand it back: tasks plus the
// dependency edges, with BlockedBy and Blocks derived on every read the same
// way selectTask does it — handles of the dependencies that are not yet DONE
// or CANCELLED, and the reverse: the tasks waiting on this one, while this
// one is not yet DONE or CANCELLED. Deriving rather than storing is what the
// test is about: the gate in SetStatus fires on what the read produced, so a
// fake that pre-painted BlockedBy would prove less than one that recomputes
// it from the statuses.
//
// Everything the test never goes through answers errNotImplemented rather than
// doing nothing plausible: a test drifting into Delete would fail loudly
// instead of silently deleting nothing and being believed.
type fakeRepo struct {
	projects map[string]*Project
	tasks    map[uuid.UUID]*Task
	deps     map[uuid.UUID][]uuid.UUID // task -> what it waits on, in set order

	// foreign is a writer that lands between the service's read and its write —
	// the window the real UPDATE cannot see through. gone deletes the row, to
	// moves it. SetStatus applies it before acting, and only once.
	foreign map[uuid.UUID]foreignWrite
}

// foreignWrite is the other writer's move.
type foreignWrite struct {
	gone bool
	to   string
}

var errNotImplemented = errors.New("fakeRepo: not used by the tests under it")

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		projects: map[string]*Project{},
		tasks:    map[uuid.UUID]*Task{},
		deps:     map[uuid.UUID][]uuid.UUID{},
		foreign:  map[uuid.UUID]foreignWrite{},
	}
}

// blockedBy mirrors the blocked_by subquery: the non-terminal dependencies,
// with the handle the refusal will name.
func (f *fakeRepo) blockedBy(id uuid.UUID) []Handle {
	var out []Handle
	for _, depID := range f.deps[id] {
		dep, ok := f.tasks[depID]
		if !ok {
			continue
		}
		if !terminal(dep.Status) {
			out = append(out, dep.Handle)
		}
	}
	return out
}

// blocking mirrors the blocks subquery, and like it filters on BOTH sides: a
// closed task holds nothing, and a closed dependent is no longer being held.
// Dropping either check is a different wrong answer, which is why
// TestBlocksIsTheReverseEdge asserts both.
func (f *fakeRepo) blocking(id uuid.UUID) []Handle {
	t, ok := f.tasks[id]
	if !ok || terminal(t.Status) {
		return []Handle{}
	}
	out := []Handle{}
	for otherID, deps := range f.deps {
		other, ok := f.tasks[otherID]
		if !ok || terminal(other.Status) {
			continue
		}
		for _, depID := range deps {
			if depID == id {
				out = append(out, other.Handle)
			}
		}
	}
	// The SQL orders both subqueries by project then number; the fake sorts so
	// that a test can assert the list rather than its contents in any order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// terminal is the SQL's NOT IN ('DONE','CANCELLED'), written once.
func terminal(status string) bool {
	return status == StatusDone || status == StatusCancelled
}

func (f *fakeRepo) withDerived(id uuid.UUID) (*Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	out := *t
	// Postgres answers '{}' for this column, never null — NOT NULL DEFAULT '{}'
	// (migration 0005) — and this read mirrors what Postgres would hand back.
	if out.Labels == nil {
		out.Labels = []string{}
	}
	out.BlockedBy = f.blockedBy(id)
	out.Blocks = f.blocking(id)
	return &out, nil
}

func (f *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (*Task, error) {
	return f.withDerived(id)
}

func (f *fakeRepo) GetByHandle(_ context.Context, h Handle) (*Task, error) {
	for _, t := range f.tasks {
		if t.Handle == h {
			return f.withDerived(t.ID)
		}
	}
	return nil, ErrNotFound
}

func (f *fakeRepo) SetStatus(_ context.Context, id uuid.UUID, from, to string) (*Task, error) {
	if fw, ok := f.foreign[id]; ok {
		delete(f.foreign, id)
		if fw.gone {
			delete(f.tasks, id)
		} else {
			f.tasks[id].Status = fw.to
		}
	}
	t, ok := f.tasks[id]
	if !ok {
		// Zero rows, like the real repository's UPDATE answers it. The real one
		// cannot tell the row being gone from the status having moved, and a
		// fake that could would be testing itself instead of the service's
		// second read — the part under test.
		return nil, ErrStaleStatus
	}
	if t.Status != from {
		return nil, ErrStaleStatus
	}
	t.Status = to
	return f.withDerived(id)
}

func (f *fakeRepo) SetDependencies(_ context.Context, id uuid.UUID, deps []uuid.UUID) (*Task, error) {
	if _, ok := f.tasks[id]; !ok {
		return nil, ErrNotFound
	}
	f.deps[id] = deps
	return f.withDerived(id)
}

func (f *fakeRepo) CreateProject(_ context.Context, p Project) (*Project, error) {
	if p.Components == nil {
		p.Components = []string{}
	}
	if _, ok := f.projects[p.Key]; ok {
		return nil, fmt.Errorf("%w: project %s already exists", ErrInvalid, p.Key)
	}
	f.projects[p.Key] = &p
	return f.GetProject(context.Background(), p.Key)
}
func (f *fakeRepo) ListProjects(_ context.Context) ([]Project, error) {
	out := []Project{}
	for _, p := range f.projects {
		proj := *p
		if proj.Components == nil {
			proj.Components = []string{}
		}
		out = append(out, proj)
	}
	return out, nil
}
func (f *fakeRepo) DeleteProject(context.Context, string) error {
	return errNotImplemented
}
func (f *fakeRepo) Delete(context.Context, uuid.UUID) error {
	return errNotImplemented
}

// Create stores the row as written; the service is what normalised it before
// the write, and that is what the test under it reads back.
func (f *fakeRepo) Create(_ context.Context, t *Task) (*Task, error) {
	f.tasks[t.ID] = t
	return f.withDerived(t.ID)
}

// Patch applies only the fields that were sent — the real one's COALESCE, where
// a nil field is the one that was not sent.
func (f *fakeRepo) Patch(_ context.Context, id uuid.UUID, p patch) (*Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	if p.goal != nil {
		t.Goal = *p.goal
	}
	if p.description != nil {
		t.Description = *p.description
	}
	if p.acceptance != nil {
		t.AcceptanceCriteria = *p.acceptance
	}
	if p.points != nil {
		t.Points = p.points
	}
	if p.labels != nil {
		t.Labels = *p.labels
	}
	// The real one is NULLIF($7, ''): not sent leaves it, "" clears it to the
	// NULL the column uses for "no component".
	if p.component != nil {
		if *p.component == "" {
			t.Component = nil
		} else {
			t.Component = p.component
		}
	}
	return f.withDerived(id)
}
func (f *fakeRepo) SetAssignee(_ context.Context, id uuid.UUID, assignee *string) (*Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	t.Assignee = assignee
	return f.withDerived(id)
}

// Return mirrors the real one's guard and its effects.
func (f *fakeRepo) Return(_ context.Context, id uuid.UUID) (*Task, error) {
	t, ok := f.tasks[id]
	if !ok || t.Status != StatusInProgress {
		return nil, ErrStaleStatus
	}
	t.Status = StatusTodo
	reporter := t.Reporter
	t.Assignee = &reporter
	return f.withDerived(id)
}

// Park mirrors the real one's guard and its effects: TODO with the assignee
// left in place, the new dependency set written.
func (f *fakeRepo) Park(_ context.Context, id uuid.UUID, deps []uuid.UUID) (*Task, error) {
	t, ok := f.tasks[id]
	if !ok || t.Status != StatusInProgress {
		return nil, ErrStaleStatus
	}
	t.Status = StatusTodo
	f.deps[id] = deps
	return f.withDerived(id)
}
func (f *fakeRepo) SetSummary(context.Context, uuid.UUID, string) (*Task, error) {
	return nil, errNotImplemented
}

// List answers the filters the tests under it use: project, statuses and
// labels (the task must carry every label named, like the real @> does).
// Sorted by handle so a test can assert the result, not its contents in any
// order.
func (f *fakeRepo) List(_ context.Context, lf ListFilter) ([]Task, error) {
	out := []Task{}
	for _, t := range f.tasks {
		if lf.Project != "" && t.ProjectKey != lf.Project {
			continue
		}
		if len(lf.Statuses) > 0 {
			in := false
			for _, s := range lf.Statuses {
				if t.Status == s {
					in = true
				}
			}
			if !in {
				continue
			}
		}
		if lf.Open && terminal(t.Status) {
			// The real one is NOT IN ('DONE','CANCELLED'); terminal() is that
			// pair, written once.
			continue
		}
		if len(lf.Labels) > 0 && !hasAllLabels(t.Labels, lf.Labels) {
			continue
		}
		if lf.Component != "" && (t.Component == nil || *t.Component != lf.Component) {
			continue
		}
		if d, err := f.withDerived(t.ID); err == nil {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectKey != out[j].ProjectKey {
			return out[i].ProjectKey < out[j].ProjectKey
		}
		return out[i].Handle.Number < out[j].Handle.Number
	})
	return out, nil
}

func hasAllLabels(have, want []string) bool {
	for _, w := range want {
		ok := false
		for _, h := range have {
			if h == w {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// GetProject and SetComponents stand in for the two projects reads behind
// a component: the 400 asks the project for its list, and the 409
// counts the tasks that would be orphaned by a list change.
func (f *fakeRepo) GetProject(_ context.Context, key string) (*Project, error) {
	p, ok := f.projects[key]
	if !ok {
		return nil, fmt.Errorf("%w: no project %s", ErrNotFound, key)
	}
	out := *p
	if out.Components == nil {
		out.Components = []string{}
	}
	return &out, nil
}

func (f *fakeRepo) SetComponents(_ context.Context, key string, components []string) (*Project, error) {
	p, ok := f.projects[key]
	if !ok {
		return nil, fmt.Errorf("%w: no project %s", ErrNotFound, key)
	}
	if components == nil {
		components = []string{}
	}
	removed := []string{}
	for _, c := range p.Components {
		if !containsString(components, c) {
			removed = append(removed, c)
		}
	}
	held := 0
	for _, t := range f.tasks {
		if t.ProjectKey != key || t.Component == nil {
			continue
		}
		for _, c := range removed {
			if *t.Component == c {
				held++
				break
			}
		}
	}
	if held > 0 {
		return nil, fmt.Errorf("%w: %d task(s) of project %s still hold a "+
			"component this list removes", ErrInUse, held, key)
	}
	p.Components = components
	return f.GetProject(context.Background(), key)
}

// ListLabels is the same answer ListLabels in repository.go gives from SQL,
// over the tasks the test seeded: a label is listed while any task of the
// project carries it, the count is open tasks only, busiest first.
func (f *fakeRepo) ListLabels(_ context.Context, project string) ([]LabelCount, error) {
	out := []LabelCount{}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, t := range f.tasks {
		if t.ProjectKey != project {
			continue
		}
		for _, l := range t.Labels {
			// listed and counted are separate facts: a label that only a closed
			// task carries is listed with 0, and the 0 must not read as "not
			// listed yet".
			if !seen[l] {
				seen[l] = true
				out = append(out, LabelCount{Label: l})
			}
			if !terminal(t.Status) {
				counts[l]++
			}
		}
	}
	for i := range out {
		out[i].OpenTasks = counts[out[i].Label]
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OpenTasks != out[j].OpenTasks {
			return out[i].OpenTasks > out[j].OpenTasks
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}

// seed puts a task straight into the graph, the way the test means one: a
// handle that can be said out loud, and a status the graph already holds.
func seed(f *fakeRepo, handle, status string) *Task {
	h, err := ParseHandle(handle)
	if err != nil {
		panic(err)
	}
	id := uuid.New()
	t := &Task{
		ID:         id,
		Handle:     h,
		ProjectKey: h.Project,
		Status:     status,
	}
	f.tasks[id] = t
	return t
}

// seedProject puts a project into the fake with its component list —
// the list a task's component must be drawn from.
func seedProject(f *fakeRepo, key string, components ...string) *Project {
	if components == nil {
		components = []string{}
	}
	p := &Project{Key: key, Name: key + " project", Components: components}
	f.projects[key] = p
	return p
}

// The gate under test, at the layer that owns it: IN_PROGRESS while anything
// is still open in the way is refused, and the refusal names what is in the
// way — the part a caller can act on without a second read.
func TestSetStatusRefusesWhileDependsOnIsOpen(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	svc := &Service{repo: fake}

	blockers := []*Task{
		seed(fake, "ABC-2", StatusDraft),
		seed(fake, "ABC-3", StatusTodo),
	}
	blocked := seed(fake, "ABC-1", StatusTodo)
	_, err := svc.SetDependencies(ctx, blocked.ID, DependenciesRequest{DependsOn: []string{
		blockers[0].ID.String(), blockers[1].ID.String(),
	}})
	require.NoError(t, err)

	_, err = svc.SetStatus(ctx, blocked.ID, StatusRequest{Status: StatusInProgress})
	require.Error(t, err, "IN_PROGRESS must be refused while a dependency is open")
	require.ErrorIs(t, err, ErrIllegalTransition,
		"this is a disagreement about where the work is, not a typo — 409, not 400")
	for _, b := range blockers {
		require.Contains(t, err.Error(), b.Handle.String(),
			"a refusal that does not name the blockers is one that gets investigated")
	}

	got, err := svc.Get(ctx, blocked.ID)
	require.NoError(t, err)
	require.Equal(t, StatusTodo, got.Status, "the refusal must not move the task")
}

// The same refusal at the door the caller actually knocks on: 409, with the
// blockers in the body. The status-code part matters because ErrIllegalTransition
// is the error that shares a code with nothing else — a refactor that refiles it
// under ErrInvalid changes the answer to 400 and this is what notices.
func TestSetStatusConflictAnswers409NamingBlockers(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	blocker := seed(fake, "ABC-2", StatusTodo)
	blocked := seed(fake, "ABC-1", StatusTodo)
	fake.deps[blocked.ID] = []uuid.UUID{blocker.ID}

	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/tasks/"+blocked.ID.String()+"/status",
		strings.NewReader(`{"status":"IN_PROGRESS"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "ABC-2")
}

// The gate must be a gate, not a wall: the moment what the task waits on is
// closed, the same request goes through. Without this half, a "fix" that
// refuses IN_PROGRESS unconditionally would read as a success.
func TestSetStatusAllowsOnceBlockersAreClosed(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	svc := &Service{repo: fake}

	closed := []*Task{
		seed(fake, "ABC-2", StatusDone),
		seed(fake, "ABC-3", StatusCancelled),
	}
	blocked := seed(fake, "ABC-1", StatusTodo)
	_, err := svc.SetDependencies(ctx, blocked.ID, DependenciesRequest{DependsOn: []string{
		closed[0].ID.String(), closed[1].ID.String(),
	}})
	require.NoError(t, err)

	who := "alisa"
	fake.tasks[blocked.ID].Assignee = &who
	got, err := svc.SetStatus(ctx, blocked.ID, StatusRequest{Status: StatusInProgress})
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, got.Status)
}

// The assignee half of the same rule: nothing goes into IN_PROGRESS without an
// owner. A task the board says is being worked on that nobody is assigned to
// is a claim nobody can act on — and a dispatcher would keep handing it out
// as work in flight. The refusal must name what is missing, not just refuse.
func TestSetStatusRefusesWithoutAnAssignee(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	svc := &Service{repo: fake}

	unassigned := seed(fake, "ABC-1", StatusTodo)
	_, err := svc.SetStatus(ctx, unassigned.ID, StatusRequest{Status: StatusInProgress})
	require.Error(t, err, "IN_PROGRESS must be refused while nobody is assigned")
	require.ErrorIs(t, err, ErrIllegalTransition,
		"this is a disagreement about who owns the work, not a typo — 409, not 400")
	require.Contains(t, err.Error(), "ABC-1", "the refusal must name the task")
	require.Contains(t, err.Error(), "assignee",
		"a refusal that does not say what is missing is one that gets investigated")

	got, err := svc.Get(ctx, unassigned.ID)
	require.NoError(t, err)
	require.Equal(t, StatusTodo, got.Status, "the refusal must not move the task")
}

// The same refusal from the other side of the board: QA that goes back to work
// without an owner is the same lie as starting an unowned task from TODO.
func TestSetStatusFromQARefusesWithoutAnAssignee(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	svc := &Service{repo: fake}

	qa := seed(fake, "ABC-2", StatusQA)
	_, err := svc.SetStatus(ctx, qa.ID, StatusRequest{Status: StatusInProgress})
	require.Error(t, err, "back from QA is still a move into IN_PROGRESS")
	require.ErrorIs(t, err, ErrIllegalTransition)
	require.Contains(t, err.Error(), "assignee")
}

// Assigning lifts the refusal: the gate is about ownership, not about the word.
// Without this half, a "fix" that refuses IN_PROGRESS unconditionally would
// read as a success.
func TestSetStatusAllowsOnceSomeoneIsAssigned(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	svc := &Service{repo: fake}

	task := seed(fake, "ABC-3", StatusTodo)
	_, err := svc.SetStatus(ctx, task.ID, StatusRequest{Status: StatusInProgress})
	require.ErrorIs(t, err, ErrIllegalTransition, "unassigned must be refused first")

	who := "alisa"
	_, err = svc.SetAssignee(ctx, task.ID, AssigneeRequest{Assignee: &who})
	require.NoError(t, err)

	got, err := svc.SetStatus(ctx, task.ID, StatusRequest{Status: StatusInProgress})
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, got.Status)
}

// The assignee refusal at the door the caller actually knocks on: 409, with
// the missing assignee in the body — the same answer shape as the dependency
// gate, so a caller can treat both refusals alike.
func TestSetStatusConflictAnswers409NamingAssignee(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	unassigned := seed(fake, "ABC-1", StatusTodo)

	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/tasks/"+unassigned.ID.String()+"/status",
		strings.NewReader(`{"status":"IN_PROGRESS"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "assignee")
}

// Blocks is the other half of the dependency: not what this task waits on, but
// what waits on it. Both status filters are asserted here because they are
// different mistakes with different symptoms, and the one on the dependent's
// side is the easy one to miss — without it a task goes on claiming to hold a
// dependent that was cancelled.
func TestBlocksIsTheReverseEdge(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	svc := &Service{repo: fake}

	holder := seed(fake, "ABC-1", StatusTodo)
	waiting := seed(fake, "ABC-2", StatusTodo)
	alsoWaiting := seed(fake, "ABC-3", StatusDraft)
	giveUp := seed(fake, "ABC-4", StatusTodo)
	// ABC-1 is moved into IN_PROGRESS below: the assignee gate needs an owner
	// for that move, and this test is about the edge list, not about the gate.
	executor := "executor"
	fake.tasks[holder.ID].Assignee = &executor
	for _, dependent := range []*Task{waiting, alsoWaiting, giveUp} {
		_, err := svc.SetDependencies(ctx, dependent.ID,
			DependenciesRequest{DependsOn: []string{holder.ID.String()}})
		require.NoError(t, err)
	}

	got, err := svc.Get(ctx, holder.ID)
	require.NoError(t, err)
	require.Equal(t, []Handle{
		{Project: "ABC", Number: 2},
		{Project: "ABC", Number: 3},
		{Project: "ABC", Number: 4},
	}, got.Blocks, "everything waiting on ABC-1, and nothing else")
	require.Empty(t, got.BlockedBy, "ABC-1 waits on nothing — the edges are not the same list")

	// The far side closes: it is no longer being held, so it stops counting.
	// Without this the number inflates with every abandoned dependent, which is
	// the one number the field exists to report.
	_, err = svc.SetStatus(ctx, giveUp.ID, StatusRequest{Status: StatusCancelled})
	require.NoError(t, err)
	got, err = svc.Get(ctx, holder.ID)
	require.NoError(t, err)
	require.Equal(t, []Handle{
		{Project: "ABC", Number: 2},
		{Project: "ABC", Number: 3},
	}, got.Blocks, "a cancelled dependent is not being held")

	// The near side closes: a finished task holds nothing at all, however many
	// dependents still point at it.
	for _, s := range []string{StatusInProgress, StatusQA, StatusUAT, StatusDone} {
		_, err = svc.SetStatus(ctx, holder.ID, StatusRequest{Status: s})
		require.NoError(t, err, "moving ABC-1 to %s", s)
	}
	got, err = svc.Get(ctx, holder.ID)
	require.NoError(t, err)
	require.Empty(t, got.Blocks, "a DONE task holds nothing")

	// And the dependents agree: ABC-1 is closed, so it no longer blocks them.
	stillWaiting, err := svc.Get(ctx, waiting.ID)
	require.NoError(t, err)
	require.Empty(t, stillWaiting.BlockedBy, "a closed dependency stops blocking")
}

// Deleted between the read and the write: the write sees zero rows and cannot
// say which of the two happened, so the service must, with the read that
// follows. The answer decides what the caller does next — a 409 says "look at
// the task and try again", and retrying has nothing to retry when the task is
// gone.
func TestSetStatusDeletedBetweenReadAndWriteAnswers404(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	task := seed(fake, "ABC-1", StatusTodo)
	// The move under test is the write race, not the assignee gate: give the
	// task an owner so the request reaches the write at all.
	who := "alisa"
	fake.tasks[task.ID].Assignee = &who
	fake.foreign[task.ID] = foreignWrite{gone: true}

	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/tasks/"+task.ID.String()+"/status",
		strings.NewReader(`{"status":"IN_PROGRESS"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "not found")
}

// Moved between the read and the write: the same zero rows, but the second
// read finds the task, so the 409 stands. Without this half, a fix that
// answered 404 to every refused write would read as a success.
func TestSetStatusMovedBetweenReadAndWriteAnswers409(t *testing.T) {
	fake := newFakeRepo()
	h := NewHandler(&Service{repo: fake})
	r := mux.NewRouter()
	RegisterRoutes(r, h)

	task := seed(fake, "ABC-1", StatusTodo)
	// The move under test is the write race, not the assignee gate: give the
	// task an owner so the request reaches the write at all.
	who := "alisa"
	fake.tasks[task.ID].Assignee = &who
	fake.foreign[task.ID] = foreignWrite{to: StatusCancelled}

	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/tasks/"+task.ID.String()+"/status",
		strings.NewReader(`{"status":"IN_PROGRESS"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "changed status")
}
