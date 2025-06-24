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

// MetricsClient provides access to resource metrics
type MetricsClient interface {
	// GetResourceMetric gets the given resource metric for the specified pods
	GetResourceMetric(ctx context.Context, pods []corev1.Pod, resourceName corev1.ResourceName) ([]*resource.Quantity, error)
}

// CustomMetricsClient provides access to custom metrics
type CustomMetricsClient interface {
	// GetPodsMetric gets the given custom metric for the specified pods
	GetPodsMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier, pods []corev1.Pod) ([]*resource.Quantity, error)

	// GetObjectMetric gets the given custom metric for the specified object
	GetObjectMetric(ctx context.Context, namespace string, object v1alpha1.CrossVersionObjectReference, metric v1alpha1.MetricIdentifier) (*resource.Quantity, error)
}

// ExternalMetricsClient provides access to external metrics
type ExternalMetricsClient interface {
	// GetExternalMetric gets the given external metric
	GetExternalMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier) ([]*resource.Quantity, error)
}
