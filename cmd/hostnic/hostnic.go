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
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/pkg/errors"
	"github.com/yunify/hostnic-cni/pkg/driver"
	"github.com/yunify/hostnic-cni/pkg/networkutils"
	"github.com/yunify/hostnic-cni/pkg/rpc"
	"github.com/yunify/hostnic-cni/pkg/rpcwrapper"
	"google.golang.org/grpc"
	"k8s.io/klog"
)

const (
	ipamDAddress       = "127.0.0.1:41080"
	defaultLogFilePath = "/tmp/hostnic.log"
)

type NetConf struct {
	// CNIVersion is the plugin version
	CNIVersion string `json:"cniVersion,omitempty"`

	// Name is the plugin name
	Name string `json:"name"`

	// Type is the plugin type
	Type string `json:"type"`

	// VethPrefix is the prefix to use when constructing the host-side
	// veth device name. It should be no more than four characters, and
	// defaults to 'eni'.
	VethPrefix string `json:"vethPrefix"`
}

// K8sArgs is the valid CNI_ARGS used for Kubernetes
type K8sArgs struct {
	types.CommonArgs

	// IP is pod's ip address
	IP net.IP

	// K8S_POD_NAME is pod's name
	K8S_POD_NAME types.UnmarshallableString

	// K8S_POD_NAMESPACE is pod's namespace
	K8S_POD_NAMESPACE types.UnmarshallableString

	// K8S_POD_INFRA_CONTAINER_ID is pod's container id
	K8S_POD_INFRA_CONTAINER_ID types.UnmarshallableString
}

func init() {
	klog.InitFlags(nil)
	flag.Set("logtostderr", "false")
	flag.Set("alsologtostderr", "false")
	flag.Set("v", "2")
	flag.Parse()
	runtime.LockOSThread()
}

func main() {
	f, err := os.OpenFile(defaultLogFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Print("Error openlog file ", err)
		os.Exit(1)
	}
	defer f.Close()
	klog.SetOutput(f)
	defer klog.Flush()
	skel.PluginMain(cmdAdd, cmdDel, version.All)
}

func cmdAdd(args *skel.CmdArgs) error {
	return add(args, driver.New(), rpcwrapper.NewGRPC(), rpcwrapper.New())
}

func add(args *skel.CmdArgs, driverClient driver.NetworkAPIs, grpcW rpcwrapper.GRPC, rpcW rpcwrapper.RPC) error {
	klog.V(1).Infof("Received CNI add request: ContainerID(%s) Netns(%s) IfName(%s) Args(%s) Path(%s) argsStdinData(%s)",
		args.ContainerID, args.Netns, args.IfName, args.Args, args.Path, args.StdinData)

	conf := NetConf{}
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		klog.Errorf("Error loading config from args: %v", err)
		return errors.Wrap(err, "add cmd: error loading config from args")
	}

	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		klog.Errorf("Failed to load k8s config from arg: %v", err)
		return errors.Wrap(err, "add cmd: failed to load k8s config from arg")
	}

	// Default the host-side veth prefix to 'nic'.
	if conf.VethPrefix == "" {
		conf.VethPrefix = "nic"
	}
	if len(conf.VethPrefix) > 4 {
		return errors.New("conf.VethPrefix must be less than 4 characters long")
	}

	cniVersion := conf.CNIVersion

	// Set up a connection to the ipamD server.
	conn, err := grpcW.Dial(ipamDAddress, grpc.WithInsecure())
	if err != nil {
		klog.Errorf("Failed to connect to backend server for pod %s namespace %s container %s: %v",
			string(k8sArgs.K8S_POD_NAME),
			string(k8sArgs.K8S_POD_NAMESPACE),
			string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
			err)
		return errors.Wrap(err, "add cmd: failed to connect to backend server")
	}
	defer conn.Close()
	c := rpcW.NewCNIBackendClient(conn)
	r, err := c.AddNetwork(context.Background(),
		&rpc.AddNetworkRequest{
			Netns:                      args.Netns,
			K8S_POD_NAME:               string(k8sArgs.K8S_POD_NAME),
			K8S_POD_NAMESPACE:          string(k8sArgs.K8S_POD_NAMESPACE),
			K8S_POD_INFRA_CONTAINER_ID: string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
			IfName:                     args.IfName})

	if err != nil {
		klog.Errorf("Error received from AddNetwork grpc call for pod %s namespace %s container %s: %v",
			string(k8sArgs.K8S_POD_NAME),
			string(k8sArgs.K8S_POD_NAMESPACE),
			string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
			err)
		return err
	}

	if !r.Success {
		klog.Errorf("Failed to assign an IP address to pod %s, namespace %s container %s,err: %s",
			string(k8sArgs.K8S_POD_NAME),
			string(k8sArgs.K8S_POD_NAMESPACE),
			string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
			r.Message)
		return fmt.Errorf("add cmd: failed to assign an IP address to container, err: %s", r.Message)
	}

	klog.V(1).Infof("Received add network response for pod %s namespace %s container %s: %s, table %d, external-SNAT: %v, vpcCIDR: %v",
		string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
		r.IPv4Addr, r.DeviceNumber, r.UseExternalSNAT, r.VPCcidrs)

	addr := &net.IPNet{
		IP:   net.ParseIP(r.IPv4Addr),
		Mask: net.IPv4Mask(255, 255, 255, 255),
	}
	// build hostVethName
	// Note: the maximum length for linux interface name is 15
	hostVethName := generateHostVethName(conf.VethPrefix, string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_NAME))
	err = driverClient.SetupNS(hostVethName, args.IfName, args.Netns, addr, int(r.DeviceNumber), r.VPCcidrs, networkutils.GetVPNNet(r.IPv4Addr), r.UseExternalSNAT)

	if err != nil {
		klog.Errorf("Failed SetupPodNetwork for pod %s namespace %s container %s: %v",
			string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID), err)

		// return allocated IP back to IP pool
		r, delErr := c.DelNetwork(context.Background(),
			&rpc.DelNetworkRequest{
				K8S_POD_NAME:               string(k8sArgs.K8S_POD_NAME),
				K8S_POD_NAMESPACE:          string(k8sArgs.K8S_POD_NAMESPACE),
				K8S_POD_INFRA_CONTAINER_ID: string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
				IPv4Addr:                   r.IPv4Addr,
				Reason:                     "SetupNSFailed"})

		if delErr != nil {
			klog.Errorf("Error received from DelNetwork grpc call for pod %s namespace %s container %s: %v",
				string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID), delErr)
		}

		if !r.Success {
			klog.Errorf("Failed to release IP of pod %s namespace %s container %s: %v",
				string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID), delErr)
		}
		return errors.Wrap(err, "add command: failed to setup network")
	}

	ips := []*current.IPConfig{
		{
			Version: "4",
			Address: *addr,
		},
	}

	result := &current.Result{
		IPs: ips,
	}

	return types.PrintResult(result, cniVersion)
}

