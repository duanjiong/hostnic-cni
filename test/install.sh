#!/bin/bash

cp /mnt/hostnic-cni/bin/hostnic  /opt/cni/bin/
cp /mnt/hostnic-cni/bin/hostnic-ipam /opt/cni/bin/
cp //mnt/hostnic-cni/test/test.conf /etc/cni/net.d/10-hostnic.conf