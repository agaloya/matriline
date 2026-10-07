#!/bin/bash
# build-void.sh - build a bootable Void Linux (glibc, runit) disk on /dev/vdb
# from the official ROOTFS tarball. Runs as root inside the temporary Debian
# builder VM started by "labctl build" (the host never needs root).
#
# usage: build-void.sh <void-rootfs.tar.xz> <ssh-pubkey-file> <guest-setup.sh> [<hostname> <fs>]
# (labctl passes hostname and fs to every image builder; Void always uses void and 9p)
#
# Adapted from the Void Linux Handbook, "Installation via chroot":
#   https://docs.voidlinux.org/installation/guides/chroot.html
# (ROOTFS method: xbps-install -Su xbps; xbps-install -u; install base-system;
#  remove base-container-full; configure fstab, grub and xbps-reconfigure -fa).
set -euxo pipefail
TAR=$1 PUBKEY=$2 SETUP=$3
DISK=/dev/vdb
MNT=/mnt/void
REPO=https://repo-default.voidlinux.org/current

command -v xz >/dev/null || { apt-get update -qq; apt-get install -y -qq xz-utils; }

# One bootable MBR partition (BIOS boot through GRUB, like the cloud images).
wipefs -a $DISK
echo 'label: dos
,,L,*' | sfdisk -q $DISK
udevadm settle
mkfs.ext4 -q -F -L voidroot ${DISK}1
mkdir -p $MNT
mount ${DISK}1 $MNT
tar xpf "$TAR" --xattrs-include='*.*' --numeric-owner -C $MNT
cp /etc/resolv.conf $MNT/etc/resolv.conf
for d in dev proc sys; do mount --rbind /$d $MNT/$d; mount --make-rslave $MNT/$d; done
install -m 0755 "$SETUP" $MNT/root/guest-setup.sh
install -m 0644 "$PUBKEY" $MNT/root/lab.pub
UUID=$(blkid -s UUID -o value ${DISK}1)

chroot $MNT /bin/bash -euxo pipefail -s <<EOF
export XBPS_ARCH=x86_64
xbps-install -Sy -R $REPO -u xbps
xbps-install -y -R $REPO -u
# A VM needs no hardware firmware: keep base-system's firmware packages out
# (ignorepkg, as documented in the Void Handbook "Advanced Usage" section).
mkdir -p /etc/xbps.d
printf 'ignorepkg=%s\n' linux-firmware linux-firmware-amd linux-firmware-intel \
	linux-firmware-nvidia linux-firmware-network linux-firmware-broadcom wifi-firmware > /etc/xbps.d/90-matriline-lab.conf
xbps-install -y -R $REPO base-system grub python3
xbps-remove -Ry linux-firmware linux-firmware-amd linux-firmware-intel linux-firmware-nvidia \
	linux-firmware-network linux-firmware-broadcom wifi-firmware 2>/dev/null || true
# xbps-install fails on already-installed packages, so add the rest one by one.
for p in openssh sudo dhcpcd iproute2; do xbps-query \$p >/dev/null || xbps-install -y -R $REPO \$p; done
xbps-remove -y base-container-full || true

echo void > /etc/hostname
echo 'UUID=$UUID / ext4 defaults,noatime 0 1' > /etc/fstab
echo 'en_US.UTF-8 UTF-8' >> /etc/default/libc-locales
xbps-reconfigure -f glibc-locales
ln -sf /usr/share/zoneinfo/UTC /etc/localtime

# User "matriline": key-only ssh, passwordless sudo. Password field "*"
# (not "!") so OpenSSH does not treat the account as locked.
useradd -m -s /bin/bash -G wheel matriline
usermod -p '*' matriline
install -d -m 700 -o matriline -g matriline /home/matriline/.ssh
install -m 600 -o matriline -g matriline /root/lab.pub /home/matriline/.ssh/authorized_keys
echo 'matriline ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/matriline
chmod 440 /etc/sudoers.d/matriline
sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config

# Stable NIC names like the cloud-init VMs: mgmt (slirp) and lan (to the router).
mkdir -p /etc/udev/rules.d
cat > /etc/udev/rules.d/70-matriline-lab.rules <<'EOR'
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="52:54:00:4c:00:03", NAME="mgmt"
SUBSYSTEM=="net", ACTION=="add", ATTR{address}=="52:54:00:4c:01:03", NAME="lan"
EOR
# mgmt: static address, no gateway (ssh hostfwd only). lan: DHCP via dhcpcd.
cat >> /etc/rc.local <<'EOR'
ip link set mgmt up
ip addr add 10.0.2.15/24 dev mgmt 2>/dev/null || true
EOR
cat >> /etc/dhcpcd.conf <<'EOR'
# matriline-lab: only the intranet NIC uses DHCP (default route + DNS)
allowinterfaces lan
ipv4only
EOR

# runit services
for s in sshd dhcpcd agetty-ttyS0; do ln -sf /etc/sv/\$s /etc/runit/runsvdir/default/; done
rm -f /etc/runit/runsvdir/default/agetty-tty[2-6]

# Kernel, initramfs and bootloader
echo 'hostonly="yes"' > /etc/dracut.conf.d/matriline-lab.conf
echo 'add_drivers+=" virtio_blk virtio_pci virtio_net ext4 "' >> /etc/dracut.conf.d/matriline-lab.conf
sed -i 's/^#\?GRUB_TIMEOUT=.*/GRUB_TIMEOUT=1/' /etc/default/grub
sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/GRUB_CMDLINE_LINUX_DEFAULT="loglevel=4 console=tty0 console=ttyS0,115200"/' /etc/default/grub
grub-install --target=i386-pc $DISK
xbps-reconfigure -fa
grub-mkconfig -o /boot/grub/grub.cfg

# Host shares + ORCA wrappers (mounts happen at first boot).
/root/guest-setup.sh compute 9p --no-mount
rm -f /root/guest-setup.sh /root/lab.pub
rm -rf /var/cache/xbps/*
EOF

rm -f $MNT/etc/resolv.conf
umount -R $MNT
fstrim -v ${DISK}1 2>/dev/null || true
sync
echo "build-void: OK"
