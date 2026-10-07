#!/usr/bin/env bash
# winvm.sh - Windows 10 / 11 helper hosts for the client port (docs/PORTING.md section 2).
# Not part of the 7-VM topology: user-mode networking, SSH on a host port; the VM reaches the
# host (10.0.2.2) and through it the test servers forwarded there.
#
#   lab/windows/winvm.sh build <win10|win11> <path-to-windows.iso>   disk + unattended answer ISO
#   lab/windows/winvm.sh install <win10|win11>     first boot: unattended setup (~20-40 min)
#   lab/windows/winvm.sh start|stop <win10|win11>  normal runs
#   lab/windows/winvm.sh ssh <win10|win11> [cmd]   PowerShell over OpenSSH (win10: 2241, win11: 2242)
#   FWD="44391" lab/windows/winvm.sh start win11   also forward host port 44391 to the VM
#
# The ISO is the user's: an official image (e.g. the 90-day evaluation from the Microsoft
# Evaluation Center); downloading it means accepting Microsoft's license, so the script never
# downloads it. Hardware chosen so Windows needs no extra drivers: SATA (AHCI) disk, e1000e NIC.
# Windows 11's TPM 2.0 / Secure Boot / RAM checks are skipped with the documented LabConfig
# keys during setup (fine for a test VM; no swtpm needed). Firmware: OVMF (edk2-ovmf).
# Unattend reference: https://learn.microsoft.com/en-us/windows-hardware/customize/desktop/unattend/
set -euo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
D=$LAB/images/windows
KEY=$LAB/keys/lab_ed25519
OVMF_CODE=/usr/share/edk2/x64/OVMF_CODE.4m.fd
OVMF_VARS=/usr/share/edk2/x64/OVMF_VARS.4m.fd
mkdir -p "$D"
vm=${2:-}
case $vm in
win10) PORT=2241 ;;
win11) PORT=2242 ;;
"") ;;
*) echo "vm must be win10 or win11" >&2; exit 2 ;;
esac

answer_file() { # writes autounattend.xml for $vm
	local pub; pub=$(cat "$KEY.pub")
	# Consumer ISOs (not the evaluation ones) ask for a product key. The Pro edition is
	# installed with Microsoft's published KMS client setup key (GVLK), which installs
	# without activating (no KMS server here): fine for a test VM, which then shows the
	# "activate Windows" notice. Keys: https://learn.microsoft.com/en-us/windows-server/get-started/kms-client-activation-keys
	local edition gvlk=W269N-WFGWX-YVC9B-4J6C9-T83GX
	case $vm in win10) edition="Windows 10 Pro" ;; win11) edition="Windows 11 Pro" ;; esac
	cat <<EOF
<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage><UILanguage>en-US</UILanguage></SetupUILanguage>
      <InputLocale>en-US</InputLocale><SystemLocale>en-US</SystemLocale><UILanguage>en-US</UILanguage><UserLocale>en-US</UserLocale>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add"><Order>1</Order><Path>reg add HKLM\\SYSTEM\\Setup\\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>2</Order><Path>reg add HKLM\\SYSTEM\\Setup\\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>3</Order><Path>reg add HKLM\\SYSTEM\\Setup\\LabConfig /v BypassRAMCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
      </RunSynchronous>
      <DiskConfiguration>
        <Disk wcm:action="add"><DiskID>0</DiskID><WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add"><Order>1</Order><Type>EFI</Type><Size>260</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>2</Order><Type>MSR</Type><Size>16</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>3</Order><Type>Primary</Type><Extend>true</Extend></CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add"><Order>1</Order><PartitionID>1</PartitionID><Format>FAT32</Format><Label>System</Label></ModifyPartition>
            <ModifyPartition wcm:action="add"><Order>2</Order><PartitionID>3</PartitionID><Format>NTFS</Format><Label>Windows</Label><Letter>C</Letter></ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>
      <ImageInstall><OSImage><InstallTo><DiskID>0</DiskID><PartitionID>3</PartitionID></InstallTo>
        <InstallFrom><MetaData wcm:action="add"><Key>/IMAGE/NAME</Key><Value>$edition</Value></MetaData></InstallFrom></OSImage></ImageInstall>
      <UserData><AcceptEula>true</AcceptEula>
        <ProductKey><Key>$gvlk</Key><WillShowUI>OnError</WillShowUI></ProductKey></UserData>
    </component>
  </settings>
  <settings pass="specialize">
    <component name="Microsoft-Windows-Deployment" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add"><Order>1</Order><Path>reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\OOBE /v BypassNRO /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
      </RunSynchronous>
    </component>
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>en-US</InputLocale><SystemLocale>en-US</SystemLocale><UILanguage>en-US</UILanguage><UserLocale>en-US</UserLocale>
    </component>
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE><HideEULAPage>true</HideEULAPage><HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideLocalAccountScreen>true</HideLocalAccountScreen><HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE><ProtectYourPC>3</ProtectYourPC></OOBE>
      <UserAccounts><LocalAccounts><LocalAccount wcm:action="add"><Name>matriline</Name><Group>Administrators</Group>
        <Password><Value>matriline-lab</Value><PlainText>true</PlainText></Password></LocalAccount></LocalAccounts></UserAccounts>
      <AutoLogon><Enabled>true</Enabled><Username>matriline</Username><LogonCount>1</LogonCount>
        <Password><Value>matriline-lab</Value><PlainText>true</PlainText></Password></AutoLogon>
      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add"><Order>1</Order>
          <CommandLine>powershell -NoProfile -Command "for (\$i = 0; \$i -lt 40 -and -not (Get-Service sshd -ErrorAction SilentlyContinue); \$i++) { Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0 -ErrorAction SilentlyContinue; if (-not (Get-Service sshd -ErrorAction SilentlyContinue)) { Start-Sleep 30 } }; Set-Service sshd -StartupType Automatic; Start-Service sshd; New-NetFirewallRule -Name sshd -DisplayName sshd -Protocol TCP -LocalPort 22 -Action Allow -ErrorAction SilentlyContinue; Set-Content -Path C:\\ProgramData\\ssh\\administrators_authorized_keys -Value '$pub'; icacls C:\\ProgramData\\ssh\\administrators_authorized_keys /inheritance:r /grant Administrators:F /grant SYSTEM:F; New-ItemProperty -Path HKLM:\\SOFTWARE\\OpenSSH -Name DefaultShell -Value C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe -PropertyType String -Force"</CommandLine>
        </SynchronousCommand>
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
EOF
}

