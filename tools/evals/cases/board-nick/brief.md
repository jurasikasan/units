task_board: subjects by nick everywhere — a return's actor, note_by.

The board names subjects by nick in every field but three: `return` and `give_up` take `actor_id`, a uuid; the event's `data.note_by` is a uuid; and a park's note is signed by a subject id hardcoded in the unit (`schedulerID`), which means nothing inside it.

Change:
- `return` (HTTP `POST /tasks/{id}/return`) and the MCP `give_up` take `actor`, a nick, like `create_task` already does. A body still sending `actor_id` is a 400 whose message names `actor` (a caller sending the old field learns which one to use, not "actor is required").
- `data.note_by` in `task_board.task.*` events is that nick. A note the board makes itself (parking) has no `note_by` at all: the consumer that writes it into a discussion attributes it. The unit holds no subject id any more (`schedulerID` goes).
- Swagger docs regenerated (`doc.sh`), README and CONTRACT.md where they name `actor_id` or a uuid `note_by`.

Go names the change must have (tests elsewhere will use them): `ReturnRequest` has `Actor string` (json `actor`) and `Reason string`; the service's `Return(ctx, id, ReturnRequest)` keeps its shape; the event's `NoteBy` is a string (a nick, empty for the board's own lines) and is left out of `data` when empty.

Update the tests that send `actor_id`, and add ones for: a return by nick, `actor_id` refused by name, a park's event with no `note_by`.
