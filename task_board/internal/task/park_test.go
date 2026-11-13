package task

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Work IN_PROGRESS gets an open dependency put on it, and the board
// parks it — TODO again so a dispatcher stops handing it out, the executor
// left in place so the same executor resumes when the dependency closes, and
// the reason in the thread. The expected values are the board's, not the
// service's: a fix that parks with a different status or drops the executor
// must fail these.
func TestSetDependenciesParksWorkInProgressUnderAnOpenDependency(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}

	executor := "executor"
	task := seed(fake, "ABC-5", StatusInProgress)
	task.Assignee = &executor
	blocker := seed(fake, "ABC-4", StatusTodo)

	got, err := svc.SetDependencies(ctx, task.ID, DependenciesRequest{
		DependsOn: []string{blocker.ID.String()},
	})
	require.NoError(t, err)

	// TODO again, so a dispatcher (which takes only Ready = TODO and nothing
	// holding) stops handing it out while ABC-4 is open.
	require.Equal(t, StatusTodo, got.Status, "an IN_PROGRESS task under an open dependency is parked, not left in flight")

	// ...with the same executor: Return hands the task to its reporter, parking
	// must not — the whole point is that this executor resumes it.
	require.NotNil(t, got.Assignee)
	require.Equal(t, executor, *got.Assignee)

	// ...with the dependency written, so the park is real and shows up in the
	// list as held.
	require.Equal(t, []Handle{{Project: "ABC", Number: 4}}, got.BlockedBy)

	// ...and on the stream as the move that happened, with the reason for the
	// discussion, naming the dependency.
	require.Len(t, rec.events, 1)
	line := rec.events[0].Note
	require.Contains(t, line, "Parked")
	require.Contains(t, line, "ABC-4", "the line names what the work waits for")
	require.Empty(t, rec.events[0].NoteBy,
		"said by nobody: the executor would read its own line as words it said, "+
			"and the board knows no other subject — the consumer attributes it")
	require.Equal(t, []Event{{
		Type: EventStatus, TaskID: task.ID, Handle: task.Handle,
		From: StatusInProgress, To: StatusTodo, Assignee: &executor,
		Note: line,
	}}, rec.events)
}

// Several open dependencies: every one of them is in the line, because a
// record that names only the first would be a record that hides the rest.
func TestParkNamesEveryOpenDependency(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}

	task := seed(fake, "ABC-7", StatusInProgress)
	task.Assignee = ptr("someone")
	blockers := []*Task{
		seed(fake, "ABC-2", StatusInProgress),
		seed(fake, "ABC-3", StatusTodo),
	}
	got, err := svc.SetDependencies(ctx, task.ID, DependenciesRequest{DependsOn: []string{
		blockers[0].ID.String(), blockers[1].ID.String(),
	}})
	require.NoError(t, err)
	require.Equal(t, StatusTodo, got.Status)

	require.Len(t, rec.events, 1)
	line := rec.events[0].Note
	for _, b := range blockers {
		require.Contains(t, line, b.Handle.String(),
			"a parked task that does not name all its blockers is parked without a record")
	}
}

// The dependency is closed: the edge may still be written (the task may wait on
// what the graph says), but parking is a reaction to work being held, and a
// closed task holds nothing.
func TestSetDependenciesWithAClosedDependencyDoesNotPark(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}

	executor := "executor"
	task := seed(fake, "ABC-5", StatusInProgress)
	task.Assignee = &executor
	closed := []*Task{
		seed(fake, "ABC-4", StatusDone),
		seed(fake, "ABC-3", StatusCancelled),
	}

	got, err := svc.SetDependencies(ctx, task.ID, DependenciesRequest{DependsOn: []string{
		closed[0].ID.String(), closed[1].ID.String(),
	}})
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, got.Status, "a closed dependency holds nothing — the work stays in flight")
	require.Equal(t, executor, *got.Assignee)
	require.Empty(t, got.BlockedBy)
	require.Empty(t, rec.events, "no move, no event")
}

// A task that is not being worked keeps exactly its state: it is already out
// of a dispatcher's hands (or the edge was just recorded for later), and a
// park would be a move the board invented.
func TestSetDependenciesOnATodoTaskDoesNotPark(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}

	executor := "executor"
	task := seed(fake, "ABC-5", StatusTodo)
	task.Assignee = &executor
	blocker := seed(fake, "ABC-4", StatusInProgress)

	got, err := svc.SetDependencies(ctx, task.ID, DependenciesRequest{
		DependsOn: []string{blocker.ID.String()},
	})
	require.NoError(t, err)
	require.Equal(t, StatusTodo, got.Status)
	require.Equal(t, executor, *got.Assignee)
	require.Empty(t, rec.events)
}

// An IN_PROGRESS task whose new set is empty is not being held at all: an
// empty dependency set is an ordinary request, not a reason to move the work.
func TestSetDependenciesClearingDepsOnWorkInProgressDoesNotPark(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRepo()
	rec := &recorder{}
	svc := &Service{repo: fake, events: rec}

	executor := "executor"
	task := seed(fake, "ABC-5", StatusInProgress)
	task.Assignee = &executor
	fake.deps[task.ID] = []uuid.UUID{uuid.New()} // something it waits on now

	got, err := svc.SetDependencies(ctx, task.ID, DependenciesRequest{})
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, got.Status)
	require.Empty(t, rec.events)
}
