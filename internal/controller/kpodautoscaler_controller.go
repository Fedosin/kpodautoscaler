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

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	libkpa "github.com/Fedosin/libkpa/algorithm"
	libkpaapi "github.com/Fedosin/libkpa/api"
	libkpametrics "github.com/Fedosin/libkpa/metrics"
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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kpav1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
	"github.com/Fedosin/kpodautoscaler/internal/pkg/metrics"
)

// KPodAutoscalerReconciler reconciles a KPodAutoscaler object
type KPodAutoscalerReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	MetricsClient *metrics.MetricsClient

	Log logr.Logger
	// Recorder for events
	Recorder record.EventRecorder
	// scalerWorkers tracks the worker goroutines for each KPA
	scalerWorkers map[types.NamespacedName]*scalerWorker
	workersMu     sync.Mutex
}

// scalerWorker represents a background worker for a single KPA instance
type scalerWorker struct {
	ctx             context.Context
	cancel          context.CancelFunc
	kpaKey          types.NamespacedName
	reconciler      *KPodAutoscalerReconciler
	recommendations chan int32
}

// TODO: make these configurable
const (
	stableTimeWindowGranularity = time.Second
	panicTimeWindowGranularity  = time.Second

	deploymentKind  = "Deployment"
	statefulSetKind = "StatefulSet"
)

// +kubebuilder:rbac:groups=autoscaling.kpodautoscaler.io,resources=kpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling.kpodautoscaler.io,resources=kpodautoscalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=autoscaling.kpodautoscaler.io,resources=kpodautoscalers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments/scale;statefulsets/scale,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods;nodes,verbs=get;list
// +kubebuilder:rbac:groups=custom.metrics.k8s.io,resources=*,verbs=get;list
// +kubebuilder:rbac:groups=external.metrics.k8s.io,resources=*,verbs=get;list

