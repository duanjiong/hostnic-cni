#!/bin/sh
cp /app/hostnic  /opt/cni/bin/
cp /app/hostnic-ipam /opt/cni/bin/
cp /etc/hostnic/10-hostnic.conf /etc/cni/net.d/