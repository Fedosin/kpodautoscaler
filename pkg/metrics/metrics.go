/*
Copyright 2024.

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

package metrics

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

// MetricsClient provides access to various metrics APIs
type MetricsClient struct {
	client.Client
	restMapper meta.RESTMapper
}

// NewMetricsClient creates a new  metrics client
func NewMetricsClient(c client.Client, mapper meta.RESTMapper) *MetricsClient {
	// For simplicity, we'll just store the client and mapper
	// In a real implementation, you'd create the metrics client from the config
	return &MetricsClient{
		Client:     c,
		restMapper: mapper,
	}
}

// GetResourceMetric gets CPU or memory metrics for pods
func (mc *MetricsClient) GetResourceMetric(ctx context.Context, pods []corev1.Pod, resourceName corev1.ResourceName) ([]*resource.Quantity, error) {
	values := make([]*resource.Quantity, 0, len(pods))

	// For simplicity, return mock data
	// In a real implementation, you'd query the metrics server
	for range pods {
		// Return 100m CPU or 100Mi memory as mock values
		if resourceName == corev1.ResourceCPU {
			values = append(values, resource.NewScaledQuantity(100, resource.Milli))
		} else {
			values = append(values, resource.NewScaledQuantity(100, resource.Mega))
		}
	}

	return values, nil
}

// GetPodsMetric gets custom metrics for pods
func (mc *MetricsClient) GetPodsMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier, pods []corev1.Pod) ([]*resource.Quantity, error) {
	values := make([]*resource.Quantity, 0, len(pods))

	// For simplicity, return mock data
	for range pods {
		values = append(values, resource.NewScaledQuantity(50, resource.Milli))
	}

	return values, nil
}

// GetObjectMetric gets metrics for a Kubernetes object
func (mc *MetricsClient) GetObjectMetric(ctx context.Context, namespace string, object v1alpha1.CrossVersionObjectReference, metric v1alpha1.MetricIdentifier) (*resource.Quantity, error) {
	// For simplicity, return mock data
	return resource.NewScaledQuantity(100, resource.Milli), nil
}

// GetExternalMetric gets external metrics
func (mc *MetricsClient) GetExternalMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier) ([]*resource.Quantity, error) {
	// For simplicity, return mock data
	return []*resource.Quantity{resource.NewScaledQuantity(200, resource.Milli)}, nil
}
