package qcclient

import (
	"github.com/yunify/hostnic-cni/pkg/types"
)

// QingCloudAPI is a wrapper interface of qingcloud api
type QingCloudAPI interface {
	QingCloudNetAPI
}

// QingCloudNetAPI  do dirty works on  net interface on qingcloud
type QingCloudNetAPI interface {
	CreateNicsAndAttach(vxnet *types.VxNet, num int) ([]*types.HostNic, error) //not attach
	DeleteNics(nicIDs []string) error
	DeattachNics(nicIDs []string, sync bool) error
	GetVxNets([]string) (map[string]*types.VxNet, error)
	GetInstanceID() string
	GetCreatedNics(num, offsite int) ([]*types.HostNic, error)
	GetPrimaryNIC() (*types.HostNic, error)
	AttachNics(nicIDs []string) error
	GetNicsAndAttach(vxnet *types.VxNet, nics []string) ([]*types.HostNic, error)
	GetNics(vxnet *types.VxNet, nics []string) ([]*types.HostNic, error)
}
