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

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	autoscalingv1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
	"github.com/Fedosin/kpodautoscaler/pkg/libkpa-integration"
	"github.com/Fedosin/kpodautoscaler/pkg/metrics"
)

// KPodAutoscalerReconciler reconciles a KPodAutoscaler object
type KPodAutoscalerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger

	// Metrics clients
	MetricsClient         metrics.MetricsClient
	CustomMetricsClient   metrics.CustomMetricsClient
	ExternalMetricsClient metrics.ExternalMetricsClient

	// Recorder for events
	Recorder record.EventRecorder

	// Track active scaling goroutines
	activeScalersMutex sync.Mutex
	activeScalers      map[types.NamespacedName]*scalerContext
}

// scalerContext holds the context and channel for a per-CR scaling goroutine
type scalerContext struct {
	cancel      context.CancelFunc
	recommendCh chan int32
}

// recommendation holds a scaling recommendation
type recommendation struct {
	replicas int32
	reason   string
}

//+kubebuilder:rbac:groups=autoscaling.io,resources=kpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling.io,resources=kpodautoscalers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=autoscaling.io,resources=kpodautoscalers/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments;replicasets;statefulsets,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments/scale;replicasets/scale;statefulsets/scale,verbs=get;update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list
//+kubebuilder:rbac:groups=custom.metrics.k8s.io,resources=*,verbs=get;list
//+kubebuilder:rbac:groups=external.metrics.k8s.io,resources=*,verbs=get;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *KPodAutoscalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("kpodautoscaler", req.NamespacedName)

	// Fetch the KPodAutoscaler instance
	kpa := &autoscalingv1alpha1.KPodAutoscaler{}
	if err := r.Get(ctx, req.NamespacedName, kpa); err != nil {
		if errors.IsNotFound(err) {
			// Object not found, stop any active scaler
			r.stopScaler(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Set default minReplicas if not specified
	if kpa.Spec.MinReplicas == nil {
		one := int32(1)
		kpa.Spec.MinReplicas = &one
	}

	// Set default metrics if not specified
	if len(kpa.Spec.Metrics) == 0 {
		kpa.Spec.Metrics = []autoscalingv1alpha1.MetricSpec{
			{
				Type: autoscalingv1alpha1.ResourceMetricType,
				Resource: &autoscalingv1alpha1.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv1alpha1.MetricTarget{
						Type:               autoscalingv1alpha1.UtilizationMetricType,
						AverageUtilization: int32Ptr(80),
					},
				},
			},
		}
	}

	// Start or ensure scaler goroutine is running
	r.ensureScaler(ctx, kpa)

	// Process any pending recommendations
	scaler := r.getScaler(req.NamespacedName)
	if scaler != nil {
		select {
		case replicas := <-scaler.recommendCh:
			// Apply the scaling recommendation
			if err := r.applyScale(ctx, kpa, replicas); err != nil {
				log.Error(err, "Failed to apply scale")
				r.updateCondition(kpa, autoscalingv1alpha1.AbleToScale, corev1.ConditionFalse, "FailedUpdateScale", err.Error())
				return ctrl.Result{}, err
			}
		default:
			// No recommendation available
		}
	}

	// Update status
	if err := r.updateStatus(ctx, kpa); err != nil {
		log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	// Requeue to continuously monitor
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KPodAutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.activeScalers = make(map[types.NamespacedName]*scalerContext)

	return ctrl.NewControllerManagedBy(mgr).
		For(&autoscalingv1alpha1.KPodAutoscaler{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(r)
}

// ensureScaler ensures a scaler goroutine is running for the given KPA
func (r *KPodAutoscalerReconciler) ensureScaler(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler) {
	r.activeScalersMutex.Lock()
	defer r.activeScalersMutex.Unlock()

	key := types.NamespacedName{Namespace: kpa.Namespace, Name: kpa.Name}
	if _, exists := r.activeScalers[key]; !exists {
		// Create a new scaler context
		scalerCtx, cancel := context.WithCancel(context.Background())
		recommendCh := make(chan int32, 10)

		r.activeScalers[key] = &scalerContext{
			cancel:      cancel,
			recommendCh: recommendCh,
		}

		// Start the scaler goroutine
		go r.runScaler(scalerCtx, kpa, recommendCh)
	}
}

// stopScaler stops the scaler goroutine for the given KPA
func (r *KPodAutoscalerReconciler) stopScaler(key types.NamespacedName) {
	r.activeScalersMutex.Lock()
	defer r.activeScalersMutex.Unlock()

	if scaler, exists := r.activeScalers[key]; exists {
		scaler.cancel()
		close(scaler.recommendCh)
		delete(r.activeScalers, key)
	}
}

// getScaler returns the scaler context for the given KPA
func (r *KPodAutoscalerReconciler) getScaler(key types.NamespacedName) *scalerContext {
	r.activeScalersMutex.Lock()
	defer r.activeScalersMutex.Unlock()

	return r.activeScalers[key]
}

// runScaler runs the per-CR scaling loop
func (r *KPodAutoscalerReconciler) runScaler(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, recommendCh chan<- int32) {
	log := r.Log.WithValues("kpodautoscaler", types.NamespacedName{Namespace: kpa.Namespace, Name: kpa.Name})
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Initialize autoscalers for each metric
	autoscalers := make([]*libkpa.MetricAutoscaler, 0, len(kpa.Spec.Metrics))
	for _, metric := range kpa.Spec.Metrics {
		autoscaler, err := libkpa.NewMetricAutoscaler(kpa, metric)
		if err != nil {
			log.Error(err, "Failed to create autoscaler for metric", "metric", metric.Type)
			continue
		}
		autoscalers = append(autoscalers, autoscaler)
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("Stopping scaler goroutine")
			return
		case <-ticker.C:
			// Fetch current replica count
			currentReplicas, err := r.getCurrentReplicas(ctx, kpa)
			if err != nil {
				log.Error(err, "Failed to get current replicas")
				continue
			}

			// Calculate recommendations for all metrics
			var maxReplicas int32
			var reasons []string

			for _, autoscaler := range autoscalers {
				// Fetch metric value based on type
				metricValue, err := r.fetchMetricValue(ctx, kpa, autoscaler.Metric, currentReplicas)
				if err != nil {
					log.Error(err, "Failed to fetch metric value", "metric", autoscaler.Metric.Type)
					continue
				}

				// Update the autoscaler with the new metric value
				recommendation := autoscaler.Update(metricValue, currentReplicas, time.Now())

				if recommendation > maxReplicas {
					maxReplicas = recommendation
				}

				reasons = append(reasons, fmt.Sprintf("%s: %d", autoscaler.Metric.Type, recommendation))
			}

			// Apply min/max constraints
			if maxReplicas < *kpa.Spec.MinReplicas {
				maxReplicas = *kpa.Spec.MinReplicas
			}
			if maxReplicas > kpa.Spec.MaxReplicas {
				maxReplicas = kpa.Spec.MaxReplicas
			}

			// Send recommendation if different from current
			if maxReplicas != currentReplicas {
				select {
				case recommendCh <- maxReplicas:
					log.Info("Sent scaling recommendation", "current", currentReplicas, "recommended", maxReplicas, "reasons", reasons)
				default:
					// Channel full, skip this recommendation
				}
			}
		}
	}
}

// fetchMetricValue fetches the current value for a metric
func (r *KPodAutoscalerReconciler) fetchMetricValue(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, metric autoscalingv1alpha1.MetricSpec, currentReplicas int32) (float64, error) {
	switch metric.Type {
	case autoscalingv1alpha1.ResourceMetricType:
		return r.fetchResourceMetric(ctx, kpa, metric.Resource, currentReplicas)
	case autoscalingv1alpha1.PodsMetricType:
		return r.fetchPodsMetric(ctx, kpa, metric.Pods)
	case autoscalingv1alpha1.ObjectMetricType:
		return r.fetchObjectMetric(ctx, kpa, metric.Object)
	case autoscalingv1alpha1.ExternalMetricType:
		return r.fetchExternalMetric(ctx, kpa, metric.External)
	default:
		return 0, fmt.Errorf("unknown metric type: %s", metric.Type)
	}
}

// fetchResourceMetric fetches a resource metric (CPU/Memory)
func (r *KPodAutoscalerReconciler) fetchResourceMetric(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, resource *autoscalingv1alpha1.ResourceMetricSource, currentReplicas int32) (float64, error) {
	// Get pods for the target
	pods, err := r.getPodsForTarget(ctx, kpa)
	if err != nil {
		return 0, err
	}

	if len(pods) == 0 {
		return 0, nil
	}

	// Fetch metrics from metrics server
	metrics, err := r.MetricsClient.GetResourceMetric(ctx, pods, resource.Name)
	if err != nil {
		return 0, err
	}

	// Calculate average based on target type
	var totalValue int64
	var count int

	for _, metric := range metrics {
		if metric.Value != nil {
			totalValue += metric.MilliValue()
			count++
		}
	}

	if count == 0 {
		return 0, nil
	}

	averageValue := totalValue / int64(count)

	// Convert based on target type
	if resource.Target.Type == autoscalingv1alpha1.UtilizationMetricType && resource.Target.AverageUtilization != nil {
		// Calculate total requests
		totalRequests := r.calculateResourceRequests(pods, resource.Name)
		if totalRequests == 0 {
			return 0, fmt.Errorf("no resource requests found for %s", resource.Name)
		}

		// Current utilization as percentage
		currentUtilization := float64(averageValue) / float64(totalRequests) * 100
		targetUtilization := float64(*resource.Target.AverageUtilization)

		// Return ratio of current to target utilization
		return currentUtilization / targetUtilization, nil
	} else if resource.Target.Type == autoscalingv1alpha1.AverageValueMetricType && resource.Target.AverageValue != nil {
		targetValue := float64(resource.Target.AverageValue.MilliValue())
		return float64(averageValue) / targetValue, nil
	}

	return 0, fmt.Errorf("unsupported resource metric target type: %s", resource.Target.Type)
}

// fetchPodsMetric fetches a custom pods metric
func (r *KPodAutoscalerReconciler) fetchPodsMetric(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, pods *autoscalingv1alpha1.PodsMetricSource) (float64, error) {
	// Get pods for the target
	targetPods, err := r.getPodsForTarget(ctx, kpa)
	if err != nil {
		return 0, err
	}

	// Fetch custom metrics
	metrics, err := r.CustomMetricsClient.GetPodsMetric(ctx, kpa.Namespace, pods.Metric, targetPods)
	if err != nil {
		return 0, err
	}

	// Calculate average
	var totalValue int64
	var count int

	for _, metric := range metrics {
		if metric.Value != nil {
			totalValue += metric.MilliValue()
			count++
		}
	}

	if count == 0 {
		return 0, nil
	}

	averageValue := float64(totalValue) / float64(count)

	// Compare with target
	if pods.Target.Type == autoscalingv1alpha1.AverageValueMetricType && pods.Target.AverageValue != nil {
		targetValue := float64(pods.Target.AverageValue.MilliValue())
		return averageValue / targetValue, nil
	}

	return 0, fmt.Errorf("unsupported pods metric target type: %s", pods.Target.Type)
}

// fetchObjectMetric fetches an object metric
func (r *KPodAutoscalerReconciler) fetchObjectMetric(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, object *autoscalingv1alpha1.ObjectMetricSource) (float64, error) {
	// Fetch object metric
	value, err := r.CustomMetricsClient.GetObjectMetric(ctx, kpa.Namespace, object.DescribedObject, object.Metric)
	if err != nil {
		return 0, err
	}

	// Compare with target
	if object.Target.Type == autoscalingv1alpha1.ValueMetricType && object.Target.Value != nil {
		targetValue := float64(object.Target.Value.MilliValue())
		return float64(value.MilliValue()) / targetValue, nil
	}

	return 0, fmt.Errorf("unsupported object metric target type: %s", object.Target.Type)
}

// fetchExternalMetric fetches an external metric
func (r *KPodAutoscalerReconciler) fetchExternalMetric(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, external *autoscalingv1alpha1.ExternalMetricSource) (float64, error) {
	// Fetch external metric
	values, err := r.ExternalMetricsClient.GetExternalMetric(ctx, kpa.Namespace, external.Metric)
	if err != nil {
		return 0, err
	}

	if len(values) == 0 {
		return 0, nil
	}

	// Use the first value
	value := values[0]

	// Compare with target
	if external.Target.Type == autoscalingv1alpha1.ValueMetricType && external.Target.Value != nil {
		targetValue := float64(external.Target.Value.MilliValue())
		return float64(value.MilliValue()) / targetValue, nil
	} else if external.Target.Type == autoscalingv1alpha1.AverageValueMetricType && external.Target.AverageValue != nil {
		// For external metrics, average value means we need to divide by current replicas
		currentReplicas, err := r.getCurrentReplicas(ctx, kpa)
		if err != nil {
			return 0, err
		}
		if currentReplicas == 0 {
			currentReplicas = 1
		}

		targetValue := float64(external.Target.AverageValue.MilliValue())
		currentAverage := float64(value.MilliValue()) / float64(currentReplicas)
		return currentAverage / targetValue, nil
	}

	return 0, fmt.Errorf("unsupported external metric target type: %s", external.Target.Type)
}

// getPodsForTarget gets the pods for the scale target
func (r *KPodAutoscalerReconciler) getPodsForTarget(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler) ([]corev1.Pod, error) {
	// Get the scale target
	targetKind := kpa.Spec.ScaleTargetRef.Kind
	if targetKind == "" {
		targetKind = "Deployment"
	}

	// Get selector from target
	selector, err := r.getSelectorForTarget(ctx, kpa.Namespace, targetKind, kpa.Spec.ScaleTargetRef.Name)
	if err != nil {
		return nil, err
	}

	// List pods
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(kpa.Namespace), client.MatchingLabels(selector.MatchLabels)); err != nil {
		return nil, err
	}

	// Filter running pods
	var pods []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			pods = append(pods, pod)
		}
	}

	return pods, nil
}

