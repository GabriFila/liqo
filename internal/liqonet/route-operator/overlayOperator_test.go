package routeoperator

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"syscall"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/vishvananda/netlink"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	liqoerrors "github.com/liqotech/liqo/pkg/liqonet/errors"
	"github.com/liqotech/liqo/pkg/liqonet/overlay"
)

var (
	overlayPodIP     = "10.0.0.1"
	overlayAnnKey    = vxlanMACAddressKey
	overlayAnnValue  = "45:d0:ae:c9:d6:40"
	overlayPeerIP    = "10.11.1.1"
	overlayPeerMAC   = "4e:d0:ae:c9:d6:30"
	overlayNamespace = "overlay-namespace"
	overlayPodName   = "overlay-test-pod"

	overlayTestPod          *corev1.Pod
	overlayReq              ctrl.Request
	ovc                     *OverlayController
	overlayNeigh            overlay.Neighbor
	overlayExistingNeigh    overlay.Neighbor
	overlayExistingNeighDef overlay.Neighbor
	/*** EnvTest Section ***/
	overlayScheme  = runtime.NewScheme()
	overlayEnvTest *envtest.Environment
)

var _ = Describe("OverlayOperator", func() {
	// Before each test we create an empty pod.
	// The right fields will be filled according to each test case.
	JustBeforeEach(func() {
		overlayReq = ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: overlayNamespace,
				Name:      overlayPodName,
			},
		}
		// Create the test pod with the labels already set.
		overlayTestPod = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      overlayReq.Name,
				Namespace: overlayReq.Namespace,
				Labels: map[string]string{
					podNameLabelKey:     podNameLabelValue,
					podInstanceLabelKey: podInstanceLabelValue,
				},
				Annotations: map[string]string{
					overlayAnnKey: overlayAnnValue,
				},
			},
			Spec: corev1.PodSpec{
				NodeName: "overlaytestnodename",
				Containers: []corev1.Container{
					{
						Name:            "busybox",
						Image:           "busybox",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command: []string{
							"sleep",
							"3600",
						},
					},
				},
			},
		}
		// Create dummy overlay operator.
		s, err := metav1.LabelSelectorAsSelector(PodLabelSelector)
		Expect(err).ShouldNot(HaveOccurred())
		ovc = &OverlayController{
			podSelector: s,
			podIP:       overlayPodIP,
			vxlanPeers:  make(map[string]*overlay.Neighbor, 0),
			vxlanDev:    vxlanDevice,
			Client:      k8sClient,
			nodesLock:   &sync.RWMutex{},
			vxlanNodes:  map[string]string{},
			podToNode:   map[string]string{},
		}
		// Add fdb entries for existing peer.
		Expect(addFdb(overlayExistingNeigh, vxlanDevice.Link.Attrs().Index))
		Expect(addFdb(overlayExistingNeighDef, vxlanDevice.Link.Attrs().Index)).Should(BeNil())
	})

	JustAfterEach(func() {
		Expect(flushFdbTable(vxlanDevice.Link.Index)).NotTo(HaveOccurred())
	})
	Describe("testing NewOverlayOperator function", func() {
		Context("when input parameters are not correct", func() {
			It("label selector is not correct, should return nil and error", func() {
				labelSelector := &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      podInstanceLabelKey,
							Operator: "incorrect",
							Values:   []string{podInstanceLabelValue},
						},
					},
				}
				ovc, err := NewOverlayController(overlayPodIP, labelSelector, vxlanDevice, &sync.RWMutex{}, nil, k8sClient)
				Expect(err).Should(MatchError("\"incorrect\" is not a valid pod selector operator"))
				Expect(ovc).Should(BeNil())
			})

			It("vxlan device is not correct, should return nil and error", func() {
				ovc, err := NewOverlayController(overlayPodIP, PodLabelSelector, nil, &sync.RWMutex{}, nil, k8sClient)
				Expect(err).Should(MatchError(&liqoerrors.WrongParameter{Parameter: "vxlanDevice", Reason: liqoerrors.NotNil}))
				Expect(ovc).Should(BeNil())
			})
		})

		Context("when input parameters are correct", func() {
			It("should return overlay controller and nil", func() {
				ovc, err := NewOverlayController(overlayPodIP, PodLabelSelector, vxlanDevice, &sync.RWMutex{}, nil, k8sClient)
				Expect(err).ShouldNot(HaveOccurred())
				Expect(ovc).ShouldNot(BeNil())
			})
		})
	})

	Describe("testing reconcile function", func() {
		Context("when the pod is the current one", func() {
			It("should annotate the pod with the mac address of the vxlan device", func() {
				// Set annotations to nil.
				overlayTestPod.SetFinalizers(nil)
				Eventually(func() error { return k8sClient.Create(context.TODO(), overlayTestPod) }).Should(BeNil())
				newPod := &corev1.Pod{}
				Eventually(func() error { return k8sClient.Get(context.TODO(), overlayReq.NamespacedName, newPod) }).Should(BeNil())
				newPod.Status.PodIP = overlayPodIP
				Eventually(func() error { return k8sClient.Status().Update(context.TODO(), newPod) }).Should(BeNil())
				Eventually(func() error { return k8sClient.Get(context.TODO(), overlayReq.NamespacedName, newPod) }).Should(BeNil())
				Eventually(func() error { _, err := ovc.Reconcile(context.TODO(), overlayReq); return err }).Should(BeNil())
				Eventually(func() error {
					err := k8sClient.Get(context.TODO(), overlayReq.NamespacedName, newPod)
					if err != nil {
						return err
					}
					if newPod.GetAnnotations()[overlayAnnKey] != ovc.vxlanDev.Link.HardwareAddr.String() {
						return fmt.Errorf(" error: annotated MAC %s is different than %s", newPod.GetAnnotations()[overlayAnnKey], ovc.vxlanDev.Link.HardwareAddr.String())
					}
					return nil
				}).Should(BeNil())
			})
		})

		Context("adding new peer", func() {
			It("peer does not exist", func() {
				overlayTestPod.Name = "add-peer-no-existing"
				overlayReq.Name = "add-peer-no-existing"
				Eventually(func() error { return k8sClient.Create(context.TODO(), overlayTestPod) }).Should(BeNil())
				newPod := &corev1.Pod{}
				Eventually(func() error { return k8sClient.Get(context.TODO(), overlayReq.NamespacedName, newPod) }).Should(BeNil())
				newPod.Status.PodIP = "10.1.11.1"
				Eventually(func() error { return k8sClient.Status().Update(context.TODO(), newPod) }).Should(BeNil())
				Eventually(func() error {
					err := k8sClient.Get(context.TODO(), overlayReq.NamespacedName, newPod)
					if err != nil {
						return err
					}
					if newPod.Status.PodIP != "10.1.11.1" {
						return fmt.Errorf("pod ip has not been set yet")
					}
					return nil
				}).Should(BeNil())
				Eventually(func() error { _, err := ovc.Reconcile(context.TODO(), overlayReq); return err }).Should(BeNil())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeTrue())
				//Check that we save the tuple: (nodeName, nodeIP)
				nodeIP, ok := ovc.vxlanNodes[overlayTestPod.Spec.NodeName]
				Expect(ok).Should(BeTrue())
				Expect(nodeIP).Should(Equal(newPod.Status.PodIP))
				//Check that we save the tuple: (req.string, nodeName)
				nodeName, ok := ovc.podToNode[overlayReq.String()]
				Expect(ok).Should(BeTrue())
				Expect(nodeName).Should(Equal(overlayTestPod.Spec.NodeName))
			})
		})

		Context("removing old peer", func() {
			It("peer does not exist", func() {
				overlayTestPod.Name = "del-peer-no-existing"
				overlayReq.Name = "del-peer-no-existing"
				Eventually(func() error { _, err := ovc.Reconcile(context.TODO(), overlayReq); return err }).Should(BeNil())
			})

			It("peer does exist", func() {
				overlayTestPod.Name = "del-peer-no-existing"
				overlayReq.Name = "del-peer-no-existing"
				ovc.vxlanPeers[overlayReq.String()] = &overlayExistingNeigh
				ovc.podToNode[overlayReq.String()] = overlayTestPod.Spec.NodeName
				ovc.vxlanNodes[overlayTestPod.Spec.NodeName] = overlayExistingNeigh.IP.String()
				Eventually(func() error { _, err := ovc.Reconcile(context.TODO(), overlayReq); return err }).Should(BeNil())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeFalse())
				//Check that we remove the tuple: (nodeName, nodeIP)
				_, ok = ovc.vxlanNodes[overlayTestPod.Spec.NodeName]
				Expect(ok).Should(BeFalse())
				//Check that we remove the tuple: (req.string, nodeName)
				_, ok = ovc.podToNode[overlayReq.String()]
				Expect(ok).Should(BeFalse())
			})
		})
	})

	Describe("testing addPeer function", func() {
		Context("when input parameters are incorrect", func() {
			It("incorrect MAC address, should return false and error", func() {
				overlayTestPod.Status.PodIP = overlayPodIP
				ovc.addAnnotation(overlayTestPod, overlayAnnKey, "wrongMAC")
				added, err := ovc.addPeer(overlayReq, overlayTestPod)
				Expect(err).To(HaveOccurred())
				Expect(added).Should(BeFalse())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeFalse())
			})

			It("incorrect IP address, should return false and error", func() {
				added, err := ovc.addPeer(overlayReq, overlayTestPod)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(&liqoerrors.ParseIPError{IPToBeParsed: ""}))
				Expect(added).Should(BeFalse())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeFalse())
			})
		})

		Context("when peer does not exist", func() {
			It("should return false and nil", func() {
				overlayTestPod.Status.PodIP = overlayPodIP
				added, err := ovc.addPeer(overlayReq, overlayTestPod)
				Expect(err).NotTo(HaveOccurred())
				Expect(added).Should(BeTrue())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeTrue())
				//Check that we save the tuple: (nodeName, nodeIP)
				nodeIP, ok := ovc.vxlanNodes[overlayTestPod.Spec.NodeName]
				Expect(ok).Should(BeTrue())
				Expect(nodeIP).Should(Equal(overlayTestPod.Status.PodIP))
				//Check that we save the tuple: (req.string, nodeName)
				nodeName, ok := ovc.podToNode[overlayReq.String()]
				Expect(ok).Should(BeTrue())
				Expect(nodeName).Should(Equal(overlayTestPod.Spec.NodeName))
				// Check that the fdbs entries have been inserted.
				fdbs, err := netlink.NeighList(ovc.vxlanDev.Link.Index, syscall.AF_BRIDGE)
				Expect(err).To(BeNil())
				var checkEntries = func() bool {
					var fdb1, fdb2 bool
					for _, f := range fdbs {
						if f.HardwareAddr.String() == overlayTestPod.GetAnnotations()[overlayAnnKey] {
							fdb1 = true
						}
					}
					for _, f := range fdbs {
						if f.HardwareAddr.String() == "00:00:00:00:00:00" && f.IP.String() == overlayPodIP {
							fdb2 = true
						}
					}
					if fdb2 && fdb1 {
						return true
					}
					return false
				}
				Expect(checkEntries()).Should(BeTrue())
			})
		})

		Context("when peer does exist", func() {
			It("should return false and nil", func() {
				ovc.vxlanPeers[overlayReq.String()] = &overlayExistingNeigh
				overlayTestPod.Status.PodIP = overlayPeerIP
				ovc.addAnnotation(overlayTestPod, overlayAnnKey, overlayPeerMAC)
				added, err := ovc.addPeer(overlayReq, overlayTestPod)
				Expect(err).NotTo(HaveOccurred())
				Expect(added).Should(BeFalse())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeTrue())
				//Check that the entries are only two.
				fdbs, err := netlink.NeighList(ovc.vxlanDev.Link.Index, syscall.AF_BRIDGE)
				Expect(err).To(BeNil())
				Expect(len(fdbs)).Should(BeNumerically("==", 2))

			})
		})
	})

	Describe("testing delPeer function", func() {
		Context("when peer does not exist", func() {
			It("should return false and nil", func() {
				deleted, err := ovc.delPeer(overlayReq)
				Expect(err).NotTo(HaveOccurred())
				Expect(deleted).Should(BeFalse())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeFalse())
			})
		})

		Context("when peer does exist", func() {
			It("should return true and nil", func() {
				ovc.vxlanPeers[overlayReq.String()] = &overlayExistingNeigh
				ovc.podToNode[overlayReq.String()] = overlayTestPod.Spec.NodeName
				ovc.vxlanNodes[overlayTestPod.Spec.NodeName] = overlayExistingNeigh.IP.String()
				overlayTestPod.Status.PodIP = overlayPeerIP
				ovc.addAnnotation(overlayTestPod, overlayAnnKey, overlayPeerMAC)
				deleted, err := ovc.delPeer(overlayReq)
				Expect(err).NotTo(HaveOccurred())
				Expect(deleted).Should(BeTrue())
				_, ok := ovc.vxlanPeers[overlayReq.String()]
				Expect(ok).Should(BeFalse())
				//Check that we remove the tuple: (nodeName, nodeIP)
				_, ok = ovc.vxlanNodes[overlayTestPod.Spec.NodeName]
				Expect(ok).Should(BeFalse())
				//Check that we remove the tuple: (req.string, nodeName)
				_, ok = ovc.podToNode[overlayReq.String()]
				Expect(ok).Should(BeFalse())
				//Check that the entries have been removed.
				fdbs, err := netlink.NeighList(ovc.vxlanDev.Link.Index, syscall.AF_BRIDGE)
				Expect(err).To(BeNil())
				Expect(len(fdbs)).Should(BeNumerically("==", 0))
			})
		})
	})

	Describe("testing addAnnotation function", func() {
		Context("when annotation already exists", func() {
			It("annotation is the same, should return false", func() {
				ok := ovc.addAnnotation(overlayTestPod, overlayAnnKey, overlayAnnValue)
				Expect(ok).Should(BeFalse())
				Expect(len(overlayTestPod.GetAnnotations())).Should(BeNumerically("==", 1))
			})

			It("annotation value is outdated", func() {
				newValue := "differentValue"
				ok := ovc.addAnnotation(overlayTestPod, overlayAnnKey, newValue)
				Expect(ok).Should(BeTrue())
				Expect(len(overlayTestPod.GetAnnotations())).Should(BeNumerically("==", 1))
				value, ok := overlayTestPod.GetAnnotations()[overlayAnnKey]
				Expect(value).Should(Equal(newValue))
				Expect(ok).Should(BeTrue())
			})
		})
		Context("when annotation with given key does not exist", func() {
			It("should return true", func() {
				newKey := "newTestingKey"
				ok := ovc.addAnnotation(overlayTestPod, newKey, overlayAnnValue)
				Expect(ok).Should(BeTrue())
				Expect(len(overlayTestPod.GetAnnotations())).Should(BeNumerically("==", 2))
				value, ok := overlayTestPod.GetAnnotations()[overlayAnnKey]
				Expect(value).Should(Equal(overlayAnnValue))
				Expect(ok).Should(BeTrue())
			})
		})
	})

	Describe("testing getAnnotation function", func() {
		Context("annotation exists", func() {
			It("should return the correct value", func() {
				value := ovc.getAnnotationValue(overlayTestPod, overlayAnnKey)
				Expect(value).Should(Equal(overlayAnnValue))
			})
		})
	})
	Describe("testing podFilter function", func() {
		Context("when object is not a pod", func() {
			It("should return false", func() {
				// Create a service object
				s := corev1.Service{}
				ok := ovc.podFilter(&s)
				Expect(ok).Should(BeFalse())
			})
		})

		Context("when pod has not the right labels", func() {
			It("should return false", func() {
				// Remove the labels from the test pod.
				overlayTestPod.SetLabels(nil)
				ok := ovc.podFilter(overlayTestPod)
				Expect(ok).Should(BeFalse())
			})
		})

		Context("when pod has the right labels", func() {
			It("and has same ip, should return true", func() {
				// Add ip address to the test pod.
				overlayTestPod.Status.PodIP = overlayPodIP
				ok := ovc.podFilter(overlayTestPod)
				Expect(ok).Should(BeTrue())
			})

			It("has not the same ip and has not been annotated, should return false", func() {
				overlayTestPod.SetAnnotations(nil)
				ok := ovc.podFilter(overlayTestPod)
				Expect(ok).Should(BeFalse())
			})

			It("has not the same ip and has  been annotated, should return true", func() {
				ok := ovc.podFilter(overlayTestPod)
				Expect(ok).Should(BeTrue())
			})
		})
	})
})

