#!/bin/sh
# Every move the demo made, as it went out on board_events: one line per entry,
# its ce_type and its data.
echo
echo '== every move went out on board_events, in the CloudEvents envelope'
redis-cli -h redis --raw XRANGE board_events - + |
  awk 'prev == "ce_type" { t = $0 } prev == "data" { d = $0 } { prev = $0 }
       /^[0-9]+-[0-9]+$/ && t != "" { print t "  " d; t = ""; d = "" }
       END { if (t != "") print t "  " d }'
