package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/openshift-kni/generic-plugin/internal/controller/utils"
	hwmgmtv1alpha1 "github.com/openshift-kni/oran-o2ims/api/hardwaremanagement/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type hwProfile struct {
	Name  string   `json:"name" yaml:"name"`
	Nodes []string `json:"nodes" yaml:"nodes"`
}

type cmHwProfile struct {
	Profiles []hwProfile `json:"profiles" yaml:"profiles"`
}

type allocatedCloud struct {
	CloudID    string              `json:"cloudID" yaml:"cloudID"`
	Nodegroups map[string][]string `json:"nodegroups" yaml:"nodegroups"`
}

type cmAllocatedNodes struct {
	Clouds []allocatedCloud `json:"clouds" yaml:"clouds"`
}

const (
	hwprofilesKey = "hwprofiles"
	allocatedKey  = "allocated"
	cmName        = "nodelist"
)

func getFreeNodesInProfile(hwprof cmHwProfile, allocated cmAllocatedNodes, profname string) (freenodes []string) {
	inuse := make(map[string]bool)
	for _, cloud := range allocated.Clouds {
		for groupname := range cloud.Nodegroups {
			for _, nodename := range cloud.Nodegroups[groupname] {
				inuse[nodename] = true
			}
		}
	}

	for _, profile := range hwprof.Profiles {
		if profile.Name != profname {
			continue
		}

		for _, nodename := range profile.Nodes {
			if _, used := inuse[nodename]; !used {
				freenodes = append(freenodes, nodename)
			}
		}
	}

	return
}

/////////////

type HwMgrServiceBuilder struct {
	client.Client
	logger *slog.Logger
}

type nodelist map[string]hwmgmtv1alpha1.Node
type cloudNodes map[string]nodelist

type HwMgrService struct {
	client.Client
	logger    *slog.Logger
	namespace string

	nodes cloudNodes
}

func NewHwMgrService() *HwMgrServiceBuilder {
	return &HwMgrServiceBuilder{}
}

func (b *HwMgrServiceBuilder) SetClient(
	value client.Client) *HwMgrServiceBuilder {
	b.Client = value
	return b
}

func (b *HwMgrServiceBuilder) SetLogger(
	value *slog.Logger) *HwMgrServiceBuilder {
	b.logger = value
	return b
}

func (b *HwMgrServiceBuilder) Build(ctx context.Context) (
	result *HwMgrService, err error) {
	if b.logger == nil {
		err = errors.New("logger is mandatory")
		return
	}

	service := &HwMgrService{
		Client:    b.Client,
		logger:    b.logger,
		namespace: os.Getenv("MY_POD_NAMESPACE"),
		nodes:     make(cloudNodes),
	}

	result = service
	return
}

func (h *HwMgrService) GetCurrentResources(ctx context.Context) (
	cm *corev1.ConfigMap, hwprof cmHwProfile, allocated cmAllocatedNodes, err error) {
	cm, err = utils.GetConfigmap(ctx, h.Client, cmName, h.namespace)
	if err != nil {
		err = fmt.Errorf("unable to get configmap: %w", err)
		return
	}

	hwprof, err = utils.ExtractDataFromConfigMap[cmHwProfile](cm, hwprofilesKey)
	if err != nil {
		err = fmt.Errorf("unable to parse hwprofiles from configmap: %w", err)
		return
	}

	allocated, err = utils.ExtractDataFromConfigMap[cmAllocatedNodes](cm, allocatedKey)
	if err != nil {
		// Allocated node field may not be present
		h.logger.InfoContext(ctx, "unable to parse allocated node info from configmap")
		err = nil
	}

	return
}

func (h *HwMgrService) CreateNodePool(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) error {
	cloudID := nodepool.Spec.CloudID

	h.logger.InfoContext(ctx, "Processing CreateNodePool request:",
		"cloudID", cloudID,
	)

	_, hwprof, allocated, err := h.GetCurrentResources(ctx)
	if err != nil {
		return fmt.Errorf("unable to get current resources")
	}

	for _, nodegroup := range nodepool.Spec.NodeGroup {
		freenodes := getFreeNodesInProfile(hwprof, allocated, nodegroup.HwProfile)
		if nodegroup.Size > len(freenodes) {
			return fmt.Errorf("not enough free resources in group %s: freenodes=%d", nodegroup.HwProfile, len(freenodes))
		}
	}

	return nil
}

