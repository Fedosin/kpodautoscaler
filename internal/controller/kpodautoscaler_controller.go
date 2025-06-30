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

	libkpaapi "github.com/Fedosin/libkpa/api"
	libkpamanager "github.com/Fedosin/libkpa/manager"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
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
	"github.com/Fedosin/kpodautoscaler/internal/pkg/resourcerequests"
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
	manager         *libkpamanager.Manager
}

const (
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

// run is the main loop for a scaler worker
func (w *scalerWorker) run(kpa *kpav1alpha1.KPodAutoscaler) {
	logger := ctrl.Log.WithName("worker").WithValues("kpa", w.kpaKey)
	logger.Info("Starting scaler worker")
	defer logger.Info("Stopping scaler worker")

	// Create scalers for each metric
	scalers := []*libkpamanager.Scaler{}
	for _, metricSpec := range kpa.Spec.Metrics {
		scaler, err := w.createScaler(metricSpec, kpa.Spec.ScaleTargetRef, kpa.Namespace)
		if err != nil {
			logger.Error(err, "Failed to create scaler")
			continue
		}
		scalers = append(scalers, scaler)
	}

	// Create the manager with min/max replicas and scalers
	manager := libkpamanager.NewManager(
		int32(kpa.Spec.MinReplicas.Value()),
		int32(kpa.Spec.MaxReplicas.Value()),
		scalers...,
	)
	w.manager = manager

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

			// Collect metrics and record to manager
			timestamp := time.Now()

			for i, metricSpec := range currentKPA.Spec.Metrics {
				metricValue, err := w.collectMetric(w.ctx, metricSpec, currentKPA)
				if err != nil {
					logger.Error(err, "Failed to collect metric", "metric", metricSpec.Type)
					continue
				}

				// Record metric to the corresponding scaler in the manager
				// The manager will internally handle the metric recording
				if i < len(scalers) {
					if err := w.manager.Record(getMetricName(metricSpec), metricValue, timestamp); err != nil {
						logger.Error(err, "Failed to record metric", "metric", metricSpec.Type)
					}
				}
			}

			// Get recommendation from manager
			desiredReplicas := w.manager.Scale(currentReplicas, timestamp)

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

// collectMetric collects the current metric value
func (w *scalerWorker) collectMetric(ctx context.Context, metricSpec kpav1alpha1.MetricSpec, kpa *kpav1alpha1.KPodAutoscaler) (float64, error) {
	switch metricSpec.Type {
	case kpav1alpha1.ResourceMetricType:
		return w.collectResourceMetric(ctx, metricSpec.Resource, kpa)
	case kpav1alpha1.PodsMetricType:
		return w.collectPodsMetric(ctx, metricSpec.Pods, kpa)
	case kpav1alpha1.ObjectMetricType:
		return w.collectObjectMetric(ctx, metricSpec.Object, kpa)
	case kpav1alpha1.ExternalMetricType:
		return w.collectExternalMetric(ctx, metricSpec.External, kpa)
	default:
		return 0, fmt.Errorf("unknown metric type: %s", metricSpec.Type)
	}
}

// collectResourceMetric collects CPU or memory metrics
func (w *scalerWorker) collectResourceMetric(ctx context.Context, resource *kpav1alpha1.ResourceMetricSource, kpa *kpav1alpha1.KPodAutoscaler) (float64, error) {
	if resource == nil {
		return 0, fmt.Errorf("resource metric spec is nil")
	}

	// Get pods for the target
	pods, err := w.getTargetPods(ctx, kpa)
	if err != nil {
		return 0, err
	}

	if len(pods) == 0 {
		return 0, fmt.Errorf("no pods found for target")
	}

	// Collect resource metrics
	values, err := w.reconciler.MetricsClient.GetResourceMetric(ctx, pods, resource.Name)
	if err != nil {
		return 0, err
	}

	// Calculate average
	var sum int64
	for _, v := range values {
		sum += v.MilliValue()
	}
	avgMilliValue := sum / int64(len(values))
	avgValue := float64(avgMilliValue) / 1000.0

	// For utilization metrics, convert to percentage
	if resource.Target.Type == kpav1alpha1.UtilizationMetricType {
		// Get total requested resources
		var totalRequests int64
		for _, pod := range pods {
			for _, container := range pod.Spec.Containers {
				if req, found := container.Resources.Requests[resource.Name]; found {
					totalRequests += req.MilliValue()
				}
			}
		}
		if totalRequests > 0 {
			avgRequests := float64(totalRequests) / float64(len(pods)) / 1000.0
			return (avgValue / avgRequests) * 100.0, nil
		}
	}

	return avgValue, nil
}

// getTargetPods returns the pods for the scale target
func (w *scalerWorker) getTargetPods(ctx context.Context, kpa *kpav1alpha1.KPodAutoscaler) ([]corev1.Pod, error) {
	// Get selector from target
	var selector client.MatchingLabels

	switch kpa.Spec.ScaleTargetRef.Kind {
	case deploymentKind:
		deployment := &appsv1.Deployment{}
		key := types.NamespacedName{
			Namespace: kpa.Namespace,
			Name:      kpa.Spec.ScaleTargetRef.Name,
		}
		if err := w.reconciler.Get(ctx, key, deployment); err != nil {
			return nil, err
		}
		selector = deployment.Spec.Selector.MatchLabels

	case statefulSetKind:
		statefulSet := &appsv1.StatefulSet{}
		key := types.NamespacedName{
			Namespace: kpa.Namespace,
			Name:      kpa.Spec.ScaleTargetRef.Name,
		}
		if err := w.reconciler.Get(ctx, key, statefulSet); err != nil {
			return nil, err
		}
		selector = statefulSet.Spec.Selector.MatchLabels

	default:
		return nil, fmt.Errorf("unsupported target kind: %s", kpa.Spec.ScaleTargetRef.Kind)
	}

	// List pods
	podList := &corev1.PodList{}
	if err := w.reconciler.MetricsClient.List(ctx, podList, client.InNamespace(kpa.Namespace), selector); err != nil {
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

// createScaler creates a scaler for a metric spec
func (w *scalerWorker) createScaler(metricSpec kpav1alpha1.MetricSpec, scaleTargetRef kpav1alpha1.ScaleTargetRef, namespace string) (*libkpamanager.Scaler, error) {
	// Create autoscaler config - min/max scale should come from the parent KPA, not metric config
	config := libkpaapi.AutoscalerConfig{
		MaxScaleUpRate:        1000.0,
		MaxScaleDownRate:      2.0,
		TargetValue:           0.0,
		TotalValue:            0.0,
		TargetBurstCapacity:   211.0,
		PanicThreshold:        2.0,
		ScaleDownDelay:        5 * time.Second,
		ActivationScale:       1,
		StableWindow:          60 * time.Second,
		PanicWindowPercentage: 10.0,
	}

	targetValue := getTargetValueFromMetricSpec(metricSpec)
	if targetValue == -1.0 {
		return nil, fmt.Errorf("invalid target value for metric spec")
	}

	var err error

	// Special case for resource metrics with utilization type
	if metricSpec.Type == "Resource" && metricSpec.Resource.Target.Type == "Utilization" {
		var quantity k8sresource.Quantity

		switch scaleTargetRef.Kind {
		case "Deployment":
			quantity, err = resourcerequests.GetDeploymentPodResourceRequests(w.ctx, w.reconciler.Client, namespace, scaleTargetRef.Name, metricSpec.Resource.Name)
			if err != nil {
				return nil, err
			}
		case "StatefulSet":
			quantity, err = resourcerequests.GetStatefulSetPodResourceRequests(w.ctx, w.reconciler.Client, namespace, scaleTargetRef.Name, metricSpec.Resource.Name)
			if err != nil {
				return nil, err
			}
		}
		config.TargetValue = targetValue * float64(quantity.MilliValue()) / 1000.0
	} else {
		config.TargetValue = targetValue
	}

	aggregationAlgorithm := "linear"

	// Apply defaults and config overrides
	if metricSpec.Config != nil {
		mc := metricSpec.Config

		// Apply configuration values
		if mc.MaxScaleUpRate != nil {
			config.MaxScaleUpRate = mc.MaxScaleUpRate.AsApproximateFloat64()
		}

		if mc.MaxScaleDownRate != nil {
			config.MaxScaleDownRate = mc.MaxScaleDownRate.AsApproximateFloat64()
		}

		if mc.TargetValue != nil {
			config.TargetValue = mc.TargetValue.AsApproximateFloat64()
		}

		if mc.TotalValue != nil {
			config.TotalValue = mc.TotalValue.AsApproximateFloat64()
		}

		if mc.TargetBurstCapacity != nil {
			config.TargetBurstCapacity = mc.TargetBurstCapacity.AsApproximateFloat64()
		}

		if mc.PanicThreshold != nil {
			config.PanicThreshold = mc.PanicThreshold.AsApproximateFloat64()
		}

		if mc.ScaleDownDelay != 0 {
			config.ScaleDownDelay = mc.ScaleDownDelay
		}

		if mc.ActivationScale != 0 {
			config.ActivationScale = mc.ActivationScale
		}

		if mc.StableWindow != 0 {
			config.StableWindow = mc.StableWindow
		}

		if mc.PanicWindowPercentage != nil {
			config.PanicWindowPercentage = mc.PanicWindowPercentage.AsApproximateFloat64()
		}

		if mc.AggregationAlgorithm != "" {
			aggregationAlgorithm = mc.AggregationAlgorithm
		}
	}

	scaler, err := libkpamanager.NewScaler(getMetricName(metricSpec), config, aggregationAlgorithm)
	if err != nil {
		return nil, err
	}

	return scaler, nil
}

// collectPodsMetric collects custom pod metrics
func (w *scalerWorker) collectPodsMetric(ctx context.Context, pods *kpav1alpha1.PodsMetricSource, kpa *kpav1alpha1.KPodAutoscaler) (float64, error) {
	if pods == nil {
		return 0, fmt.Errorf("pods metric spec is nil")
	}

	// Get pods for the target
	targetPods, err := w.getTargetPods(ctx, kpa)
	if err != nil {
		return 0, err
	}

	if len(targetPods) == 0 {
		return 0, fmt.Errorf("no pods found for target")
	}

	metricIdentifier := kpav1alpha1.MetricIdentifier{
		Name:     pods.Metric.Name,
		Selector: pods.Metric.Selector,
	}

	// Collect custom metrics
	values, err := w.reconciler.MetricsClient.GetPodsMetric(ctx, kpa.Namespace, metricIdentifier, targetPods)
	if err != nil {
		return 0, err
	}

	// Calculate average
	var sum int64
	for _, v := range values {
		sum += v.MilliValue()
	}
	avgMilliValue := sum / int64(len(values))
	avgValue := float64(avgMilliValue) / 1000.0

	return avgValue, nil
}

// collectObjectMetric collects metrics from a Kubernetes object
func (w *scalerWorker) collectObjectMetric(ctx context.Context, object *kpav1alpha1.ObjectMetricSource, kpa *kpav1alpha1.KPodAutoscaler) (float64, error) {
	if object == nil {
		return 0, fmt.Errorf("object metric spec is nil")
	}

	metricIdentifier := kpav1alpha1.MetricIdentifier{
		Name:     object.Metric.Name,
		Selector: object.Metric.Selector,
	}

	// Collect object metric
	value, err := w.reconciler.MetricsClient.GetObjectMetric(
		ctx,
		kpa.Namespace,
		object.DescribedObject,
		metricIdentifier,
	)
	if err != nil {
		return 0, err
	}

	currentValue := float64(value.MilliValue()) / 1000.0
	return currentValue, nil
}

// collectExternalMetric collects external metrics
func (w *scalerWorker) collectExternalMetric(ctx context.Context, external *kpav1alpha1.ExternalMetricSource, kpa *kpav1alpha1.KPodAutoscaler) (float64, error) {
	if external == nil {
		return 0, fmt.Errorf("external metric spec is nil")
	}

	metricIdentifier := kpav1alpha1.MetricIdentifier{
		Name:     external.Metric.Name,
		Selector: external.Metric.Selector,
	}

	// Collect external metric
	values, err := w.reconciler.MetricsClient.GetExternalMetric(
		ctx,
		kpa.Namespace,
		metricIdentifier,
	)
	if err != nil {
		return 0, err
	}

	// Calculate sum
	var sum int64
	for _, v := range values {
		sum += v.MilliValue()
	}

	currentValue := float64(sum) / 1000.0
	return currentValue, nil
}

// getMetricName returns the name of the metric
func getMetricName(metricSpec kpav1alpha1.MetricSpec) string {
	switch metricSpec.Type {
	case kpav1alpha1.ResourceMetricType:
		return string(metricSpec.Resource.Name)
	case kpav1alpha1.PodsMetricType:
		return metricSpec.Pods.Metric.Name
	case kpav1alpha1.ObjectMetricType:
		return metricSpec.Object.Metric.Name
	case kpav1alpha1.ExternalMetricType:
		return metricSpec.External.Metric.Name
	default:
		return ""
	}
}

// getTargetValue returns the target value for a metric target
func getTargetValueFromMetricSpec(metricSpec kpav1alpha1.MetricSpec) float64 {
	switch metricSpec.Type {
	case kpav1alpha1.ResourceMetricType:
		return getTargetValue(metricSpec.Resource.Target)
	case kpav1alpha1.PodsMetricType:
		return getTargetValue(metricSpec.Pods.Target)
	case "Object":
		return getTargetValue(metricSpec.Object.Target)
	case "External":
		return getTargetValue(metricSpec.External.Target)
	default:
		return -1.0
	}
}

// getTargetValue returns the target value for a metric target
func getTargetValue(metricTarget kpav1alpha1.MetricTarget) float64 {
	switch metricTarget.Type {
	case "Utilization":
		return float64(*metricTarget.AverageUtilization)
	case "Value":
		return metricTarget.Value.AsApproximateFloat64()
	case "AverageValue":
		return metricTarget.AverageValue.AsApproximateFloat64()
	}
	return -1.0
}
