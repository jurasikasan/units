#!/bin/bash
# tell.sh <error|warn> <stage> <reason> [<report>] — one ops event on the
# watch's bus (compose.yaml): the notifier says "<NIGHTLY_NAME> 🔴
# lab-nightly / <stage>", the report's place on a line of its own (the
# event's entity), and the reason to a person. The reason is made one line
# of at most 200 characters, as ops events are (shared/ops), or the
# notifier drops it.
set -euo pipefail
cd "$(dirname "$0")"
[ $# -eq 3 ] || [ $# -eq 4 ] || { echo "usage: $0 <error|warn> <stage> <reason> [<report>]" >&2; exit 2; }
reason=$(python3 -c '
import sys
r = " ".join(sys.argv[1].split())
print(r if len(r) <= 200 else r[:199] + "…")' "$3")
report=()
[ -n "${4:-}" ] && report=(entity report entity_id "$4")
docker compose exec -T bus redis-cli XADD ops_events '*' \
  service lab-nightly stage "$2" severity "$1" "${report[@]}" reason "$reason" >/dev/null