func addFdb(neighbor overlay.Neighbor, ifaceIndex int) error {
	return netlink.NeighAdd(&netlink.Neigh{
		LinkIndex:    ifaceIndex,
		State:        netlink.NUD_PERMANENT | netlink.NUD_NOARP,
		Family:       syscall.AF_BRIDGE,
		Flags:        netlink.NTF_SELF,
		Type:         netlink.NDA_DST,
		IP:           neighbor.IP,
		HardwareAddr: neighbor.MAC,
	})
}

func flushFdbTable(index int) error {
	fdbs, err := netlink.NeighList(index, syscall.AF_BRIDGE)
	if err != nil {
		return err
	}
	for _, f := range fdbs {
		if err := netlink.NeighDel(&f); err != nil {
			return err
		}
	}
	return nil
}

func setupOverlayTestEnv() error {
	if err := clientgoscheme.AddToScheme(overlayScheme); err != nil {
		return err
	}
	overlayEnvTest = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "deployments", "liqo", "crds")},
	}
	config, err := overlayEnvTest.Start()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:             overlayScheme,
		MetricsBindAddress: "0",
	})
	go func() {
		if err = mgr.Start(context.Background()); err != nil {
			panic(err)
		}
	}()
	k8sClient = mgr.GetClient()
	// Create overlay test namespace.
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: overlayNamespace,
		},
	}
	Eventually(func() error { return k8sClient.Create(context.TODO(), namespace) }).Should(BeNil())

	// Create symmetric routing test namespace.
	namespace = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: srcNamespace,
		},
	}
	Eventually(func() error { return k8sClient.Create(context.TODO(), namespace) }).Should(BeNil())
	return nil
}
