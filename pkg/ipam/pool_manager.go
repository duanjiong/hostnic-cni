package ipam

import (
	"fmt"
	"github.com/davecgh/go-spew/spew"
	log "github.com/sirupsen/logrus"
	"github.com/yunify/hostnic-cni/pkg/db"
	"github.com/yunify/hostnic-cni/pkg/errors"
	"github.com/yunify/hostnic-cni/pkg/ipam/datastore"
	k8sapi "github.com/yunify/hostnic-cni/pkg/k8sclient"
	"github.com/yunify/hostnic-cni/pkg/networkutils"
	"github.com/yunify/hostnic-cni/pkg/qcclient"
	"github.com/yunify/hostnic-cni/pkg/types"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nodeInfo struct {
	InstanceID string
	primaryNic *types.HostNic
}

type poolStats struct {
	cacheMiss        int
	pickupPendingNic int
}

type poolNotify struct {
	result chan IpamRequestResult
	nic    *types.HostNic
	//accessed by udevnotify handler and period sync deleting job
	//time will be updated when iaas api operations are required.
	expireTimeStamp  time.Time
	removed          bool //routeTables for delete
	failedToSetupNic bool
}

type IpamRequestResult struct {
	Err error
	Nic *types.HostNic
}

type IpamRequest struct {
	Get    bool
	Vxnet  *types.VxNet
	NicID  string
	Result chan IpamRequestResult
}

type poolManager struct {
	nodeInfo
	UdevCh chan UdevNotify
	udev   UdevAPI
	IpamCh chan *IpamRequest

	qcClient qcclient.QingCloudAPI
	conf     PoolConf

	//Only used when setting up a network card to get a routing table number.
	//The default routing table number starts at 260 and supports 63
	routeTables [types.NicNumLimit]*types.HostNic
	//Store network cards that need to be uninstalled or deleted
	pendingNic map[string]*poolNotify
	//Storage of a network card that is waiting to be mounted,
	//this does not delete the network card.
	deletingNic map[string]*poolNotify
	dataStores  map[string]*datastore.DataStore

	stats poolStats

	netlink networkutils.NetworkUtilsWrap

	lock sync.Mutex
}

func (pool *poolManager) syncNics() error {
	var (
		nics      []*types.HostNic
		err       error
		toRestore []*types.HostNic
		toDelete  []string
	)

	offset := 0
	for ; ; {
		nics, err = pool.qcClient.GetCreatedNics(pool.conf.MaxNic, offset)
		if err != nil {
			return fmt.Errorf("failed to get hostnic created nics, err=%s", err)
		}
		log.Debugf("syncNics: get %d nics offset=%d, result=%v", pool.conf.MaxNic, offset, spew.Sdump(nics))

		for _, nic := range nics {
			if nic.Using {
				toRestore = append(toRestore, nic)
			} else {
				toDelete = append(toDelete, nic.ID)
			}
		}

		if len(nics) < pool.conf.MaxNic {
			break
		}
	}

	if len(toDelete) > 0 {
		log.Infof("try to delete nics which is not bound, nics=%s", spew.Sdump(toDelete))
		if err := pool.qcClient.DeleteNics(toDelete); err != nil {
			return fmt.Errorf("failed to delete nic which is not bound, err=%s", err)
		}
	}

	for _, nic := range toRestore {
		log.Debugf("try to restore nics which is bound, nics=%s", spew.Sdump(toRestore))
		err = pool.setupNic(nic, datastore.ReservedNone)
		if err != nil {
			return fmt.Errorf("failed to setup nic while restore, nic=%s, err=%s", spew.Sdump(nic), err)
		}
	}

	//
	boundNics, err := pool.netlink.LinkByName(types.NicPrefix)
	if err != nil {
		panic(fmt.Sprintf("failed to get all hostnic by name, err=%s", err))
	}
	for _, boundNic := range boundNics {
		found := false
		for _, nic := range toRestore {
			if boundNic == nic.ID {
				found = true
			}
		}
		if !found {
			info, err := db.FindPodByNic(boundNic)
			if err != nil {
				return fmt.Errorf("failed to find db info by mac %s, err=%s", boundNic, err)
			}
			if info == nil {
				err = pool.qcClient.DeattachNics([]string{boundNic}, true)
				if err != nil {
					panic(fmt.Sprintf("deattach nics error while setup, err=%s", err))
				}
			} else {
				err = pool.restoreNic(nil, boundNic)
				if err != nil {

				}
			}
		}
	}

	return nil
}

func (pool *poolManager) SetDataStoreConf(ds *datastore.DataStore) {
	log.Infof("set vxnet %s nic cache to pool default config", ds.GetVxnet().ID)
	ds.ConfigureDataStore(&datastore.DataStoreConf{
		PoolHigh: pool.conf.PoolHigh,
		PoolLow:  pool.conf.PoolLow,
		Cool:     pool.conf.Cool,
	})
}

func (pool *poolManager) setupDataStore() {
	if len(pool.conf.Vxnet) != 0 {
		for _, vxnetConf := range pool.conf.Vxnet {
			ds, err := pool.GetDataStore(vxnetConf.Vxnet)
			if err != nil {
				panic(fmt.Sprintf("failed to get vxnet %s, %v", vxnetConf.Vxnet, err))
			}
			ds.ConfigureDataStore(&datastore.DataStoreConf{
				PoolHigh: vxnetConf.PoolHigh,
				PoolLow:  vxnetConf.PoolLow,
				Cool:     pool.conf.Cool,
			})
		}
	} else {
		vxnet := pool.primaryNic.VxNet
		ds, err := pool.GetDataStore(vxnet.ID)
		if err != nil {
			panic(fmt.Sprintf("failed to get vxnet datastore %s, %v", vxnet.ID, err))
		}
		ds.ConfigureDataStore(&datastore.DataStoreConf{
			PoolHigh: 0,
			PoolLow:  0,
			Cool:     pool.conf.Cool,
		})
	}
}

func (pool *poolManager) Start(stopCh <-chan struct{}) {
	log.Info("pool manager setup datestores")
	pool.setupDataStore()
	pool.syncNics()
	go pool.start(stopCh)
}

func NewPoolManager(client qcclient.QingCloudAPI, conf PoolConf, udev UdevAPI, netlink networkutils.NetworkUtilsWrap) *poolManager {
	pool := &poolManager{
		dataStores:  make(map[string]*datastore.DataStore),
		pendingNic:  make(map[string]*poolNotify),
		deletingNic: make(map[string]*poolNotify),
		qcClient:    client,
		conf:        conf,
		UdevCh:      make(chan UdevNotify),
		IpamCh:      make(chan *IpamRequest),
		netlink:     netlink,
		udev:        udev,
	}

	//set up node info
	var err error
	pool.InstanceID = pool.qcClient.GetInstanceID()
	pool.primaryNic, err = pool.qcClient.GetPrimaryNIC()
	if err != nil {
		panic(err)
	}

	return pool
}

func (pool *poolManager) GetDefaultDataStores() []*datastore.DataStore {
	pool.lock.Lock()
	defer pool.lock.Unlock()

	var result []*datastore.DataStore

	for _, vxnet := range pool.conf.Vxnet {
		result = append(result, pool.dataStores[vxnet.Vxnet])
	}

	if len(result) <= 0 {
		result = append(result, pool.dataStores[pool.primaryNic.VxNet.ID])
	}

	return result
}

func (pool *poolManager) GetDataStoreByPodInfo(podInfo *k8sapi.K8SPodInfo) *datastore.DataStore {
	pool.lock.Lock()
	defer pool.lock.Unlock()

	for _, ds := range pool.dataStores {
		if ds.HasPodInfo(podInfo) {
			return ds
		}
	}

	return nil
}

func (pool *poolManager) GetDataStoreForNic(nic string) (*datastore.DataStore, error) {
	nics, err := pool.qcClient.GetNics(nil, []string{nic})
	if err != nil {
		return nil, err
	}
	return pool.GetDataStore(nics[0].VxNet.ID)
}

// If not present, we will create it
func (pool *poolManager) GetDataStore(netID string) (*datastore.DataStore, error) {
	pool.lock.Lock()
	defer pool.lock.Unlock()

	result, ok := pool.dataStores[netID]
	if ok {
		return result, nil
	}

	vxnet, err := pool.qcClient.GetVxNets([]string{netID})
	if err != nil {
		return nil, err
	}
	result = datastore.NewDataStore(vxnet[netID], &datastore.DataStoreConf{
		PoolHigh: 0,
		PoolLow:  0,
		Cool:     pool.conf.Cool,
	})

	pool.dataStores[netID] = result
	return result, nil
}

func (pool *poolManager) registerPendingNic(notify *poolNotify) {
	pool.pendingNic[notify.nic.ID] = notify
	notify.expireTimeStamp = time.Now().Add(types.DefaultIAASApiTimeout)
}

func (pool *poolManager) deletePendingNic(nicID string) {
	delete(pool.pendingNic, nicID)
}

func (pool *poolManager) getPendingNic(nicID string) *poolNotify {
	if result, ok := pool.pendingNic[nicID]; ok {
		return result
	} else {
		return nil
	}
}

func (pool *poolManager) pickupPendingNic(vxnet *types.VxNet) *poolNotify {
	var result *poolNotify

	for _, nic := range pool.pendingNic {
		if nic.result == nil && nic.nic.VxNet.ID == vxnet.ID {
			result = nic
		}
	}

	return result
}

func (pool *poolManager) getPendingNicLen(vxnet string) int {
	if vxnet == "" {
		return len(pool.pendingNic)
	}
	sum := 0
	for _, nic := range pool.pendingNic {
		if nic.nic.VxNet.ID == vxnet {
			sum++
		}
	}

	return sum
}

func (pool *poolManager) getPendingNicCachedLen(vxnet string) int {
	sum := 0
	for _, nic := range pool.pendingNic {
		if nic.nic.VxNet.ID == vxnet && nic.result == nil {
			sum++
		}
	}

	return sum
}

//no need lock, we can tolerate data competition.
func (pool *poolManager) syncPendingNic() {
	for nicID, nicNotify := range pool.pendingNic {
		log.WithFields(log.Fields{
			"ID":               nicID,
			"failedToSetupNic": nicNotify.failedToSetupNic,
			"timestamp":        nicNotify.expireTimeStamp.String(),
		}).Infof("sync pending nic")

		if nicNotify.failedToSetupNic {
			pool.handleNicAdd(nicNotify)
		}

		if !time.Now().Before(nicNotify.expireTimeStamp) {
			log.WithField("nic", nicID).Warning("maybe you need to mount the card manually. ")
		}
	}
}

func getReservedType(nicNotify *poolNotify) datastore.AddressReservedType {
	reserve := datastore.ReservedNone
	if nicNotify.result != nil {
		if nicNotify.nic.Reserved {
			reserve = datastore.ReservedForever
		} else {
			reserve = datastore.ReservedOnce
		}
	}

	return reserve
}

func (pool *poolManager) handleNicAdd(nicNotify *poolNotify) {
	err := pool.setupNic(nicNotify.nic, getReservedType(nicNotify))
	if err != nil {
		logger := log.WithError(err).WithField("nicID", nicNotify.nic.ID)
		logger.Errorf("Failed to setup nicNotify in host")
		nicNotify.failedToSetupNic = true
		return
	}

	pool.deletePendingNic(nicNotify.nic.ID)
	if nicNotify.result != nil {
		nicNotify.result <- IpamRequestResult{
			Err: nil,
			Nic: nicNotify.nic,
		}
	}
}

func (pool *poolManager) handleNicAddorDel(udevNic UdevNotify) {
	log.Debugf("handle UdevNotify %+v", udevNic)

	if udevNic.Action == "add" {
		if nicNotify := pool.getPendingNic(udevNic.MAC); nicNotify != nil {
			pool.handleNicAdd(nicNotify)
		}
	} else if udevNic.Action == "remove" {
		if nicNotify, ok := pool.deletingNic[udevNic.MAC]; ok {
			//udev poolNotify is fast then qingcloud api, so delete nicNotify later
			nicNotify.removed = true
			pool.retryDeletingNic([]string{udevNic.MAC})
		}
	} else {
		log.Fatalf("unknow udev notify %+v", udevNic)
	}
}

func (pool *poolManager) getDeletingNic(nic string) *poolNotify {
	result, ok := pool.deletingNic[nic]
	if !ok {
		panic("invalid nic")
	}

	return result
}

func (pool *poolManager) removeDeletingNic(nics []string) {
	for _, nic := range nics {
		delete(pool.deletingNic, nic)
	}
}

func (pool *poolManager) getDeletingNicLen(vxnet string) int {
	if vxnet == "" {
		return len(pool.deletingNic)
	}
	sum := 0
	for _, nic := range pool.deletingNic {
		if nic.nic.VxNet.ID == vxnet {
			sum++
		}
	}

	return sum
}

func (pool *poolManager) retryDeletingNic(nics []string) {
	for _, nic := range nics {
		pool.deletingNic[nic].expireTimeStamp = time.Now().Add(types.DefaultIAASApiTimeout)
	}
}

func (pool *poolManager) registerDeletingNic(notify *poolNotify) {
	pool.deletingNic[notify.nic.ID] = notify
	notify.expireTimeStamp = time.Now().Add(types.DefaultIAASApiTimeout)
}

func (pool *poolManager) syncDeletingNic() {
	var (
		toDelete   []string
		toDeattach []string
		err        error
	)

	for nicID, nicNotify := range pool.deletingNic {
		logger := log.WithFields(log.Fields{
			"ID":       nicID,
			"removed":  nicNotify.removed,
			"reserved": nicNotify.nic.Reserved,
		})

		if nicNotify.removed {
			err := pool.cleanupNic(nicNotify.nic)
			if err != nil {
				logger.WithError(err).Warning("syncDeletingNic: cannot cleanup nic")
			}

			if !nicNotify.nic.Reserved && !time.Now().Before(nicNotify.expireTimeStamp) {
				toDelete = append(toDelete, nicID)
			} else if nicNotify.nic.Reserved {
				pool.deletePendingNic(nicID)
			}
		} else {
			if !time.Now().Before(nicNotify.expireTimeStamp) {
				toDeattach = append(toDeattach, nicID)
			}
		}
	}

	if len(toDelete) > 0 {
		err = pool.qcClient.DeleteNics(toDelete)
		if err == nil {
			pool.removeDeletingNic(toDelete)
		} else {
			if err != errors.ErrResourceBusy {
				log.WithError(err).Errorf("syncDeletingNic: cannot delete nics %+v", toDelete)
			}
			pool.retryDeletingNic(toDelete)
		}
	}

	if len(toDeattach) > 0 {
		err = pool.qcClient.DeattachNics(toDeattach, false)
		if err != nil {
			log.WithError(err).Errorf("syncDeletingNic: cannot deattach nics %+v", toDelete)
			pool.retryDeletingNic(toDeattach)
		}
	}
}

func (pool *poolManager) start(stopCh <-chan struct{}) {
	log.Infoln("Starting pool reconciling")

	go pool.udev.Monitor(pool.UdevCh)
	pool.updateDataStore()

	for {
		select {
		case <-time.After(time.Duration(pool.conf.Delay) * time.Second):
			pool.syncPendingNic()
			pool.syncDeletingNic()
			pool.updateDataStore()
		case udev := <-pool.UdevCh:
			pool.handleNicAddorDel(udev)
		case create := <-pool.IpamCh:
			if create.Get {
				pool.getNic(nil, create.NicID, create.Result)
			} else {
				pool.createNic(create.Vxnet, create.Result)
			}
		case <-stopCh:
			log.Infoln("Receive stop signals, stop pool manager")
			return
		}
	}
}

func (pool *poolManager) cleanupDataStore(ds *datastore.DataStore) {
	toDelete := ds.GetFreeNic()
	if len(toDelete) <= 0 {
		return
	}

	var toDeleteID []string
	for _, nic := range toDelete {
		toDeleteID = append(toDeleteID, nic.ID)
		pool.registerDeletingNic(&poolNotify{
			nic: nic,
		})
	}

	err := pool.qcClient.DeattachNics(toDeleteID, false)
	if err != nil {
		log.Warningln(err)
	}
}

func (pool *poolManager) cacheDataStore(ds *datastore.DataStore) {
	used := pool.getRouteTableLen()
	pendingAll := pool.getPendingNicLen("")
	deletingAll := pool.getDeletingNicLen("")
	vxnet := ds.GetVxnet().ID
	want := ds.GetWantNicNum()
	cached := pool.getPendingNicCachedLen(vxnet)

	//pendingAll deletingAll, and used will overlap with each other
	totalAvail := pool.conf.MaxNic - used - pendingAll - deletingAll

	if cached >= want {
		return
	} else {
		want = want - cached
	}

	if want > 0 {
		log.WithFields(log.Fields{
			"used":        used,
			"pendingAll":  pendingAll,
			"deletingAll": deletingAll,
			"want":        want,
			"vxnet":       vxnet,
			"totalAvail":  totalAvail,
			"cached":      cached,
		}).Info("cache datastore")

		if totalAvail < want {
			log.Warningf("Please check freeNics, want %d, totalAvail %d", want, totalAvail)
			want = totalAvail
		}

		pool.cacheNics(ds.GetVxnet(), want)
	}
}

func (pool *poolManager) updateDataStore() {
	for _, ds := range pool.dataStores {
		pool.cleanupDataStore(ds)
		pool.cacheDataStore(ds)
	}
}

func (pool *poolManager) FindDataStore(address string) *datastore.DataStore {
	pool.lock.Lock()
	defer pool.lock.Unlock()

	for _, ds := range pool.dataStores {
		if ds.HaveAddressInfo(address) {
			return ds
		}
	}

	return nil
}

func (pool *poolManager) restoreNic(vxnet *types.VxNet, nic string) error {
	nics, err := pool.qcClient.GetNics(vxnet, []string{nic})
	if err != nil {
		return err
	}

	return pool.setupNic(nics[0], datastore.ReservedForever)
}

func (pool *poolManager) setRouteTableNum(nic *types.HostNic) {
	result := -1

	if nic.RouteTableNum > 0 {
		result = nic.RouteTableNum - types.DefaultRouteTableBase
	} else {
		for routeTable, tmp := range pool.routeTables {
			if tmp != nil && tmp.ID != nic.ID {
				continue
			} else {
				result = routeTable
				break
			}
		}
	}

	if result < 0 {
		panic("invalid route table number")
	}

	pool.routeTables[result] = nic
	nic.RouteTableNum = result + types.DefaultRouteTableBase
}

func (pool *poolManager) unsetRouteTableNum(nic *types.HostNic) {
	if nic.RouteTableNum <= 0 {
		return
	}

	result := -1
	for routeTable, tmp := range pool.routeTables {
		if tmp != nil && tmp.ID == nic.ID {
			result = routeTable
			break
		}
	}

	nic.RouteTableNum = 0
	pool.routeTables[result] = nil
}

func (pool *poolManager) getRouteTableLen() int {
	sum := 0
	for _, tmp := range pool.routeTables {
		if tmp != nil {
			sum++
		}
	}
	return sum
}

//reserve set when bind nic
func (pool *poolManager) setupNic(nic *types.HostNic, reserve datastore.AddressReservedType) error {
	logger := log.WithFields(log.Fields{
		"Address": nic.Address,
		"Mac":     nic.HardwareAddr,
		"reserve": reserve,
	})

	logger.Info("set up nic")

	//It is possible that the network card obtained through api does not match
	//the one on the host computer, because the user has changed the name of
	//the network card.
	if nic.RouteTableNum <= 0 {
		link, err := pool.netlink.LinkByMacAddr(nic.ID)
		if err != nil {
			if err != errors.ErrNotFound {
				return err
			}
			//maybe passthrough vnic
			nic.RouteTableNum = pool.netlink.GetRouteTableNum(nic)
			if nic.RouteTableNum <= 0 {
				logger.WithError(err).Errorf("Neither the routing table number can be found by mac address nor by policy route.")
				return err
			} else {
				pool.setRouteTableNum(nic)
			}
		} else {
			if strings.HasPrefix(link.Attrs().Name, types.NicPrefix) {
				nic.RouteTableNum, _ = strconv.Atoi(strings.TrimPrefix(link.Attrs().Name, types.NicPrefix))
				pool.setRouteTableNum(nic)
			} else {
				pool.setRouteTableNum(nic)
				name := types.GetHostNicName(nic.RouteTableNum)
				if err = pool.netlink.LinkSetName(link, name); err != nil {
					logger.WithError(err).Errorf("cannot set nic name to %s", name)
					return err
				}
			}
		}
	}

	logger.Infof("use route table %d", nic.RouteTableNum)
	err := pool.netlink.SetupNicNetwork(nic)
	if err != nil {
		logger.WithError(err).Errorf("failed to set up nic network")
		return err
	}

	ds, _ := pool.GetDataStore(nic.VxNet.ID)
	ds.AddNIC(nic)
	ds.AddIPv4Address(nic.ID, nic.Address, reserve)

	return nil
}

func (pool *poolManager) cleanupNic(nic *types.HostNic) error {
	err := pool.netlink.CleanupNicNetwork(nic)
	if err != nil {
		return err
	}

	pool.unsetRouteTableNum(nic)
	return nil
}

func (pool *poolManager) getNic(vxnet *types.VxNet, nicID string, result chan IpamRequestResult) {
	nics, err := pool.qcClient.GetNicsAndAttach(vxnet, []string{nicID})
	if err != nil {
		result <- IpamRequestResult{
			Err: err,
			Nic: nil,
		}
		return
	}

	notify := &poolNotify{
		nic:    nics[0],
		result: result,
	}
	pool.registerPendingNic(notify)
}

func (pool *poolManager) createNic(vxnet *types.VxNet, result chan IpamRequestResult) {
	var (
		ds     *datastore.DataStore
		notify *poolNotify
	)

	//step1: trying to get an unowned NIC from the map
	notify = pool.pickupPendingNic(vxnet)
	if notify != nil {
		goto found
	}

	//step2: try bulk caching a network card
	ds, _ = pool.GetDataStore(vxnet.ID)
	pool.cacheDataStore(ds)
	notify = pool.pickupPendingNic(vxnet)
	if notify != nil {
		goto found
	}

	//step3: try caching only one nic
	pool.cacheNics(vxnet, 1)
	notify = pool.pickupPendingNic(vxnet)
	if notify != nil {
		goto found
	}

	result <- IpamRequestResult{
		Err: fmt.Errorf("cannot alloc nic"),
		Nic: nil,
	}
found:
	if notify != nil {
		notify.result = result
	}
	return
}

func (pool *poolManager) cacheNics(vxnet *types.VxNet, num int) {
	logger := log.WithFields(log.Fields{
		"vxnet": vxnet.ID,
		"num":   num,
	})

	logger.Info("try to cache nics")

	var (
		nics []*types.HostNic
		err  error
	)

	nics, err = pool.qcClient.CreateNicsAndAttach(vxnet, num)
	if err != nil {
		log.Errorf("Failed to create a nic in %s, err: %s", pool.primaryNic.VxNet.ID, err.Error())
		return
	}

	for _, nic := range nics {
		notify := &poolNotify{
			nic: nic,
		}
		pool.registerPendingNic(notify)
	}
}
