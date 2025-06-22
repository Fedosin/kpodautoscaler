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

package metrics

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

// DummyMetricsClient is a dummy implementation of MetricsClient for testing
type DummyMetricsClient struct{}

// GetResourceMetric returns dummy metrics
func (d *DummyMetricsClient) GetResourceMetric(ctx context.Context, pods []corev1.Pod, resourceName corev1.ResourceName) ([]PodMetric, error) {
	// Return dummy metrics - 50% utilization for all pods
	metrics := make([]PodMetric, len(pods))
	for i, pod := range pods {
		// Get the resource request
		var totalRequest int64
		for _, container := range pod.Spec.Containers {
			if request, ok := container.Resources.Requests[resourceName]; ok {
				totalRequest += request.MilliValue()
			}
		}

		// Return 50% of request as current usage
		value := resource.NewMilliQuantity(totalRequest/2, resource.DecimalSI)
		metrics[i] = PodMetric{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			Value:     value,
		}
	}
	return metrics, nil
}

// DummyCustomMetricsClient is a dummy implementation of CustomMetricsClient for testing
type DummyCustomMetricsClient struct{}

// GetPodsMetric returns dummy custom metrics
func (d *DummyCustomMetricsClient) GetPodsMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier, pods []corev1.Pod) ([]PodMetric, error) {
	// Return dummy metrics - 100 units per pod
	metrics := make([]PodMetric, len(pods))
	for i, pod := range pods {
		value := resource.NewQuantity(100, resource.DecimalSI)
		metrics[i] = PodMetric{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			Value:     value,
		}
	}
	return metrics, nil
}

// GetObjectMetric returns a dummy object metric
func (d *DummyCustomMetricsClient) GetObjectMetric(ctx context.Context, namespace string, object v1alpha1.CrossVersionObjectReference, metric v1alpha1.MetricIdentifier) (*resource.Quantity, error) {
	// Return dummy metric - 1000 units
	return resource.NewQuantity(1000, resource.DecimalSI), nil
}

// DummyExternalMetricsClient is a dummy implementation of ExternalMetricsClient for testing
type DummyExternalMetricsClient struct{}

// GetExternalMetric returns dummy external metrics
func (d *DummyExternalMetricsClient) GetExternalMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier) ([]*resource.Quantity, error) {
	// Return dummy metric - 500 units
	value := resource.NewQuantity(500, resource.DecimalSI)
	return []*resource.Quantity{value}, nil
}
