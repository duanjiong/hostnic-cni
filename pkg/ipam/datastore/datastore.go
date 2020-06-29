package datastore

import (
	"container/heap"
	"fmt"
	"github.com/davecgh/go-spew/spew"
	k8sapi "github.com/yunify/hostnic-cni/pkg/k8sclient"
	"github.com/yunify/hostnic-cni/pkg/types"
	"sync"
	"time"
	"github.com/yunify/hostnic-cni/pkg/errors"
)

// NICIPPool contains NIC/IP Pool information. Exported fields will be marshaled for introspection.
type NICIPPool struct {
	Nic *types.HostNic
	// IPv4Addresses shows whether each address is assigned, the key is IP address, which must
	// be in dot-decimal notation with no leading zeros and no whitespace(eg: "10.1.0.253")
	IPv4Addresses map[string]*AddressInfo
	Assigned      int
}

// AddressInfo contains information about an IP, Exported fields will be marshaled for introspection.
type AddressInfo struct {
	index          int //for priority queue
	Address        string
	Pod            *PodKey
	UnassignedTime time.Time
	NIC            *NICIPPool
	reserved       AddressReservedType
}

// PodKey is used to locate pod IP
type PodKey struct {
	name      string
	namespace string
	container string
}

type DataStoreConf struct {
	PoolHigh int
	PoolLow  int
	//How long to wait for a network card to be recycled
	//to avoid repeated creation and deletion,the default time is 60 seconds
	Cool int
}

// DataStore contains vxnet level NIC/IP
type DataStore struct {
	nicIPPools map[string]*NICIPPool   //total
	podsIP     map[PodKey]*AddressInfo //using
	queue      PriorityQueue           //idle
	vxnet      *types.VxNet
	conf       *DataStoreConf
	lock       sync.RWMutex
}

// NewDataStore returns DataStore structure
func NewDataStore(vxnet *types.VxNet, conf *DataStoreConf) *DataStore {
	ds := &DataStore{
		nicIPPools: make(map[string]*NICIPPool),
		podsIP:     make(map[PodKey]*AddressInfo),
		queue:      make(PriorityQueue, 0),
		vxnet:      vxnet,
		conf:       conf,
	}

	heap.Init(&ds.queue)
	return ds
}

// AddNIC add NIC to data store
func (ds *DataStore) AddNIC(nic *types.HostNic) {
	if nic.RouteTableNum <= 0 || nic.VxNet.ID != ds.vxnet.ID {
		panic(fmt.Sprintf("AddNic: invalid Nic %s", spew.Sdump(nic)))
	}

	ds.lock.Lock()
	defer ds.lock.Unlock()

	_, ok := ds.nicIPPools[nic.ID]
	if ok {
		panic(fmt.Errorf("AddNic: duplicate Nic %s", spew.Sdump(nic)))
	}
	ds.nicIPPools[nic.ID] = &NICIPPool{
		Nic:           nic,
		IPv4Addresses: make(map[string]*AddressInfo),
		Assigned:      0,
	}
}

type AddressReservedType int

const (
	ReservedNone    AddressReservedType = iota
	ReservedOnce    AddressReservedType = 1
	ReservedForever AddressReservedType = 2
)

// AddIPv4Address add an IP of an NIC to data store
func (ds *DataStore) AddIPv4Address(nicID string, ipv4 string, reserved AddressReservedType) {
	if ipv4 == "" {
		panic(fmt.Sprintf("AddIpv4Address: invalid address %s", ipv4))
	}

	ds.lock.Lock()
	defer ds.lock.Unlock()

	curNIC, ok := ds.nicIPPools[nicID]
	if !ok {
		panic(fmt.Sprintf("AddIPv4Address: unknown NIC %s", nicID))
	}

	addr, ok := curNIC.IPv4Addresses[ipv4]
	if ok {
		panic(fmt.Sprintf("AddIPv4Address: dumplicate address %s", spew.Sdump(addr)))
	}

	addrInfo := &AddressInfo{
		Address:        ipv4,
		NIC:            curNIC,
		UnassignedTime: time.Now(),
		reserved:       reserved,
	}
	curNIC.IPv4Addresses[ipv4] = addrInfo

	if reserved == ReservedNone {
		heap.Push(&ds.queue, addrInfo)
	}
}

func (ds *DataStore) ConfigureDataStore(conf *DataStoreConf) {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	ds.conf = conf
}

// AssignPodIPv4Address assigns an IPv4 address to pod
// It returns the assigned IPv4 address, device number, error
func (ds *DataStore) AssignPodIPv4Address(k8sPod *k8sapi.K8SPodInfo, restore bool) (bool, *AddressInfo, error) {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	podKey := PodKey{
		name:      k8sPod.Name,
		namespace: k8sPod.Namespace,
		container: k8sPod.Container,
	}
	ipAddr, ok := ds.podsIP[podKey]
	if ok {
		if k8sPod.IP != "" && ipAddr.Address != k8sPod.IP {
			// The caller invoke multiple times to assign(PodName/NameSpace --> same IPAddress). It is not a error, but not very efficient.
			return false, nil, fmt.Errorf("AssignPodIPv4Address:  pod %s with multiple IP addresses %s", spew.Sdump(k8sPod), spew.Sdump(ipAddr))
		}
		return true, ipAddr, nil
	}

	addr, err := ds.assignPodIPv4AddressUnsafe(k8sPod, restore)

	return false, addr, err
}

