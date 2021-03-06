#!/bin/sh

/bin/bash

function CleanUp() {
    echo "===== Deleting HOSTNIC-AGENT ==========="
    rm -f  /host/opt/cni/bin/hostnic
    rm -f  /host/opt/cni/bin/portmap
    rm -f  /host/etc/cni/net.d/10-ahostnic.conflist
    rm -f /host/etc/cni/net.d/99-loopback.conf
}

trap CleanUp EXIT SIGINT SIGQUIT

echo "===== Starting installing HOSTNIC-CNI ========="
CleanUp

cp /app/hostnic /host/opt/cni/bin/
#cp /app/portmap /host/opt/cni/bin/


echo "===== Starting HOSTNIC-AGENT ==========="
dlv --listen=:40000 --headless=true --api-version=2 --accept-multiclient exec /app/hostnic-agent

