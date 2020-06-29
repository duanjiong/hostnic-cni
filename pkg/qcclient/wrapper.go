package qcclient

import (
	"fmt"
	"github.com/davecgh/go-spew/spew"
	log "github.com/sirupsen/logrus"
	"github.com/yunify/hostnic-cni/pkg/errors"
	"github.com/yunify/hostnic-cni/pkg/types"
	"github.com/yunify/qingcloud-sdk-go/client"
	"github.com/yunify/qingcloud-sdk-go/config"
	"github.com/yunify/qingcloud-sdk-go/service"
	"io/ioutil"
	"net"
	"strings"
	"time"
)

const (
	instanceIDFile = "/etc/qingcloud/instance-id"
	defaultOpTimeout    = 180 * time.Second
	defaultWaitInterval = 10 * time.Second
)

var _ QingCloudAPI = &qingcloudAPIWrapper{}

type qingcloudAPIWrapper struct {
	nicService      *service.NicService
	vxNetService    *service.VxNetService
	instanceService *service.InstanceService
	vpcService      *service.RouterService
	jobService      *service.JobService
	tagService      *service.TagService

	userID      string
	instanceID  string
	clusterName string
	nicTagID    string
}

// NewQingCloudClient create a qingcloud client to manipulate cloud resources
func NewQingCloudClient() QingCloudAPI {
	instanceID, err := ioutil.ReadFile(instanceIDFile)
	if err != nil {
		panic(fmt.Sprintf("Load instance-id from %s error: %v", instanceIDFile, err))
	}

	qsdkconfig, err := config.NewDefault()
	if err != nil {
		panic(err)
	}
	if err = qsdkconfig.LoadUserConfig(); err != nil {
		panic(err)
	}

	qcService, err := service.Init(qsdkconfig)
	if err != nil {
		panic(err)
	}

	nicService, err := qcService.Nic(qsdkconfig.Zone)
	if err != nil {
		panic(err)
	}

	vxNetService, err := qcService.VxNet(qsdkconfig.Zone)
	if err != nil {
		panic(err)
	}

	jobService, err := qcService.Job(qsdkconfig.Zone)
	if err != nil {
		panic(err)
	}

	instanceService, err := qcService.Instance(qsdkconfig.Zone)
	if err != nil {
		panic(err)
	}

	tagService, err := qcService.Tag(qsdkconfig.Zone)
	if err != nil {
		panic(err)
	}

	vpcService, _ := qcService.Router(qsdkconfig.Zone)

	//useid
	api, _ := qcService.Accesskey(qsdkconfig.Zone)
	output, err := api.DescribeAccessKeys(&service.DescribeAccessKeysInput{
		AccessKeys: []*string{&qsdkconfig.AccessKeyID},
	})
	if err != nil {
		panic(err)
	}
	if len(output.AccessKeySet) == 0 {
		err = fmt.Errorf("AccessKey %s have not userid", qsdkconfig.AccessKeyID)
		panic(err)
	}

	q := &qingcloudAPIWrapper{
		nicService:      nicService,
		vxNetService:    vxNetService,
		instanceService: instanceService,
		vpcService:      vpcService,
		jobService:      jobService,
		tagService:      tagService,
		userID:          *output.AccessKeySet[0].Owner,
		instanceID:      string(instanceID),
	}

	q.nicTagID = q.setupNicTag()

	return q
}

func (q *qingcloudAPIWrapper) GetInstanceID() string {
	return q.instanceID
}

func (q *qingcloudAPIWrapper) AttachNics(nicIDs []string) error {
	input := &service.AttachNicsInput{
		Nics:     service.StringSlice(nicIDs),
		Instance: &q.instanceID,
	}
	output, err := q.nicService.AttachNics(input)
	if err != nil {
		return err
	}

	if *output.RetCode != 0 {
		return fmt.Errorf("failed to attatch nics: %s", *output.Message)
	}

	return nil
}

