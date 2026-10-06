#!/bin/sh
# Restrict the MealQuest published host port (10.60.1.14:8090) to Traefik
# (10.60.1.33) and the Docker host itself (10.60.1.14); drop every other
# source.
#
# DOCKER-USER runs after Docker's DNAT, so the destination there is the
# container address and port, not the published ones. Match the original
# destination address and port with conntrack instead of -d/--dport.
#
# The rules must sit at the top of DOCKER-USER, ahead of any other project's
# rules (an earlier RETURN or DROP would decide MealQuest traffic first).
# The port is never left open while the rules are updated:
#
#   1. If the chain already starts with exactly the three rules below and
#      holds no other mealquest-firewall rule, change nothing.
#   2. Otherwise insert the DROP at position 1 first, so the port is closed
#      to every source (including Traefik, briefly) from here on.
#   3. Delete every other rule tagged mealquest-firewall, by rule number,
#      highest first.
#   4. Insert the two RETURN rules above the DROP.
#
# Re-runs leave the same ordered rules at positions 1-3 and no duplicates.
# Installed as /usr/local/sbin/mealquest-firewall.
set -eu

chain=DOCKER-USER
tag="-m comment --comment mealquest-firewall"
match="-m conntrack --ctorigdst 10.60.1.14 --ctorigdstport 8090 --ctdir ORIGINAL"

# The three rules as iptables -S prints them, in their required order.
want="-A $chain -s 10.60.1.33/32 $match $tag -j RETURN
-A $chain -s 10.60.1.14/32 $match $tag -j RETURN
-A $chain $match $tag -j DROP"

rules=$(iptables -w -S "$chain" | grep -e '^-A ' || true)
head=$(printf '%s\n' "$rules" | head -n 3)
tagged=$(printf '%s\n' "$rules" | grep -c -e '--comment mealquest-firewall ' || true)
if [ "$head" = "$want" ] && [ "$tagged" -eq 3 ]; then
	exit 0
fi

iptables -w -I "$chain" 1 $match $tag -j DROP

iptables -w -S "$chain" | grep -e '^-A ' |
	awk '/--comment mealquest-firewall / { print NR }' |
	sort -rn | while read -r num; do
	if [ "$num" -ne 1 ]; then
		iptables -w -D "$chain" "$num"
	fi
done

iptables -w -I "$chain" 1 -s 10.60.1.14/32 $match $tag -j RETURN
iptables -w -I "$chain" 1 -s 10.60.1.33/32 $match $tag -j RETURN
