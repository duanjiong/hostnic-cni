//
// =========================================================================
// Copyright (C) 2020 by Yunify, Inc...
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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	bv "github.com/containernetworking/plugins/pkg/utils/buildversion"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	types2 "github.com/yunify/hostnic-cni/pkg/types"
	"net"
	"runtime"
	"syscall"
)

func init() {
	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
}

const (
	HostNicPassThrough = "passthrough"
	HostNicVeth        = "veth"
	HostNicPrefix      = "nic"
	defaultIfName      = "veth1"
)

func setupContainerVeth(netns ns.NetNS, hostIfName, ifName string, conf types2.NetConf, pr *current.Result) (*current.Interface, *current.Interface, error) {
	mtu := conf.MTU
	service := conf.Service

	hostInterface := &current.Interface{}
	containerInterface := &current.Interface{}

	err := netns.Do(func(hostNS ns.NetNS) error {
		hostVeth, contVeth, err := ip.SetupVethWithName(ifName, hostIfName, mtu, hostNS)
		if err != nil {
			return err
		}

		hostInterface.Name = hostVeth.Name
		hostInterface.Mac = hostVeth.HardwareAddr.String()
		containerInterface.Name = contVeth.Name
		containerInterface.Mac = contVeth.HardwareAddr.String()
		containerInterface.Sandbox = netns.Path()

		if conf.HostNicType == HostNicVeth {
			for _, ipc := range pr.IPs {
				// All addresses apply to the container veth interface
				ipc.Interface = current.Int(1)
				//ipc.Address.Mask = net.CIDRMask(32, 32)
			}
			pr.Interfaces = []*current.Interface{hostInterface, containerInterface}
			pr.Routes = nil
			if err = ipam.ConfigureIface(ifName, pr); err != nil {
				return err
			}
		}

		ipc := pr.IPs[0]
		ipc.Gateway = net.ParseIP("169.254.1.1")
		err = netlink.NeighAdd(&netlink.Neigh{
			LinkIndex:    contVeth.Index,
			IP:           ipc.Gateway,
			HardwareAddr: hostVeth.HardwareAddr,
			State:        netlink.NUD_PERMANENT,
			Family:       syscall.AF_INET,
		})
		if err != nil {
			return fmt.Errorf("error add permanent arp for container veth")
		}

		var (
			r []netlink.Route
		)

		addrBits := 32
		if ipc.Version == "6" {
			addrBits = 128
		}
		r = append(r, netlink.Route{
			LinkIndex: contVeth.Index,
			Dst: &net.IPNet{
				IP:   ipc.Gateway,
				Mask: net.CIDRMask(addrBits, addrBits),
			},
			Scope: netlink.SCOPE_LINK,
			Src:   ipc.Address.IP,
		})
		if conf.HostNicType == HostNicVeth {
			_, defNet, _ := net.ParseCIDR("0.0.0.0/0")
			r = append(r, netlink.Route{
				LinkIndex: contVeth.Index,
				Dst:       defNet,
				Scope:     netlink.SCOPE_UNIVERSE,
				Gw:        ipc.Gateway,
				Src:       ipc.Address.IP,
			})
		} else {
			_, serviceNet, err := net.ParseCIDR(service)
			if err != nil {
				return fmt.Errorf("service cidr invalid")
			}
			r = append(r, netlink.Route{
				LinkIndex: contVeth.Index,
				Dst:       serviceNet,
				Scope:     netlink.SCOPE_UNIVERSE,
				Gw:        ipc.Gateway,
				Src:       ipc.Address.IP,
			})
		}
		for _, r := range r {
			if err := netlink.RouteAdd(&r); err != nil {
				return fmt.Errorf("failed to add route %v: %v", r, err)
			}
		}

		return nil
	})

	return hostInterface, containerInterface, err
}

func setupHostVeth(vethName string, result *current.Result) error {
	// hostVeth moved namespaces and may have a new ifindex
	hostVeth, err := netlink.LinkByName(vethName)
	if err != nil {
		return fmt.Errorf("failed to lookup %q: %v", vethName, err)
	}

	route := &netlink.Route{
		LinkIndex: hostVeth.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Dst: &net.IPNet{
			IP:   result.IPs[0].Address.IP,
			Mask: net.CIDRMask(32, 32),
		},
	}
	err = netlink.RouteAdd(route)
	if err != nil {
		log.WithFields(log.Fields{
			"LinkIndex": route.LinkIndex,
			"Dst":       route.Dst,
		}).WithError(err).Error("cannot add route")
		return fmt.Errorf("failed to add route to pod, err=%+v", err)
	}

	return nil
}

