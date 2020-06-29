#!/bin/bash

cp /mnt/hostnic-cni/bin/hostnic  /opt/cni/bin/
cp /mnt/hostnic-cni/bin/hostnic-ipam /opt/cni/bin/
cp /mnt/hostnic-cni/test/test.conf /etc/cni/net.d/10-hostnic.conf

export PATH=$PATH:/opt/cni/bin/

export CNI_PATH=/opt/cni/bin/
export NETCONFPATH=/etc/cni/net.d/
#export CNI_ARGS="K8S_POD_NAME=openldap-0;K8S_POD_NAMESPACE=kubesphere-system;K8S_POD_INFRA_CONTAINER_ID=testcontainer"
export CNI_IFNAME=eth0
export CNI_ARGS="K8S_POD_NAME=redis-6fd6c6d6f9-fmrrv;K8S_POD_NAMESPACE=kubesphere-system;K8S_POD_INFRA_CONTAINER_ID=testcontainer"


ip netns add test

#cnitool $1 hostnic /var/run/netns/test