#!/bin/bash
# build-devuan.sh - build a bootable Devuan 6 "excalibur" disk (sysvinit + ifupdown) on
# /dev/vdb with debootstrap. Runs as root inside the temporary Debian builder VM started
# by "labctl build" (the host never needs root). Devuan publishes no cloud image.
#
# usage: build-devuan.sh <devuan-archive-keyring.gpg> <ssh-pubkey-file> <guest-setup.sh> <hostname> <fs>
#
# Sources (block level, adapted):
#   - debootstrap install of a Debian-like system ("Installing Debian GNU/Linux from a
#     Unix/Linux System"): https://www.debian.org/releases/stable/amd64/apds03.en.html
#   - Devuan archive keyring and merged mirror: https://files.devuan.org/devuan-archive-keyring.gpg,
#     http://deb.devuan.org/merged (https://www.devuan.org/os/packages)
#   - interfaces(5) (ifupdown) and its "dhcp" method, which runs dhcpcd when that is the
#     DHCP client installed: https://manpages.debian.org/trixie/ifupdown/interfaces.5.en.html
set -euxo pipefail
KEYRING=$1 PUBKEY=$2 SETUP=$3 NAME=$4 FS=$5
DISK=/dev/vdb
MNT=/mnt/devuan
SUITE=excalibur
MIRROR=http://deb.devuan.org/merged

apt-get update -qq
apt-get install -y -qq debootstrap
# Debian's debootstrap ships the Devuan suite scripts; "ceres" is their common script.
SCRIPT=/usr/share/debootstrap/scripts/$SUITE
[ -e "$SCRIPT" ] || SCRIPT=/usr/share/debootstrap/scripts/ceres

# One bootable MBR partition (BIOS boot through GRUB, like the cloud images).
wipefs -a $DISK
echo 'label: dos
,,L,*' | sfdisk -q $DISK
udevadm settle
mkfs.ext4 -q -F -L devuanroot ${DISK}1
mkdir -p $MNT
mount ${DISK}1 $MNT
# devuan-keyring: the keyring on files.devuan.org lacks the current excalibur archive key
# (apt in the new system failed with NO_PUBKEY B3982868D104092C); the package has it.
debootstrap --variant=minbase --keyring="$KEYRING" --include=sysvinit-core,devuan-keyring $SUITE $MNT $MIRROR "$SCRIPT"
for d in dev proc sys; do mount --rbind /$d $MNT/$d; mount --make-rslave $MNT/$d; done
cp /etc/resolv.conf $MNT/etc/resolv.conf
install -m 0755 "$SETUP" $MNT/root/guest-setup.sh
install -m 0644 "$PUBKEY" $MNT/root/lab.pub
UUID=$(blkid -s UUID -o value ${DISK}1)

chroot $MNT /bin/bash -euxo pipefail -s <<EOF
export DEBIAN_FRONTEND=noninteractive
echo 'deb $MIRROR $SUITE main' > /etc/apt/sources.list
apt-get update -qq
# A VM needs no firmware or recommended extras. e2fsprogs is not in minbase, and without
# fsck.ext4 the sysvinit root fsck fails and the boot stops in maintenance mode.
apt-get install -y -qq --no-install-recommends linux-image-amd64 grub-pc initramfs-tools e2fsprogs \
	ifupdown dhcpcd-base openssh-server sudo python3 iproute2 kmod procps less

echo $NAME > /etc/hostname
printf '127.0.0.1 localhost\n127.0.1.1 $NAME.lab $NAME\n' > /etc/hosts
echo 'UUID=$UUID / ext4 defaults,noatime 0 1' > /etc/fstab
ln -sf /usr/share/zoneinfo/UTC /etc/localtime

# User "matriline": key-only ssh, passwordless sudo (same as the other lab nodes).
useradd -m -s /bin/bash matriline
usermod -p '*' matriline
install -d -m 700 -o matriline -g matriline /home/matriline/.ssh
install -m 600 -o matriline -g matriline /root/lab.pub /home/matriline/.ssh/authorized_keys
echo 'matriline ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/matriline
chmod 440 /etc/sudoers.d/matriline
sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config

# Stable NIC names by MAC (labctl MAC plan: 52:54:00:4c:00:xx mgmt, 52:54:00:4c:01:xx lan).
mkdir -p /etc/udev/rules.d
cat > /etc/udev/rules.d/70-matriline-lab.rules <<'EOR'
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="52:54:00:4c:00:*", NAME="mgmt"
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="52:54:00:4c:01:*", NAME="lan"
EOR

# ifupdown: mgmt static without gateway (ssh hostfwd only), lan by DHCP. The DHCP client
# is dhcpcd (started by ifup) because it follows the carrier: after the link bounce that
# labctl does on a profile change it asks again and gets the new address. dhclient
# ignores the carrier and kept the old address; its usual companions do not help: ifplugd
# is no longer in Debian 13 / Devuan 6, and netplugd 1.2.9.2 never reaped its "ifup" child
# ("Unexpected child ... exited"), so it stayed in WAIT_IN and ignored later link changes.
cat > /etc/network/interfaces <<'EOR'
auto lo
iface lo inet loopback

auto mgmt
iface mgmt inet static
	address 10.0.2.15/24

auto lan
iface lan inet dhcp
EOR
printf '# matriline-lab: IPv4 only, like the other nodes\nipv4only\n' >> /etc/dhcpcd.conf

# Kernel console on the serial port (labctl keeps run/<vm>.serial.log), short GRUB timeout.
sed -i 's/^#\?GRUB_TIMEOUT=.*/GRUB_TIMEOUT=1/' /etc/default/grub
sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/GRUB_CMDLINE_LINUX_DEFAULT="console=tty0 console=ttyS0,115200"/' /etc/default/grub
grub-install --target=i386-pc $DISK
update-initramfs -u -k all
update-grub

# Host shares + ORCA wrappers (mounts happen at boot).
/root/guest-setup.sh compute $FS --no-mount
rm -f /root/guest-setup.sh /root/lab.pub
apt-get clean
rm -rf /var/lib/apt/lists/*
EOF

rm -f $MNT/etc/resolv.conf
umount -R $MNT
fstrim -v ${DISK}1 2>/dev/null || true
sync
echo "build-devuan: OK"
