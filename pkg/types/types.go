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

package types

import (
	"fmt"
	"github.com/containernetworking/cni/pkg/types"
	"net"
	"time"
)

const (
	DefaultSocketPath     = "/var/run/hostnic/hostnic.socket"
	DefaultUnixSocketPath = "unix://" + DefaultSocketPath
	DefaultConfigPath     = "/etc/hostnic"
	DefaultPoolSyn        = 3
	DefaultIAASApiTimeout = 30 *time.Second
	DefaultDeleteNicDelay = 10 * time.Second
	DefaultNicCool        = 60
	DefaultConfigName     = "hostnic"
	DefaultPoolSize       = 3
	DefaultMaxPoolSize    = 5
	DefaultRetryNum       = 3
	NicNumLimit           = 63
	DefaultRouteTableBase = 260
	NicPrefix             = "hostnic_"
)

func GetHostNicName(routeTableNum int) string {
	if routeTableNum <= 0 {
		panic("invalid RouteTableNum")
	}
	return fmt.Sprintf("%s%d", NicPrefix, routeTableNum)
}

type HostNic struct {
	ID           string `json:"id"`
	VxNet        *VxNet `json:"vxNet"`
	HardwareAddr string `json:"hardwareAddr"`
	Address      string `json:"address"`

	RouteTableNum int //setup when setup network

	IsPrimary bool `json:"IsPrimary"` //only use when setup
	Using     bool `json:"using"`     //only use when setup
	Reserved  bool //used when nic was created by user
}

type VxNet struct {
	ID string `json:"id"`
	//GateWay eg: 192.168.1.1
	GateWay string `json:"gateWay"`
	//Network eg: 192.168.1.0/24
	Network *net.IPNet `json:"network"`
	//RouterId
	RouterID string `json:"router_id"`
}

type HostInstance struct {
	ID        string `json:"id"`
	RouterID  string `json:"router_id"`
	ClusterID string `json:"cluster_id"`
}

type VPC struct {
	Network *net.IPNet
	ID      string
}

type ResourceType string

const (
	ResourceTypeInstance ResourceType = "instance"
	ResourceTypeVxnet    ResourceType = "vxnet"
	ResourceTypeNic      ResourceType = "nic"
	ResourceTypeVPC      ResourceType = "vpc"
)

type NetConf struct {
	CNIVersion   string          `json:"cniVersion,omitempty"`
	Name         string          `json:"name,omitempty"`
	Type         string          `json:"type,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
	IPAM         struct {
		Name string
		Type string `json:"type"`
	} `json:"ipam,omitempty"`
	MTU            int    `json:"mtu"`
	HostVethPrefix string `json:"veth_prefix"`
	HostNicType    string `json:"hostnic_type"`
	Service        string `json:"service_cidr"`
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
