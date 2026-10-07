#!/bin/sh
# guest-setup.sh - one-time setup run as root inside every lab node VM
# (cloud-init runcmd, or an image builder, guest/build-*.sh, inside a chroot).
#
# usage: guest-setup.sh <role> <fs> [--no-mount]
#   role: compute  -> ORCA 6.1.1 + OpenMPI + Matriline binaries (6.1.0 removed 2026-10-05)
#         relay    -> Matriline binaries only
#   fs:   9p       -> QEMU -virtfs (virtio-9p) shares
#         virtiofs -> virtiofsd shares (where the kernel lacks 9p, e.g. Debian cloud; also devuan)
#
# The host directories are exported read-only by QEMU/virtiofsd with mount tags:
#   orca611 -> /opt/orca-6.1.1
#   openmpi -> /opt/openmpi-4.1.8  mlbin -> /opt/matriline/bin
# Paths are identical to the host so job files and fingerprints match.
set -eu
ROLE=${1:?role}
FS=${2:?fs}
NOMOUNT=${3:-}
MARK="# matriline-lab"

case "$ROLE" in
compute) SHARES="orca611:/opt/orca-6.1.1 openmpi:/opt/openmpi-4.1.8 mlbin:/opt/matriline/bin" ;;
relay) SHARES="mlbin:/opt/matriline/bin" ;;
*) echo "unknown role $ROLE" >&2; exit 2 ;;
esac

# NIC names. cloud-init renames the NICs to mgmt/lan (network-config
# "set-name"), but it cannot rename a NIC that is already up, and Ubuntu
# 26.04's dracut initramfs (systemd-networkd) brings every NIC up before
# cloud-init runs ("[busy] Error renaming ..." in cloud-init.log). Later boots
# are fine (udev applies the generated .link files), so on first boot rename
# here: down, rename, up, then let systemd-networkd re-match its config.
# MAC plan from labctl: 52:54:00:4c:00:xx = mgmt, 52:54:00:4c:01:xx = lan.
renamed=""
for d in /sys/class/net/*; do
	dev=${d##*/}
	case $(cat "$d/address" 2>/dev/null) in
	52:54:00:4c:00:*) want=mgmt ;;
	52:54:00:4c:01:*) want=lan ;;
	*) continue ;;
	esac
	[ "$dev" = "$want" ] && continue
	ip link set dev "$dev" down && ip link set dev "$dev" name "$want" && ip link set dev "$want" up \
		&& renamed=1 || echo "WARNING: cannot rename $dev to $want" >&2
done
if [ -n "$renamed" ] && command -v networkctl >/dev/null 2>&1; then
	networkctl reload 2>/dev/null || true
	networkctl reconfigure mgmt lan 2>/dev/null || true
fi

# Load the transport modules early on every boot (systemd and non-systemd).
if [ "$FS" = 9p ]; then MODS="9p 9pnet_virtio"; else MODS="virtiofs"; fi
if [ -d /etc/modules-load.d ] || command -v systemctl >/dev/null 2>&1; then
	mkdir -p /etc/modules-load.d
	printf '%s\n' $MODS >/etc/modules-load.d/matriline-lab.conf
fi
if [ -f /etc/modules ]; then # Alpine (OpenRC) and others
	for m in $MODS; do grep -qx "$m" /etc/modules || echo "$m" >>/etc/modules; done
fi
for m in $MODS; do modprobe "$m" 2>/dev/null || true; done

# fstab entries (idempotent: old lab lines are replaced).
grep -v "$MARK" /etc/fstab >/etc/fstab.lab.tmp || true
for s in $SHARES; do
	tag=${s%%:*}; dir=${s#*:}
	mkdir -p "$dir"
	if [ "$FS" = 9p ]; then
		# cache=loose is safe for the never-changing ORCA trees; the Matriline
		# build output changes on the host, so it uses cache=none: with cache=mmap
		# the Arch guest kept serving the previous binary after a rebuild (seen in
		# the lab; newer kernels keep page cache for mmap mode).
		cache=loose; [ "$tag" = mlbin ] && cache=none
		echo "$tag $dir 9p trans=virtio,version=9p2000.L,ro,msize=524288,cache=$cache,nofail 0 0 $MARK" >>/etc/fstab.lab.tmp
	else
		echo "$tag $dir virtiofs ro,nofail 0 0 $MARK" >>/etc/fstab.lab.tmp
	fi
done
cat /etc/fstab.lab.tmp >/etc/fstab && rm -f /etc/fstab.lab.tmp
command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload 2>/dev/null || true
if [ "$NOMOUNT" != "--no-mount" ]; then
	for s in $SHARES; do
		dir=${s#*:}
		mountpoint -q "$dir" 2>/dev/null || mount "$dir" || echo "WARNING: cannot mount $dir" >&2
	done
fi

# ORCA wrappers, same content as the host's /usr/local/bin/orca-61x.
if [ "$ROLE" = compute ]; then
	rm -f /usr/local/bin/orca-610
	for v in 611:6.1.1; do
		short=${v%%:*}; ver=${v#*:}
		cat >/usr/local/bin/orca-$short <<EOF
#!/bin/sh
# ORCA $ver with its OpenMPI 4.1.8 (does not affect the system OpenMPI)
export PATH=/opt/orca-$ver:/opt/openmpi-4.1.8/bin:\$PATH
export LD_LIBRARY_PATH=/opt/orca-$ver/lib:/opt/openmpi-4.1.8/lib\${LD_LIBRARY_PATH:+:\$LD_LIBRARY_PATH}
exec /opt/orca-$ver/orca "\$@"
EOF
		chmod 755 /usr/local/bin/orca-$short
	done
	# "orca" = current version (6.1.1), like on the host.
	sed 's/^# ORCA 6.1.1/# orca = ORCA 6.1.1 (current)/' /usr/local/bin/orca-611 >/usr/local/bin/orca
	chmod 755 /usr/local/bin/orca
fi

# Lab nodes must accept test connections: disable distro firewalls if present
# (the NAT/firewall behaviour under test lives in the router VM).
if command -v systemctl >/dev/null 2>&1; then
	systemctl disable --now firewalld 2>/dev/null || true
	systemctl disable --now ufw 2>/dev/null || true
fi
echo "guest-setup: role=$ROLE fs=$FS done"
