/*
Copyright 2025 The KPodAutoscaler Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resourcerequests

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPodResourceRequests returns the total resource requests for all containers in a pod template
func GetPodResourceRequests(podTemplate *corev1.PodTemplateSpec, resourceName corev1.ResourceName) resource.Quantity {
	total := resource.NewQuantity(0, resource.DecimalSI)

	for _, container := range podTemplate.Spec.Containers {
		if req, found := container.Resources.Requests[resourceName]; found {
			total.Add(req)
		}
	}

	return *total
}

// GetDeploymentPodResourceRequests fetches a deployment and returns its pod resource requests
func GetDeploymentPodResourceRequests(ctx context.Context, c client.Client, namespace, name string, resourceName corev1.ResourceName) (resource.Quantity, error) {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.Get(ctx, key, deployment); err != nil {
		return resource.Quantity{}, err
	}

	return GetPodResourceRequests(&deployment.Spec.Template, resourceName), nil
}

// GetStatefulSetPodResourceRequests fetches a statefulset and returns its pod resource requests
func GetStatefulSetPodResourceRequests(ctx context.Context, c client.Client, namespace, name string, resourceName corev1.ResourceName) (resource.Quantity, error) {
	statefulSet := &appsv1.StatefulSet{}
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}

	if err := c.Get(ctx, key, statefulSet); err != nil {
		return resource.Quantity{}, err
	}

	return GetPodResourceRequests(&statefulSet.Spec.Template, resourceName), nil
}