func cmdAddVeth(conf types2.NetConf, hostIfName, contIfName string, result *current.Result, netns ns.NetNS) error {
	link, err := netlink.LinkByName(hostIfName)
	if link != nil {
		return nil
	}

	hostInterface, _, err := setupContainerVeth(netns, hostIfName, contIfName, conf, result)
	if err != nil {
		return err
	}

	if err = setupHostVeth(hostInterface.Name, result); err != nil {
		return err
	}

	return err
}

func getLink(hwaddr string) (netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list node links: %v", err)
	}

	hwAddr, err := net.ParseMAC(hwaddr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MAC address %q: %v", hwaddr, err)
	}

	for _, link := range links {
		if bytes.Equal(link.Attrs().HardwareAddr, hwAddr) {
			return link, nil
		}
	}

	return nil, fmt.Errorf("failed to find physical interface")
}

func moveLinkIn(hostDev netlink.Link, containerNs ns.NetNS, ifName string, pr *current.Result) (netlink.Link, error) {
	containerInterface := &current.Interface{}

	if err := netlink.LinkSetNsFd(hostDev, int(containerNs.Fd())); err != nil {
		return nil, err
	}

	var contDev netlink.Link
	if err := containerNs.Do(func(_ ns.NetNS) error {
		var err error
		contDev, err = netlink.LinkByName(hostDev.Attrs().Name)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", hostDev.Attrs().Name, err)
		}

		// Save host device name into the container device's alias property
		if err := netlink.LinkSetAlias(contDev, contDev.Attrs().Name); err != nil {
			return fmt.Errorf("failed to set alias to %q: %v", contDev.Attrs().Name, err)
		}
		log.Infof("set nic %s alias to %s", contDev.Attrs().Name, contDev.Attrs().Alias)

		// Rename container device to respect args.IfName
		if err := netlink.LinkSetName(contDev, ifName); err != nil {
			return fmt.Errorf("failed to rename device %q to %q: %v", hostDev.Attrs().Name, ifName, err)
		}
		// Retrieve link again to get up-to-date name and attributes
		contDev, err = netlink.LinkByName(ifName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", ifName, err)
		}

		for _, ipc := range pr.IPs {
			ipc.Interface = current.Int(0)
		}
		containerInterface.Name = contDev.Attrs().Name
		containerInterface.Mac = contDev.Attrs().HardwareAddr.String()
		containerInterface.Sandbox = containerNs.Path()
		pr.Interfaces = []*current.Interface{containerInterface}
		if err = ipam.ConfigureIface(ifName, pr); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return contDev, nil
}

func cmdAddPassThrough(conf types2.NetConf, hostIfName, contIfName string, result *current.Result, netns ns.NetNS) error {
	if len(result.Interfaces) == 0 {
		return errors.New("IPAM plugin returned missing Interface config")
	}

	if conf.Service == "" {
		return fmt.Errorf("Netconf should config service_cidr")
	}

	if result.Interfaces[0].Mac == "" {
		return nil
	}
	hostDev, err := getLink(result.Interfaces[0].Mac)
	if err != nil {
		return fmt.Errorf("failed to find host device: %v", err)
	}

	_, err = moveLinkIn(hostDev, netns, contIfName, result)
	if err != nil {
		return fmt.Errorf("failed to move link %v", err)
	}

	hostInterface, _, err := setupContainerVeth(netns, hostIfName, defaultIfName, conf, result)
	if err = setupHostVeth(hostInterface.Name, result); err != nil {
		return err
	}

	return err
}

func cmdAdd(args *skel.CmdArgs) error {
	conf := types2.NetConf{}
	if err := json.Unmarshal(args.StdinData, &conf); err != nil {
		return fmt.Errorf("failed to load netconf: %v", err)
	}
	if conf.HostVethPrefix == "" {
		conf.HostVethPrefix = HostNicPrefix
	}

	// run the IPAM plugin and get back the config to apply
	r, err := ipam.ExecAdd(conf.IPAM.Type, args.StdinData)
	if err != nil {
		return err
	}

	// Invoke ipam del if err to avoid ip leak
	defer func() {
		if err != nil {
			ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		}
	}()

	// Convert whatever the IPAM result was into the current Result type
	result, err := current.NewResultFromResult(r)
	if err != nil {
		return err
	}

	if len(result.IPs) == 0 {
		return errors.New("IPAM plugin returned missing IP config")
	}

	if err := ip.EnableForward(result.IPs); err != nil {
		return fmt.Errorf("Could not enable IP forwarding: %v", err)
	}

	netns, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("failed to open netns %q: %v", args.Netns, err)
	}
	defer netns.Close()

	hostIfName := fmt.Sprintf("%s%s", conf.HostVethPrefix, args.ContainerID[:11])
	contIfName := args.IfName

	log.WithFields(log.Fields{
		"hostIfName": hostIfName,
		"contIfName": contIfName,
	}).Info("setup veth pair")
	switch conf.HostNicType {
	case HostNicVeth:
		err = cmdAddVeth(conf, hostIfName, contIfName, result, netns)

	case HostNicPassThrough:
		err = cmdAddPassThrough(conf, hostIfName, contIfName, result, netns)

	default:
		err = fmt.Errorf("not support hostnic_type %s", conf.HostNicType)
	}

	if err != nil {
		return err
	} else {
		return types.PrintResult(result, conf.CNIVersion)
	}
}

