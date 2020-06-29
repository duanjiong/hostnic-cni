package networkutils

import (
	"fmt"
	"github.com/pkg/errors"
	linkerrors "github.com/yunify/hostnic-cni/pkg/errors"
	"github.com/vishvananda/netlink"
	"github.com/yunify/hostnic-cni/pkg/types"
	"golang.org/x/sys/unix"
	"net"
	"strings"
)

const (
	// 1024 is reserved for (IP rule not to <VPC's subnet> table main)
	fromContainerRulePriority = 1536
)

type NetworkUtils struct {
}

func (n NetworkUtils) CleanupNicNetwork(nic *types.HostNic) error {
	rule := netlink.NewRule()
	rule.Table = nic.RouteTableNum
	rule.Src = &net.IPNet{
		IP:   net.ParseIP(nic.Address),
		Mask: net.CIDRMask(32, 32)}
	err := netlink.RuleDel(rule)
	if !isNotExistsError(err) {
		return err
	}

	return nil
}

func (n NetworkUtils) GetRouteTableNum(nic *types.HostNic) int {
	rules, _ := getRuleListBySrc(net.IPNet{
		IP:   net.ParseIP(nic.Address),
		Mask: net.CIDRMask(32, 32),
	})
	if len(rules) != 1 {
		return 0
	}
	return rules[0].Table
}

//Note: setup NetworkManager to disable dhcp on nic
// SetupNicNetwork adds default route to route table (nic-<nic_table>)
func (n NetworkUtils) SetupNicNetwork(nic *types.HostNic) error {
	link, err := netlink.LinkByName(types.GetHostNicName(nic.RouteTableNum))
	if err != nil {
		//maybe passthrough
		return nil
	}

	// Save host device name into the container device's alias property
	if err := netlink.LinkSetAlias(link, link.Attrs().Name); err != nil {
		return fmt.Errorf("failed to set alias to %q: %v", link.Attrs().Name, err)
	}

	err = netlink.LinkSetUp(link)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("set nic %s up", nic.ID))
	}

	addrs, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("list address on nic %s", nic.ID))
	}
	for _, addr := range addrs {
		err = netlink.AddrDel(link, &addr)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("cannot delete address on nic %s", nic.ID))
		}
	}

	routes := []netlink.Route{
		// Add a direct link route for the host's NIC IP only
		{
			LinkIndex: link.Attrs().Index,
			Dst:       nic.VxNet.Network,
			Scope:     netlink.SCOPE_LINK,
			Table:     nic.RouteTableNum,
		},
		//TODO: find out which kernel bug will cause the problem below.
		//In centos 7.5 (kernel 3.10.0-862),  the following route will fail to add.
		// Route all other traffic via the host's NIC IP
		{
			LinkIndex: link.Attrs().Index,
			Dst: &net.IPNet{
				IP:   net.IPv4zero,
				Mask: net.CIDRMask(0, 32),
			},
			Scope: netlink.SCOPE_UNIVERSE,
			Gw:    net.ParseIP(nic.VxNet.GateWay),
			Table: nic.RouteTableNum,
		},
	}
	for _, r := range routes {
		err = netlink.RouteAdd(&r)
		if err != nil && !isRouteExistsError(err) {
			return errors.Wrap(err, fmt.Sprintf("cannot addr route on nic %s", nic.ID))
		}
	}

	rule := netlink.NewRule()
	rule.Table = nic.RouteTableNum
	rule.Src = &net.IPNet{
		IP:   net.ParseIP(nic.Address),
		Mask: net.CIDRMask(32, 32),
	}
	rule.Priority = fromContainerRulePriority

	rules, err := getRuleListBySrc(*rule.Src)
	for _, tmp := range rules {
		if tmp.Table != nic.RouteTableNum {
			netlink.RuleDel(&tmp)
		}
	}

	err = netlink.RuleAdd(rule)
	if err != nil && !isRuleExistsError(err) {
		return errors.Wrap(err, fmt.Sprintf("cannot addr rule on nic %s", nic.ID))
	}

	return nil
}

func (n NetworkUtils) LinkSetName(link netlink.Link, name string) error {
	return netlink.LinkSetName(link, name)
}

func (n NetworkUtils) LinkByMacAddr(macAddr string) (netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		attr := link.Attrs()
		if attr.HardwareAddr.String() == macAddr {
			return link, nil
		}
	}
	return nil,  linkerrors.ErrNotFound
}

func (n NetworkUtils) LinkByName(name string) ([]string, error) {
	var result []string

	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}

	for _, link := range links {
		attr := link.Attrs()
		if strings.Contains(attr.Name, name) {
			result = append(result, attr.HardwareAddr.String())
		}
	}

	return result, nil
}