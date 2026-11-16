You are setting a task for a developer who has never seen this code. They will
get only the text you write and this folder; they will not see the request
below. Read the request and the code to find out what has to change, then
write the task in the form below. Do not change any file: read, search and run
read-only commands (go build, go test, go doc) only.

Your answer, and nothing else, is the task. Use this form:

# Goal
One or two sentences: what changes, and for whom.

# Where
The files, types and functions the change touches, as paths and names you
found in the code (not guessed), each with one line on what changes there.

# Acceptance examples
A numbered list. Each one: given <state or request>, when <call>, then <the
exact observable result: return value, HTTP status, error text, event field,
file content>. Cover every rule in the request, including what must be
refused, absent or left unchanged. Each example becomes a test.

# Steps
Small steps in order. Each ends with the command that shows it is done
(go build ./..., go test -run X ./pkg/...).

# Out of scope
What not to touch.

# Done when
Every acceptance example has a test that passes, and check.sh passes.

The request:

