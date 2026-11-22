#!/bin/bash
# offbox/box.sh <command> — the box's side of the off-box copy: what a pull
# (offbox/pull.sh, on another machine) asks of the lab, over SSH or locally.
#
#   dump           a fresh archive (../backup.sh) into the backups directory
#   list           every archive there, "<sha256>  <name>"
#   cat <name>     one archive's bytes
#   bytes          the store's bytes, one sha256 per line
#   byte <sha256>  one of them
#   pulled         a good pull ended: the marker is touched
#   stale [hours]  exit 1, with a line, when nobody has pulled for that long
#                  (24 by default) or ever
#
# LAB_BACKUPS is the backups directory (../backups by default), where
# backup.sh writes and the marker `.pulled` sits. LAB_BYTES_DIR is a
# directory of content-addressed bytes, each file named by its sha256; empty,
# there are none: lab runs no artifact_store yet, and a picture tools/images
# draws is journaled in threads' database, so it rides the dump. When the
# store joins, `bytes` and `byte` read its bucket instead.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
backups=$(realpath -m "${LAB_BACKUPS:-$here/../backups}")
bytes_dir=${LAB_BYTES_DIR:-}
marker=$backups/.pulled

name_ok() { [[ $1 =~ ^lab-[0-9]{8}T[0-9]{6}Z\.tar\.gz$ ]] || { echo "not an archive's name: $1" >&2; exit 2; }; }
sha_ok() { [[ $1 =~ ^[0-9a-f]{64}$ ]] || { echo "not a sha256: $1" >&2; exit 2; }; }

case "${1:-}" in
  dump)
    basename "$("$here/../backup.sh" "$backups")" ;;
  list)
    mkdir -p "$backups"
    cd "$backups"
    for f in lab-*.tar.gz; do [ -f "$f" ] && sha256sum "$f"; done; true ;;
  cat)
    name_ok "${2:-}"; cat "$backups/$2" ;;
  bytes)
    [ -n "$bytes_dir" ] && [ -d "$bytes_dir" ] || exit 0
    find "$bytes_dir" -maxdepth 1 -type f -printf '%f\n' | grep -E '^[0-9a-f]{64}$' || true ;;
  byte)
    sha_ok "${2:-}"; cat "$bytes_dir/$2" ;;
  pulled)
    mkdir -p "$backups"; touch "$marker" ;;
  stale)
    hours=${2:-24}
    if [ ! -f "$marker" ]; then
      echo "lab backup: nobody has pulled a copy off this box yet"; exit 1
    fi
    if [ -n "$(find "$marker" -mmin +$((hours * 60)))" ]; then
      echo "lab backup: nobody has pulled a copy off this box since $(date -u -r "$marker" +%Y-%m-%dT%H:%MZ), over ${hours}h"; exit 1
    fi
    echo "lab backup: last pulled $(date -u -r "$marker" +%Y-%m-%dT%H:%MZ)" ;;
  *)
    sed -n '2,13p' "$0" >&2; exit 2 ;;
esac
