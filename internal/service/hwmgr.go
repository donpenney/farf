package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/openshift-kni/generic-plugin/internal/controller/utils"
	hwmgmtv1alpha1 "github.com/openshift-kni/oran-o2ims/api/hardwaremanagement/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type HwMgrServiceBuilder struct {
	client.Client
	logger *slog.Logger
}

type nodelist map[string]hwmgmtv1alpha1.Node
type cloudNodes map[string]nodelist

type HwMgrService struct {
	client.Client
	logger *slog.Logger

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
		Client: b.Client,
		logger: b.logger,
		nodes:  make(cloudNodes),
	}

	b.logger.Debug(
		"HwMgrService build:",
	)

	result = service
	return
}

func (h *HwMgrService) CreateNodePool(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) error {
	cloudID := nodepool.Spec.CloudID

	h.logger.InfoContext(ctx, "Processing CreateNodePool request:",
		"cloudID", cloudID,
	)

	// Nothing to do yet

	return nil
}

func (h *HwMgrService) CheckNodePoolProgress(ctx context.Context, nodepool *hwmgmtv1alpha1.NodePool) error {
	cloudID := nodepool.Spec.CloudID

	for _, nodegroup := range nodepool.Spec.NodeGroup {
		h.logger.InfoContext(ctx, "Processing CheckNodePoolProgress request:",
			"cloudID", cloudID,
			"nodegroup name", nodegroup.Name,
		)

		// Has cloud been setup?
		if _, ok := h.nodes[cloudID]; !ok {
			h.logger.InfoContext(ctx, "Initializing map for cloud", "cloudID", cloudID)
			h.nodes[cloudID] = make(nodelist)
		}

		for i := 0; i < nodegroup.Size; i++ {
			// Create a node
			nodename := fmt.Sprintf("%s-%s-%d", cloudID, nodegroup.Name, i)

			// Has node been created?
			if _, ok := h.nodes[cloudID][nodename]; ok {
				h.logger.InfoContext(ctx, "Node previously created:",
					"cloudID", cloudID,
					"nodegroup name", nodegroup.Name,
					"nodename", nodename,
				)
				// Move onto the next node
				continue
			}

			h.logger.InfoContext(ctx, "Creating node:",
				"cloudID", cloudID,
				"nodegroup name", nodegroup.Name,
				"nodename", nodename,
			)

			node := &hwmgmtv1alpha1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodename,
					Namespace: nodepool.Namespace,
				},
				Spec: hwmgmtv1alpha1.NodeSpec{
					NodePool:  cloudID,
					GroupName: nodegroup.Name,
					HwProfile: nodegroup.HwProfile,
				},
			}

			if err := h.Client.Create(ctx, node); err != nil {
				return fmt.Errorf("failed to create Node: %w", err)
			}
			h.nodes[cloudID][nodename] = *node.DeepCopy()

			// Return now, so we're creating one node at a time
			return nil
		}
	}

	// If we get here, all nodes have been created

	// Update pool status
	utils.SetStatusCondition(&nodepool.Status.Conditions,
		utils.NodePoolConditionTypes.Unprovisioned,
		utils.NodePoolConditionReasons.Completed,
		metav1.ConditionFalse,
		"Finished creation")

	utils.SetStatusCondition(&nodepool.Status.Conditions,
		utils.NodePoolConditionTypes.Provisioned,
		utils.NodePoolConditionReasons.Completed,
		metav1.ConditionTrue,
		"Created")

	if updateErr := utils.UpdateK8sCRStatus(ctx, h.Client, nodepool); updateErr != nil {
		return fmt.Errorf("failed to update status for NodePool %s: %w", nodepool.Name, updateErr)
	}

	return nil
}