// Reconcile is part of the main kubernetes reconciliation loop
func (r *KPodAutoscalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the KPodAutoscaler instance
	kpa := &kpav1alpha1.KPodAutoscaler{}
	err := r.Get(ctx, req.NamespacedName, kpa)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, could have been deleted after reconcile request
			// Stop the worker if it exists
			r.stopWorker(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Set up finalizer
	finalizerName := "kpodautoscaler.io/finalizer"
	if kpa.DeletionTimestamp.IsZero() {
		// Add finalizer if not present
		if !controllerutil.ContainsFinalizer(kpa, finalizerName) {
			controllerutil.AddFinalizer(kpa, finalizerName)
			if err := r.Update(ctx, kpa); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// Object is being deleted
		if controllerutil.ContainsFinalizer(kpa, finalizerName) {
			// Stop the worker
			r.stopWorker(req.NamespacedName)

			// Remove finalizer
			controllerutil.RemoveFinalizer(kpa, finalizerName)
			if err := r.Update(ctx, kpa); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure worker is running
	worker := r.ensureWorker(req.NamespacedName, kpa)

	// Check for recommendations
	select {
	case desiredReplicas := <-worker.recommendations:
		logger.Info("Received scaling recommendation", "desiredReplicas", desiredReplicas)

		// Update the target resource
		if err := r.scaleTarget(ctx, kpa, desiredReplicas); err != nil {
			logger.Error(err, "Failed to scale target")
			err = r.updateStatus(ctx, kpa, err)
			if err != nil {
				logger.Error(err, "Failed to update status")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, err
		}

		// Update status
		if err := r.updateStatus(ctx, kpa, nil); err != nil {
			logger.Error(err, "Failed to update status")
			return ctrl.Result{}, err
		}

		// Requeue to check for more recommendations
		return ctrl.Result{RequeueAfter: time.Second}, nil

	default:
		// No recommendations available, requeue after a delay
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

// ensureWorker ensures a worker goroutine is running for the given KPA
func (r *KPodAutoscalerReconciler) ensureWorker(key types.NamespacedName, kpa *kpav1alpha1.KPodAutoscaler) *scalerWorker {
	r.workersMu.Lock()
	defer r.workersMu.Unlock()

	if r.scalerWorkers == nil {
		r.scalerWorkers = make(map[types.NamespacedName]*scalerWorker)
	}

	// Check if worker already exists
	if worker, exists := r.scalerWorkers[key]; exists {
		return worker
	}

	// Create new worker
	ctx, cancel := context.WithCancel(context.Background())
	worker := &scalerWorker{
		ctx:             ctx,
		cancel:          cancel,
		kpaKey:          key,
		reconciler:      r,
		recommendations: make(chan int32, 10),
	}

	// Start worker goroutine
	go worker.run(kpa.DeepCopy())

	r.scalerWorkers[key] = worker
	return worker
}

// stopWorker stops the worker goroutine for the given KPA
func (r *KPodAutoscalerReconciler) stopWorker(key types.NamespacedName) {
	r.workersMu.Lock()
	defer r.workersMu.Unlock()

	if worker, exists := r.scalerWorkers[key]; exists {
		worker.cancel()
		close(worker.recommendations)
		delete(r.scalerWorkers, key)
	}
}

// run is the main loop for a scaler worker
func (w *scalerWorker) run(kpa *kpav1alpha1.KPodAutoscaler) {
	logger := ctrl.Log.WithName("worker").WithValues("kpa", w.kpaKey)
	logger.Info("Starting scaler worker")
	defer logger.Info("Stopping scaler worker")

	// Create metric collectors for each metric spec
	collectors := make([]*metricCollector, 0, len(kpa.Spec.Metrics))
	for _, metricSpec := range kpa.Spec.Metrics {
		collector := newMetricCollector(metricSpec, kpa, w.reconciler.MetricsClient)
		collectors = append(collectors, collector)
	}

	// Create a ticker for metric collection (1 second interval)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			// Fetch current KPA
			currentKPA := &kpav1alpha1.KPodAutoscaler{}
			if err := w.reconciler.Get(w.ctx, w.kpaKey, currentKPA); err != nil {
				if errors.IsNotFound(err) {
					return // KPA was deleted
				}
				logger.Error(err, "Failed to get KPA")
				continue
			}

			// Get current replicas
			currentReplicas, err := w.getCurrentReplicas(currentKPA)
			if err != nil {
				logger.Error(err, "Failed to get current replicas")
				continue
			}

			// Collect metrics and compute recommendations
			maxRecommendation := currentReplicas
			for _, collector := range collectors {
				recommendation, err := collector.collectAndRecommend(w.ctx, currentReplicas)
				if err != nil {
					logger.Error(err, "Failed to collect metric", "metric", collector.metricSpec.Type)
					continue
				}

				// Take the maximum of all recommendations
				if recommendation > maxRecommendation {
					maxRecommendation = recommendation
				}
			}

			// Apply min/max constraints
			desiredReplicas := maxRecommendation
			if desiredReplicas < int32(currentKPA.Spec.MinReplicas.Value()) {
				desiredReplicas = int32(currentKPA.Spec.MinReplicas.Value())
			}
			if desiredReplicas > int32(currentKPA.Spec.MaxReplicas.Value()) {
				desiredReplicas = int32(currentKPA.Spec.MaxReplicas.Value())
			}

			// Send recommendation if different from current
			if desiredReplicas != currentReplicas {
				select {
				case w.recommendations <- desiredReplicas:
					logger.Info("Sent scaling recommendation", "current", currentReplicas, "desired", desiredReplicas)
				default:
					// Channel is full, skip this recommendation
				}
			}
		}
	}
}

// getCurrentReplicas gets the current replica count of the target resource
func (w *scalerWorker) getCurrentReplicas(kpa *kpav1alpha1.KPodAutoscaler) (int32, error) {
	switch kpa.Spec.ScaleTargetRef.Kind {
	case deploymentKind:
		deployment := &appsv1.Deployment{}
		key := types.NamespacedName{
			Namespace: kpa.Namespace,
			Name:      kpa.Spec.ScaleTargetRef.Name,
		}
		if err := w.reconciler.Get(w.ctx, key, deployment); err != nil {
			return 0, err
		}
		if deployment.Spec.Replicas != nil {
			return *deployment.Spec.Replicas, nil
		}
		return 1, nil

	case statefulSetKind:
		statefulSet := &appsv1.StatefulSet{}
		key := types.NamespacedName{
			Namespace: kpa.Namespace,
			Name:      kpa.Spec.ScaleTargetRef.Name,
		}
		if err := w.reconciler.Get(w.ctx, key, statefulSet); err != nil {
			return 0, err
		}
		if statefulSet.Spec.Replicas != nil {
			return *statefulSet.Spec.Replicas, nil
		}
		return 1, nil

	default:
		return 0, fmt.Errorf("unsupported target kind: %s", kpa.Spec.ScaleTargetRef.Kind)
	}
}

// metricCollector collects metrics and provides scaling recommendations
type metricCollector struct {
	metricSpec    kpav1alpha1.MetricSpec
	kpa           *kpav1alpha1.KPodAutoscaler
	metricsClient *metrics.MetricsClient
	stableWindow  libkpaapi.MetricAggregator
	panicWindow   libkpaapi.MetricAggregator
	autoscaler    *libkpa.SlidingWindowAutoscaler
}

// newMetricCollector creates a new metric collector
func newMetricCollector(spec kpav1alpha1.MetricSpec, kpa *kpav1alpha1.KPodAutoscaler, metricsClient *metrics.MetricsClient) *metricCollector {
	// Get metric config
	config := spec.Config

	// Apply defaults
	algorithm := "linear"
	stableWindowSize := 60 * time.Second
	panicWindowSize := 6 * time.Second
	panicThreshold := 2.0
	scaleDownDelay := 5 * time.Second
	targetBurstCapacity := 211.0
	maxScaleUpRate := 1000.0
	maxScaleDownRate := 2.0
	targetValue := 0.0
	totalValue := 0.0
	activationScale := int32(1)

	if config != nil {
		if config.AggregationAlgorithm != "" {
			algorithm = config.AggregationAlgorithm
		}
		if config.MaxScaleUpRate != nil {
			maxScaleUpRate = config.MaxScaleUpRate.AsApproximateFloat64()
		}
		if config.MaxScaleDownRate != nil {
			maxScaleDownRate = config.MaxScaleDownRate.AsApproximateFloat64()
		}
		if config.PanicThreshold != nil {
			panicThreshold = config.PanicThreshold.AsApproximateFloat64()
		}
		if config.ScaleDownDelay != 0 {
			scaleDownDelay = config.ScaleDownDelay
		}
		if config.TargetBurstCapacity != nil {
			targetBurstCapacity = config.TargetBurstCapacity.AsApproximateFloat64()
		}
		if config.TargetValue != nil {
			targetValue = config.TargetValue.AsApproximateFloat64()
		}
		if config.TotalValue != nil {
			totalValue = config.TotalValue.AsApproximateFloat64()
		}
		if config.StableWindow != 0 {
			stableWindowSize = config.StableWindow
		}
		if config.PanicWindowPercentage != nil {
			panicWindowPercentage := config.PanicWindowPercentage.AsApproximateFloat64()
			panicWindowSize = time.Duration(float64(stableWindowSize) * panicWindowPercentage / 100.0)
		}
		if config.PanicThreshold != nil {
			panicThreshold = config.PanicThreshold.AsApproximateFloat64()
		}
		if config.ScaleDownDelay != 0 {
			scaleDownDelay = config.ScaleDownDelay
		}
		if config.ActivationScale != 0 {
			activationScale = config.ActivationScale
		}
	}

	// Create windows based on algorithm
	var stableWindow, panicWindow libkpaapi.MetricAggregator
	if algorithm == "weighted" {
		stableWindow = libkpametrics.NewWeightedTimeWindow(stableWindowSize, stableTimeWindowGranularity)
		panicWindow = libkpametrics.NewWeightedTimeWindow(panicWindowSize, panicTimeWindowGranularity)
	} else {
		stableWindow = libkpametrics.NewTimeWindow(stableWindowSize, stableTimeWindowGranularity)
		panicWindow = libkpametrics.NewTimeWindow(panicWindowSize, panicTimeWindowGranularity)
	}

	// Create autoscaler config
	autoscalerConfig := libkpaapi.AutoscalerConfig{
		MaxScaleUpRate:      maxScaleUpRate,
		MaxScaleDownRate:    maxScaleDownRate,
		TargetValue:         targetValue,
		TotalValue:          totalValue,
		TargetBurstCapacity: targetBurstCapacity,
		PanicThreshold:      panicThreshold,
		ScaleDownDelay:      scaleDownDelay,
		MinScale:            int32(kpa.Spec.MinReplicas.Value()),
		MaxScale:            int32(kpa.Spec.MaxReplicas.Value()),
		ActivationScale:     activationScale,
		// ScaleToZeroGracePeriod: kpa.Spec.ScaleToZeroGracePeriod,
	}

	return &metricCollector{
		metricSpec:    spec,
		kpa:           kpa,
		metricsClient: metricsClient,
		stableWindow:  stableWindow,
		panicWindow:   panicWindow,
		autoscaler:    libkpa.NewSlidingWindowAutoscaler(autoscalerConfig),
	}
}

// collectAndRecommend collects metrics and returns a scaling recommendation
func (mc *metricCollector) collectAndRecommend(ctx context.Context, currentReplicas int32) (int32, error) {
	timestamp := time.Now()

	// Collect metric value
	value, _, err := mc.collectMetric(ctx)
	if err != nil {
		return currentReplicas, err
	}

	// Add value to windows
	mc.stableWindow.Record(timestamp, value)
	mc.panicWindow.Record(timestamp, value)

	// Get averages
	stableAvg := mc.stableWindow.WindowAverage(timestamp)
	panicAvg := mc.panicWindow.WindowAverage(timestamp)

	metricSnapshot := libkpametrics.NewMetricSnapshot(stableAvg, panicAvg, currentReplicas, timestamp)

	recommendation := mc.autoscaler.Scale(metricSnapshot, timestamp)
	if !recommendation.ScaleValid {
		return currentReplicas, fmt.Errorf("scale recommendation is not valid")
	}

	return recommendation.DesiredPodCount, nil
}

// collectMetric collects the current metric value and target value
func (mc *metricCollector) collectMetric(ctx context.Context) (float64, float64, error) {
	switch mc.metricSpec.Type {
	case "Resource":
		return mc.collectResourceMetric(ctx)
	case "Pods":
		return mc.collectPodsMetric(ctx)
	case "Object":
		return mc.collectObjectMetric(ctx)
	case "External":
		return mc.collectExternalMetric(ctx)
	default:
		return 0, 0, fmt.Errorf("unknown metric type: %s", mc.metricSpec.Type)
	}
}

// collectResourceMetric collects CPU or memory metrics
func (mc *metricCollector) collectResourceMetric(ctx context.Context) (float64, float64, error) {
	if mc.metricSpec.Resource == nil {
		return 0, 0, fmt.Errorf("resource metric spec is nil")
	}

	// Get pods for the target
	pods, err := mc.getTargetPods(ctx)
	if err != nil {
		return 0, 0, err
	}

	if len(pods) == 0 {
		return 0, 0, fmt.Errorf("no pods found for target")
	}

	// Collect resource metrics
	values, err := mc.metricsClient.GetResourceMetric(ctx, pods, mc.metricSpec.Resource.Name)
	if err != nil {
		return 0, 0, err
	}

	// Calculate average
	var sum int64
	for _, v := range values {
		sum += v.MilliValue()
	}
	avgMilliValue := sum / int64(len(values))
	avgValue := float64(avgMilliValue) / 1000.0

	// Calculate target value based on target type
	var targetValue float64
	target := mc.metricSpec.Resource.Target

	switch target.Type {
	case kpav1alpha1.UtilizationMetricType:
		if target.AverageUtilization == nil {
			return 0, 0, fmt.Errorf("averageUtilization is nil")
		}
		// For utilization, we need to get the total requested resources
		var totalRequests int64
		for _, pod := range pods {
			for _, container := range pod.Spec.Containers {
				if req, found := container.Resources.Requests[mc.metricSpec.Resource.Name]; found {
					totalRequests += req.MilliValue()
				}
			}
		}
		if totalRequests == 0 {
			return 0, 0, fmt.Errorf("no resource requests found")
		}
		avgRequests := float64(totalRequests) / float64(len(pods)) / 1000.0
		targetValue = avgRequests * float64(*target.AverageUtilization) / 100.0

	case kpav1alpha1.AverageValueMetricType:
		if target.AverageValue == nil {
			return 0, 0, fmt.Errorf("averageValue is nil")
		}
		targetValue = float64(target.AverageValue.MilliValue()) / 1000.0

	case kpav1alpha1.ValueMetricType:
		return 0, 0, fmt.Errorf("value target type not supported for resource metrics")

	default:
		return 0, 0, fmt.Errorf("unknown target type: %s", target.Type)
	}

	return avgValue, targetValue, nil
}

// collectPodsMetric collects custom metrics from pods
func (mc *metricCollector) collectPodsMetric(ctx context.Context) (float64, float64, error) {
	if mc.metricSpec.Pods == nil {
		return 0, 0, fmt.Errorf("pods metric spec is nil")
	}

	// Get pods for the target
	pods, err := mc.getTargetPods(ctx)
	if err != nil {
		return 0, 0, err
	}

	if len(pods) == 0 {
		return 0, 0, fmt.Errorf("no pods found for target")
	}

	metricIdentifier := kpav1alpha1.MetricIdentifier{
		Name:     mc.metricSpec.Pods.Metric.Name,
		Selector: mc.metricSpec.Pods.Metric.Selector,
	}

	// Collect custom metrics
	values, err := mc.metricsClient.GetPodsMetric(ctx, mc.kpa.Namespace, metricIdentifier, pods)
	if err != nil {
		return 0, 0, err
	}

	// Calculate average
	var sum int64
	for _, v := range values {
		sum += v.MilliValue()
	}
	avgMilliValue := sum / int64(len(values))
	avgValue := float64(avgMilliValue) / 1000.0

	// Get target value
	target := mc.metricSpec.Pods.Target
	var targetValue float64

	switch target.Type {
	case kpav1alpha1.AverageValueMetricType:
		if target.AverageValue == nil {
			return 0, 0, fmt.Errorf("averageValue is nil")
		}
		targetValue = float64(target.AverageValue.MilliValue()) / 1000.0

	default:
		return 0, 0, fmt.Errorf("unsupported target type for pods metric: %s", target.Type)
	}

	return avgValue, targetValue, nil
}

// collectObjectMetric collects metrics from a Kubernetes object
func (mc *metricCollector) collectObjectMetric(ctx context.Context) (float64, float64, error) {
	if mc.metricSpec.Object == nil {
		return 0, 0, fmt.Errorf("object metric spec is nil")
	}

	metricIdentifier := kpav1alpha1.MetricIdentifier{
		Name:     mc.metricSpec.Object.Metric.Name,
		Selector: mc.metricSpec.Object.Metric.Selector,
	}

	// Collect object metric
	value, err := mc.metricsClient.GetObjectMetric(
		ctx,
		mc.kpa.Namespace,
		mc.metricSpec.Object.DescribedObject,
		metricIdentifier,
	)
	if err != nil {
		return 0, 0, err
	}

	currentValue := float64(value.MilliValue()) / 1000.0

	// Get target value
	target := mc.metricSpec.Object.Target
	var targetValue float64

	switch target.Type {
	case kpav1alpha1.ValueMetricType:
		if target.Value == nil {
			return 0, 0, fmt.Errorf("value is nil")
		}
		targetValue = float64(target.Value.MilliValue()) / 1000.0

	case kpav1alpha1.AverageValueMetricType:
		if target.AverageValue == nil {
			return 0, 0, fmt.Errorf("averageValue is nil")
		}
		// For object metrics with AverageValue, we divide by current replicas
		deployment := &appsv1.Deployment{}
		key := types.NamespacedName{
			Namespace: mc.kpa.Namespace,
			Name:      mc.kpa.Spec.ScaleTargetRef.Name,
		}
		if err := mc.metricsClient.Get(ctx, key, deployment); err != nil {
			return 0, 0, err
		}
		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		targetValue = float64(target.AverageValue.MilliValue()) / 1000.0 * float64(replicas)

	default:
		return 0, 0, fmt.Errorf("unsupported target type for object metric: %s", target.Type)
	}

	return currentValue, targetValue, nil
}

// collectExternalMetric collects external metrics
func (mc *metricCollector) collectExternalMetric(ctx context.Context) (float64, float64, error) {
	if mc.metricSpec.External == nil {
		return 0, 0, fmt.Errorf("external metric spec is nil")
	}

	metricIdentifier := kpav1alpha1.MetricIdentifier{
		Name:     mc.metricSpec.External.Metric.Name,
		Selector: mc.metricSpec.External.Metric.Selector,
	}

	// Collect external metric
	values, err := mc.metricsClient.GetExternalMetric(
		ctx,
		mc.kpa.Namespace,
		metricIdentifier,
	)
	if err != nil {
		return 0, 0, err
	}

	// Calculate sum or average based on target type
	target := mc.metricSpec.External.Target
	var currentValue, targetValue float64

	switch target.Type {
	case kpav1alpha1.ValueMetricType:
		// Sum all values
		var sum int64
		for _, v := range values {
			sum += v.MilliValue()
		}
		currentValue = float64(sum) / 1000.0

		if target.Value == nil {
			return 0, 0, fmt.Errorf("value is nil")
		}
		targetValue = float64(target.Value.MilliValue()) / 1000.0

	case kpav1alpha1.AverageValueMetricType:
		// Average all values
		var sum int64
		for _, v := range values {
			sum += v.MilliValue()
		}
		if len(values) > 0 {
			currentValue = float64(sum) / float64(len(values)) / 1000.0
		}

		if target.AverageValue == nil {
			return 0, 0, fmt.Errorf("averageValue is nil")
		}
		targetValue = float64(target.AverageValue.MilliValue()) / 1000.0

	default:
		return 0, 0, fmt.Errorf("unsupported target type for external metric: %s", target.Type)
	}

	return currentValue, targetValue, nil
}

// getTargetPods returns the pods for the scale target
func (mc *metricCollector) getTargetPods(ctx context.Context) ([]corev1.Pod, error) {
	// Get selector from target
	var selector client.MatchingLabels

	switch mc.kpa.Spec.ScaleTargetRef.Kind {
	case deploymentKind:
		deployment := &appsv1.Deployment{}
		key := types.NamespacedName{
			Namespace: mc.kpa.Namespace,
			Name:      mc.kpa.Spec.ScaleTargetRef.Name,
		}
		if err := mc.metricsClient.Get(ctx, key, deployment); err != nil {
			return nil, err
		}
		selector = deployment.Spec.Selector.MatchLabels

	case statefulSetKind:
		statefulSet := &appsv1.StatefulSet{}
		key := types.NamespacedName{
			Namespace: mc.kpa.Namespace,
			Name:      mc.kpa.Spec.ScaleTargetRef.Name,
		}
		if err := mc.metricsClient.Get(ctx, key, statefulSet); err != nil {
			return nil, err
		}
		selector = statefulSet.Spec.Selector.MatchLabels

	default:
		return nil, fmt.Errorf("unsupported target kind: %s", mc.kpa.Spec.ScaleTargetRef.Kind)
	}

	// List pods
	podList := &corev1.PodList{}
	if err := mc.metricsClient.List(ctx, podList, client.InNamespace(mc.kpa.Namespace), selector); err != nil {
		return nil, err
	}

	// Filter running pods
	var runningPods []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			runningPods = append(runningPods, pod)
		}
	}

	return runningPods, nil
}

// scaleTarget updates the target resource with the desired replica count
func (r *KPodAutoscalerReconciler) scaleTarget(ctx context.Context, kpa *kpav1alpha1.KPodAutoscaler, desiredReplicas int32) error {
	switch kpa.Spec.ScaleTargetRef.Kind {
	case deploymentKind:
		scale := &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kpa.Spec.ScaleTargetRef.Name,
				Namespace: kpa.Namespace,
			},
			Spec: autoscalingv1.ScaleSpec{
				Replicas: desiredReplicas,
			},
		}

		// Update scale subresource
		if err := r.SubResource("scale").Update(ctx, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kpa.Spec.ScaleTargetRef.Name,
				Namespace: kpa.Namespace,
			},
		}, client.WithSubResourceBody(scale)); err != nil {
			return err
		}

		// Record event
		r.Recorder.Eventf(kpa, corev1.EventTypeNormal, "SuccessfulRescale", "Scaled deployment to %d replicas", desiredReplicas)

	case statefulSetKind:
		scale := &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kpa.Spec.ScaleTargetRef.Name,
				Namespace: kpa.Namespace,
			},
			Spec: autoscalingv1.ScaleSpec{
				Replicas: desiredReplicas,
			},
		}

		// Update scale subresource
		if err := r.SubResource("scale").Update(ctx, &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kpa.Spec.ScaleTargetRef.Name,
				Namespace: kpa.Namespace,
			},
		}, client.WithSubResourceBody(scale)); err != nil {
			return err
		}

		// Record event
		r.Recorder.Eventf(kpa, corev1.EventTypeNormal, "SuccessfulRescale", "Scaled statefulset to %d replicas", desiredReplicas)

	default:
		return fmt.Errorf("unsupported target kind: %s", kpa.Spec.ScaleTargetRef.Kind)
	}

	return nil
}