// getSelectorForTarget gets the label selector for the scale target
func (r *KPodAutoscalerReconciler) getSelectorForTarget(ctx context.Context, namespace string, kind string, name string) (*metav1.LabelSelector, error) {
	switch kind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
			return nil, err
		}
		return deployment.Spec.Selector, nil
	case "ReplicaSet":
		replicaSet := &appsv1.ReplicaSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, replicaSet); err != nil {
			return nil, err
		}
		return replicaSet.Spec.Selector, nil
	case "StatefulSet":
		statefulSet := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, statefulSet); err != nil {
			return nil, err
		}
		return statefulSet.Spec.Selector, nil
	default:
		return nil, fmt.Errorf("unsupported scale target kind: %s", kind)
	}
}

// calculateResourceRequests calculates the total resource requests for pods
func (r *KPodAutoscalerReconciler) calculateResourceRequests(pods []corev1.Pod, resourceName corev1.ResourceName) int64 {
	var total int64

	for _, pod := range pods {
		for _, container := range pod.Spec.Containers {
			if request, ok := container.Resources.Requests[resourceName]; ok {
				total += request.MilliValue()
			}
		}
	}

	return total / int64(len(pods))
}

// getCurrentReplicas gets the current replica count for the scale target
func (r *KPodAutoscalerReconciler) getCurrentReplicas(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler) (int32, error) {
	scale, err := r.getScaleForTarget(ctx, kpa)
	if err != nil {
		return 0, err
	}
	return scale.Spec.Replicas, nil
}

