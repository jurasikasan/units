#!/bin/bash
# offbox/pull.sh — the lab's backups off its box, onto this machine: a fresh
# dump is taken there, then every archive and every one of the store's bytes
# not yet here is copied, each checked against its sha256. Nothing here is
# ever deleted, so the copy is a superset of every dump the box ever held.
# A copy older than a day is said aloud: a line, exit 1, and a desktop
# notification.
#
# Settings come from the environment or the env file (LAB_PULL_ENV, by
# default _ignored/lab-pull.env in this checkout; offbox/pull.env.example
# shows them). LAB_PULL_SSH empty runs the box's side here, in LAB_PULL_LAB:
# that is how check.sh pulls from a local lab.
#
# Run by a systemd user timer (offbox/lab-pull.timer) every hour; a pull is
# taken when the last good one is older than LAB_PULL_EVERY hours, so a
# failing one is retried within the hour and a working one dumps once a day.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
env_file=${LAB_PULL_ENV:-$here/../../../_ignored/lab-pull.env}
if [ -f "$env_file" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$env_file"
  set +a
fi

ssh_to=${LAB_PULL_SSH:-}                  # an SSH destination, an alias in ~/.ssh/config
lab=${LAB_PULL_LAB:?LAB_PULL_LAB: the lab directory (stacks/lab) on the box}
dest=${LAB_PULL_DEST:?LAB_PULL_DEST: where the copy is kept here}
every=${LAB_PULL_EVERY:-20}               # hours between pulls
stale=${LAB_PULL_STALE:-24}               # hours after which the copy is stale
dump=${LAB_PULL_DUMP:-yes}                # take a fresh dump before pulling
notify=${LAB_PULL_NOTIFY-notify-send}     # empty: no desktop notification

mkdir -p "$dest/dumps" "$dest/bytes"
good=$dest/last-good

# The box's side, over SSH or here.
box() {
  if [ -n "$ssh_to" ]; then
    ssh -o BatchMode=yes -o ConnectTimeout=20 "$ssh_to" "cd $(printf %q "$lab") && ./offbox/box.sh $(printf '%q ' "$@")"
  else
    (cd "$lab" && ./offbox/box.sh "$@")
  fi
}

say_stale() {
  local when="never"
  [ -f "$good" ] && when=$(date -r "$good" '+%Y-%m-%d %H:%M')
  local line="lab backup is stale: the last good pull off the box was $when, over ${stale}h ago"
  echo "$line" >&2
  if [ -n "$notify" ] && command -v "$notify" >/dev/null 2>&1; then
    "$notify" -u critical "lab backup" "$line" >/dev/null 2>&1 || true
  fi
}
is_stale() { [ ! -f "$good" ] || [ -n "$(find "$good" -mmin +$((stale * 60)))" ]; }

pull() {
  local name sha f n=0 b=0
  if [ "$dump" = yes ]; then
    name=$(box dump) || { echo "the dump on the box failed" >&2; return 1; }
    echo "dumped on the box: $name"
  fi
  local list
  list=$(box list) || { echo "the box did not list its archives" >&2; return 1; }
  [ -n "$list" ] || { echo "the box holds no archive" >&2; return 1; }
  while read -r sha name; do
    [ -n "$name" ] || continue
    f=$dest/dumps/$name
    if [ -f "$f" ]; then
      [ "$(sha256sum < "$f" | cut -d' ' -f1)" = "$sha" ] && continue
      f=$f.$sha   # the box's differs from the one here: both are kept
      [ -f "$f" ] && continue
    fi
    box cat "$name" > "$f.partial" < /dev/null || { rm -f "$f.partial"; echo "$name did not arrive" >&2; return 1; }
    [ "$(sha256sum < "$f.partial" | cut -d' ' -f1)" = "$sha" ] || { rm -f "$f.partial"; echo "$name did not arrive intact" >&2; return 1; }
    mv "$f.partial" "$f"; n=$((n + 1))
  done <<< "$list"

  list=$(box bytes) || { echo "the box did not list the store's bytes" >&2; return 1; }
  while read -r sha; do
    [ -n "$sha" ] && [ ! -f "$dest/bytes/$sha" ] || continue
    box byte "$sha" > "$dest/bytes/$sha.partial" < /dev/null || { rm -f "$dest/bytes/$sha.partial"; echo "$sha did not arrive" >&2; return 1; }
    [ "$(sha256sum < "$dest/bytes/$sha.partial" | cut -d' ' -f1)" = "$sha" ] || { rm -f "$dest/bytes/$sha.partial"; echo "$sha did not arrive intact" >&2; return 1; }
    mv "$dest/bytes/$sha.partial" "$dest/bytes/$sha"; b=$((b + 1))
  done <<< "$list"

  box pulled || { echo "the box's marker was not touched" >&2; return 1; }
  touch "$good"
  echo "pulled $n archive(s) and $b of the store's bytes; here: $(find "$dest/dumps" -name 'lab-*.tar.gz*' ! -name '*.partial' | wc -l) archive(s), $(find "$dest/bytes" -type f ! -name '*.partial' | wc -l) bytes, in $dest"
}

status=0
if [ "${1:-}" = --now ] || [ ! -f "$good" ] || [ -n "$(find "$good" -mmin +$((every * 60)))" ]; then
  pull || status=1
fi
if is_stale; then
  say_stale; status=1
fi
exit $status
