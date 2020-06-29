package datastore

import (
	"container/heap"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	k8sapi "github.com/yunify/hostnic-cni/pkg/k8sclient"
	"github.com/yunify/hostnic-cni/pkg/types"
	"time"
)

var _ = Describe("Datastore", func() {
	It("queue", func() {
		queue := make(PriorityQueue, 0)
		heap.Init(&queue)

		test1 := &AddressInfo{
			Address:        "192.168.0.1",
			UnassignedTime: time.Now().Add(30 * time.Second),
		}
		heap.Push(&queue, test1)
		test2 := &AddressInfo{
			Address:        "192.168.0.2",
			UnassignedTime: time.Now(),
		}
		heap.Push(&queue, test2)
		test3 := &AddressInfo{
			Address:        "192.168.0.3",
			UnassignedTime: time.Now().Add(10 * time.Second),
		}
		heap.Push(&queue, test3)

		Expect(len(queue)).To(Equal(3))
		Expect(heap.Pop(&queue).(*AddressInfo).Address).To(Equal("192.168.0.2"))
		Expect(len(queue)).To(Equal(2))
	})

	It("datastore", func() {
		testVxnet := &types.VxNet{
			ID: "testvxnet",
		}

		conf := &DataStoreConf{
			PoolHigh: 2,
			PoolLow:  1,
			Cool:     10,
		}
		ds := NewDataStore(testVxnet, conf)

		testNic := &types.HostNic{
			ID:            "testnic",
			VxNet:         testVxnet,
			RouteTableNum: 1,
		}
		ds.AddNIC(testNic)
		ds.AddIPv4Address(testNic.ID, "192.168.0.1", ReservedNone)
		time.Sleep(time.Second)

		testNic2 := &types.HostNic{
			ID:    "testnic2",
			VxNet: testVxnet,
			RouteTableNum: 2,
		}
		ds.AddNIC(testNic2)
		ds.AddIPv4Address(testNic2.ID, "192.168.0.2", ReservedOnce)
		time.Sleep(time.Second)

		testNic3 := &types.HostNic{
			ID:    "testnic3",
			VxNet: testVxnet,
			RouteTableNum: 3,
		}
		ds.AddNIC(testNic3)
		ds.AddIPv4Address(testNic3.ID, "192.168.0.3", ReservedForever)
		time.Sleep(time.Second)

		testNic4 := &types.HostNic{
			ID:    "testnic4",
			VxNet: testVxnet,
			RouteTableNum: 4,
		}
		ds.AddNIC(testNic4)
		ds.AddIPv4Address(testNic4.ID, "192.168.0.4", ReservedNone)
		time.Sleep(time.Second)

		Expect(ds.GetNumOfFreeAddr()).To(Equal(2))
		Expect(ds.GetNumOfReservedAddr()).To(Equal(2))
		Expect(ds.GetNumOfTotalAddr()).To(Equal(4))

		podInfo := &k8sapi.K8SPodInfo{
			Namespace: "testns",
			Name:      "testpod",
			Container: "testcontainer",
		}
		_, addrInfo, err := ds.AssignPodIPv4Address(podInfo, false)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(addrInfo.Address).To(Equal("192.168.0.1"))
		Expect(addrInfo.NIC.Nic.ID).To(Equal(testNic.ID))
		Expect(len(ds.queue)).To(Equal(1))
		addrInfo, err = ds.UnassignPodIPv4Address(podInfo)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(addrInfo.Address).To(Equal("192.168.0.1"))
		Expect(addrInfo.NIC.Nic.ID).To(Equal(testNic.ID))
		Expect(len(ds.queue)).To(Equal(2))

		podInfo.IP = "192.168.0.2"
		_, addrInfo, err = ds.AssignPodIPv4Address(podInfo, true)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(addrInfo.Address).To(Equal("192.168.0.2"))
		Expect(addrInfo.NIC.Nic.ID).To(Equal(testNic2.ID))
		Expect(len(ds.queue)).To(Equal(2))
		addrInfo, err = ds.UnassignPodIPv4Address(podInfo)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(addrInfo.Address).To(Equal("192.168.0.2"))
		Expect(addrInfo.NIC.Nic.ID).To(Equal(testNic2.ID))
		Expect(len(ds.queue)).To(Equal(3))

		podInfo.IP = "192.168.0.3"
		_, addrInfo, err = ds.AssignPodIPv4Address(podInfo, true)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(addrInfo.Address).To(Equal("192.168.0.3"))
		Expect(addrInfo.NIC.Nic.ID).To(Equal(testNic3.ID))
		Expect(len(ds.queue)).To(Equal(3))
		addrInfo, err = ds.UnassignPodIPv4Address(podInfo)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(addrInfo.Address).To(Equal("192.168.0.3"))
		Expect(addrInfo.NIC.Nic.ID).To(Equal(testNic3.ID))
		Expect(len(ds.queue)).To(Equal(3))

		nics := ds.GetFreeNic()
		Expect(len(nics)).To(Equal(1))
		Expect(nics[0].RouteTableNum).To(Equal(3))
		Expect(len(ds.queue)).To(Equal(3))
		time.Sleep(10 * time.Second)
		nics = ds.GetFreeNic()
		Expect(len(nics)).To(Equal(1))
		Expect(len(ds.queue)).To(Equal(2))

		conf.PoolHigh = 0
		conf.PoolLow = 0
		ds.ConfigureDataStore(conf)
		nics = ds.GetFreeNic()
		Expect(len(nics)).To(Equal(2))
		Expect(len(ds.queue)).To(Equal(0))

		conf.PoolHigh = 2
		conf.PoolLow = 1
		ds.ConfigureDataStore(conf)
		Expect(ds.GetWantNicNum()).To(Equal(1))

		ds.AddNIC(testNic2)
		ds.AddIPv4Address(testNic2.ID, "192.168.0.2", ReservedOnce)
		ds.AddNIC(testNic3)
		ds.AddIPv4Address(testNic3.ID, "192.168.0.3", ReservedForever)

		time.Sleep(11 * time.Second)
		nics = ds.GetFreeNic()
		Expect(len(nics)).To(Equal(1))
		Expect(nics[0].ID).To(Equal(testNic3.ID))
		Expect(len(ds.queue)).To(Equal(1))
	})
})