func (h *HwMgrService) AllocateNode(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) error {
	cloudID := nodepool.Spec.CloudID

	// Inject a delay before allocating node
	time.Sleep(10 * time.Second)

	cm, hwprof, allocated, err := h.GetCurrentResources(ctx)
	if err != nil {
		return fmt.Errorf("unable to get current resources")
	}

	var cloud *allocatedCloud
	for i, iter := range allocated.Clouds {
		if iter.CloudID == cloudID {
			cloud = &allocated.Clouds[i]
			break
		}
	}
	if cloud == nil {
		// The cloud wasn't found in the list, so create a new entry
		allocated.Clouds = append(allocated.Clouds, allocatedCloud{CloudID: cloudID, Nodegroups: make(map[string][]string)})
		cloud = &allocated.Clouds[len(allocated.Clouds)-1]
	}

	// Check available resources
	for _, nodegroup := range nodepool.Spec.NodeGroup {
		used := cloud.Nodegroups[nodegroup.Name]
		remaining := nodegroup.Size - len(used)
		if remaining <= 0 {
			// This group is allocated
			h.logger.InfoContext(ctx, "nodegroup is fully allocated", "nodegroup", nodegroup.Name)
			continue
		}

		freenodes := getFreeNodesInProfile(hwprof, allocated, nodegroup.HwProfile)
		if remaining > len(freenodes) {
			return fmt.Errorf("not enough free resources remaining in group %s", nodegroup.HwProfile)
		}

		// Grab the first node
		nodename := freenodes[0]
		cloud.Nodegroups[nodegroup.Name] = append(cloud.Nodegroups[nodegroup.Name], nodename)

		// Update the configmap
		yamlString, err := yaml.Marshal(&allocated)
		if err != nil {
			return fmt.Errorf("unable to marshal allocated data: %w", err)
		}
		cm.Data[allocatedKey] = string(yamlString)
		if err := h.Client.Update(ctx, cm); err != nil {
			return fmt.Errorf("failed to update configmap: %w", err)
		}

		if err := h.CreateNode(ctx, cloudID, nodename, nodegroup.Name, nodegroup.HwProfile); err != nil {
			return fmt.Errorf("failed to create allocated node: %w", err)
		}
	}

	return nil
}

func (h *HwMgrService) CreateNode(ctx context.Context, cloudID, nodename, groupname, hwprofile string) error {

	h.logger.InfoContext(ctx, "Creating node:",
		"cloudID", cloudID,
		"nodegroup name", groupname,
		"nodename", nodename,
	)

	node := &hwmgmtv1alpha1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodename,
			Namespace: h.namespace,
		},
		Spec: hwmgmtv1alpha1.NodeSpec{
			NodePool:  cloudID,
			GroupName: groupname,
			HwProfile: hwprofile,
		},
	}

	if err := h.Client.Create(ctx, node); err != nil {
		return fmt.Errorf("failed to create Node: %w", err)
	}

	return nil
}

func (h *HwMgrService) DeleteNode(ctx context.Context, nodename string) error {

	h.logger.InfoContext(ctx, "Deleting node:",
		"nodename", nodename,
	)

	node := &hwmgmtv1alpha1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodename,
			Namespace: h.namespace,
		},
	}

	if err := h.Client.Delete(ctx, node); err != nil {
		return fmt.Errorf("failed to delete Node: %w", err)
	}

	return nil
}

