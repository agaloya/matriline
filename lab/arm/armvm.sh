#!/usr/bin/env bash
# armvm.sh - an emulated aarch64 helper host for the ORCA cross-architecture test
# (docs/PORTING.md section 1). Not part of the 7-VM lab topology: user-mode networking, so
# it reaches the host (10.0.2.2) and through it the lab servers forwarded there.
#
#   lab/arm/armvm.sh build        download Debian 13 arm64, overlay disk, cloud-init seed
#   lab/arm/armvm.sh start|stop   run it (QEMU TCG: no KVM for another architecture)
#   lab/arm/armvm.sh ssh [cmd]    log in (host port 2230)
#   lab/arm/armvm.sh orca TARBALL copy the ORCA arm64 package in and unpack to /opt/orca-6.1.1
#
# 2 vCPUs and 3 GB RAM (user's choice, 2026-10-04). Emulation is 5-20x slower than native:
# tiny calculations only.
# Image: https://cloud.debian.org/images/cloud/ (genericcloud = cloud-init, virtio drivers).
# Firmware: edk2 AAVMF (pacman: edk2-aarch64). QEMU "virt" machine: https://www.qemu.org/docs/master/system/arm/virt.html
set -euo pipefail
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
D=$LAB/images/arm
KEY=$LAB/keys/lab_ed25519
URL=https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2
FW=/usr/share/edk2/aarch64/QEMU_EFI.fd
PORT=2230
mkdir -p "$D"

ssh_vm() { ssh -q -i "$KEY" -p $PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 matriline@127.0.0.1 "$@"; }

case ${1:-} in
build)
	[[ -f $D/base.qcow2 ]] || curl -fL --retry 3 -o "$D/base.qcow2.part" "$URL" && { [[ -f $D/base.qcow2 ]] || mv "$D/base.qcow2.part" "$D/base.qcow2"; }
	[[ -f $D/arm.qcow2 ]] || qemu-img create -q -f qcow2 -F qcow2 -b "$D/base.qcow2" "$D/arm.qcow2" 20G
	cp "$FW" "$D/efi-code.fd" && truncate -s 64M "$D/efi-code.fd"
	[[ -f $D/efi-vars.fd ]] || truncate -s 64M "$D/efi-vars.fd"
	t=$(mktemp -d)
	printf 'instance-id: arm-1\nlocal-hostname: arm\n' >"$t/meta-data"
	cat >"$t/user-data" <<EOF
#cloud-config
users:
  - name: matriline
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    lock_passwd: true
    ssh_authorized_keys: ["$(cat "$KEY.pub")"]
growpart: {mode: auto, devices: ["/"]}
EOF
	xorriso -as mkisofs -quiet -V cidata -J -r -o "$D/seed.iso" "$t/user-data" "$t/meta-data" 2>/dev/null
	rm -rf "$t"
	echo "built $D" ;;
start)
	qemu-system-aarch64 -name arm -machine virt -cpu max -smp 2 -m 3072 \
		-drive if=pflash,format=raw,readonly=on,file="$D/efi-code.fd" \
		-drive if=pflash,format=raw,file="$D/efi-vars.fd" \
		-drive if=virtio,format=qcow2,file="$D/arm.qcow2" \
		-drive if=virtio,format=raw,readonly=on,file="$D/seed.iso" \
		-netdev user,id=n0,hostfwd=tcp:127.0.0.1:$PORT-:22 -device virtio-net-pci,netdev=n0 \
		-device virtio-rng-pci -display none -daemonize -pidfile "$D/arm.pid" \
		-serial "file:$D/serial.log"
	echo "started (pid $(cat "$D/arm.pid")); boot under emulation takes a few minutes" ;;
stop)
	[[ -f $D/arm.pid ]] && kill "$(cat "$D/arm.pid")" 2>/dev/null; rm -f "$D/arm.pid"; echo stopped ;;
ssh)
	shift; ssh_vm "$@" ;;
orca)
	f=${2:?usage: armvm.sh orca TARBALL}
	ssh_vm "sudo mkdir -p /opt/orca-6.1.1 && sudo chown matriline: /opt/orca-6.1.1"
	ssh_vm "tar -C /opt/orca-6.1.1 --strip-components=1 -xJf -" <"$f"
	ssh_vm "ls /opt/orca-6.1.1 | head; du -sh /opt/orca-6.1.1" ;;
*) sed -n '2,13p' "$0"; exit 2 ;;
esac