qemu_cmd() { # boot the VM; $1 = extra drives (installer + answer ISOs on install)
	# FWD="44391 ...": also forward these host ports (127.0.0.1) to the same ports in the VM,
	# e.g. a Matriline server running in it
	local fwd="" p
	for p in ${FWD:-}; do fwd+=",hostfwd=tcp:127.0.0.1:$p-:$p"; done
	qemu-system-x86_64 -name "$vm" -machine q35,accel=kvm -cpu host -smp 2 -m 4096 \
		-drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE" \
		-drive if=pflash,format=raw,file="$D/$vm-vars.fd" \
		-device ahci,id=ahci -drive id=disk,if=none,format=qcow2,file="$D/$vm.qcow2" -device ide-hd,drive=disk,bus=ahci.0 \
		$1 \
		-netdev user,id=n0,hostfwd=tcp:127.0.0.1:$PORT-:22$fwd -device e1000e,netdev=n0 \
		-device pcie-root-port,id=hp1,chassis=1,slot=1 -device pcie-root-port,id=hp2,chassis=2,slot=2 \
		-device qemu-xhci -device usb-tablet -display none -daemonize -pidfile "$D/$vm.pid" \
		-monitor "unix:$D/$vm.mon,server=on,wait=off" \
		-serial "file:$D/$vm.serial.log"
}

case ${1:-} in
build)
	iso=$(readlink -f "${3:?usage: winvm.sh build <win10|win11> <iso>}")
	[[ -f $iso ]] || { echo "no such ISO: $iso" >&2; exit 1; }
	ln -sf "$iso" "$D/$vm-install.iso"
	[[ -f $D/$vm.qcow2 ]] || qemu-img create -q -f qcow2 "$D/$vm.qcow2" 64G
	cp "$OVMF_VARS" "$D/$vm-vars.fd"
	t=$(mktemp -d); answer_file >"$t/autounattend.xml"
	xorriso -as mkisofs -quiet -V UNATTEND -J -r -o "$D/$vm-unattend.iso" "$t/autounattend.xml" 2>/dev/null
	rm -rf "$t"
	echo "built $D/$vm.qcow2 and the answer ISO; next: winvm.sh install $vm" ;;
install)
	qemu_cmd "-drive file=$D/$vm-install.iso,media=cdrom,if=none,id=cd0 -device ide-cd,drive=cd0,bus=ahci.1,bootindex=0 \
		-drive file=$D/$vm-unattend.iso,media=cdrom,if=none,id=cd1 -device ide-cd,drive=cd1,bus=ahci.2"
	# the DVD boot loader asks "Press any key to boot from CD or DVD" for a few seconds
	for _ in $(seq 15); do echo "sendkey ret" | socat - "UNIX-CONNECT:$D/$vm.mon" >/dev/null 2>&1 || true; sleep 1; done
	echo "installing $vm unattended; SSH on port $PORT once setup and the first logon finish (20-40 min)" ;;
start)
	# EXTRA_ISO=<file.iso>: attach it as a CD (e.g. an ORCA installer), so that copying
	# installers does not grow the VM disk (a qcow2 file never shrinks: win11 reached 48 GB)
	extra=""
	[[ -n ${EXTRA_ISO:-} ]] && extra="-drive file=$(readlink -f "$EXTRA_ISO"),media=cdrom,if=none,id=cd9,readonly=on -device ide-cd,drive=cd9,bus=ahci.3"
	qemu_cmd "$extra"; echo "started $vm (ssh port $PORT)" ;;
stop) [[ -f $D/$vm.pid ]] && kill "$(cat "$D/$vm.pid")" 2>/dev/null; rm -f "$D/$vm.pid"; echo stopped ;;
ssh)
	shift 2
	ssh -q -i "$KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 matriline@127.0.0.1 "$@" ;;
*) sed -n '2,19p' "$0"; exit 2 ;;
esac