// generateHostVethName returns a name to be used on the host-side veth device.
func generateHostVethName(prefix, namespace, podname string) string {
	h := sha1.New()
	h.Write([]byte(fmt.Sprintf("%s.%s", namespace, podname)))
	return fmt.Sprintf("%s%s", prefix, hex.EncodeToString(h.Sum(nil))[:11])
}

func del(args *skel.CmdArgs, driverClient driver.NetworkAPIs, grpcW rpcwrapper.GRPC, rpcW rpcwrapper.RPC) error {

	klog.V(1).Infof("Received CNI del request: ContainerID(%s) Netns(%s) IfName(%s) Args(%s) Path(%s) argsStdinData(%s)",
		args.ContainerID, args.Netns, args.IfName, args.Args, args.Path, args.StdinData)

	conf := NetConf{}
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		klog.Errorf("Failed to load netconf from args %v", err)
		return errors.Wrap(err, "del cmd: failed to load netconf from args")
	}

	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		klog.Errorf("Failed to load k8s config from args: %v", err)
		return errors.Wrap(err, "del cmd: failed to load k8s config from args")
	}

	// notify local IP address manager to free secondary IP
	// Set up a connection to the server.
	conn, err := grpcW.Dial(ipamDAddress, grpc.WithInsecure())
	if err != nil {
		klog.Errorf("Failed to connect to backend server for pod %s namespace %s container %s: %v",
			string(k8sArgs.K8S_POD_NAME),
			string(k8sArgs.K8S_POD_NAMESPACE),
			string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
			err)

		return errors.Wrap(err, "del cmd: failed to connect to backend server")
	}
	defer conn.Close()

	c := rpcW.NewCNIBackendClient(conn)
	request := &rpc.DelNetworkRequest{
		K8S_POD_NAME:               string(k8sArgs.K8S_POD_NAME),
		K8S_POD_NAMESPACE:          string(k8sArgs.K8S_POD_NAMESPACE),
		K8S_POD_INFRA_CONTAINER_ID: string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID),
		Reason:                     "PodDeleted",
	}
	r, err := c.DelNetwork(context.Background(), request)
	if err != nil {
		klog.Errorf("Error received from DelNetwork grpc call for pod %s namespace %s container %s: %v",
			string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID), err)
		return err
	}

	if !r.Success {
		klog.Errorf("Failed to process delete request for pod %s namespace %s container %s: %v",
			string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID), err)
		return errors.Wrap(err, "del cmd: failed to process delete request")
	}
	if r.IPv4Addr == "" {
		klog.Warningf("Try to delete a pod %s namespace %swith noip", string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE))
		return nil
	}
	addr := &net.IPNet{
		IP:   net.ParseIP(r.IPv4Addr),
		Mask: net.IPv4Mask(255, 255, 255, 255),
	}

	err = driverClient.TeardownNS(addr, int(r.DeviceNumber))

	if err != nil {
		klog.Errorf("Failed on TeardownPodNetwork for pod %s namespace %s container %s: %v",
			string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID), err)
		return err
	}
	return nil
}

func cmdDel(args *skel.CmdArgs) error {
	return del(args, driver.New(), rpcwrapper.NewGRPC(), rpcwrapper.New())
}
