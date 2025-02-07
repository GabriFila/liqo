// Copyright 2019-2021 The Liqo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package foreignclusteroperator

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	discoveryv1alpha1 "github.com/liqotech/liqo/apis/discovery/v1alpha1"
	crdreplicator "github.com/liqotech/liqo/internal/crdReplicator"
	"github.com/liqotech/liqo/pkg/utils"
	foreigncluster "github.com/liqotech/liqo/pkg/utils/foreignCluster"
)

// ensureResourceRequest ensures the presence of a resource request to be sent to the specified ForeignCluster.
func (r *ForeignClusterReconciler) ensureResourceRequest(ctx context.Context,
	foreignCluster *discoveryv1alpha1.ForeignCluster) (*discoveryv1alpha1.ResourceRequest, error) {
	klog.Infof("[%v] ensuring ResourceRequest existence", foreignCluster.Spec.ClusterIdentity.ClusterID)

	localClusterID := r.clusterID.GetClusterID()
	remoteClusterID := foreignCluster.Spec.ClusterIdentity.ClusterID
	localNamespace := foreignCluster.Status.TenantNamespace.Local

	authURL, err := foreigncluster.GetHomeAuthURL(ctx, r.LiqoNamespacedClient, r.Client,
		r.authServiceAddressOverride, r.authServicePortOverride, r.liqoNamespace)
	if err != nil {
		return nil, err
	}

	resourceRequest := &discoveryv1alpha1.ResourceRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      localClusterID,
			Namespace: localNamespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, resourceRequest, func() error {
		labels := resourceRequest.GetLabels()
		requiredLabels := resourceRequestLabels(remoteClusterID)
		if labels == nil {
			labels = requiredLabels
		} else {
			for k, v := range requiredLabels {
				labels[k] = v
			}
		}
		resourceRequest.SetLabels(labels)

		resourceRequest.Spec = discoveryv1alpha1.ResourceRequestSpec{
			ClusterIdentity: discoveryv1alpha1.ClusterIdentity{
				ClusterID:   localClusterID,
				ClusterName: r.clusterName,
			},
			AuthURL: authURL,
		}

		return controllerutil.SetControllerReference(foreignCluster, resourceRequest, r.Scheme)
	})
	if err != nil {
		klog.Error(err)
		return nil, err
	}
	klog.V(utils.FromResult(result)).Infof("[%v] ensured the existence of ResourceRequest (with %v operation)",
		remoteClusterID, result)

	return resourceRequest, nil
}

// deleteResourceRequest deletes a resource request related to the specified ForeignCluster.
func (r *ForeignClusterReconciler) deleteResourceRequest(ctx context.Context, foreignCluster *discoveryv1alpha1.ForeignCluster) error {
	klog.Infof("[%v] ensuring that the ResourceRequest does not exist", foreignCluster.Spec.ClusterIdentity.ClusterID)
	if err := r.Client.DeleteAllOf(ctx,
		&discoveryv1alpha1.ResourceRequest{}, client.MatchingLabels(resourceRequestLabels(foreignCluster.Spec.ClusterIdentity.ClusterID)),
		client.InNamespace(foreignCluster.Status.TenantNamespace.Local)); err != nil {
		klog.Error(err)
		return err
	}
	klog.Infof("[%v] ensured that the ResourceRequest does not exist", foreignCluster.Spec.ClusterIdentity.ClusterID)
	return nil
}

func resourceRequestLabels(remoteClusterID string) map[string]string {
	return map[string]string{
		crdreplicator.LocalLabelSelector: "true",
		crdreplicator.DestinationLabel:   remoteClusterID,
	}
}
