package task

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVocabularyDroppedStates pins words this board deliberately does not have.
// Both are load-bearing decisions rather than tidying, and both are the kind
// that drift back in when someone ports a handler across: there is no REVIEW
// but QA and UAT, because "check it against the criteria" and "the person who
// asked accepts it" are different jobs, and no BLOCKED, because being stuck is
// a change of assignee and a conversation, not a state of the work.
func TestVocabularyDroppedStates(t *testing.T) {
	for _, gone := range []string{"REVIEW", "BLOCKED", "PLANNING", "TRIAGE"} {
		require.False(t, ValidStatus(gone), "%s is back in the vocabulary", gone)
	}
	for _, want := range []string{
		StatusDraft, StatusTodo, StatusInProgress,
		StatusQA, StatusUAT, StatusDone, StatusCancelled,
	} {
		require.True(t, ValidStatus(want), "%s is missing", want)
	}
}

// TestIllegalTransitions names jumps that must be refused. The first is the one
// that matters: DRAFT → DONE is how a task gets marked finished without anyone
// looking at it.
func TestIllegalTransitions(t *testing.T) {
	cases := [][2]string{
		{StatusDraft, StatusDone},
		{StatusDraft, StatusInProgress},
		{StatusTodo, StatusQA},
		{StatusInProgress, StatusUAT},
		{StatusInProgress, StatusDone},
		{StatusDone, StatusCancelled},
		{StatusDone, StatusInProgress},
	}
	for _, c := range cases {
		require.False(t, CanTransition(c[0], c[1]), "%s → %s should be refused", c[0], c[1])
	}
}

// TestHappyPath walks the flow the way work actually moves, including the two
// backward edges. Those are not an oversight: QA that fails and UAT that
// rejects both send the work back, and a flow that only moves forward makes
// people lie about where it is.
func TestHappyPath(t *testing.T) {
	paths := [][]string{
		{
			StatusDraft, StatusTodo, StatusInProgress, StatusQA,
			StatusInProgress, StatusQA, StatusUAT,
			StatusInProgress, StatusQA, StatusUAT, StatusDone,
		},
		// Machine-checked work closes from QA on its evidence; UAT is for what
		// a person has to see.
		{StatusDraft, StatusTodo, StatusInProgress, StatusQA, StatusDone},
	}
	for _, path := range paths {
		for i := 0; i+1 < len(path); i++ {
			require.True(t, CanTransition(path[i], path[i+1]),
				"%s → %s should be allowed", path[i], path[i+1])
		}
	}
}

// TestEveryStatusReachable catches the quiet failure mode of a hand-written
// transition table: a status that exists in the vocabulary, satisfies the CHECK
// constraint, and can never be entered. Nothing goes red when that happens —
// the state simply never appears on the board.
func TestEveryStatusReachable(t *testing.T) {
	seen := map[string]bool{StatusDraft: true}
	for queue := []string{StatusDraft}; len(queue) > 0; {
		from := queue[0]
		queue = queue[1:]
		for _, to := range transitions[from] {
			require.True(t, ValidStatus(to),
				"%s → %s points at a status outside the vocabulary", from, to)
			if !seen[to] {
				seen[to] = true
				queue = append(queue, to)
			}
		}
	}
	for status := range transitions {
		require.True(t, seen[status], "%s cannot be reached from DRAFT", status)
	}
}

// TestDoneIsTerminal is its own test because it is the one edge that will get
// argued about — someone will want to reopen a task. Reopening is rewriting
// history; the honest move is a new task saying what actually happened.
// CANCELLED is deliberately not terminal: cancelling by mistake happens, and
// the alternative throws away the chat, which holds all the working context.
func TestDoneIsTerminal(t *testing.T) {
	require.Empty(t, transitions[StatusDone])
	require.NotEmpty(t, transitions[StatusCancelled])
}

// TestOptionalRefDistinguishesAbsentFromEmpty pins why ParentTaskID is a *string
// on the wire. Absent and null both mean "not staffed"; an empty string means
// the client thinks it is saying that but has no way to, and silently accepting
// it hides the bug in the client.
func TestOptionalRefDistinguishesAbsentFromEmpty(t *testing.T) {
	blank, bad, good := "", "nope", "2a1f8c6e-0000-4000-8000-000000000000"

	got, err := optionalUUID("parent_task_id", nil)
	require.NoError(t, err)
	require.Nil(t, got)

	_, err = optionalUUID("parent_task_id", &blank)
	require.ErrorIs(t, err, ErrInvalid)

	_, err = optionalUUID("parent_task_id", &bad)
	require.ErrorIs(t, err, ErrInvalid)

	got, err = optionalUUID("parent_task_id", &good)
	require.NoError(t, err)
	require.Equal(t, good, got.String())
}

