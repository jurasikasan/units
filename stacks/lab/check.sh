#!/bin/bash
# The lab from .env.example, on free ports (throwaway.sh), the whole round
# but thin in the browser: up; one browser (e2e/chat.mjs, scenario smoke)
# logs in, lands as the ghost, steps into its avatar, sees the assistant's
# greeting and says one thing that comes back through reins, threads, the
# driver, the mind and the fake model. The logic is checked at the API
# (api.sh, curl through the door): an avatar taken on one device is held
# there and an act from another is refused; a thread opened, said in,
# answered by the assistant, renamed, the assistant taken and handed back
# to the fake profile by a steward, the thread deleted for whoever reads,
# the avatar given back; the program steers its own subjects with its
# password; what the browser held lapses past the grace; a person gets
# mail; gate, mind and face recreated on new addresses are still reached
# through nginx; a second account added, a backup taken, everything
# deleted, the backup restored, the second account logs in, and the first
# steers the same subjects; then the backups leave the box (offbox/, the
# pull run here without SSH): a dump and the store's bytes pulled and
# checked, nothing pulled ever deleted, a copy a day old called stale on
# both sides, and a third account, dave, back from the pulled copy alone;
# last, a new avatar made at the API, listed, its nick refused again.
#
# The browser's full scenarios run nightly, on a lab of their own
# (nightly/e2e.sh, README "Nightly"): the browser under a loaded machine
# flakes where the code does not, and a check that flakes is not read.
set -euo pipefail
cd "$(dirname "$0")"

. ./throwaway.sh lab-check

