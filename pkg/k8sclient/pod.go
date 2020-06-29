package k8sclient

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	AnnoHostNicVxnet = "network.kubesphere.io/vxnet"
	AnnoHostNic      = "network.kubesphere.io/nic"
)

// K8SPodInfo provides pod info
type K8SPodInfo struct {
	// HostName is pod's name
	Name string
	// Namespace is pod's namespace
	Namespace string
	// Container is pod's container id
	Container string
	// IP is pod's ipv4 address
	IP      string
	NicID   string
	Vxnet   string
}

func (k *k8sHelper) UpdatePodInfo(podInfo *K8SPodInfo) error {
	pod, err := k.podLister.Pods(podInfo.Namespace).Get(podInfo.Name)
	if err != nil {
		return err
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	if pod.Annotations[AnnoHostNicVxnet] == podInfo.Vxnet {
		return nil
	} else {
		pod.Annotations[AnnoHostNicVxnet] = podInfo.Vxnet
		_, err  = k.clientset.CoreV1().Pods(podInfo.Namespace).Update(pod)
	}

	return err
}

func (k *k8sHelper) GetNodeInfo() (string, error) {
	node, err := k.nodeLister.Get(k.nodeName)
	if err != nil {
		return "", err
	}

	annotations := node.GetAnnotations()
	if annotations != nil {
		return annotations[AnnoHostNicVxnet], nil
	}
	return "", nil
}

// GetCurrentNodePods return the list of pods running on the local nodes
func (k *k8sHelper) GetCurrentNodePods() ([]*K8SPodInfo, error) {
	var localPods []*K8SPodInfo

	pods, err := k.podLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, pod := range pods {
		if pod.Spec.NodeName == k.nodeName && !pod.Spec.HostNetwork {
			localPods = append(localPods, getPodInfo(pod))
		}
	}
	return localPods, nil
}

func getPodInfo(pod *corev1.Pod) *K8SPodInfo {
	tmp := &K8SPodInfo{
		Name:      pod.GetName(),
		Namespace: pod.GetNamespace(),
		IP:        pod.Status.PodIP,
	}
	annotations := pod.GetAnnotations()
	if annotations != nil {
		tmp.Vxnet = annotations[AnnoHostNicVxnet]
		tmp.NicID = annotations[AnnoHostNic]
	}

	return tmp
}

func (k *k8sHelper) GetPodInfo(namespace, name string) (*K8SPodInfo, error) {
	pod, err := k.podLister.Pods(namespace).Get(name)
	if err != nil {
		return nil, err
	}

	return getPodInfo(pod), nil
}
