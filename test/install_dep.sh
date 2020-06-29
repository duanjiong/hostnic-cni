#!/bin/bash

#install cnitool to test hostnic-ipam & hostnic
go get github.com/containernetworking/cni
go install github.com/containernetworking/cni/cnitool