// TestAcceptanceCriteriaRequired is the field that cancelling auto-DONE made
// mandatory. Without it QA has nothing to check against, and "it looks done"
// comes back as the real closing rule.
func TestAcceptanceCriteriaRequired(t *testing.T) {
	base := CreateRequest{
		ProjectKey:         "ABC",
		Goal:               "buy milk",
		AcceptanceCriteria: "a litre is in the fridge",
		Reporter:           "reporter",
	}
	_, err := base.Validate()
	require.NoError(t, err)

	for _, blank := range []string{"", "   ", "\t\n"} {
		req := base
		req.AcceptanceCriteria = blank
		_, err := req.Validate()
		require.ErrorIs(t, err, ErrInvalid, "acceptance_criteria %q was accepted", blank)
	}

	req := base
	req.Reporter = ""
	_, err = req.Validate()
	require.ErrorIs(t, err, ErrInvalid, "reporter may not be omitted")
}

// TestHandleRoundTrip pins the one property that makes a handle worth having:
// what a person says is what the service accepts back. A name that renders one
// way and parses another is a name nobody can look anything up with.
func TestHandleRoundTrip(t *testing.T) {
	for _, s := range []string{"ABC-1", "ABC-412", "XYZ2-99", "A1B2C3D4E5-7"} {
		h, err := ParseHandle(s)
		require.NoError(t, err, "%q should parse", s)
		require.Equal(t, s, h.String())
	}
}

func TestHandleRejects(t *testing.T) {
	// Lower case is refused here but upper-cased on the way in at creation:
	// looking something up is an exact-match question, registering a project is
	// not. The asymmetry is deliberate, which is why both are tested.
	for _, s := range []string{
		"", "412", "ABC", "ABC-", "-412", "abc-412", "B-1",
		"TOOLONGAKEY-1", "ABC-0", "ABC-x", "ABC-4-12",
	} {
		_, err := ParseHandle(s)
		require.ErrorIs(t, err, ErrInvalid, "%q should not parse as a handle", s)
	}
}

// TestParseStatusListAcceptsKnownWords pins the two things the board header
// needs: several statuses in one filter, and whitespace around the commas
// tolerated the way other fields in this file are trimmed.
func TestParseStatusListAcceptsKnownWords(t *testing.T) {
	got, err := parseStatusList("QA, UAT")
	require.NoError(t, err)
	require.Equal(t, []string{StatusQA, StatusUAT}, got)
}

// TestParseStatusListRefusesUnknownWord: one bad
// entry refuses the whole filter rather than silently dropping it, because a
// board missing a column with no explanation is worse than a 400.
func TestParseStatusListRefusesUnknownWord(t *testing.T) {
	_, err := parseStatusList("QA,REVIEW")
	require.ErrorIs(t, err, ErrInvalid)

	_, err = parseStatusList("")
	require.ErrorIs(t, err, ErrInvalid, "status present but empty should refuse, not list everything")
}

// TestValidOrder pins the sort vocabulary, and that an unknown word
// is refused rather than quietly falling back to the default.
func TestValidOrder(t *testing.T) {
	for _, want := range []string{OrderCreated, OrderNumber, OrderUpdated} {
		require.True(t, ValidOrder(want), "%s should be a valid order", want)
	}
	for _, bad := range []string{"", "handle", "CREATED", "created_at"} {
		require.False(t, ValidOrder(bad), "%q should not be a valid order", bad)
	}
}

// TestListRefusesReadyWithStatus: ready=true
// already hard-codes status=TODO in the repository, so ANDing a caller's own
// status list on top of it would either silently empty out (status list
// without TODO) or silently repeat TODO (status list with it) — both answered
// with a 400 instead, before the query is ever built. repo is left nil because
// the conflict is caught before it would be touched.
func TestListRefusesReadyWithStatus(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.List(context.Background(), ListFilter{
		Ready:    true,
		Statuses: []string{StatusQA},
	})
	require.ErrorIs(t, err, ErrInvalid)
}

// TestProjectKeyNormalised covers the other half: a key typed in lower case is a
// shouting convention, not a different project, so it is accepted and raised
// rather than refused with a lecture.
func TestProjectKeyNormalised(t *testing.T) {
	req := CreateProjectRequest{Key: " abc ", Name: "Agent framework"}
	require.NoError(t, req.Validate())
	require.Equal(t, "ABC", req.Key)

	for _, bad := range []string{"", "B", "1BOT", "ABC-X", "TOOLONGAKEY"} {
		req := CreateProjectRequest{Key: bad, Name: "x"}
		require.ErrorIs(t, req.Validate(), ErrInvalid, "%q should not be a key", bad)
	}
}
