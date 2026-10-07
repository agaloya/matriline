#!/usr/bin/env bash
# macvm.sh - macOS x86-64 helper host for the client port (docs/PORTING.md section 3).
# Like lab/windows/winvm.sh: user-mode networking, SSH on a host port (2243), the VM reaches
# the host as 10.0.2.2. macOS on QEMU boots through OpenCore, as in the OSX-KVM project
# (https://github.com/kholia/OSX-KVM, GPL-3.0; OpenCore.qcow2 and the OVMF files come from
# it, commit 4c378a4b5e0b); the QEMU options follow its OpenCore-Boot.sh. Apple's licence only
# allows macOS virtual machines on Apple hardware: this is a short-lived test VM (user's
# decision, 2026-10-05). The installer image is the user's (lab/images/macos/*.iso, md5
# 3f472c065e0f7179664afc4775f2535f).
#
#   lab/macos/macvm.sh build            empty 64G disk + copies of the firmware variables
#   lab/macos/macvm.sh install          boot the installer (screen on VNC 127.0.0.1:5902)
#   lab/macos/macvm.sh start|stop       normal runs (after the installation)
#   lab/macos/macvm.sh ssh [cmd]        once Remote Login is enabled in the VM
#   lab/macos/macvm.sh screen <file.png>   screenshot (no VNC client needed)
#   lab/macos/macvm.sh click <x> <y>        mouse click (screen 1920x1080)
#
# The macOS installer cannot run unattended: Disk Utility (erase the 64G disk as APFS) and
# the installer need a few clicks, through VNC (e.g. vncviewer 127.0.0.1:5902) or the
# QEMU monitor (sendkey / mouse).
set -euo pipefail
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
D=$LAB/images/macos
KEY=$LAB/keys/lab_ed25519
PORT=2243
ISO=$D/installer.img # link to the installer image (its own name has spaces)

qemu_cmd() { # $1 = extra drives
	# isa-applesmc osk: the string macOS checks for an Apple SMC, as used by OSX-KVM
	qemu-system-x86_64 -name macos -enable-kvm -m 6144 \
		-cpu Haswell-noTSX,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on,+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt,check \
		-machine q35 -smp 4,cores=2,sockets=1 \
		-device qemu-xhci,id=xhci -device usb-kbd,bus=xhci.0 -device usb-tablet,bus=xhci.0 \
		-device isa-applesmc,osk="ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc" \
		-drive if=pflash,format=raw,readonly=on,file="$D/OVMF_CODE_4M.fd" \
		-drive if=pflash,format=raw,file="$D/vars.fd" \
		-smbios type=2 \
		-device ich9-ahci,id=sata \
		-drive id=OpenCoreBoot,if=none,snapshot=on,format=qcow2,file="$D/OpenCore.qcow2" \
		-device ide-hd,bus=sata.2,drive=OpenCoreBoot \
		$1 \
		-drive id=MacHDD,if=none,file="$D/mac_hdd.qcow2",format=qcow2 -device ide-hd,bus=sata.4,drive=MacHDD \
		-netdev user,id=net0,hostfwd=tcp:127.0.0.1:$PORT-:22 -device virtio-net-pci,netdev=net0,id=net0,mac=52:54:00:c9:18:27 \
		-device vmware-svga -display none -vnc 127.0.0.1:2 \
		-daemonize -pidfile "$D/macos.pid" -monitor "unix:$D/macos.mon,server=on,wait=off" \
		-qmp "unix:$D/macos.qmp,server=on,wait=off"
}

case ${1:-} in
build)
	[[ -f $D/mac_hdd.qcow2 ]] || qemu-img create -q -f qcow2 "$D/mac_hdd.qcow2" 64G
	cp "$D/OVMF_VARS-1920x1080.fd" "$D/vars.fd"
	echo "built $D/mac_hdd.qcow2; next: macvm.sh install" ;;
install)
	[[ -e $ISO ]] || { echo "no installer image: link it as $ISO" >&2; exit 1; }
	qemu_cmd "-drive id=InstallMedia,if=none,file=$ISO,format=raw,offset=$((262208*512)),size=$((34340800*512)),snapshot=on -device ide-hd,bus=sata.3,drive=InstallMedia"
	echo "installer booting; screen on VNC 127.0.0.1:5902" ;;
start) qemu_cmd ""; echo "started macos (ssh port $PORT)" ;;
stop) [[ -f $D/macos.pid ]] && kill "$(cat "$D/macos.pid")" 2>/dev/null; rm -f "$D/macos.pid"; echo stopped ;;
ssh)
	shift
	ssh -q -i "$KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 matriline@127.0.0.1 "$@" ;;
screen)
	out=${2:?usage: macvm.sh screen <file.png>}
	echo "screendump $out.ppm" | socat - "UNIX-CONNECT:$D/macos.mon" >/dev/null; sleep 1
	python3 - "$out.ppm" "$out" <<'PY'
import sys, zlib, struct
d = open(sys.argv[1], 'rb').read(); parts = d.split(b'\n', 3); w, h = map(int, parts[1].split()); px = parts[3]
raw = b''.join(b'\x00' + px[y*w*3:(y+1)*w*3] for y in range(h))
c = lambda t, b: struct.pack('>I', len(b)) + t + b + struct.pack('>I', zlib.crc32(t + b) & 0xffffffff)
open(sys.argv[2], 'wb').write(b'\x89PNG\r\n\x1a\n' + c(b'IHDR', struct.pack('>IIBBBBB', w, h, 8, 2, 0, 0, 0)) + c(b'IDAT', zlib.compress(raw)) + c(b'IEND', b''))
PY
	rm -f "$out.ppm"; echo "$out" ;;
click) # click <x> <y> on a 1920x1080 screen (absolute pointer, QMP input-send-event)
	python3 - "$D/macos.qmp" "${2:?x}" "${3:?y}" <<'PY'
import json, socket, sys, time
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); f = s.makefile('rw')
f.readline(); f.write(json.dumps({"execute": "qmp_capabilities"}) + "\n"); f.flush(); f.readline()
x, y = int(sys.argv[2]) * 32767 // 1919, int(sys.argv[3]) * 32767 // 1079
def ev(events):
    f.write(json.dumps({"execute": "input-send-event", "arguments": {"events": events}}) + "\n"); f.flush(); f.readline()
ev([{"type": "abs", "data": {"axis": "x", "value": x}}, {"type": "abs", "data": {"axis": "y", "value": y}}])
time.sleep(0.2)
ev([{"type": "btn", "data": {"down": True, "button": "left"}}]); time.sleep(0.1)
ev([{"type": "btn", "data": {"down": False, "button": "left"}}])
PY
	;;
*) sed -n '2,20p' "$0"; exit 2 ;;
esac