func (h *HwMgrService) IsNodeFullyAllocated(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) (bool, error) {
	cloudID := nodepool.Spec.CloudID

	_, hwprof, allocated, err := h.GetCurrentResources(ctx)
	if err != nil {
		return false, fmt.Errorf("unable to get current resources")
	}

	var cloud *allocatedCloud
	for i, iter := range allocated.Clouds {
		if iter.CloudID == cloudID {
			cloud = &allocated.Clouds[i]
			break
		}
	}
	if cloud == nil {
		// Cloud has not been allocated yet
		return false, nil
	}

	// Check allocated resources
	for _, nodegroup := range nodepool.Spec.NodeGroup {
		used := cloud.Nodegroups[nodegroup.Name]
		remaining := nodegroup.Size - len(used)
		if remaining <= 0 {
			// This group is allocated
			h.logger.InfoContext(ctx, "nodegroup is fully allocated", "nodegroup", nodegroup.Name)
			continue
		}

		freenodes := getFreeNodesInProfile(hwprof, allocated, nodegroup.HwProfile)
		if remaining > len(freenodes) {
			return false, fmt.Errorf("not enough free resources remaining in group %s", nodegroup.HwProfile)
		}

		// Cloud is not fully allocated, and there are resources available
		return false, nil
	}

	return true, nil
}

func (h *HwMgrService) GetAllocatedNodes(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) (allocatedNodes []string, err error) {
	cloudID := nodepool.Spec.CloudID

	_, _, allocated, err := h.GetCurrentResources(ctx)
	if err != nil {
		err = fmt.Errorf("unable to get current resources")
		return
	}

	var cloud *allocatedCloud
	for i, iter := range allocated.Clouds {
		if iter.CloudID == cloudID {
			cloud = &allocated.Clouds[i]
			break
		}
	}
	if cloud == nil {
		// Cloud has not been allocated yet
		return
	}

	// Get allocated resources
	for _, nodegroup := range nodepool.Spec.NodeGroup {
		allocatedNodes = append(allocatedNodes, cloud.Nodegroups[nodegroup.Name]...)
	}

	slices.Sort(allocatedNodes)
	return
}

func (h *HwMgrService) CheckNodePoolProgress(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) (full bool, err error) {
	cloudID := nodepool.Spec.CloudID

	if full, err = h.IsNodeFullyAllocated(ctx, nodepool); err != nil {
		err = fmt.Errorf("failed to check nodepool allocation: %w", err)
		return
	} else if full {
		// Node is fully allocated
		return
	}

	for _, nodegroup := range nodepool.Spec.NodeGroup {
		h.logger.InfoContext(ctx, "Allocating node for CheckNodePoolProgress request:",
			"cloudID", cloudID,
			"nodegroup name", nodegroup.Name,
		)

		if err = h.AllocateNode(ctx, nodepool); err != nil {
			err = fmt.Errorf("failed to allocate node: %w", err)
			return
		}
	}

	return
}

func (h *HwMgrService) ReleaseNodePool(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) error {
	cloudID := nodepool.Spec.CloudID

	h.logger.InfoContext(ctx, "Processing ReleaseNodePool request:",
		"cloudID", cloudID,
	)

	cm, _, allocated, err := h.GetCurrentResources(ctx)
	if err != nil {
		return fmt.Errorf("unable to get current resources")
	}

	index := -1
	for i, cloud := range allocated.Clouds {
		if cloud.CloudID == cloudID {
			index = i
			break
		}
	}

	if index == -1 {
		h.logger.InfoContext(ctx, "no allocated nodes found", "cloudID", cloudID)
		return nil
	}

	for groupname := range allocated.Clouds[index].Nodegroups {
		for _, nodename := range allocated.Clouds[index].Nodegroups[groupname] {
			if err := h.DeleteNode(ctx, nodename); err != nil {
				return fmt.Errorf("failed to delete node %s: %w", nodename, err)
			}
		}
	}

	allocated.Clouds = slices.Delete[[]allocatedCloud](allocated.Clouds, index, index+1)

	// Update the configmap
	yamlString, err := yaml.Marshal(&allocated)
	if err != nil {
		return fmt.Errorf("unable to marshal allocated data: %w", err)
	}
	cm.Data[allocatedKey] = string(yamlString)
	if err := h.Client.Update(ctx, cm); err != nil {
		return fmt.Errorf("failed to update configmap: %w", err)
	}

	return nil
}
