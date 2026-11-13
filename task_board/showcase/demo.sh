#!/bin/sh
set -u
B=http://task_board:8099/api/v1
step() { printf '\n== %s\n' "$1"; }
call() { # method path [body]: the status code, then the body
  if [ $# -ge 3 ]; then
    curl -s -w ' %{http_code}\n' -X "$1" -H 'Content-Type: application/json' -d "$3" "$B$2"
  else
    curl -s -w ' %{http_code}\n' -X "$1" "$B$2"
  fi
}
id() { sed -n 's/.*"id":"\([^"]*\)".*/\1/p'; }
show() { grep -oE '"(handle|status|assignee|blocked_by|closed_at)":(\[[^]]*\]|"[^"]*"|null)| [0-9]{3}$' | tr '\n' ' '; echo; }

step "a project, and two tasks numbered inside it"
call POST /projects '{"key":"ABC","name":"a project"}'
A=$(call POST /tasks '{"project_key":"ABC","goal":"lay the floor","acceptance_criteria":"you can walk on it","reporter":"ann"}' | id)
B2=$(call POST /tasks '{"project_key":"ABC","goal":"paint the walls","acceptance_criteria":"no bare plaster","reporter":"ann"}' | id)
call GET "/tasks?project_key=ABC&order=number" | grep -oE '"handle":"[^"]*"|"status":"[^"]*"' | paste - - 

step "the second waits on the first: blocked_by is derived, never stored"
call PUT /tasks/$B2/dependencies "{\"depends_on\":[\"$A\"]}" | show

step "a move the flow does not allow is refused, naming what is allowed"
call PUT /tasks/$A/status '{"status":"DONE"}'

step "the first is staffed and walks the flow to DONE"
call PUT /tasks/$A/assignee '{"assignee":"bob"}' | show
for s in TODO IN_PROGRESS QA DONE; do call PUT /tasks/$A/status "{\"status\":\"$s\"}" | show; done

step "a claim that expects the task elsewhere is a 409: two claimers cannot both win"
call PUT /tasks/$B2/status '{"status":"TODO","from":"QA"}'

step "the second, read by its spoken name, is no longer blocked"
call GET /tasks/ABC-2 | show
