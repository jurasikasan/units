#!/bin/sh
# job_runner_dind's entrypoint (BOT-673): dockerd as the image starts it, then a
# run may reach the internet and nothing private — not the stack, not the LAN,
# not the host. Fails closed: if the rule cannot be set, dockerd is stopped and
# the container exits, so job_runner (healthy-gated on this) never starts runs.
set -u

dockerd-entrypoint.sh dockerd --host=unix:///var/run/dind/docker.sock &
pid=$!
trap 'kill -TERM $pid; wait $pid; exit 0' TERM INT

i=0
until docker info >/dev/null 2>&1; do
  kill -0 $pid 2>/dev/null || exit 1
  i=$((i + 1)); [ $i -lt 120 ] || { echo "dind-egress: dockerd did not come up"; kill -TERM $pid; exit 1; }
  sleep 1
done

# The entrypoint picks nft or legacy iptables for dockerd; use whichever holds
# dockerd's DOCKER-USER chain.
ipt=
for c in iptables /usr/local/sbin/.iptables-legacy/iptables; do
  "$c" -nL DOCKER-USER >/dev/null 2>&1 && { ipt=$c; break; }
done
[ -n "$ipt" ] || { echo "dind-egress: no DOCKER-USER chain"; kill -TERM $pid; exit 1; }

# Forwarded traffic not arriving from outside (eth0) is a run's; replies to a run
# come in on eth0 and are not matched.
for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16; do
  "$ipt" -C DOCKER-USER ! -i eth0 -d "$net" -j REJECT 2>/dev/null \
    || "$ipt" -I DOCKER-USER ! -i eth0 -d "$net" -j REJECT \
    || { echo "dind-egress: rule for $net not set"; kill -TERM $pid; exit 1; }
done
touch /run/egress-ready
echo "dind-egress: runs reach the internet only ($ipt)"

wait $pid
