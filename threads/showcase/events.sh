#!/bin/sh
# Every append the demo made, as it went out on threads_events: the first one
# whole, then one line per entry. An event names the entry, never its content;
# a reader reads the entry back with ?after={seq}.
set -eu
R="redis-cli -h redis --raw"

echo
echo '== the first append, as it went out on threads_events (CloudEvents, binary mode)'
$R XRANGE threads_events - + COUNT 1

echo
echo '== every append: ce_subject (the thread), data — appends that were refused are not here'
$R XRANGE threads_events - + |
  awk 'prev == "ce_subject" { s = $0 } prev == "data" { d = $0 } { prev = $0 }
       /^[0-9]+-[0-9]+$/ && s != "" { print s "  " d; s = ""; d = "" }
       END { if (s != "") print s "  " d }'
echo "$($R XLEN threads_events) events"
