package task

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is the only thing in this service that speaks SQL.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// selectTask is the column list, and the last four entries are the point of
// it: depends_on, blocked_by and blocks are computed in the same statement
// rather than fetched per task afterwards. A list of forty tasks must not
// become forty-one queries, and more importantly these are derived facts — the
// moment they are fetched separately, someone will cache one of them.
//
// blocked_by and blocks each carry the other side's project and number
// because that is what a reader does with them: says them out loud.
//
// blocks is the same join read the other way — b on the task_id side, t on the
// depends_on side — and it needs the status filter on BOTH, for two separate
// reasons that are easy to mistake for one:
//
//   - on t, because a closed task holds nothing: for a DONE task the subquery
//     must answer empty however many dependents point at it;
//   - on b, because a closed dependent is no longer being held: a cancelled
//     task is not waiting for anything, so claiming to hold it inflates the
//     only number this field exists to report.
//
// The second is easy to miss: without it a task keeps claiming to hold a
// dependent that was cancelled. Note the asymmetry with blocked_by,
// which needs the filter only on b: there, t is the task being read, and a
// closed task is still allowed to say what it was waiting on.
//
// open_child_* are the subtasks not yet DONE or CANCELLED, filtered on the
// child only, like blocked_by: a closed parent with open children is a state
// worth seeing, not one to hide.
const selectTask = `
SELECT t.id, t.project_key, t.handle, t.goal, t.description,
       t.acceptance_criteria, t.status, t.origin, t.discussion,
       t.reporter, t.assignee, t.parent_task_id, t.points, t.summary_report,
       t.labels, t.component, t.created_at, t.updated_at, t.closed_at,
       COALESCE((SELECT array_agg(d.depends_on_id ORDER BY d.created_at)
                 FROM task_dependencies d
                 WHERE d.task_id = t.id), '{}') AS depends_on,
       COALESCE((SELECT array_agg(b.project_key ORDER BY b.project_key, b.handle)
                 FROM task_dependencies d
                 JOIN tasks b ON b.id = d.depends_on_id
                 WHERE d.task_id = t.id
                   AND b.status NOT IN ('DONE','CANCELLED')), '{}') AS blocked_keys,
       COALESCE((SELECT array_agg(b.handle ORDER BY b.project_key, b.handle)
                 FROM task_dependencies d
                 JOIN tasks b ON b.id = d.depends_on_id
                 WHERE d.task_id = t.id
                   AND b.status NOT IN ('DONE','CANCELLED')), '{}') AS blocked_numbers,
       COALESCE((SELECT array_agg(b.project_key ORDER BY b.project_key, b.handle)
                 FROM task_dependencies d
                 JOIN tasks b ON b.id = d.task_id
                 WHERE d.depends_on_id = t.id
                   AND t.status NOT IN ('DONE','CANCELLED')
                   AND b.status NOT IN ('DONE','CANCELLED')), '{}') AS blocking_keys,
       COALESCE((SELECT array_agg(b.handle ORDER BY b.project_key, b.handle)
                 FROM task_dependencies d
                 JOIN tasks b ON b.id = d.task_id
                 WHERE d.depends_on_id = t.id
                   AND t.status NOT IN ('DONE','CANCELLED')
                   AND b.status NOT IN ('DONE','CANCELLED')), '{}') AS blocking_numbers,
       COALESCE((SELECT array_agg(c.project_key ORDER BY c.project_key, c.handle)
                 FROM tasks c
                 WHERE c.parent_task_id = t.id
                   AND c.status NOT IN ('DONE','CANCELLED')), '{}') AS open_child_keys,
       COALESCE((SELECT array_agg(c.handle ORDER BY c.project_key, c.handle)
                 FROM tasks c
                 WHERE c.parent_task_id = t.id
                   AND c.status NOT IN ('DONE','CANCELLED')), '{}') AS open_child_numbers
FROM tasks t`

// ErrUnknownRef means one of the task's references — origin chat, reporter,
// assignee, parent, project — points at a row that does not exist. It is the
// foreign key talking, and it is surfaced as its own error so the handler can
// answer 400 rather than 500: the caller sent a bad value, the service is fine.
var ErrUnknownRef = errors.New("unknown reference")

