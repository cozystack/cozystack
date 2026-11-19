wget -O /tmp/package-signing-pubkey.asc https://packages.linbit.com/package-signing-pubkey.asc
gpg --yes -o /etc/apt/trusted.gpg.d/linbit-keyring.gpg --dearmor /tmp/package-signing-pubkey.asc
PVERS=$(pveversion | awk -F'[/.]' '{print $2}')
echo "deb [signed-by=/etc/apt/trusted.gpg.d/linbit-keyring.gpg] http://packages.linbit.com/public/ proxmox-$PVERS drbd-9" > /etc/apt/sources.list
apt update && apt -y install drbd-dkms
echo "options drbd usermode_helper=disabled" > /etc/modprobe.d/drbd.conf
echo drbd > /etc/modules-load.d/drbd.conf
modprobe drbd
kubectl label node "${HOSTNAME}" node-role.kubernetes.io/linstor= --overwrite
