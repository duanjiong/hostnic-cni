package ipam

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/davecgh/go-spew/spew"
	log "github.com/sirupsen/logrus"
	"github.com/yunify/hostnic-cni/pkg/db"
	"github.com/yunify/hostnic-cni/pkg/errors"
	"github.com/yunify/hostnic-cni/pkg/ipam/datastore"
	//dberrors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/yunify/hostnic-cni/pkg/k8sclient"
	"github.com/yunify/hostnic-cni/pkg/rpc"
	"google.golang.org/grpc"
	"net"
	"os"
)

// IpamD is the core manager in hostnic which store pod ips and nics
type IpamD struct {
	K8sClient k8sclient.K8sHelper

	pool *poolManager

	conf *ServerConf
}

func NewIpamD(client k8sclient.K8sHelper, conf *ServerConf, pool *poolManager) *IpamD {
	return &IpamD{
		conf:      conf,
		pool:      pool,
		K8sClient: client,
	}
}

func (s *IpamD) setup() error {
	iter := db.LevelDB.NewIterator(nil, nil)
	for iter.Next() {
		var (
			info   db.NetworkInfo
			key string
		)
		// Remember that the contents of the returned slice should not be modified, and
		// only valid until the next call to Next.
		json.Unmarshal(iter.Value(), &info)
		key = string(iter.Key())

		log.Debugf("ipam setup: resotre key=%s, value=%s", key, spew.Sdump(info))

		pod := &k8sclient.K8SPodInfo{
			Name:      info.K8S_POD_NAME,
			Namespace: info.K8S_POD_NAMESPACE,
			Container: key,
			IP:        info.IPv4Addr,
		}

		ds := s.pool.FindDataStore(info.IPv4Addr)
		if ds == nil {
			panic(fmt.Sprintf("failed to get datastore for pod %+v", info))
		}

		_, _, err := ds.AssignPodIPv4Address(pod, true)
		if err != nil {
			panic(fmt.Sprintf("cannot restore address, err=%s, pod=%s", err, spew.Sdump(pod)))
		}
	}
	iter.Release()
	err := iter.Error()
	if err != nil {
		return fmt.Errorf("error iter db while setup ipam, err=%s", err)
	}

	return nil
}

func (s *IpamD) Start(stopCh <-chan struct{}) {
	err := s.setup()
	if err != nil {
		panic(fmt.Errorf("error while setup ipam, err=%s", err))
	}
	s.startGrpcServer()
}

// startGrpcServer starting the GRPC server
func (s *IpamD) startGrpcServer() {
	socketFilePath := s.conf.ServerPath

	err := os.Remove(socketFilePath)
	if err != nil {
		log.WithError(err).Warningf("cannot remove file %s", socketFilePath)
	}

	listener, err := net.Listen("unix", socketFilePath)
	if err != nil {
		log.WithError(err).Panicf("Failed to listen to %s", socketFilePath)
	}

	//start up server rpc routine
	grpcServer := grpc.NewServer()
	rpc.RegisterCNIBackendServer(grpcServer, s)
	go grpcServer.Serve(listener)
}

func (s *IpamD) chooseDataStore(podInfo *k8sclient.K8SPodInfo) ([]*datastore.DataStore, error) {
	var (
		err         error
		ds          *datastore.DataStore
		availableDS []*datastore.DataStore
	)

	if podInfo.NicID != "" {
		ds, err = s.pool.GetDataStoreForNic(podInfo.NicID)
	} else if podInfo.Vxnet != "" {
		ds, err = s.pool.GetDataStore(podInfo.Vxnet)
	} else {
		nodeVxnet, err := s.K8sClient.GetNodeInfo()
		if err != nil {
			return nil, fmt.Errorf("cannot get node vxnet %+v", err)
		}
		if nodeVxnet != "" {
			ds, err = s.pool.GetDataStore(nodeVxnet)
			s.pool.SetDataStoreConf(ds)
		} else {
			return s.pool.GetDefaultDataStores(), nil
		}
	}

	if err != nil {
		return nil, err
	}

	availableDS = append(availableDS, ds)
	return availableDS, nil
}

