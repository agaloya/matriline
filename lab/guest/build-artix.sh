#!/bin/bash
# build-artix.sh - build a bootable Artix Linux disk (dinit + ConnMan) on /dev/vdb with
# pacstrap and the Artix repositories. Runs as root inside the temporary builder VM
# started by "labctl build", booted from the official Arch Linux cloud image (it has
# pacman; the host never needs root). Artix publishes no cloud image, only live ISOs.
#
# usage: build-artix.sh <ssh-pubkey-file> <guest-setup.sh> <hostname> <fs>
#
# Sources (block level, adapted):
#   - Artix installation guide (pacstrap/basestrap base + init, fstab, grub, services
#     enabled as dinit.d/boot.d symlinks): https://wiki.artixlinux.org/Main/Installation
#   - Artix "Migration" guide (artix-keyring + pacman-key --populate artix on an Arch
#     system): https://wiki.artixlinux.org/Main/Migration
#   - Repository layout (system, world, galaxy): https://mirror1.artixlinux.org/repos/
#   - ConnMan provisioning files: connman-service.config(5)
#     https://git.kernel.org/pub/scm/network/connman/connman.git/tree/doc/config-format.txt
set -euxo pipefail
PUBKEY=$1 SETUP=$2 NAME=$3 FS=$4
DISK=/dev/vdb
MNT=/mnt/artix
REPO=https://mirror1.artixlinux.org/repos

# Builder: pacstrap/arch-chroot come from arch-install-scripts.
pacman -Sy --noconfirm --needed arch-install-scripts

# Artix signing keys into the builder's pacman keyring. The keyring package is taken
# from the official mirror over TLS (it cannot be verified before its keys are known),
# then every package below is verified against it.
d=$(mktemp -d)
curl -fsS -o "$d/system.db" "$REPO/system/os/x86_64/system.db"
pkg=$(bsdtar -xOf "$d/system.db" 'artix-keyring-*/desc' | awk '/%FILENAME%/{getline; print; exit}')
curl -fsS -o "$d/$pkg" "$REPO/system/os/x86_64/$pkg"
bsdtar -xf "$d/$pkg" -C / usr/share/pacman/keyrings
pacman-key --populate artix

cat >"$d/artix.conf" <<EOF
[options]
Architecture = auto
SigLevel = Required DatabaseOptional
LocalFileSigLevel = Optional
ParallelDownloads = 5
[system]
Server = $REPO/\$repo/os/\$arch
[world]
Server = $REPO/\$repo/os/\$arch
[galaxy]
Server = $REPO/\$repo/os/\$arch
EOF

# One bootable MBR partition (BIOS boot through GRUB, like the cloud images).
wipefs -a $DISK
echo 'label: dos
,,L,*' | sfdisk -q $DISK
udevadm settle
mkfs.ext4 -q -F -L artixroot ${DISK}1
mkdir -p $MNT
mount ${DISK}1 $MNT
# -M: keep Artix's own mirrorlist (artix-mirrorlist), not the builder's Arch one.
# Every init-specific package is named explicitly so pacman never picks an OpenRC/runit/s6
# provider. No linux-firmware: a VM needs none.
pacstrap -C "$d/artix.conf" -M $MNT base linux mkinitcpio dinit elogind-dinit \
	openssh openssh-dinit connman connman-dinit grub sudo python iproute2 less
install -m 0755 "$SETUP" $MNT/root/guest-setup.sh
install -m 0644 "$PUBKEY" $MNT/root/lab.pub
UUID=$(blkid -s UUID -o value ${DISK}1)

arch-chroot $MNT /bin/bash -euxo pipefail -s <<EOF
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

# ConnMan manages only lan (DHCP: default route, DNS through ConnMan's local proxy). It
# follows the carrier, so the link bounce labctl does after a profile change gets a new
# lease. mgmt is blacklisted: as a second wired service without a gateway, ConnMan made it
# the default service and installed "default dev mgmt" (seen on the first boot). mgmt gets
# its static address (no gateway, ssh hostfwd only) from rc.local instead. No online
# check: it would send HTTP probes to the real internet from every home.
mkdir -p /var/lib/connman /etc/connman
cat > /var/lib/connman/matriline-lab.config <<'EOR'
[service_lan]
Type = ethernet
DeviceName = lan
IPv4 = dhcp
IPv6 = off
EOR
printf '[General]\nEnableOnlineCheck = false\nNetworkInterfaceBlacklist = mgmt\n' > /etc/connman/main.conf
cat >> /etc/rc.local <<'EOR'
ip link set mgmt up
ip addr add 10.0.2.15/24 dev mgmt 2>/dev/null || true
EOR

# dinit services: enabled = symlink in /etc/dinit.d/boot.d.
for s in sshd connmand; do ln -sf ../\$s /etc/dinit.d/boot.d/\$s; done

# Kernel, initramfs and bootloader (serial console for run/<vm>.serial.log).
sed -i 's/^MODULES=.*/MODULES=(virtio_blk virtio_pci ext4)/' /etc/mkinitcpio.conf
mkinitcpio -P
sed -i 's/^#\?GRUB_TIMEOUT=.*/GRUB_TIMEOUT=1/' /etc/default/grub
sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT=.*/GRUB_CMDLINE_LINUX_DEFAULT="loglevel=4 console=tty0 console=ttyS0,115200"/' /etc/default/grub
grub-install --target=i386-pc $DISK
grub-mkconfig -o /boot/grub/grub.cfg

# Host shares + ORCA wrappers (mounts happen at boot).
/root/guest-setup.sh compute $FS --no-mount
rm -f /root/guest-setup.sh /root/lab.pub
find /var/cache/pacman/pkg -type f -delete
EOF

# ConnMan writes /run/connman/resolv.conf; its tmpfiles "L" line does not replace the
# plain /etc/resolv.conf that the filesystem package installs, so the guest had no DNS.
# (Done outside arch-chroot, which bind-mounts the builder's resolv.conf there.)
ln -sf /run/connman/resolv.conf $MNT/etc/resolv.conf
umount -R $MNT
fstrim -v ${DISK}1 2>/dev/null || true
sync
echo "build-artix: OK"