func cmdDelVeth(contIfName string, netns ns.NetNS) error {
	log.Infof("delete container veth %s", contIfName)

	// There is a netns so try to clean up. Delete can be called multiple times
	// so don't return an error if the device is already removed.
	// If the device isn't there then don't try to clean up IP masq either.
	err := netns.Do(func(_ ns.NetNS) error {
		var err error
		_, err = ip.DelLinkByNameAddr(contIfName)
		if err != nil && err == ip.ErrLinkNotFound {
			return nil
		}
		return err
	})

	if err != nil {
		return err
	}

	return err
}

func moveLinkOut(containerNs ns.NetNS, ifName string) error {
	defaultNs, err := ns.GetCurrentNS()
	if err != nil {
		return err
	}
	defer defaultNs.Close()

	return containerNs.Do(func(_ ns.NetNS) error {
		dev, err := netlink.LinkByName(ifName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", ifName, err)
		}

		// Devices can be renamed only when down
		if err = netlink.LinkSetDown(dev); err != nil {
			return fmt.Errorf("failed to set %q down: %v", ifName, err)
		}

		// Rename device to it's original name
		if err = netlink.LinkSetName(dev, dev.Attrs().Alias); err != nil {
			return fmt.Errorf("failed to restore %q to original name %q: %v", ifName, dev.Attrs().Alias, err)
		}
		defer func() {
			if err != nil {
				// if moving device to host namespace fails, we should revert device name
				// to ifName to make sure that device can be found in retries
				_ = netlink.LinkSetName(dev, ifName)
			}
		}()

		if err = netlink.LinkSetNsFd(dev, int(defaultNs.Fd())); err != nil {
			return fmt.Errorf("failed to move %q to host netns: %v", dev.Attrs().Alias, err)
		}
		return nil
	})
}

func cmdDelPassThrough(hostIfName, contIfName string, netns ns.NetNS) error {
	err := moveLinkOut(netns, contIfName)
	if err != nil {
		return err
	}

	err = ip.DelLinkByName(hostIfName)
	if err != nil && err == ip.ErrLinkNotFound {
		return nil
	}

	return err
}

func cmdDel(args *skel.CmdArgs) error {
	conf := types2.NetConf{}
	err := json.Unmarshal(args.StdinData, &conf)
	if err != nil {
		return fmt.Errorf("failed to load netconf: %v", err)
	}

	if err := ipam.ExecDel(conf.IPAM.Type, args.StdinData); err != nil {
		return err
	}

	if args.Netns == "" {
		return nil
	}
	netns, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("failed to open netns %q: %v", args.Netns, err)
	}
	defer netns.Close()

	contIfName := args.IfName
	hostIfName := fmt.Sprintf("%s%s", conf.HostVethPrefix, args.ContainerID[:11])
	switch conf.HostNicType {
	case HostNicVeth:
		err = cmdDelVeth(contIfName, netns)

	case HostNicPassThrough:
		err = cmdDelPassThrough(hostIfName, contIfName, netns)

	default:
		err = fmt.Errorf("not support hostnic_type %s", conf.HostNicType)
	}

	return err
}

func main() {
	skel.PluginMain(cmdAdd, nil, cmdDel, version.All, bv.BuildString("hostnic"))
}
