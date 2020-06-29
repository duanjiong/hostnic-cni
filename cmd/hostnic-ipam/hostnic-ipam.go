//
// =========================================================================
// Copyright (C) 2017 by Yunify, Inc...
// -------------------------------------------------------------------------
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this work except in compliance with the License.
// You may obtain a copy of the License in the LICENSE file, or at:
//
//  http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// =========================================================================
//

package main

import (
	"context"
	"encoding/json"
	bv "github.com/containernetworking/plugins/pkg/utils/buildversion"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"net"
	"runtime"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/pkg/errors"
	"github.com/yunify/hostnic-cni/pkg/rpc"
	. "github.com/yunify/hostnic-cni/pkg/types"
	"google.golang.org/grpc"
)

func init() {
	runtime.LockOSThread()
}

func main() {
	log.SetLevel(log.DebugLevel)

	skel.PluginMain(cmdAdd, nil, cmdDel, version.All, bv.BuildString("hostnic-ipam"))
}

func cmdAdd(args *skel.CmdArgs) error {
	conf := NetConf{}
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		return errors.Wrap(err, "add cmd: error loading config from args")
	}

	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		return errors.Wrap(err, "add cmd: failed to load k8s config from arg")
	}

	cniVersion := conf.CNIVersion

	// Set up a connection to the ipamD server.
	conn, err := grpc.Dial(DefaultUnixSocketPath, grpc.WithInsecure())
	if err != nil {
		return errors.Wrap(err, "add cmd: failed to connect to backend server")
	}
	defer conn.Close()
	c := rpc.NewCNIBackendClient(conn)
	r, err := c.AddNetwork(context.Background(),
		&rpc.NetworkRequest{
			Netns:                      args.Netns,
			K8S_POD_NAME:               string(k8sArgs.K8S_POD_NAME),
			K8S_POD_NAMESPACE:          string(k8sArgs.K8S_POD_NAMESPACE),
			K8S_POD_INFRA_CONTAINER_ID: string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
			IfName:                     args.IfName,
		})

	if err != nil {
		return err
	}

	log.Debugf("addNetwork reply %+v", *r)
	_, defNet, _ := net.ParseCIDR("0.0.0.0/0")
	_, hostnicNet, _ := net.ParseCIDR(r.IPv4Subnet)
	link, _ := netlink.LinkByName(GetHostNicName(int(r.RouteTableNum)))
	name := GetHostNicName(int(r.RouteTableNum))
	mac := ""
	if link != nil {
		name = link.Attrs().Name
		mac = link.Attrs().HardwareAddr.String()
	}
	result := &current.Result{
		IPs: []*current.IPConfig{
			{
				Version: "4",
				Address: net.IPNet{
					IP:   net.ParseIP(r.IPv4Addr),
					Mask: hostnicNet.Mask,
				},
				Gateway: net.ParseIP(r.GW),
			},
		},
		Routes: []*types.Route{
			{
				Dst: *defNet,
				GW:  net.ParseIP(r.GW),
			},
		},
		Interfaces: []*current.Interface{
			{
				Name: name,
				Mac:  mac,
			},
		},
	}

	return types.PrintResult(result, cniVersion)
}

func cmdDel(args *skel.CmdArgs) error {
	conf := types.NetConf{}
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		return errors.Wrap(err, "del cmd: failed to load netconf from args")
	}

	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		return errors.Wrap(err, "del cmd: failed to load k8s config from args")
	}

	// notify local IP address manager to free secondary IP
	// Set up a connection to the server.
	conn, err := grpc.Dial(DefaultUnixSocketPath, grpc.WithInsecure())
	if err != nil {
		return errors.Wrap(err, "del cmd: failed to connect to backend server")
	}
	defer conn.Close()

	c := rpc.NewCNIBackendClient(conn)
	request := &rpc.NetworkRequest{
		K8S_POD_NAME:               string(k8sArgs.K8S_POD_NAME),
		K8S_POD_NAMESPACE:          string(k8sArgs.K8S_POD_NAMESPACE),
		K8S_POD_INFRA_CONTAINER_ID: string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
		Netns:                      args.Netns,
		IfName:                     args.IfName,
	}
	_, err = c.DelNetwork(context.Background(), request)

	return err
}