func constructHostnic(vxnet *types.VxNet, nic *service.NIC) *types.HostNic {
	hostnic := &types.HostNic{
		ID:           *nic.NICID,
		VxNet:        vxnet,
		HardwareAddr: *nic.NICID,
		Address:      *nic.PrivateIP,
	}

	if *nic.Role == 1 {
		hostnic.IsPrimary = true
	}

	if *nic.Status == "in-use" {
		hostnic.Using = true
	}

	return hostnic
}

func (q *qingcloudAPIWrapper) GetNics(vxnet *types.VxNet, nics []string) ([]*types.HostNic, error) {
	input := &service.DescribeNicsInput{
		Nics: service.StringSlice(nics),
	}

	output, err := q.nicService.DescribeNics(input)
	if err != nil {
		return nil, err
	}

	result := make([]*types.HostNic, 0)
	if *output.RetCode == 0 && len(output.NICSet) > 0 {
		for _, nic := range output.NICSet {
			if vxnet == nil {
				tmp, err := q.GetVxNets([]string{*nic.VxNetID})
				if err != nil {
					return nil, err
				}
				vxnet = tmp[*nic.VxNetID]
			}
			hostnic := constructHostnic(vxnet, nic)
			hostnic.Reserved = true
			result = append(result, hostnic)
		}
	} else if *output.RetCode != 0 {
		return nil, fmt.Errorf("failed to get nics: %s", *output.Message)
	}

	return result, nil
}

func (q *qingcloudAPIWrapper) GetCreatedNics(num, offset int) ([]*types.HostNic, error) {
	input := &service.DescribeNicsInput{
		Limit:   &num,
		Offset:  &offset,
		NICName: service.String(types.NicPrefix + q.instanceID),
	}

	output, err := q.nicService.DescribeNics(input)
	if err != nil {
		return nil, err
	}
	if *output.RetCode != 0 {
		return nil, fmt.Errorf("DescribeNics Failed [%+v]", *output)
	}

	var (
		nics  []*types.HostNic
		netID []string
	)
	for _, nic := range output.NICSet {
		hostnic := constructHostnic(&types.VxNet{
			ID: *nic.VxNetID,
		}, nic)
		nics = append(nics, hostnic)
		netID = append(netID, *nic.VxNetID)
	}

	if len(netID) > 0 {
		vxnets, err := q.GetVxNets(RemoveRepByMap(netID))
		if err != nil {
			return nil, err
		}

		for _, nic := range nics {
			nic.VxNet = vxnets[nic.VxNet.ID]
		}
	}

	return nics, nil
}

func (q *qingcloudAPIWrapper) GetPrimaryNIC() (*types.HostNic, error) {
	input := &service.DescribeNicsInput{
		Instances: []*string{&q.instanceID},
		Status:    service.String("in-use"),
		Limit:     service.Int(types.NicNumLimit),
	}

	output, err := q.nicService.DescribeNics(input)
	if err != nil {
		return nil, err
	}
	if *output.RetCode != 0 {
		return nil, fmt.Errorf("GetPrimaryNIC Failed [%+v]", *output)
	}

	var primaryNic *types.HostNic
	for _, nic := range output.NICSet {
		if *nic.Role == 1 {
			vxnet, err := q.GetVxNets([]string{*nic.VxNetID})
			if err != nil {
				return nil, err
			}
			primaryNic = constructHostnic(vxnet[*nic.VxNetID], nic)
			break
		}
	}

	return primaryNic, nil
}

