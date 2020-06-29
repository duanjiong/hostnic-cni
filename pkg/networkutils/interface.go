package networkutils

import (
	"fmt"
	"github.com/vishvananda/netlink"
	"github.com/yunify/hostnic-cni/pkg/types"
	"net"
)

type NetworkUtilsWrap interface {
	CleanupNicNetwork(nic *types.HostNic) error
	SetupNicNetwork(nic *types.HostNic) error
	LinkByMacAddr(macAddr string) (netlink.Link, error)
	LinkSetName(link netlink.Link, name string) error
	GetRouteTableNum(nic *types.HostNic) int
	LinkByName(name string) ([]string, error)
}

type NetworkUtilsFake struct {
	Links  map[string]netlink.Link
	Rules  map[string]netlink.Rule
	Routes map[int][]netlink.Route
}

type LinkFake struct {
	Name string
}

func (link LinkFake) LinkByName(name string) ([]string, error) {
	return nil, nil
}

func (link LinkFake) Attrs() *netlink.LinkAttrs {
	return &netlink.LinkAttrs{
		Name: link.Name,
	}
}

func (link LinkFake) Type() string {
	return ""
}

func NewNetworkUtilsFake() NetworkUtilsFake {
	return NetworkUtilsFake{
		Links:  make(map[string]netlink.Link),
		Rules:  make(map[string]netlink.Rule),
		Routes: make(map[int][]netlink.Route),
	}
}

func (n NetworkUtilsFake) GetRouteTableNum(nic *types.HostNic) int {
	return 0
}


func (n NetworkUtilsFake) LinkByMacAddr(macAddr string) (netlink.Link, error) {
	link, ok := n.Links[macAddr]
	if !ok {
		return nil, fmt.Errorf("cannot find link by link")
	}

	return link, nil
}

func (n NetworkUtilsFake) CleanupNicNetwork(nic *types.HostNic) error {
	delete(n.Rules, nic.Address)
	return nil
}

func (n NetworkUtilsFake) LinkSetName(link netlink.Link, name string) error {
	link.Attrs().Name = name
	return nil
}

func (n NetworkUtilsFake) SetupNicNetwork(nic *types.HostNic) error {
	link, ok := n.Links[nic.ID]
	if !ok {
		return fmt.Errorf("cannot found link")
	}

	routes := []netlink.Route{
		{
			LinkIndex: link.Attrs().Index,
			Dst:       nic.VxNet.Network,
			Scope:     netlink.SCOPE_LINK,
			Table:     nic.RouteTableNum,
		},
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
	n.Routes[nic.RouteTableNum] = routes

	rule := netlink.NewRule()
	rule.Table = nic.RouteTableNum
	rule.Src = &net.IPNet{
		IP:   net.ParseIP(nic.Address),
		Mask: net.CIDRMask(32, 32),
	}
	rule.Priority = fromContainerRulePriority

	n.Rules[nic.Address] = *rule

	return nil
}
