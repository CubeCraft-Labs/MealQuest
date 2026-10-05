#!/bin/sh
# Restrict the MealQuest published host port to Traefik (10.60.1.33) and the
# Docker host itself (10.60.1.14); drop every other source.
#
# DOCKER-USER runs after Docker's DNAT, so the destination port there is the
# container port (8080), not the published one. Match the original
# destination port with conntrack instead of --dport.
#
# Idempotent: each rule is checked (-C) before it is added, so re-runs add
# nothing. Installed as /usr/local/sbin/mealquest-firewall.
set -eu

port=8090
match="-p tcp -m conntrack --ctorigdstport $port --ctdir ORIGINAL"

iptables -C DOCKER-USER -s 10.60.1.33/32 $match -j RETURN 2>/dev/null ||
	iptables -I DOCKER-USER 1 -s 10.60.1.33/32 $match -j RETURN
iptables -C DOCKER-USER -s 10.60.1.14/32 $match -j RETURN 2>/dev/null ||
	iptables -I DOCKER-USER 2 -s 10.60.1.14/32 $match -j RETURN
iptables -C DOCKER-USER $match -j DROP 2>/dev/null ||
	iptables -A DOCKER-USER $match -j DROP