// Which constraint fired, in words the caller can act on. Matching on the
// constraint name rather than the message keeps this working if the server ever
// answers in another language — same reason the SQLSTATE is checked and not the
// text. A name missing from this map still produces ErrUnknownRef, just without
// the detail; it fails closed rather than falling through to a 500.
var refNames = map[string]string{
	"tasks_parent_task_id_fkey":            "parent_task_id refers to a task that does not exist",
	"tasks_project_key_fkey":               "project_key refers to a project that does not exist — create it first",
	"task_dependencies_task_id_fkey":       "the task being linked does not exist",
	"task_dependencies_depends_on_id_fkey": "depends_on refers to a task that does not exist",
}

// refErr turns a foreign key violation or a CHECK violation into something the
// caller can be told, and leaves every other database error alone.
func refErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case "23503": // foreign_key_violation
		if detail, ok := refNames[pgErr.ConstraintName]; ok {
			return fmt.Errorf("%w: %s", ErrUnknownRef, detail)
		}
		return fmt.Errorf("%w: %s", ErrUnknownRef, pgErr.ConstraintName)
	case "23514": // check_violation
		if pgErr.ConstraintName == "task_dependencies_check" {
			return fmt.Errorf("%w: a task cannot depend on itself", ErrInvalid)
		}
		return fmt.Errorf("%w: %s", ErrInvalid, pgErr.ConstraintName)
	case "23505": // unique_violation
		return fmt.Errorf("%w: %s already exists", ErrInvalid, pgErr.ConstraintName)
	}
	return nil
}

// row is what pgx.Row and pgx.Rows have in common, so the column list is
// written out once instead of once per query.
type row interface{ Scan(dest ...any) error }

func scanTask(r row) (*Task, error) {
	var (
		t               Task
		blockedKeys     []string
		blockedNumbers  []int64
		blockingKeys    []string
		blockingNumbers []int64
		childKeys       []string
		childNumbers    []int64
	)
	err := r.Scan(&t.ID, &t.ProjectKey, &t.Handle.Number, &t.Goal, &t.Description,
		&t.AcceptanceCriteria, &t.Status, &t.Origin, &t.Discussion,
		&t.Reporter, &t.Assignee, &t.ParentTaskID, &t.Points, &t.SummaryReport,
		&t.Labels, &t.Component, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt,
		&t.DependsOn, &blockedKeys, &blockedNumbers,
		&blockingKeys, &blockingNumbers, &childKeys, &childNumbers)
	if err != nil {
		return nil, err
	}
	t.Handle.Project = t.ProjectKey

	// The column is NOT NULL DEFAULT '{}' (migration 0005), so this should not
	// fire — but a reader must get [] and never null, and this is the only read
	// that exists.
	if t.Labels == nil {
		t.Labels = []string{}
	}

	// Two arrays rather than one composite column, zipped here: a composite
	// type would have to be declared in the schema and understood by pgx, which
	// is a lot of machinery for a pair of scalars. The ORDER BY in each pair of
	// subqueries is identical, which is what makes the zip correct.
	if t.BlockedBy, err = zipHandles("blocked_by", blockedKeys, blockedNumbers); err != nil {
		return nil, err
	}
	if t.Blocks, err = zipHandles("blocks", blockingKeys, blockingNumbers); err != nil {
		return nil, err
	}
	if t.OpenSubtasks, err = zipHandles("open_subtasks", childKeys, childNumbers); err != nil {
		return nil, err
	}
	return &t, nil
}

func zipHandles(field string, keys []string, numbers []int64) ([]Handle, error) {
	if len(keys) != len(numbers) {
		return nil, fmt.Errorf("%s came back ragged: %d keys, %d numbers",
			field, len(keys), len(numbers))
	}
	out := make([]Handle, len(keys))
	for i := range keys {
		out[i] = Handle{Project: keys[i], Number: numbers[i]}
	}
	return out, nil
}

