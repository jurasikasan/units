#!/bin/bash
# run.sh <job> — one run of a nightly job, as its timer starts it
# (lab-nightly@<job>.service): e2e, the browser's full scenarios on a lab
# of their own (e2e.sh).
#
# The job runs on NIGHTLY_REF (origin/main, fetched first), checked out in
# a throwaway worktree, never this checkout's working tree; the worktree
# goes when the job ends, whatever it changed. Its output is kept in
# NIGHTLY_REPORTS as <job>-<stamp>.log, beside its report when it writes one
# (<job>-<stamp>.md). When the job has something to say (a line in
# NIGHTLY_SUMMARY), one message goes to a person through the watch
# (tell.sh): an error when the job failed, a warning when it only has
# news; then where the report lies. Nothing to say, nothing sent.
#
# Exit 1 when the job failed or could not be told: the unit shows failed
# in `systemctl --user`, beside the message.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
job=${1:-}
case "$job" in e2e) ;; *) echo "usage: $0 e2e" >&2; exit 2 ;; esac

envf=$here/.env
[ -f "$envf" ] || envf=$here/.env.example
setting() { local v; v=$(sed -n "s/^$1=//p" "$envf" | tail -1); echo "${!1:-${v:-$2}}"; }
ref=$(setting NIGHTLY_REF origin/main)
fetch=$(setting NIGHTLY_FETCH yes)
reports=$(setting NIGHTLY_REPORTS "$repo/_ignored/nightly")
keep=$(setting NIGHTLY_KEEP_DAYS 30)

mkdir -p "$reports/state"
exec 9>"$reports/.$job.lock"
flock -n 9 || { echo "$job: a run is still going" >&2; exit 1; }
find "$reports" -maxdepth 1 -name "$job-*" -mtime +"$keep" -delete

stamp=$(date +%Y-%m-%d-%H%M)
log=$reports/$job-$stamp.log
report=$reports/$job-$stamp.md
summary=$(mktemp)
tmp=$(mktemp -d)
tree=$tmp/units
cleanup() {
  # Its own worktree only: a prune would touch other checkouts' entries.
  git -C "$repo" worktree remove --force "$tree" >/dev/null 2>&1 || rm -rf "$tree"
  rm -rf "$tmp" "$summary"
}
trap cleanup EXIT

{
  echo "# $job on $ref, $(date -Is)"
  if [ "$fetch" = yes ]; then git -C "$repo" fetch -q origin || echo "# the fetch failed: on the ref as this clone has it"; fi
} > "$log" 2>&1

if git -C "$repo" worktree add -q --detach "$tree" "$ref" >> "$log" 2>&1; then
  echo "# at $(git -C "$tree" rev-parse --short HEAD)" >> "$log"
  NIGHTLY_THROWAWAY=yes NIGHTLY_REPORT=$report NIGHTLY_SUMMARY=$summary NIGHTLY_STATE=$reports/state \
    "$tree/stacks/lab/nightly/$job.sh" >> "$log" 2>&1
  code=$?
else
  echo "no worktree of $ref" > "$summary"
  code=1
fi
echo "# exit $code" >> "$log"

# Exit 1 is red as the job sees it, told by its summary; any other failure
# is the job breaking, and is told.
said=$(head -1 "$summary")
[ "$code" -gt 1 ] && [ -z "$said" ] && said="ended $code with nothing said"
[ -z "$said" ] && exit "$code"

where=$log
[ -f "$report" ] && where=$report
severity=warn
[ "$code" -ne 0 ] && severity=error
if ! "$here/tell.sh" "$severity" "$job" "$said" "$where" >> "$log" 2>&1; then
  echo "$job: could not tell anyone (is the watch up? docker compose ps in $here): $said" >&2
  exit 1
fi
[ "$code" -eq 0 ]
