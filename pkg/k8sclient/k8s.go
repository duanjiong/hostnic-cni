package k8sclient

import (
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/informers"
	coreinformer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"os"
	"time"
)

const (
	// NodeNameEnvKey is env to get the name of current node
	NodeNameEnvKey = "MY_NODE_NAME"
)

// K8sHelper is used to commucate with k8s apiserver
type K8sHelper interface {
	GetCurrentNodePods() ([]*K8SPodInfo, error)
	GetPodInfo(namespace, name string) (*K8SPodInfo, error)
	GetNodeInfo() (string, error)
	UpdatePodInfo(podInfo *K8SPodInfo) error
}

type k8sHelper struct {
	nodeName     string
	clientset    kubernetes.Interface
	nodeLister   corelisters.NodeLister
	nodeSynced   cache.InformerSynced
	nodeInformer coreinformer.NodeInformer
	podInformer  coreinformer.PodInformer
	podLister    corelisters.PodLister
	podSynced    cache.InformerSynced
}

func NewK8sHelper(stopCh <-chan struct{}) K8sHelper {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Failed to get k8s config, err:%v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to get k8s clientset, err:%v", err)
	}

	nodeName := os.Getenv(NodeNameEnvKey)
	kubeInformerFactory := informers.NewSharedInformerFactory(clientset, time.Minute*1)
	podInformer := kubeInformerFactory.Core().V1().Pods()
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()

	helper := &k8sHelper{
		nodeName:     nodeName,
		clientset:    clientset,
		podInformer:  podInformer,
		podLister:    podInformer.Lister(),
		podSynced:    podInformer.Informer().HasSynced,
		nodeInformer: nodeInformer,
		nodeLister:   nodeInformer.Lister(),
		nodeSynced:   nodeInformer.Informer().HasSynced,
	}

	go podInformer.Informer().Run(stopCh)
	go nodeInformer.Informer().Run(stopCh)

	if ok := cache.WaitForCacheSync(stopCh, helper.podSynced, helper.nodeSynced); !ok {
		log.Fatal("failed to wait for caches to sync")
	}
	return helper
}