// CreateProject registers a line of work. Returns ErrInvalid if the key is
// taken — re-registering a key silently would hand one project's numbering to
// another.
func (r *Repository) CreateProject(ctx context.Context, p Project) (*Project, error) {
	// The column is NOT NULL DEFAULT '{}' (migration 0006), so write an empty
	// list as an empty list — never nil, and never a caller's omission.
	if p.Components == nil {
		p.Components = []string{}
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO projects (key, name, components) VALUES ($1, $2, $3)`,
		p.Key, p.Name, p.Components)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: project %s already exists", ErrInvalid, p.Key)
		}
		if ref := refErr(err); ref != nil {
			return nil, ref
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return &p, nil
}

func (r *Repository) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT key, name, components FROM projects ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Key, &p.Name, &p.Components); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		// NOT NULL DEFAULT '{}', so this is a safety net like Labels' in
		// scanTask: a reader gets [] and never null.
		if p.Components == nil {
			p.Components = []string{}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject is the read behind a component check: create and patch
// both ask "is this value in the project's list?", and the list is on the
// project, not the task.
func (r *Repository) GetProject(ctx context.Context, key string) (*Project, error) {
	var p Project
	err := r.pool.QueryRow(ctx,
		`SELECT key, name, components FROM projects WHERE key = $1`, key).
		Scan(&p.Key, &p.Name, &p.Components)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no project %s", ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if p.Components == nil {
		p.Components = []string{}
	}
	return &p, nil
}

// SetComponents replaces the list in full, and is the refusal: a
// value the new list drops may still be carried by the project's tasks, and
// deleting a registry entry out from under a row is how a 400 becomes a row
// nobody can clear, because the list no longer offers the clear. So it does
// not delete: it answers 409 with the count, the same shape as DeleteProject.
func (r *Repository) SetComponents(ctx context.Context, key string, components []string) (*Project, error) {
	if components == nil {
		components = []string{}
	}
	current, err := r.GetProject(ctx, key)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, c := range current.Components {
		if !containsString(components, c) {
			removed = append(removed, c)
		}
	}
	if len(removed) > 0 {
		var held int
		if err := r.pool.QueryRow(ctx,
			`SELECT count(*) FROM tasks WHERE project_key = $1
				AND component = ANY($2)`, key, removed).Scan(&held); err != nil {
			return nil, fmt.Errorf("count component holders: %w", err)
		}
		if held > 0 {
			return nil, fmt.Errorf("%w: %d task(s) of project %s still hold a "+
				"component this list removes (%s) — clear the component on them "+
				"first (PATCH with \"component\": \"\")", ErrInUse, held, key,
				strings.Join(removed, ", "))
		}
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE projects SET components = $2 WHERE key = $1`, key, components)
	if err != nil {
		return nil, fmt.Errorf("set components: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("%w: no project %s", ErrNotFound, key)
	}
	return r.GetProject(ctx, key)
}

// DeleteProject removes a line of work, and refuses while it still holds tasks.
//
// Deliberately not a cascade. The typical caller is a test run cleaning up
// after itself, and it knows its own task ids because it just created them —
// so a single call that removes an unknown amount of work buys nothing and
// costs the one mistake nobody recovers from. The refusal counts what is in the
// way rather than naming it: a test project holds a handful, and a human
// pointing this at a real project wants the number, not forty handles.
func (r *Repository) DeleteProject(ctx context.Context, key string) error {
	var held int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM tasks WHERE project_key = $1`, key).Scan(&held)
	if err != nil {
		return fmt.Errorf("count project tasks: %w", err)
	}
	if held > 0 {
		return fmt.Errorf("%w: project %s still holds %d task(s) — delete them "+
			"first; this does not cascade", ErrInUse, key, held)
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM projects WHERE key = $1`, key)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: no project %s", ErrNotFound, key)
	}
	return nil
}