program=$(sed -n 's/^LAB_PROGRAM=//p' .env.example)
program_password=$(sed -n 's/^LAB_PROGRAM_PASSWORD=//p' .env.example)
# The program, with its password as HTTP Basic: its own subjects on the API,
# no page.
as_program() {
  docker compose run --rm --no-deps --entrypoint sh demo -c '
    hp=${LAB_URL#https://} who=$0:$1
    c() { curl -sk --connect-to "$hp:nginx:443" -u "$who" "$@"; }
    nicks=$(c "$LAB_URL/api/reins/me/subjects" | grep -o "\"nick\":\"[^\"]*\"" | sort | tr "\n" " ")
    echo "steers: $nicks"
    [ "$nicks" = "\"nick\":\"$0\" \"nick\":\"$0-assistant\" " ] || exit 1
    page=$(c -o /dev/null -w "%{http_code}" "$LAB_URL/"); echo "page: $page"; [ "$page" = 302 ] || exit 1
    bad=$(curl -sk --connect-to "$hp:nginx:443" -u "$0:wrong" -o /dev/null -w "%{http_code}" "$LAB_URL/api/reins/me/subjects"); echo "wrong password: $bad"; [ "$bad" = 401 ]
  ' "$program" "$program_password"
}
add_carol() { echo carol-lab | docker compose run --rm --no-deps -T gate gate-user add carol carol@example.com Carol; }
backup() { ./backup.sh "$work/backups" > "$work/archive"; }
# The browsers are closed: past the grace (45 s here), no principal holds
# anything, and the assistant is still the fake profile's.
lapsed() {
  q() { docker compose exec -T reins-db psql -U reins -tAc "$1"; }
  for _ in $(seq 40); do
    [ "$(q "SELECT count(*) FROM holds WHERE holder LIKE 'principal:%'")" = 0 ] && break
    sleep 3
  done
  q "SELECT holder FROM holds" | sort | tr '\n' ' '
  [ "$(q "SELECT count(*) FROM holds WHERE holder LIKE 'principal:%'")" = 0 ] && [ "$(q "SELECT holder FROM holds")" = "brain:fake" ]
}
wipe() { docker compose --profile '*' down -v; }
restore() { ./restore.sh "$(cat "$work/archive")"; }
# Which subjects the first account steers, as reins keeps it.
steered() { docker compose exec -T reins-db psql -U reins -tAc "SELECT subject_id FROM stewardship WHERE principal_id = '$first_user' ORDER BY subject_id"; }
keep_steered() { steered > "$work/steered"; [ "$(wc -l < "$work/steered")" = 2 ] || { cat "$work/steered"; exit 1; }; }
same_steered() { [ "$(steered)" = "$(cat "$work/steered")" ] || { echo "before:"; cat "$work/steered"; echo "after:"; steered; exit 1; }; }

# Off the box: offbox/pull.sh with no SSH, the box's side run in this
# directory with its backups in $work/box, the store's bytes a directory of
# files named by sha256; the copy kept in $work/offbox.
pull() { env LAB_PULL_ENV=/dev/null LAB_PULL_SSH= LAB_PULL_LAB="${lab_dir:-$PWD}" LAB_PULL_DEST="$work/offbox" LAB_PULL_NOTIFY= LAB_PULL_DUMP=yes \
  LAB_BACKUPS="$work/box" LAB_BYTES_DIR="$work/box-bytes" offbox/pull.sh "$@"; }
box_stale() { LAB_BACKUPS="$work/box" offbox/box.sh stale; }
held() { echo "$(find "$work/offbox/dumps" -name 'lab-*' | wc -l) $(find "$work/offbox/bytes" -type f | wc -l)"; }
first_pull() {
  mkdir -p "$work/box-bytes"
  for b in one two; do echo "$b" > "$work/box-bytes/tmp"; mv "$work/box-bytes/tmp" "$work/box-bytes/$(sha256sum < "$work/box-bytes/tmp" | cut -d' ' -f1)"; done
  pull --now && box_stale && [ "$(held)" = "1 2" ] || { held; exit 1; }
}
box_says_stale() {
  touch -d '25 hours ago' "$work/box/.pulled"
  local out
  if out=$(box_stale); then echo "$out"; exit 1; fi
  echo "$out"; echo "$out" | grep -q "nobody has pulled"
}
add_dave() { echo dave-lab | docker compose run --rm --no-deps -T gate gate-user add dave dave@example.com Dave; }
# The box loses its first archive and a byte; the copy here keeps them.
keeps_all() {
  rm "$work/box"/lab-*.tar.gz
  find "$work/box-bytes" -type f | head -1 | xargs rm
  pull --now && box_stale && [ "$(held)" = "2 2" ] || { held; exit 1; }
}
here_says_stale() {
  touch -d '25 hours ago' "$work/offbox/last-good"
  local out
  if out=$(lab_dir=/nowhere pull 2>&1); then echo "$out"; exit 1; fi
  echo "$out"; echo "$out" | grep -q "lab backup is stale"
}
restore_pulled() { ./restore.sh "$(find "$work/offbox/dumps" -name 'lab-*.tar.gz' | sort | tail -1)"; }

run "build"          docker compose --profile e2e build -q
run "up"             docker compose up -d --wait
e2e smoke "a login lands as the ghost, steps into its avatar, the assistant has greeted, one message there and back" E2E_FIRST_LOGIN=1
run "the driver said the answer as $first_user-assistant"  bash -c 'docker compose logs driver | grep -q "msg=replied subject=$0-assistant"' "$first_user"
run "the stream carried the reply as it was written"  bash -c '[ "$(docker compose exec -T bus redis-cli XLEN driver_events)" -gt 2 ]'
run "at the API: holds, a thread said in and answered, renamed, the kill switch, deleted, given back"  docker compose run --rm --entrypoint sh demo /api.sh "$first_user" "$first_password"
run "the pages gone, what they held lapses: nobody holds $first_user"  lapsed
run "the program $program steers its own subjects through /api/ with its password, no page"  as_program
run "first account: its subjects after login, chat, mail"  docker compose run --rm demo "$first_user" "$first_password" mail
run "reins keeps two subjects for it"  keep_steered
run "gate, mind, face on new addresses"  ../../scripts/move_address.sh gate mind face
run "nginx finds them there"  docker compose run --rm demo "$first_user" "$first_password"
run "add carol"      add_carol
run "backup"         backup
run "wipe"           wipe
run "restore"        restore
run "carol is back"  docker compose run --rm demo carol carol-lab
run "the first account steers the same subjects"  same_steered
run "and finds them through the door"  docker compose run --rm demo "$first_user" "$first_password"
run "off the box: a fresh dump and the store's bytes pulled, each checked by its sha256"  first_pull
run "the box says when nobody has pulled for a day"  box_says_stale
run "add dave"       add_dave
run "pulled again: what the box no longer has is still here"  keeps_all
run "the copy here says when it is a day old, the pull failing"  here_says_stale
run "wipe"           wipe
run "restore from the pulled copy"  restore_pulled
run "dave is back"   docker compose run --rm demo dave dave-lab
run "the first account steers the same subjects"  same_steered
# Last: a new avatar made adds to the two subjects every check above counts.
run "a new avatar made at the API, listed, its nick refused again"  docker compose run --rm --entrypoint sh demo /api.sh "$first_user" "$first_password" avatar

finish
