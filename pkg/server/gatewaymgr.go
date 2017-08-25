package server

import (
	"net"

	"github.com/orcaman/concurrent-map"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/yunify/hostnic-cni/pkg"
	"github.com/yunify/hostnic-cni/pkg/provider/qingcloud"
)

type GatewayManager struct {
	gatewayMgr   cmap.ConcurrentMap
	resourceStub *qingcloud.QCNicProvider
}

func NewGatewayManager(qcstub *qingcloud.QCNicProvider) *GatewayManager {
	return &GatewayManager{gatewayMgr: cmap.New(), resourceStub: qcstub}
}

func (pool *GatewayManager) CollectGatewayNic() ([]*pkg.HostNic, error) {
	log.Infof("Collect existing nic as gateway cadidate")
	var nicidList []*string
	if linklist, err := netlink.LinkList(); err != nil {
		return nil, err
	} else {
		for _, link := range linklist {
			if link.Attrs().Flags&net.FlagLoopback == 0 {
				nicid := link.Attrs().HardwareAddr.String()
				nicidList = append(nicidList, pkg.StringPtr(nicid))
				log.Debugf("Found nic %s on host", nicid)
			}
		}
	}
	niclist, err := pool.resourceStub.GetNics(nicidList)
	if err != nil {
		return nil, err
	}
	var unusedList []*pkg.HostNic
	for _, nic := range niclist {
		niclink, err := pkg.LinkByMacAddr(nic.ID)
		if err != nil {
			return nil, err
		}
		if niclink.Attrs().Flags&net.FlagUp != 0 {
			if ok := pool.gatewayMgr.SetIfAbsent(nic.VxNet.ID, nic.Address); ok {
				continue
			} else {
				netlink.LinkSetDown(niclink)
			}
		}
		log.Debugf("nic %s is unused,status is up: %t", nic.ID, niclink.Attrs().Flags&netlink.OperUp != 0)
		unusedList = append(unusedList, nic)
	}
	log.Infof("Found following nic as gateway")
	for key, value := range pool.gatewayMgr.Items() {
		log.Infof("vxnet: %s gateway: %s", key, value.(string))
	}
	return unusedList, nil
}

func (pool *GatewayManager) GetOrAllocateGateway(vxnetid string) (string, error) {
	var gatewayIp string
	if item, ok := pool.gatewayMgr.Get(vxnetid); !ok {

		//allocate nic

		nic, err := pool.resourceStub.CreateNicInVxnet(vxnetid)
		if err != nil {
			return "", err
		}
		niclink, err := pkg.LinkByMacAddr(nic.HardwareAddr)
		if err != nil {
			return "", err
		}
		err = netlink.LinkSetUp(niclink)
		if err != nil {
			return "", err
		}
		pool.gatewayMgr.Set(nic.VxNet.ID, nic.Address)

		return nic.Address, nil
	} else {
		gatewayIp = item.(string)
		return gatewayIp, nil
	}
}