// AddNetwork handle add pod request
func (s *IpamD) AddNetwork(context context.Context, in *rpc.NetworkRequest) (*rpc.NetworkReply, error) {
	var (
		resp        rpc.NetworkReply
		err         error
		availableDS []*datastore.DataStore
		exists      bool
		addrInfo    *datastore.AddressInfo
	)

	logger := log.WithFields(log.Fields{
		"action":       "AddNetwork",
		"podName":      in.K8S_POD_NAME,
		"podNamespace": in.K8S_POD_NAMESPACE,
		"podContainer": in.K8S_POD_INFRA_CONTAINER_ID,
	})

	podInfo, err := s.K8sClient.GetPodInfo(in.K8S_POD_NAMESPACE, in.K8S_POD_NAME)
	if err != nil {
		logger.WithError(err).Errorf("cannot get podinfo")
		return nil, err
	}

	request := &IpamRequest{}

	availableDS, err = s.chooseDataStore(podInfo)
	if err != nil {
		return nil, err
	}

	for _, ds := range availableDS {
		if podInfo.NicID != "" {
			request = &IpamRequest{
				Get:    true,
				Vxnet:  nil,
				NicID:  podInfo.NicID,
				Result: make(chan IpamRequestResult, 1),
			}
			s.pool.IpamCh <- request
		} else {
			exists, addrInfo, err = ds.AssignPodIPv4Address(podInfo, false)
			if err != nil {
				logger.WithError(err).Info("addNetwork: cache miss, try to allocate nic")
				request = &IpamRequest{
					Get:    false,
					Vxnet:  ds.GetVxnet(),
					Result: make(chan IpamRequestResult, 1),
				}
				s.pool.IpamCh <- request
			}
		}

		if addrInfo == nil {
			select {
			case <-context.Done():
				err = fmt.Errorf("context removed")
			case tmp := <-request.Result:
				err = tmp.Err
				if err == nil {
					nic := tmp.Nic
					podInfo.IP = nic.Address
					exists, addrInfo, err = ds.AssignPodIPv4Address(podInfo, true)
				}
			}
		}

		if err != nil {
			logger.WithError(err).Errorf("failed to alloc nic")
			continue
		}

		hostNic := addrInfo.NIC.Nic

		logger.WithFields(log.Fields{
			"Address":       addrInfo.Address,
			"RouteTableNum": hostNic.RouteTableNum,
		}).Info("addNetwork: finish alloc ip")

		resp.IPv4Addr = addrInfo.Address
		resp.IPv4Subnet = hostNic.VxNet.Network.String()
		resp.Exist = exists
		resp.RouteTableNum = int32(hostNic.RouteTableNum)
		resp.GW = hostNic.VxNet.GateWay

		if !exists {
			err = db.SetNetworkInfo(in.K8S_POD_INFRA_CONTAINER_ID, &db.NetworkInfo{
				K8S_POD_NAME:      in.K8S_POD_NAME,
				K8S_POD_NAMESPACE: in.K8S_POD_NAMESPACE,
				Netns:             in.Netns,
				IfName:            in.IfName,
				IPv4Addr:          resp.IPv4Addr,
				NicID:             addrInfo.NIC.Nic.ID,
			})
			if err != nil {
				logger.WithError(err).Errorf("failed to write db while addNetwork")
				ds.UnassignPodIPv4Address(podInfo)
				return nil, err
			}
		}

		return &resp, nil
	}

	return nil, fmt.Errorf("datastore no avaliable address, please check datastore")
}

// DelNetwork handle del pod request
func (s *IpamD) DelNetwork(context context.Context, in *rpc.NetworkRequest) (*rpc.NetworkReply, error) {
	var (
		resp rpc.NetworkReply
		ds   *datastore.DataStore
		err  error
	)

	logger := log.WithFields(log.Fields{
		"action":       "DelNetwork",
		"podName":      in.K8S_POD_NAME,
		"podNamespace": in.K8S_POD_NAMESPACE,
		"podContainer": in.K8S_POD_INFRA_CONTAINER_ID,
	})

	logger.Debugf("receive delNetwork action")

	podInfo := &k8sclient.K8SPodInfo{
		Name:      in.K8S_POD_NAME,
		Namespace: in.K8S_POD_NAMESPACE,
		Container: in.K8S_POD_INFRA_CONTAINER_ID,
	}

	ds = s.pool.GetDataStoreByPodInfo(podInfo)
	if ds == nil {
		logger.Warningf("cannot get ds for pod %+v", podInfo)
		return &rpc.NetworkReply{
			Exist:         false,
		}, nil
	}

	addrInfo, _ := ds.UnassignPodIPv4Address(podInfo)
	if err != nil {
		if errors.ErrIPReleased == err {
			return &rpc.NetworkReply{
				Exist:         false,
			}, nil
		}
		return nil, err
	} else {
		resp.IPv4Addr = addrInfo.Address
		resp.RouteTableNum = int32(addrInfo.NIC.Nic.RouteTableNum)
	}

	err = db.DeleteNetworkInfo(in.K8S_POD_INFRA_CONTAINER_ID)
	if err != nil {
		return nil, fmt.Errorf("failed to delete db networkinfo, err=%s", err)
	}

	logger.Info("finish delete ip")

	return &resp, nil
}