// getScaleForTarget gets the scale subresource for the target
func (r *KPodAutoscalerReconciler) getScaleForTarget(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler) (*autoscalingv1.Scale, error) {
	targetKind := kpa.Spec.ScaleTargetRef.Kind
	if targetKind == "" {
		targetKind = "Deployment"
	}

	scale := &autoscalingv1.Scale{}
	scaleKey := types.NamespacedName{Namespace: kpa.Namespace, Name: kpa.Spec.ScaleTargetRef.Name}

	switch targetKind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, scaleKey, deployment); err != nil {
			return nil, err
		}
		scale.Spec.Replicas = *deployment.Spec.Replicas
		scale.Status.Replicas = deployment.Status.Replicas
	case "ReplicaSet":
		replicaSet := &appsv1.ReplicaSet{}
		if err := r.Get(ctx, scaleKey, replicaSet); err != nil {
			return nil, err
		}
		scale.Spec.Replicas = *replicaSet.Spec.Replicas
		scale.Status.Replicas = replicaSet.Status.Replicas
	case "StatefulSet":
		statefulSet := &appsv1.StatefulSet{}
		if err := r.Get(ctx, scaleKey, statefulSet); err != nil {
			return nil, err
		}
		scale.Spec.Replicas = *statefulSet.Spec.Replicas
		scale.Status.Replicas = statefulSet.Status.Replicas
	default:
		return nil, fmt.Errorf("unsupported scale target kind: %s", targetKind)
	}

	return scale, nil
}

