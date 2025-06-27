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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/restmapper"
	"k8s.io/metrics/pkg/client/clientset/versioned"
	custommetrics "k8s.io/metrics/pkg/client/custom_metrics"
	externalmetrics "k8s.io/metrics/pkg/client/external_metrics"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

// MetricsClient provides access to various metrics APIs
type MetricsClient struct {
	client.Client
	restMapper meta.RESTMapper

	mClient  versioned.Interface
	cmClient custommetrics.CustomMetricsClient
	emClient externalmetrics.ExternalMetricsClient
}

// NewMetricsClient creates a new  metrics client
func NewMetricsClient(c client.Client, mapper meta.RESTMapper) *MetricsClient {
	config := ctrl.GetConfigOrDie()
	emClient, err := externalmetrics.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	cmClient, err := NewCustomMetricsClient()
	if err != nil {
		panic(err)
	}

	mClient, err := versioned.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	return &MetricsClient{
		Client:     c,
		restMapper: mapper,
		mClient:    mClient,
		emClient:   emClient,
		cmClient:   cmClient,
	}
}

// NewCustomMetricsClient builds and returns a CustomMetricsClient.
func NewCustomMetricsClient() (custommetrics.CustomMetricsClient, error) {
	cfg := ctrl.GetConfigOrDie()

	// Create Discovery client
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating discovery client: %w", err)
	}

	// Build RESTMapper from available API group resources
	grs, err := restmapper.GetAPIGroupResources(disco)
	if err != nil {
		return nil, fmt.Errorf("getting API group resources: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(grs)

	// Prepare AvailableAPIsGetter and CustomMetricsClient
	available := custommetrics.NewAvailableAPIsGetter(disco)
	customMetricsClient := custommetrics.NewForConfig(cfg, mapper, available)

	// Keep cache fresh:
	stopCh := make(chan struct{})
	go custommetrics.PeriodicallyInvalidate(available, 10*time.Minute, stopCh)

	return customMetricsClient, nil
}

// GetResourceMetric gets CPU or memory metrics for pods
func (mc *MetricsClient) GetResourceMetric(ctx context.Context, pods []corev1.Pod, resourceName corev1.ResourceName) ([]*resource.Quantity, error) {
	values := make([]*resource.Quantity, 0, len(pods))

	if len(pods) == 0 {
		return values, nil
	}

	for _, pod := range pods {
		podMetrics, err := mc.mClient.MetricsV1beta1().PodMetricses(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get metrics for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		}

		podTotalUsage := resource.NewQuantity(0, resource.DecimalSI)
		for _, container := range podMetrics.Containers {
			if usage, found := container.Usage[resourceName]; found {
				podTotalUsage.Add(usage)
			}
		}
		values = append(values, podTotalUsage)
	}

	return values, nil
}

// GetPodsMetric gets custom metrics for pods
func (mc *MetricsClient) GetPodsMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier, pods []corev1.Pod) ([]*resource.Quantity, error) {
	metricsIface := mc.cmClient.NamespacedMetrics(namespace)

	var err error

	selector := labels.NewSelector()
	if metric.Selector != nil {
		selector, err = metav1.LabelSelectorAsSelector(metric.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %v", err)
		}
	}

	groupKind := schema.GroupKind{Group: "", Kind: "Pod"}

	values := make([]*resource.Quantity, 0, len(pods))

	for _, pod := range pods {
		metricValue, err := metricsIface.GetForObject(groupKind, pod.Name, metric.Name, selector)
		if err != nil {
			return nil, fmt.Errorf("error fetching custom metric %q: %v", metric.Name, err)
		}

		values = append(values, &metricValue.Value)
	}

	return values, nil
}

// GetObjectMetric gets metrics for a Kubernetes object
func (mc *MetricsClient) GetObjectMetric(ctx context.Context, namespace string, object v1alpha1.CrossVersionObjectReference, metric v1alpha1.MetricIdentifier) (*resource.Quantity, error) {
	metricsIface := mc.cmClient.NamespacedMetrics(namespace)

	var err error

	selector := labels.NewSelector()
	if metric.Selector != nil {
		selector, err = metav1.LabelSelectorAsSelector(metric.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %v", err)
		}
	}

	group, err := groupFromAPIVersion(object.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("error parsing group from API version: %v", err)
	}

	metricValue, err := metricsIface.GetForObject(schema.GroupKind{Group: group, Kind: object.Kind}, object.Name, metric.Name, selector)
	if err != nil {
		return nil, fmt.Errorf("error fetching custom metric %q: %v", metric.Name, err)
	}

	return &metricValue.Value, nil
}

// GetExternalMetric gets external metrics
func (mc *MetricsClient) GetExternalMetric(ctx context.Context, namespace string, metric v1alpha1.MetricIdentifier) ([]*resource.Quantity, error) {
	metricsIface := mc.emClient.NamespacedMetrics(namespace)

	var err error

	selector := labels.NewSelector()
	if metric.Selector != nil {
		selector, err = metav1.LabelSelectorAsSelector(metric.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %v", err)
		}
	}
	metricList, err := metricsIface.List(metric.Name, selector)
	if err != nil {
		return nil, fmt.Errorf("error fetching external metric %q: %v", metric.Name, err)
	}

	values := make([]*resource.Quantity, 0, len(metricList.Items))
	for _, item := range metricList.Items {
		values = append(values, &item.Value)
	}

	return values, nil
}

func groupFromAPIVersion(apiVersion string) (string, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "", err
	}
	// gv.Group is empty for core
	if gv.Group == "" {
		return "core", nil
	}
	return gv.Group, nil
}