// updateStatus updates the KPodAutoscaler status
func (r *KPodAutoscalerReconciler) updateStatus(ctx context.Context, kpa *kpav1alpha1.KPodAutoscaler, scaleErr error) error {
	// Get current replicas
	var currentReplicas int32
	switch kpa.Spec.ScaleTargetRef.Kind {
	case deploymentKind:
		deployment := &appsv1.Deployment{}
		key := types.NamespacedName{
			Namespace: kpa.Namespace,
			Name:      kpa.Spec.ScaleTargetRef.Name,
		}
		if err := r.Get(ctx, key, deployment); err != nil {
			return err
		}
		if deployment.Status.Replicas > 0 {
			currentReplicas = deployment.Status.Replicas
		} else if deployment.Spec.Replicas != nil {
			currentReplicas = *deployment.Spec.Replicas
		} else {
			currentReplicas = 1
		}

	case statefulSetKind:
		statefulSet := &appsv1.StatefulSet{}
		key := types.NamespacedName{
			Namespace: kpa.Namespace,
			Name:      kpa.Spec.ScaleTargetRef.Name,
		}
		if err := r.Get(ctx, key, statefulSet); err != nil {
			return err
		}
		if statefulSet.Status.Replicas > 0 {
			currentReplicas = statefulSet.Status.Replicas
		} else if statefulSet.Spec.Replicas != nil {
			currentReplicas = *statefulSet.Spec.Replicas
		} else {
			currentReplicas = 1
		}
	}

	// Update status fields
	kpa.Status.ObservedGeneration = &kpa.Generation
	kpa.Status.CurrentReplicas = currentReplicas

	if scaleErr == nil {
		kpa.Status.LastScaleTime = &metav1.Time{Time: time.Now()}
		kpa.Status.DesiredReplicas = currentReplicas
	}

	// Update conditions
	now := metav1.Now()

	// ScalingActive condition
	scalingActiveCondition := kpav1alpha1.KPodAutoscalerCondition{
		Type:               kpav1alpha1.ScalingActive,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "ScalingActive",
		Message:            "the KPA controller is able to scale if necessary",
	}
	if scaleErr != nil {
		scalingActiveCondition.Status = corev1.ConditionFalse
		scalingActiveCondition.Reason = "ScalingError"
		scalingActiveCondition.Message = scaleErr.Error()
	}

	// AbleToScale condition
	ableToScaleCondition := kpav1alpha1.KPodAutoscalerCondition{
		Type:               kpav1alpha1.AbleToScale,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "SucceededGetScale",
		Message:            "the KPA controller was able to get the target's current scale",
	}

	// Update or append conditions
	conditions := []kpav1alpha1.KPodAutoscalerCondition{scalingActiveCondition, ableToScaleCondition}
	for _, newCond := range conditions {
		found := false
		for i, cond := range kpa.Status.Conditions {
			if cond.Type == newCond.Type {
				if cond.Status != newCond.Status {
					kpa.Status.Conditions[i] = newCond
				} else {
					kpa.Status.Conditions[i].LastTransitionTime = cond.LastTransitionTime
				}
				found = true
				break
			}
		}
		if !found {
			kpa.Status.Conditions = append(kpa.Status.Conditions, newCond)
		}
	}

	// Update the status
	return r.Status().Update(ctx, kpa)
}

// SetupWithManager sets up the controller with the Manager.
func (r *KPodAutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize metrics client if not already set
	if r.MetricsClient == nil {
		r.MetricsClient = metrics.NewMetricsClient(mgr.GetClient(), mgr.GetRESTMapper())
	}

	// Initialize recorder if not already set
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("kpodautoscaler-controller")
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kpav1alpha1.KPodAutoscaler{}).
		Complete(r)
}