func (q *qingcloudAPIWrapper) GetNicsAndAttach(vxnet *types.VxNet, nics []string) ([]*types.HostNic, error) {
	input := &service.DescribeNicsInput{
		Nics: service.StringSlice(nics),
	}

	output, err := q.nicService.DescribeNics(input)
	if err != nil {
		return nil, err
	}
	if *output.RetCode != 0 {
		return nil, fmt.Errorf("failed to get nics: %s", *output.Message)
	}

	result := make([]*types.HostNic, 0)
	if *output.RetCode == 0 && len(output.NICSet) > 0 {
		for _, nic := range output.NICSet {
			if vxnet == nil {
				tmp, err := q.GetVxNets([]string{*nic.VxNetID})
				if err != nil {
					return nil, err
				}
				vxnet = tmp[*nic.VxNetID]
			}
			hostnic := constructHostnic(vxnet, nic)
			hostnic.Reserved = true
			result = append(result, hostnic)
		}

		if err = q.AttachNics(nics); err != nil {
			log.WithFields(log.Fields{
				"nics": nics,
			}).WithError(err).Errorf("Maybe you need to manually check why you can't bind")
			return nil, err
		}
	}

	return result, nil
}

func (q *qingcloudAPIWrapper) CreateNicsAndAttach(vxnet *types.VxNet, num int) ([]*types.HostNic, error) {
	nicName := types.NicPrefix + q.instanceID
	input := &service.CreateNicsInput{
		Count:   service.Int(num),
		VxNet:   &vxnet.ID,
		NICName: service.String(nicName),
	}

	output, err := q.nicService.CreateNics(input)
	if err != nil {
		return nil, err
	}

	if *output.RetCode != 0 {
		return nil, fmt.Errorf("failed to create nics: %s", *output.Message)
	}

	result := make([]*types.HostNic, 0)
	nics := make([]string, 0)
	for _, nic := range output.Nics {
		hostnic := types.HostNic{
			ID:           *nic.NICID,
			VxNet:        vxnet,
			HardwareAddr: *nic.NICID,
			Address:      *nic.PrivateIP,
		}

		result = append(result, &hostnic)
		nics = append(nics, *nic.NICID)
	}

	//may need to tag the card later.
	/*
		if err = q.attachNicTag(nics); err != nil {
			log.WithError(err).Errorf("cannot attach tag for nics %+v", nics)
			if err = q.DeleteNics(nics); err != nil {
				log.WithError(err).Errorf("cannot delete nics %+v", nics)
				return nil, err
			}
			return nil, err
		}
	*/

	if err = q.AttachNics(nics); err != nil {
		log.WithError(err).Errorf("cannot attach  nics %+v", nics)
		if err = q.DeleteNics(nics); err != nil {
			log.WithError(err).Errorf("cannot delete nics %+v", nics)
			return nil, err
		}
		return nil, err
	}

	return result, nil
}

func (q *qingcloudAPIWrapper) DeattachNics(nicIDs []string, sync bool) error {
	if len(nicIDs) <= 0 {
		return nil
	}

	input := &service.DetachNicsInput{
		Nics: service.StringSlice(nicIDs),
	}
	output, err := q.nicService.DetachNics(input)
	if err != nil {
		return fmt.Errorf("failed to deattach nics, err=%s, input=%s, output=%s", err, spew.Sdump(input), spew.Sdump(output))
	}

	if sync {
		return client.WaitJob(q.jobService, *output.JobID, defaultOpTimeout, defaultWaitInterval)
	}

	return nil
}

func (q *qingcloudAPIWrapper) DeleteNics(nicIDs []string) error {
	if len(nicIDs) <= 0 {
		return nil
	}

	input := &service.DeleteNicsInput{
		Nics: service.StringSlice(nicIDs),
	}
	_, err := q.nicService.DeleteNics(input)
	if err != nil {
		if strings.Contains(err.Error(), "QingCloud Error: Code (1400)") {
			return errors.ErrResourceBusy
		}
		return err
	}

	return nil
}