func (ds *DataStore) HasPodInfo(k8sPod *k8sapi.K8SPodInfo) bool {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	podKey := PodKey{
		name:      k8sPod.Name,
		namespace: k8sPod.Namespace,
		container: k8sPod.Container,
	}

	_, ok := ds.podsIP[podKey]

	return ok
}

func (ds *DataStore) findAddressInfo(address string) *AddressInfo {
	for _, nic := range ds.nicIPPools {
		for _, addr := range nic.IPv4Addresses {
			if addr.Address == address {
				return addr
			}
		}
	}

	return nil
}

func (ds *DataStore) HaveAddressInfo(address string) bool {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	if ds.findAddressInfo(address) != nil {
		return true
	}

	return false
}

// It returns the assigned IPv4 address, device number, error
func (ds *DataStore) assignPodIPv4AddressUnsafe(k8sPod *k8sapi.K8SPodInfo, restore bool) (*AddressInfo, error) {
	podKey := PodKey{
		name:      k8sPod.Name,
		namespace: k8sPod.Namespace,
		container: k8sPod.Container,
	}

	var addr *AddressInfo

	if restore {
		addr = ds.findAddressInfo(k8sPod.IP)
		if addr == nil {
			return nil, errors.ErrUnknownPodIP
		} else {
			if addr.reserved == ReservedOnce {
				addr.reserved = ReservedNone
			} else if addr.reserved == ReservedNone {
				heap.Remove(&ds.queue, addr.index)
			}
		}
	} else {
		if ds.queue.Len() > 0 {
			addr = heap.Pop(&ds.queue).(*AddressInfo)
		} else {
			return nil, errors.ErrNoAvailableIP
		}
	}

	addr.Pod = &podKey
	addr.NIC.Assigned++
	ds.podsIP[podKey] = addr
	return addr, nil
}

// UnassignPodIPv4Address a) find out the IP address based on PodName and PodNameSpace
// b)  mark IP address as unassigned c) returns IP address, NIC's device number, error
func (ds *DataStore) UnassignPodIPv4Address(k8sPod *k8sapi.K8SPodInfo) (*AddressInfo, error) {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	podKey := PodKey{
		name:      k8sPod.Name,
		namespace: k8sPod.Namespace,
		container: k8sPod.Container,
	}
	ipAddr := ds.podsIP[podKey]
	if ipAddr == nil {
		return nil, errors.ErrIPReleased
	}

	ipAddr.Pod = nil
	ipAddr.NIC.Assigned--
	ipAddr.UnassignedTime = time.Now()
	delete(ds.podsIP, podKey)

	if ipAddr.reserved == ReservedNone {
		heap.Push(&ds.queue, ipAddr)
	} else if ipAddr.reserved == ReservedForever {
		ipAddr.UnassignedTime = time.Now().Add(-time.Duration(ds.conf.Cool) * time.Second)
	}

	return ipAddr, nil
}

func (ds *DataStore) GetNumOfFreeAddr() int {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	return ds.queue.Len()
}

func (ds *DataStore) GetNumOfReservedAddr() int {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	return ds.getNumOfTotalAddr() - len(ds.podsIP) - ds.queue.Len()
}

func (ds *DataStore) getNumOfTotalAddr() int {
	sum := 0
	for _, nic := range ds.nicIPPools {
		sum += len(nic.IPv4Addresses)
	}

	return sum
}

func (ds *DataStore) GetNumOfTotalAddr() int {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	return ds.getNumOfTotalAddr()
}

func (ds *DataStore) nicCanFreed(nic *NICIPPool) bool {
	if nic.Assigned > 0 {
		return false
	}

	free := true
	for _, addr := range nic.IPv4Addresses {
		if time.Now().Before(addr.UnassignedTime.Add(time.Duration(ds.conf.Cool) * time.Second)) {
			free = false
			break
		} else {
			if addr.reserved == ReservedOnce {
				free = false
				addr.reserved = ReservedNone
				heap.Push(&ds.queue, addr)
				addr.UnassignedTime = time.Now()
			}
		}
	}
	return free
}

func nicCached(nic *NICIPPool) bool {
	cached := true
	for _, addr := range nic.IPv4Addresses {
		if addr.reserved != ReservedNone {
			cached = false
		}
	}
	return cached
}

func (ds *DataStore) deleteNic(nic *NICIPPool) {
	for _, addr := range nic.IPv4Addresses {
		if addr.reserved == ReservedNone {
			heap.Remove(&ds.queue, addr.index)
		}
	}
	delete(ds.nicIPPools, nic.Nic.ID)
}

func (ds *DataStore) GetFreeNic() []*types.HostNic {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	result := make([]*types.HostNic, 0)
	avail := ds.conf.PoolHigh
	for _, nic := range ds.nicIPPools {
		if ds.nicCanFreed(nic) {
			if avail > 0 && nicCached(nic) {
				avail--
				continue
			}
			ds.deleteNic(nic)
			result = append(result, nic.Nic)
		}
	}

	return result
}

func (ds *DataStore) GetWantNicNum() int {
	ds.lock.Lock()
	defer ds.lock.Unlock()

	result := ds.conf.PoolLow - ds.queue.Len()
	if result < 0 {
		return 0
	}

	return result
}

func (ds *DataStore) GetVxnet() *types.VxNet {
	return ds.vxnet
}