// applyScale applies the scaling decision to the target
func (r *KPodAutoscalerReconciler) applyScale(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, replicas int32) error {
	targetKind := kpa.Spec.ScaleTargetRef.Kind
	if targetKind == "" {
		targetKind = "Deployment"
	}

	scaleKey := types.NamespacedName{Namespace: kpa.Namespace, Name: kpa.Spec.ScaleTargetRef.Name}

	switch targetKind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, scaleKey, deployment); err != nil {
			return err
		}
		deployment.Spec.Replicas = &replicas
		if err := r.Update(ctx, deployment); err != nil {
			return err
		}
	case "ReplicaSet":
		replicaSet := &appsv1.ReplicaSet{}
		if err := r.Get(ctx, scaleKey, replicaSet); err != nil {
			return err
		}
		replicaSet.Spec.Replicas = &replicas
		if err := r.Update(ctx, replicaSet); err != nil {
			return err
		}
	case "StatefulSet":
		statefulSet := &appsv1.StatefulSet{}
		if err := r.Get(ctx, scaleKey, statefulSet); err != nil {
			return err
		}
		statefulSet.Spec.Replicas = &replicas
		if err := r.Update(ctx, statefulSet); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported scale target kind: %s", targetKind)
	}

	// Update last scale time
	now := metav1.Now()
	kpa.Status.LastScaleTime = &now
	kpa.Status.DesiredReplicas = replicas

	// Record event
	r.Recorder.Eventf(kpa, corev1.EventTypeNormal, "SuccessfulRescale", "New size: %d", replicas)

	return nil
}