// Delete removes a task, and nothing else: its discussion and origin are
// links, and the chats they name are not the board's.
//
// Children are refused by name rather than by the parent_task_id constraint,
// which is NO ACTION on purpose: removing a parent should not silently take its
// subtree, and the caller is told which tasks are in the way.
func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	rows, err := r.pool.Query(ctx, `
		SELECT project_key, handle FROM tasks
		WHERE parent_task_id = $1
		ORDER BY project_key, handle`, id)
	if err != nil {
		return fmt.Errorf("find children: %w", err)
	}
	var children []Handle
	for rows.Next() {
		var h Handle
		if err := rows.Scan(&h.Project, &h.Number); err != nil {
			rows.Close()
			return fmt.Errorf("scan child: %w", err)
		}
		children = append(children, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("find children: %w", err)
	}
	if len(children) > 0 {
		return fmt.Errorf("%w: it is the parent of %s", ErrInUse, joinHandles(children))
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Create allocates the task's number and inserts it, both inside one
// transaction. The allocation is an UPDATE ... RETURNING on the project row, so
// two concurrent creations in the same project queue behind a row lock and
// cannot be handed the same number — while creations in different projects
// touch different rows and do not wait on each other.
//
// A rolled-back insert leaves a gap in the numbering. Gaps are fine; two tasks
// called ABC-12 are not.
func (r *Repository) Create(ctx context.Context, t *Task) (*Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var number int64
	err = tx.QueryRow(ctx, `
		UPDATE projects SET next_handle = next_handle + 1
		WHERE key = $1
		RETURNING next_handle - 1`, t.ProjectKey).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		// Caught here rather than by the foreign key, because this statement
		// runs first and would otherwise fail with a less useful message.
		return nil, fmt.Errorf("%w: project_key refers to a project that does "+
			"not exist — create it first", ErrUnknownRef)
	}
	if err != nil {
		return nil, fmt.Errorf("allocate handle: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO tasks (id, project_key, handle, goal, description,
			acceptance_criteria, status, origin, discussion, reporter,
			assignee, parent_task_id, points, labels, component)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		t.ID, t.ProjectKey, number, t.Goal, t.Description, t.AcceptanceCriteria,
		t.Status, t.Origin, t.Discussion, t.Reporter, t.Assignee, t.ParentTaskID,
		t.Points, t.Labels, t.Component)
	if err != nil {
		if ref := refErr(err); ref != nil {
			return nil, ref
		}
		return nil, fmt.Errorf("insert task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Read back rather than assembling the answer here: created_at and the
	// derived columns are the database's to produce, and a struct built on this
	// side is a second opinion about them.
	return r.GetByID(ctx, t.ID)
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*Task, error) {
	return r.one(ctx, selectTask+` WHERE t.id = $1`, id)
}

// GetByHandle finds a task by its spoken name. Same task, second address.
func (r *Repository) GetByHandle(ctx context.Context, h Handle) (*Task, error) {
	return r.one(ctx, selectTask+` WHERE t.project_key = $1 AND t.handle = $2`,
		h.Project, h.Number)
}

func (r *Repository) one(ctx context.Context, query string, args ...any) (*Task, error) {
	t, err := scanTask(r.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select task: %w", err)
	}
	return t, nil
}

// SetStatus moves a task, and takes `from` rather than trusting the read that
// preceded it. The guard in the WHERE clause is the whole point: the service
// reads the task, decides the move is legal, and writes — and between those two
// steps an agent or a person can have moved it. No rows updated means the task
// is no longer where we thought, which is a conflict and not a success.
//
// One case this statement cannot see at all: the row deleted in that same
// window also arrives as zero rows, and zero rows is zero rows. The service
// is what reads again to tell the two apart — moved stays a conflict, deleted
// becomes a 404 (service.go).
func (r *Repository) SetStatus(ctx context.Context, id uuid.UUID, from, to string) (*Task, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE tasks SET status = $3, updated_at = now()
		WHERE id = $1 AND status = $2`, id, from, to)
	if err != nil {
		return nil, fmt.Errorf("update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrStaleStatus
	}
	return r.GetByID(ctx, id)
}

// SetAssignee hands a task to somebody, or back to nobody when assignee is nil.
//
// It does not touch status, and that separation is the point: taking a task,
// handing it on, and moving it along the board are different events that only
// sometimes coincide. Coming back from QA, in particular, leaves the assignee
// alone — who picks the work up next is a decision, not a consequence, and
// silently returning it to whoever wrote it would make that decision for them.
func (r *Repository) SetAssignee(ctx context.Context, id uuid.UUID, assignee *string) (*Task, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE tasks SET assignee = $2, updated_at = now()
		WHERE id = $1`, id, assignee)
	if err != nil {
		if ref := refErr(err); ref != nil {
			return nil, ref
		}
		return nil, fmt.Errorf("set assignee: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.GetByID(ctx, id)
}

// Patch changes only the fields that were sent. COALESCE rather than a built-up
// SET list: five optional columns is where a query builder starts to look
// reasonable and starts being the place a column gets forgotten. labels comes
// through as []string or nil — nil is the field that was not sent, and the
// column keeps what it had.
func (r *Repository) Patch(ctx context.Context, id uuid.UUID, p patch) (*Task, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE tasks SET
			goal                = COALESCE($2, goal),
			description         = COALESCE($3, description),
			acceptance_criteria = COALESCE($4, acceptance_criteria),
			points              = COALESCE($5, points),
			labels              = COALESCE($6, labels),
			-- $7 is cast: "$7 IS NULL" gives Postgres nothing to infer a
			-- type from, and every PATCH failed with 42P08 (found live).
			component           = CASE
			                       WHEN $7::text IS NULL THEN component
			                       ELSE NULLIF($7::text, '')
			                     END,
			discussion          = COALESCE($8, discussion),
			updated_at          = now()
		WHERE id = $1`, id, p.goal, p.description, p.acceptance, p.points, p.labels,
		p.component, p.discussion)
	if err != nil {
		return nil, fmt.Errorf("patch task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.GetByID(ctx, id)
}

// Return puts work in progress back in the queue: TODO, assigned to its
// reporter, in one statement. Guarded on IN_PROGRESS like SetStatus is on
// from: zero rows means the task is elsewhere (or gone), reported as
// ErrStaleStatus for the service to tell apart.
func (r *Repository) Return(ctx context.Context, id uuid.UUID) (*Task, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE tasks SET status = $2, assignee = reporter, updated_at = now()
		WHERE id = $1 AND status = $3`, id, StatusTodo, StatusInProgress)
	if err != nil {
		return nil, fmt.Errorf("return task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrStaleStatus
	}
	return r.GetByID(ctx, id)
}

// SetSummary writes the report of what was done. Separate from the status
// change that usually follows it, so that a report can be revised without
// moving the task, and so a transition never silently overwrites one.
func (r *Repository) SetSummary(ctx context.Context, id uuid.UUID, report string) (*Task, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE tasks SET summary_report = $2, updated_at = now()
		WHERE id = $1`, id, report)
	if err != nil {
		return nil, fmt.Errorf("set summary: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.GetByID(ctx, id)
}

// ErrStaleStatus means the task is no longer in the status the write named,
// and is still there. It wraps ErrIllegalTransition because that is what the
// caller should do about it — look at where the task is now and decide again —
// and because the HTTP answer is the same 409. A deletion in the same window
// is indistinguishable at this level (zero rows either way); the service reads
// again to tell the two apart and answers 404 for the deleted one (service.go).
var ErrStaleStatus = fmt.Errorf("%w: the task changed status while this request "+
	"was being handled", ErrIllegalTransition)

// ErrCycle is a dependency set that would make a task wait, through some chain,
// on itself. Such a graph has no legal order of work, and nothing downstream
// could do anything sensible with it.
var ErrCycle = fmt.Errorf("%w: this would make the task depend on itself through "+
	"a chain of other tasks, and nothing in that ring could ever start", ErrInvalid)

// depsGraphLock is the advisory lock key every dependency write takes.
const depsGraphLock int64 = 0x7461736b64657073 // "taskdeps"

// SetDependencies replaces a task's whole dependency set.
//
// Replace rather than add, because the decision to refuse rather than warn came
// with a condition: removing a dependency must be no more work than adding one,
// or the refusal gets routed around by editing status in the database.
//
// The ring check runs after the writes and inside the same transaction, so it
// sees exactly the graph being committed. Writers are serialised by an advisory
// lock (replaceDepsTx), so two transactions cannot each add one edge and close a
// ring neither saw.
func (r *Repository) SetDependencies(ctx context.Context, id uuid.UUID, deps []uuid.UUID) (*Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.replaceDepsTx(ctx, tx, id, deps); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return r.GetByID(ctx, id)
}

// replaceDepsTx writes the new dependency set onto an open transaction and
// checks it for a ring. Shared by SetDependencies and Park so that the ring
// check runs against exactly the graph being committed in both, and so the two
// cannot drift into different definitions of "the same graph".
func (r *Repository) replaceDepsTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, deps []uuid.UUID) error {
	// One lock for the whole graph: a ring can span any tasks, so a per-task
	// lock would not do. Released at commit or rollback.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, depsGraphLock); err != nil {
		return fmt.Errorf("lock dependency graph: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM task_dependencies WHERE task_id = $1`, id); err != nil {
		return fmt.Errorf("clear dependencies: %w", err)
	}
	for _, dep := range deps {
		_, err := tx.Exec(ctx, `
			INSERT INTO task_dependencies (task_id, depends_on_id)
			VALUES ($1, $2)`, id, dep)
		if err != nil {
			if ref := refErr(err); ref != nil {
				return ref
			}
			return fmt.Errorf("add dependency: %w", err)
		}
	}

	var ring bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE reached(id) AS (
			SELECT depends_on_id FROM task_dependencies WHERE task_id = $1
			UNION
			SELECT d.depends_on_id
			FROM task_dependencies d
			JOIN reached r ON d.task_id = r.id
		)
		SELECT EXISTS (SELECT 1 FROM reached WHERE id = $1)`, id).Scan(&ring)
	if err != nil {
		return fmt.Errorf("check for a ring: %w", err)
	}
	if ring {
		return ErrCycle
	}
	return nil
}

// Park puts work in progress under an open dependency and holds it there:
// status back to TODO, the assignee left in place, the new
// dependency set written.
//
// One transaction, because the halves are only safe together — and that is the
// thing the reviewer looks for: a status move without the edge leaves the
// work parked for nothing, an edge without the move leaves IN_PROGRESS work
// holding what it holds.
//
// The difference from Return is the assignee: Return reassigns to the
// reporter because the work failed and goes back to its owner, while parking
// deliberately does not, because the work only waits and the whole point is
// that the same executor resumes it. The guard is the status, like Return's:
// zero rows means the task moved or was deleted, reported as ErrStaleStatus
// for the service to tell apart.
func (r *Repository) Park(ctx context.Context, id uuid.UUID, deps []uuid.UUID) (*Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE tasks SET status = $2, updated_at = now()
		WHERE id = $1 AND status = $3`, id, StatusTodo, StatusInProgress)
	if err != nil {
		return nil, fmt.Errorf("park task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrStaleStatus
	}

	if err := r.replaceDepsTx(ctx, tx, id, deps); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return r.GetByID(ctx, id)
}

// ListFilter is what a caller may narrow a listing by. Ready is a dispatcher's
// question — "what can be picked up right now" — and it is a filter rather than
// its own route on purpose: gorilla/mux matches in registration order, and a
// literal /tasks/ready registered after /tasks/{id} is dead code.
//
// Statuses and Ready are mutually exclusive, and that is checked in the service,
// not here: Ready already hard-codes status='TODO' below, so a Statuses list
// that disagreed would AND down to an empty result with no word about why, and
// one that agreed would just be TODO said twice without the caller knowing it.
//
// Unassigned and Assignee are exclusive by construction: the handler sets one
// or the other from the same parameter.
//
// Search is either a handle or a piece of a goal, never both: "ABC-45" typed
// into a search box means that task, and treating it as text too would also
// return every goal that happens to mention it.
//
// Labels is a set the task must carry in full: repeated ?label= parameters
// AND, the way a filter narrows, and a task that has one of the two but not
// the other is not "tagged with either" the way a reader of the request would
// take it to be.
type ListFilter struct {
	Origin     string
	Project    string
	Assignee   string
	Unassigned bool
	Statuses   []string
	Ready      bool
	// Open keeps every status but DONE and CANCELLED. It stands in for a
	// caller that would otherwise fetch the whole board to drop the two on
	// its own side, rather than importing the vocabulary. It cannot be
	// combined with Statuses — the service refuses the pair, the same way it
	// refuses Ready with Statuses above.
	Open      bool
	Order     string
	Handle    *Handle
	GoalLike  string
	Labels    []string
	Component string
}

// escapeLike makes the user's text match literally. Unescaped, "100%" would
// match every goal containing "100", and "_" any single character.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// List returns tasks, ordered as Order asks.
func (r *Repository) List(ctx context.Context, f ListFilter) ([]Task, error) {
	query := selectTask
	args := []any{}
	where := []string{}

	if f.Origin != "" {
		args = append(args, f.Origin)
		where = append(where, fmt.Sprintf("t.origin = $%d", len(args)))
	}
	if f.Project != "" {
		args = append(args, f.Project)
		where = append(where, fmt.Sprintf("t.project_key = $%d", len(args)))
	}
	if f.Assignee != "" {
		args = append(args, f.Assignee)
		where = append(where, fmt.Sprintf("t.assignee = $%d", len(args)))
	}
	if f.Unassigned {
		where = append(where, "t.assignee IS NULL")
	}
	if f.Handle != nil {
		args = append(args, f.Handle.Number)
		where = append(where, fmt.Sprintf("t.handle = $%d", len(args)))
		// No project: a bare number, which matches it in every project.
		if f.Handle.Project != "" {
			args = append(args, f.Handle.Project)
			where = append(where, fmt.Sprintf("t.project_key = $%d", len(args)))
		}
	}
	if f.GoalLike != "" {
		args = append(args, "%"+escapeLike(f.GoalLike)+"%")
		where = append(where, fmt.Sprintf(`t.goal ILIKE $%d ESCAPE '\'`, len(args)))
	}
	if len(f.Statuses) > 0 {
		args = append(args, f.Statuses)
		where = append(where, fmt.Sprintf("t.status = ANY($%d)", len(args)))
	}
	if f.Open {
		where = append(where, "t.status NOT IN ('DONE','CANCELLED')")
	}
	if len(f.Labels) > 0 {
		// @> is "the task's set contains every label named": the repeated
		// parameters AND. = is wrong for a second parameter, and ?| ORs them.
		args = append(args, f.Labels)
		where = append(where, fmt.Sprintf("t.labels @> $%d", len(args)))
	}
	if f.Component != "" {
		// Equality on the text column, not array containment: a task holds one
		// component, and the partial index on tasks (migration 0006) skips
		// the multi-module rows that hold none.
		args = append(args, f.Component)
		where = append(where, fmt.Sprintf("t.component = $%d", len(args)))
	}
	if f.Ready {
		// Approved, and nothing open in its way: no open dependency and no
		// open subtask. Computed from the graph every time it is asked, which
		// is why no event has to wake anything when a blocker closes — the
		// answer simply changes.
		//
		// The subtask half exists for dispatchers: a parent is
		// done by its children, so a parent handed to an agent while they are
		// open would be worked twice. It stands on ready only, not on the
		// IN_PROGRESS gate in SetStatus — a person moving a parent along while
		// its children run is tracking, not starting.
		where = append(where, `t.status = '`+StatusTodo+`'`,
			`NOT EXISTS (SELECT 1 FROM task_dependencies d
			             JOIN tasks b ON b.id = d.depends_on_id
			             WHERE d.task_id = t.id
			               AND b.status NOT IN ('DONE','CANCELLED'))`,
			`NOT EXISTS (SELECT 1 FROM tasks c
			             WHERE c.parent_task_id = t.id
			               AND c.status NOT IN ('DONE','CANCELLED'))`)
	}
	for i, clause := range where {
		if i == 0 {
			query += " WHERE " + clause
			continue
		}
		query += " AND " + clause
	}

	switch f.Order {
	case OrderNumber:
		// Free because handle is already a numeric column (handle.go:20-23) —
		// sorting on it is nothing more than ORDER BY an int. Sorting the
		// rendered "ABC-12" string client-side would put ABC-10 before ABC-2.
		query += ` ORDER BY t.project_key ASC, t.handle ASC`
	case OrderUpdated:
		query += ` ORDER BY t.updated_at DESC`
	default:
		query += ` ORDER BY t.created_at DESC`
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	// Non-nil so an empty result serialises as [] and not null, which every
	// client would otherwise have to special-case.
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListLabels answers "what labels does this project have, and which are in
// active use" — the list a caller chooses from when tagging. A label
// is listed while any task of the project carries it, closed ones included:
// it is still the project's vocabulary, and a 0 count says so. What is counted
// is open tasks only, ordered busiest first; the label breaks ties so the
// answer is the same from every read.
func (r *Repository) ListLabels(ctx context.Context, project string) ([]LabelCount, error) {
	// Ordered by the open count itself, not count(*): the bare count includes
	// closed tasks, and put a label of three cancelled tasks ahead of one on
	// two open ones (found live in review).
	rows, err := r.pool.Query(ctx, `
		SELECT label,
		       count(*) FILTER (WHERE t.status NOT IN ('DONE','CANCELLED')) AS open_tasks
		FROM tasks t, unnest(t.labels) AS label
		WHERE t.project_key = $1
		GROUP BY label
		ORDER BY open_tasks DESC, label ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer rows.Close()

	// Non-nil so an empty project serialises as [] and not null, like List.
	out := []LabelCount{}
	for rows.Next() {
		var lc LabelCount
		if err := rows.Scan(&lc.Label, &lc.OpenTasks); err != nil {
			return nil, fmt.Errorf("scan label: %w", err)
		}
		out = append(out, lc)
	}
	return out, rows.Err()
}
