#!/bin/sh
# install.sh - (re)install the router services from the shared lab directory.
# Safe to run again after editing files under lab/router/ on the host:
#   labctl ssh router sudo sh /mnt/lab/router/install.sh
set -eu
cp /mnt/lab/router/init.d/labnet /mnt/lab/router/init.d/pcapsampler /etc/init.d/
chmod 755 /etc/init.d/labnet /etc/init.d/pcapsampler
mkdir -p /opt/lab
# Copy the binary so a running capture is not affected by host rebuilds.
cp /mnt/lab/images/bin/pcapsampler /opt/lab/pcapsampler.new && mv /opt/lab/pcapsampler.new /opt/lab/pcapsampler
chmod 755 /opt/lab/pcapsampler
# Kernel settings for a router; conntrack tuning is done per namespace by netctl.sh.
cat >/etc/sysctl.d/90-matriline-lab.conf <<EOT
net.ipv4.ip_forward = 1
net.ipv4.conf.all.rp_filter = 0
net.ipv4.conf.default.rp_filter = 0
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
EOT
sysctl -q -p /etc/sysctl.d/90-matriline-lab.conf || true
grep -qx nf_conntrack /etc/modules || echo nf_conntrack >>/etc/modules
modprobe nf_conntrack || true
rc-update add labnet default >/dev/null
# packet capture: off unless the lab state asks for it (the user stopped it on 2026-10-05,
# "no los necesitamos ahora"); turn it on with CAPTURE=1 in lab/state/router.env
if [ "$(. /mnt/state/router.env 2>/dev/null; echo "${CAPTURE:-0}")" = 1 ]; then
	rc-update add pcapsampler default >/dev/null
else
	rc-update del pcapsampler default >/dev/null 2>&1 || true
fi
rc-service labnet restart
[ "$(. /mnt/state/router.env 2>/dev/null; echo "${CAPTURE:-0}")" = 1 ] && rc-service pcapsampler restart || rc-service pcapsampler stop >/dev/null 2>&1 || true
echo "router install: done"