// updateStatus updates the KPA status
func (r *KPodAutoscalerReconciler) updateStatus(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler) error {
	// Get current replicas
	currentReplicas, err := r.getCurrentReplicas(ctx, kpa)
	if err != nil {
		r.updateCondition(kpa, autoscalingv1alpha1.ScalingActive, corev1.ConditionFalse, "FailedGetScale", err.Error())
		return err
	}

	kpa.Status.CurrentReplicas = currentReplicas
	kpa.Status.ObservedGeneration = &kpa.Generation

	// Update conditions
	r.updateCondition(kpa, autoscalingv1alpha1.ScalingActive, corev1.ConditionTrue, "ScalingActive", "the HPA controller is able to scale")
	r.updateCondition(kpa, autoscalingv1alpha1.AbleToScale, corev1.ConditionTrue, "ReadyForNewScale", "recommended size matches current size")

	// Check if scaling is limited
	if kpa.Status.DesiredReplicas >= kpa.Spec.MaxReplicas {
		r.updateCondition(kpa, autoscalingv1alpha1.ScalingLimited, corev1.ConditionTrue, "TooManyReplicas", "the desired replica count is more than the maximum replica count")
	} else if kpa.Status.DesiredReplicas <= *kpa.Spec.MinReplicas {
		r.updateCondition(kpa, autoscalingv1alpha1.ScalingLimited, corev1.ConditionTrue, "TooFewReplicas", "the desired replica count is less than the minimum replica count")
	} else {
		r.updateCondition(kpa, autoscalingv1alpha1.ScalingLimited, corev1.ConditionFalse, "DesiredWithinRange", "the desired count is within the acceptable range")
	}

	return r.Status().Update(ctx, kpa)
}

// updateCondition updates a condition on the KPA
func (r *KPodAutoscalerReconciler) updateCondition(kpa *autoscalingv1alpha1.KPodAutoscaler, condType autoscalingv1alpha1.KPodAutoscalerConditionType, status corev1.ConditionStatus, reason, message string) {
	condition := autoscalingv1alpha1.KPodAutoscalerCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	// Check if condition already exists
	for i, existing := range kpa.Status.Conditions {
		if existing.Type == condType {
			if existing.Status != status {
				kpa.Status.Conditions[i] = condition
			}
			return
		}
	}

	// Add new condition
	kpa.Status.Conditions = append(kpa.Status.Conditions, condition)
}

// Helper functions
func int32Ptr(i int32) *int32 {
	return &i
}
