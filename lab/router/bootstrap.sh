#!/bin/sh
# bootstrap.sh - first-boot step for the router VM (run by cloud-init runcmd).
# Mounts the host shares over 9p and hands over to /mnt/lab/router/install.sh.
#   lab      -> /mnt/lab       (this repository's lab/ directory, read-only)
#   captures -> /mnt/captures  (lab/captures, writable: pcap files + state)
#   state    -> /mnt/state     (lab/state, writable: NAT profile settings)
set -eu
for m in 9p 9pnet_virtio; do
	grep -qx "$m" /etc/modules || echo "$m" >>/etc/modules
	modprobe "$m"
done
mkdir -p /mnt/lab /mnt/captures /mnt/state
if ! grep -q matriline-lab /etc/fstab; then
	o=trans=virtio,version=9p2000.L,msize=524288,cache=mmap
	cat >>/etc/fstab <<EOT
lab /mnt/lab 9p $o,ro 0 0 # matriline-lab
captures /mnt/captures 9p $o,rw 0 0 # matriline-lab
state /mnt/state 9p $o,rw 0 0 # matriline-lab
EOT
fi
mount -a
exec sh /mnt/lab/router/install.sh
