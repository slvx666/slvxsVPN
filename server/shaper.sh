#!/bin/bash
# сгенерировано build.py
DEV=ens3
IFB=ifb0
stop() {
  tc qdisc del dev $DEV root 2>/dev/null; tc qdisc del dev $DEV ingress 2>/dev/null
  tc qdisc del dev $IFB root 2>/dev/null; ip link set $IFB down 2>/dev/null
  for T in iptables ip6tables; do
    $T -t mangle -D OUTPUT -j VPN_MARK 2>/dev/null; $T -t mangle -F VPN_MARK 2>/dev/null; $T -t mangle -X VPN_MARK 2>/dev/null
    $T -t nat -D PREROUTING -j VPN_HOP 2>/dev/null; $T -t nat -F VPN_HOP 2>/dev/null; $T -t nat -X VPN_HOP 2>/dev/null
  done
}
if [ "$1" = stop ]; then stop; exit 0; fi
stop
set -e
modprobe ifb numifbs=1; modprobe act_connmark; modprobe act_mirred; modprobe cls_fw
ip link show $IFB >/dev/null 2>&1 || ip link add $IFB type ifb
ip link set $IFB up
for T in iptables ip6tables; do
  $T -t mangle -N VPN_MARK; $T -t mangle -A OUTPUT -j VPN_MARK
  $T -t mangle -A VPN_MARK -m mark ! --mark 0 -j CONNMARK --save-mark
  $T -t nat -N VPN_HOP; $T -t nat -A PREROUTING -j VPN_HOP
  $T -t nat -A VPN_HOP -i $DEV -p udp --dport 20000:40000 -j REDIRECT --to-ports 443
done
for D in $DEV $IFB; do
  tc qdisc add dev $D root handle 1: htb default 10 r2q 1000
  tc class add dev $D parent 1: classid 1:1 htb rate 10gbit ceil 10gbit burst 1m cburst 1m
  tc class add dev $D parent 1:1 classid 1:10 htb rate 9gbit ceil 10gbit burst 1m cburst 1m
  tc qdisc add dev $D parent 1:10 handle 10: fq_codel
  tc class add dev $D parent 1:1 classid 1:104 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user04-80m
  tc qdisc add dev $D parent 1:104 handle 104: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 104 fw classid 1:104
  tc class add dev $D parent 1:1 classid 1:105 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user05-80m
  tc qdisc add dev $D parent 1:105 handle 105: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 105 fw classid 1:105
  tc class add dev $D parent 1:1 classid 1:106 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user06-80m
  tc qdisc add dev $D parent 1:106 handle 106: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 106 fw classid 1:106
  tc class add dev $D parent 1:1 classid 1:107 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user07-80m
  tc qdisc add dev $D parent 1:107 handle 107: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 107 fw classid 1:107
  tc class add dev $D parent 1:1 classid 1:108 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user08-80m
  tc qdisc add dev $D parent 1:108 handle 108: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 108 fw classid 1:108
  tc class add dev $D parent 1:1 classid 1:109 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user09-80m
  tc qdisc add dev $D parent 1:109 handle 109: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 109 fw classid 1:109
  tc class add dev $D parent 1:1 classid 1:110 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user10-80m
  tc qdisc add dev $D parent 1:110 handle 110: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 110 fw classid 1:110
  tc class add dev $D parent 1:1 classid 1:111 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user11-80m
  tc qdisc add dev $D parent 1:111 handle 111: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 111 fw classid 1:111
  tc class add dev $D parent 1:1 classid 1:112 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user12-80m
  tc qdisc add dev $D parent 1:112 handle 112: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 112 fw classid 1:112
  tc class add dev $D parent 1:1 classid 1:113 htb rate 80mbit ceil 80mbit burst 256k cburst 256k  # user13-80m
  tc qdisc add dev $D parent 1:113 handle 113: fq_codel
  tc filter add dev $D parent 1: protocol all prio 1 handle 113 fw classid 1:113
done
tc qdisc add dev $DEV handle ffff: ingress
tc filter add dev $DEV parent ffff: protocol all prio 1 u32 match u32 0 0 action connmark action mirred egress redirect dev $IFB