func (q *qingcloudAPIWrapper) GetVxNets(ids []string) (map[string]*types.VxNet, error) {
	if len(ids) <= 0 {
		panic("GetVxNets: empty input")
	}

	log.WithFields(log.Fields{
		"VxNets": ids,
	}).Debug("GetVxNets input")

	input := &service.DescribeVxNetsInput{
		VxNets: service.StringSlice(ids),
		Limit:  service.Int(types.NicNumLimit),
	}

	output, err := q.vxNetService.DescribeVxNets(input)
	if err != nil {
		return nil, err
	}

	if *output.RetCode == 0 {
		var vxNets []*types.VxNet

		for _, qcVxNet := range output.VxNetSet {
			vxnetItem := &types.VxNet{
				ID:       *qcVxNet.VxNetID,
				RouterID: *qcVxNet.VpcRouterID,
			}

			if qcVxNet.Router != nil {
				vxnetItem.GateWay = *qcVxNet.Router.ManagerIP
				_, vxnetItem.Network, _ = net.ParseCIDR(*qcVxNet.Router.IPNetwork)
			} else {
				return nil, fmt.Errorf("vxnet %s should bind to vpc", *qcVxNet.VxNetID)
			}

			log.Debugf("GetVxnets %+v", vxnetItem)
			vxNets = append(vxNets, vxnetItem)
		}

		result := make(map[string]*types.VxNet, 0)
		for _, vxNet := range vxNets {
			result[vxNet.ID] = vxNet
		}

		return result, nil
	}

	return nil, fmt.Errorf("GetVxNets output message=%s", *output.Message)
}

func RemoveRepByMap(slc []string) []string {
	result := []string{}
	tempMap := map[string]byte{}
	for _, e := range slc {
		l := len(tempMap)
		tempMap[e] = 0
		if len(tempMap) != l {
			result = append(result, e)
		}
	}
	return result
}

func (q *qingcloudAPIWrapper) GetVPC(id string) (*types.VPC, error) {
	input := &service.DescribeRoutersInput{
		Routers: []*string{&id},
	}
	output, err := q.vpcService.DescribeRouters(input)
	if err != nil {
		return nil, err
	}

	if *output.RetCode == 0 {
		if len(output.RouterSet) == 0 {
			return nil, errors.NewResourceNotFoundError(types.ResourceTypeVPC, id)
		}
		vpc := &types.VPC{
			ID: *output.RouterSet[0].RouterID,
		}
		_, net, err := net.ParseCIDR(*output.RouterSet[0].VpcNetwork)
		if err != nil {
			return nil, err
		}
		vpc.Network = net
		return vpc, nil
	}

	return nil, fmt.Errorf("DescribeRouters Failed [%+v]", *output)
}

func (q *qingcloudAPIWrapper) setupNicTag() string {
	input := &service.DescribeTagsInput{
		SearchWord: service.String(types.NicPrefix + q.instanceID),
	}

	output, err := q.tagService.DescribeTags(input)
	if err != nil {
		panic(err)
	}

	if *output.RetCode != 0 {
		panic(fmt.Errorf("GetNicTag Failed [%+v]", *output.Message))
	}

	lens := len(output.TagSet)
	if lens > 1 {
		panic(fmt.Errorf("multi nic tags: %s", types.NicPrefix+q.instanceID))
	} else if lens == 0 {
		input := &service.CreateTagInput{
			TagName: service.String(types.NicPrefix + q.instanceID),
		}

		output, err := q.tagService.CreateTag(input)
		if err != nil {
			panic(err)
		}

		if *output.RetCode != 0 {
			panic(fmt.Errorf("GetNicTag Failed [%+v]", *output.Message))
		}

		return *output.TagID
	}

	return *output.TagSet[0].TagID
}

func (q *qingcloudAPIWrapper) attachNicTag(nics []string) error {
	var tagPairs []*service.ResourceTagPair

	for _, nic := range nics {
		tagPairs = append(tagPairs, &service.ResourceTagPair{
			ResourceID:   &nic,
			ResourceType: service.String(string(types.ResourceTypeNic)),
			TagID:        service.String(q.nicTagID),
		})
	}

	input := &service.AttachTagsInput{
		ResourceTagPairs: tagPairs,
	}

	output, err := q.tagService.AttachTags(input)
	if err != nil {
		return err
	}
	if *output.RetCode != 0 {
		return fmt.Errorf("AttachNicTag Failed [%+v]", *output.Message)
	}

	return nil
}
