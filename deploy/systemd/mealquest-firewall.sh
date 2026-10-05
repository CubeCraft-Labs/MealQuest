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
# Every run deletes all rules tagged with the mealquest-firewall comment and
# re-inserts them at positions 1-3, so re-runs leave the same ordered rules
# and no duplicates. Installed as /usr/local/sbin/mealquest-firewall.
set -eu

tag="-m comment --comment mealquest-firewall"
match="-m conntrack --ctorigdst 10.60.1.14 --ctorigdstport 8090 --ctdir ORIGINAL"

rules=$(iptables -w -S DOCKER-USER)
printf '%s\n' "$rules" | while read -r _ chain spec; do
	case " $spec " in
	*" --comment mealquest-firewall "*) iptables -w -D "$chain" $spec ;;
	esac
done

iptables -w -I DOCKER-USER 1 -s 10.60.1.33/32 $match $tag -j RETURN
iptables -w -I DOCKER-USER 2 -s 10.60.1.14/32 $match $tag -j RETURN
iptables -w -I DOCKER-USER 3 $match $tag -j DROP
