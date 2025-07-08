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
	"encoding/json"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kpav1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

const (
	// managedByAnnotation indicates that KPodAutoscaler should manage scaling for this ScaledObject
	managedByAnnotation = "autoscaling.kpodautoscaler.io/managed-backend"

	// kpaFinalizer ensures proper cleanup when ScaledObject is deleted
	kpaFinalizer = "kpodautoscaler.io/finalizer"

	// Annotations for storing original ScaledObject values
	minReplicasAnnotation    = "autoscaling.kpodautoscaler.io/min-replicas"
	maxReplicasAnnotation    = "autoscaling.kpodautoscaler.io/max-replicas"
	originalTargetAnnotation = "autoscaling.kpodautoscaler.io/original-target"

	// annotationValueTrue is the value of the annotation
	annotationValueTrue = "true"

	// managedKPANamePrefix is the prefix of the name of the managed KPodAutoscaler
	managedKPANamePrefix = "keda-kpa-"

	// dummyDeploymentPrefix is the prefix for dummy deployments
	dummyDeploymentPrefix = "kpa-dummy-"
)

var (
	// ScaledObjectGVK defines the GroupVersionKind for KEDA ScaledObject
	ScaledObjectGVK = schema.GroupVersionKind{
		Group:   "keda.sh",
		Version: "v1alpha1",
		Kind:    "ScaledObject",
	}
)

// ScaledObjectReconciler reconciles a ScaledObject object
type ScaledObjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=keda.sh,resources=scaledobjects/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=autoscaling.kpodautoscaler.io,resources=kpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// This controller watches KEDA ScaledObject resources and manages KPodAutoscaler
// lifecycle based on the presence of the managedByAnnotation. When a ScaledObject
// is annotated with "autoscaling.kpodautoscaler.io/managed-backend: true", this
// controller will:
// 1. Pause KEDA's scaling by setting the paused annotation
// 2. Create/update a corresponding KPodAutoscaler resource
// 3. Handle cleanup when the ScaledObject is deleted or offboarded
func (r *ScaledObjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("scaledobject", req.NamespacedName)

	// Fetch the ScaledObject instance using unstructured
	scaledObject := &unstructured.Unstructured{}
	scaledObject.SetGroupVersionKind(ScaledObjectGVK)

	if err := r.Get(ctx, req.NamespacedName, scaledObject); err != nil {
		if errors.IsNotFound(err) {
			// ScaledObject not found, likely deleted. Return and don't requeue
			log.Info("ScaledObject not found, likely deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ScaledObject")
		return ctrl.Result{}, err
	}

	log.V(1).Info("Reconciling ScaledObject",
		"name", scaledObject.GetName(),
		"namespace", scaledObject.GetNamespace(),
		"generation", scaledObject.GetGeneration())

	// Check if the ScaledObject is marked for deletion
	if scaledObject.GetDeletionTimestamp() != nil {
		log.Info("ScaledObject is marked for deletion")
		if controllerutil.ContainsFinalizer(scaledObject, kpaFinalizer) {
			// Run cleanup logic
			if err := r.handleDeletion(ctx, scaledObject); err != nil {
				log.Error(err, "Failed to handle deletion")
				return ctrl.Result{}, err
			}

			// Remove the finalizer to allow Kubernetes to complete deletion
			controllerutil.RemoveFinalizer(scaledObject, kpaFinalizer)
			if err := r.Update(ctx, scaledObject); err != nil {
				log.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
			log.Info("Finalizer removed, ScaledObject can be deleted")
		}
		return ctrl.Result{}, nil
	}

	// Check for the managed-by annotation
	annotations := scaledObject.GetAnnotations()
	managedValue, hasAnnotation := annotations[managedByAnnotation]
	shouldManage := hasAnnotation && managedValue == annotationValueTrue

	log.V(1).Info("Checking management annotation",
		"hasAnnotation", hasAnnotation,
		"value", managedValue,
		"shouldManage", shouldManage)

	if !shouldManage {
		// Offboard: ensure KPodAutoscaler is deleted and annotations/finalizer removed
		log.Info("ScaledObject is not managed by KPodAutoscaler, offboarding")
		return r.handleOffboarding(ctx, scaledObject)
	}

	// Main reconciliation path: ScaledObject should be managed by KPodAutoscaler
	log.Info("ScaledObject is managed by KPodAutoscaler")

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(scaledObject, kpaFinalizer) {
		log.Info("Adding finalizer to ScaledObject")
		controllerutil.AddFinalizer(scaledObject, kpaFinalizer)
		if err := r.Update(ctx, scaledObject); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue with the reconciliation after adding finalizer
		return ctrl.Result{RequeueAfter: 0}, nil
	}

	// Store original values before modification
	if err := r.storeOriginalValues(ctx, scaledObject); err != nil {
		log.Error(err, "Failed to store original values")
		return ctrl.Result{}, err
	}

	// Ensure dummy deployment exists
	if err := r.ensureDummyDeployment(ctx, scaledObject); err != nil {
		log.Error(err, "Failed to ensure dummy deployment")
		return ctrl.Result{}, err
	}

	// Check if ScaledObject needs to be modified to point to dummy deployment
	currentTargetName, _, _ := getNestedString(scaledObject, "spec", "scaleTargetRef", "name")
	expectedDummyName := dummyDeploymentPrefix + scaledObject.GetName()

	if currentTargetName != expectedDummyName {
		log.Info("Modifying ScaledObject to point to dummy deployment")

		// Modify the ScaledObject
		if err := r.modifyScaledObjectForDummy(scaledObject); err != nil {
			log.Error(err, "Failed to modify ScaledObject")
			return ctrl.Result{}, err
		}

		// Update the ScaledObject
		if err := r.Update(ctx, scaledObject); err != nil {
			log.Error(err, "Failed to update ScaledObject with dummy target")
			return ctrl.Result{}, err
		}

		// Requeue to continue with the reconciliation
		return ctrl.Result{RequeueAfter: 0}, nil
	}

	// Translate ScaledObject spec to KPodAutoscaler spec using original values
	kpa, err := r.translateSpec(scaledObject)
	if err != nil {
		log.Error(err, "Failed to translate ScaledObject spec to KPodAutoscaler")
		return ctrl.Result{}, err
	}

	// Set ScaledObject as the owner of KPodAutoscaler
	if err := controllerutil.SetControllerReference(scaledObject, kpa, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference")
		return ctrl.Result{}, err
	}

	// Create or update the KPodAutoscaler
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, kpa, func() error {
		// Update the spec in case it changed
		desiredKPA, err := r.translateSpec(scaledObject)
		if err != nil {
			return err
		}
		kpa.Spec = desiredKPA.Spec

		// Add labels for tracking
		if kpa.Labels == nil {
			kpa.Labels = make(map[string]string)
		}
		kpa.Labels["app.kubernetes.io/managed-by"] = "kpodautoscaler-scaledobject-controller"
		kpa.Labels["app.kubernetes.io/name"] = fmt.Sprintf("kpa-%s", scaledObject.GetName())
		kpa.Labels["app.kubernetes.io/part-of"] = scaledObject.GetName()
		kpa.Labels["scaledobject.keda.sh/name"] = scaledObject.GetName()

		return nil
	})

	if err != nil {
		log.Error(err, "Failed to create or update KPodAutoscaler")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled KPodAutoscaler", "operation", op)
	return ctrl.Result{}, nil
}

// handleDeletion cleans up the KPodAutoscaler when the ScaledObject is being deleted
func (r *ScaledObjectReconciler) handleDeletion(ctx context.Context, scaledObject *unstructured.Unstructured) error {
	log := logf.FromContext(ctx).WithValues("scaledobject", types.NamespacedName{
		Name:      scaledObject.GetName(),
		Namespace: scaledObject.GetNamespace(),
	})

	// Delete the corresponding KPodAutoscaler
	kpa := &kpav1alpha1.KPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedKPANamePrefix + scaledObject.GetName(),
			Namespace: scaledObject.GetNamespace(),
		},
	}

	log.Info("Deleting KPodAutoscaler")
	if err := r.Delete(ctx, kpa); err != nil {
		if errors.IsNotFound(err) {
			log.Info("KPodAutoscaler already deleted")
		} else {
			return fmt.Errorf("failed to delete KPodAutoscaler: %w", err)
		}
	}

	// Delete the dummy deployment
	dummyDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dummyDeploymentPrefix + scaledObject.GetName(),
			Namespace: scaledObject.GetNamespace(),
		},
	}

	log.Info("Deleting dummy deployment")
	if err := r.Delete(ctx, dummyDeployment); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Dummy deployment already deleted")
		} else {
			return fmt.Errorf("failed to delete dummy deployment: %w", err)
		}
	}

	// Wait for the KPodAutoscaler to be actually deleted before removing finalizer
	if err := r.Get(ctx, types.NamespacedName{Name: kpa.Name, Namespace: kpa.Namespace}, kpa); err != nil {
		if errors.IsNotFound(err) {
			log.Info("KPodAutoscaler successfully deleted")
			return nil
		}
		return fmt.Errorf("failed to check KPodAutoscaler deletion status: %w", err)
	}

	// KPodAutoscaler still exists, requeue
	return fmt.Errorf("waiting for KPodAutoscaler deletion")
}

// handleOffboarding removes KPodAutoscaler and cleans up annotations when management is disabled
func (r *ScaledObjectReconciler) handleOffboarding(ctx context.Context, scaledObject *unstructured.Unstructured) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("scaledobject", types.NamespacedName{
		Name:      scaledObject.GetName(),
		Namespace: scaledObject.GetNamespace(),
	})

	needsUpdate := false

	// Delete any existing KPodAutoscaler
	kpa := &kpav1alpha1.KPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedKPANamePrefix + scaledObject.GetName(),
			Namespace: scaledObject.GetNamespace(),
		},
	}

	if err := r.Delete(ctx, kpa); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete KPodAutoscaler during offboarding")
		return ctrl.Result{}, err
	} else if err == nil {
		log.Info("Deleted KPodAutoscaler during offboarding")
	}

	// Delete the dummy deployment
	dummyDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dummyDeploymentPrefix + scaledObject.GetName(),
			Namespace: scaledObject.GetNamespace(),
		},
	}

	if err := r.Delete(ctx, dummyDeployment); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete dummy deployment during offboarding")
		return ctrl.Result{}, err
	} else if err == nil {
		log.Info("Deleted dummy deployment during offboarding")
	}

	// Restore original ScaledObject values if they exist
	annotations := scaledObject.GetAnnotations()
	if annotations != nil {
		// Check if we have original values stored
		if _, hasOriginal := annotations[originalTargetAnnotation]; hasOriginal {
			// Get original values
			minReplicas, maxReplicas, originalTargetData, err := r.getOriginalValuesFromAnnotations(scaledObject)
			if err != nil {
				log.Error(err, "Failed to get original values during offboarding")
			} else {
				// Restore original min/max replicas
				if err := unstructured.SetNestedField(scaledObject.Object, minReplicas, "spec", "minReplicaCount"); err != nil {
					log.Error(err, "Failed to restore minReplicaCount")
				}

				if err := unstructured.SetNestedField(scaledObject.Object, maxReplicas, "spec", "maxReplicaCount"); err != nil {
					log.Error(err, "Failed to restore maxReplicaCount")
				}

				// Restore original scaleTargetRef
				if originalTargetData.Name != "" {
					scaleTargetRef := map[string]interface{}{
						"name":       originalTargetData.Name,
						"kind":       originalTargetData.Kind,
						"apiVersion": originalTargetData.APIVersion,
					}

					if err := unstructured.SetNestedMap(scaledObject.Object, scaleTargetRef, "spec", "scaleTargetRef"); err != nil {
						log.Error(err, "Failed to restore scaleTargetRef")
					}
				}
			}
		}

		// Remove our annotations
		delete(annotations, minReplicasAnnotation)
		delete(annotations, maxReplicasAnnotation)
		delete(annotations, originalTargetAnnotation)
		scaledObject.SetAnnotations(annotations)
		needsUpdate = true
	}

	// Remove finalizer
	if controllerutil.ContainsFinalizer(scaledObject, kpaFinalizer) {
		log.Info("Removing finalizer")
		controllerutil.RemoveFinalizer(scaledObject, kpaFinalizer)
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Update(ctx, scaledObject); err != nil {
			log.Error(err, "Failed to update ScaledObject during offboarding")
			return ctrl.Result{}, err
		}
	}

	log.Info("Successfully offboarded ScaledObject")
	return ctrl.Result{}, nil
}

// getNestedString safely gets a string field from an unstructured object
func getNestedString(obj *unstructured.Unstructured, fields ...string) (string, bool, error) {
	return unstructured.NestedString(obj.Object, fields...)
}

// getNestedInt64 safely gets an int64 field from an unstructured object
func getNestedInt64(obj *unstructured.Unstructured, fields ...string) (int64, bool, error) {
	return unstructured.NestedInt64(obj.Object, fields...)
}

// getNestedSlice safely gets a slice field from an unstructured object
func getNestedSlice(obj *unstructured.Unstructured, fields ...string) ([]interface{}, bool, error) {
	return unstructured.NestedSlice(obj.Object, fields...)
}

// translateSpec converts a ScaledObject spec to a KPodAutoscaler spec
func (r *ScaledObjectReconciler) translateSpec(scaledObject *unstructured.Unstructured) (*kpav1alpha1.KPodAutoscaler, error) {
	// Check if we should use original values from annotations
	annotations := scaledObject.GetAnnotations()
	var targetName, targetKind, targetAPIVersion string
	var minReplicas, maxReplicas int64

	if annotations != nil && annotations[managedByAnnotation] == annotationValueTrue {
		// Get original values from annotations
		var originalTargetData originalTarget
		var err error
		minReplicas, maxReplicas, originalTargetData, err = r.getOriginalValuesFromAnnotations(scaledObject)
		if err != nil {
			return nil, fmt.Errorf("failed to get original values from annotations: %w", err)
		}

		targetName = originalTargetData.Name
		targetKind = originalTargetData.Kind
		targetAPIVersion = originalTargetData.APIVersion

		// Ensure we have valid values
		if targetName == "" {
			return nil, fmt.Errorf("original target name is empty in annotations")
		}
	} else {
		// Extract ScaleTargetRef from spec (original behavior)
		var found bool
		var err error
		targetName, found, err = getNestedString(scaledObject, "spec", "scaleTargetRef", "name")
		if err != nil {
			return nil, fmt.Errorf("failed to get scaleTargetRef.name: %w", err)
		}
		if !found || targetName == "" {
			return nil, fmt.Errorf("scaleTargetRef.name is required but not found in ScaledObject spec")
		}

		targetKind, _, _ = getNestedString(scaledObject, "spec", "scaleTargetRef", "kind")
		if targetKind == "" {
			targetKind = "Deployment" // KEDA default
		}

		targetAPIVersion, _, _ = getNestedString(scaledObject, "spec", "scaleTargetRef", "apiVersion")
		if targetAPIVersion == "" {
			targetAPIVersion = "apps/v1" // KEDA default
		}

		// Extract replica counts with KEDA defaults
		var hasMin, hasMax bool
		minReplicas, hasMin, err = getNestedInt64(scaledObject, "spec", "minReplicaCount")
		if err != nil || !hasMin {
			minReplicas = 1 // KEDA default
		}

		maxReplicas, hasMax, err = getNestedInt64(scaledObject, "spec", "maxReplicaCount")
		if err != nil || !hasMax {
			maxReplicas = 100 // KEDA default
		}
	}

	kpa := &kpav1alpha1.KPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedKPANamePrefix + scaledObject.GetName(),
			Namespace: scaledObject.GetNamespace(),
		},
		Spec: kpav1alpha1.KPodAutoscalerSpec{
			ScaleTargetRef: kpav1alpha1.ScaleTargetRef{
				Kind:       targetKind,
				Name:       targetName,
				APIVersion: targetAPIVersion,
			},
			MinReplicas: *resource.NewQuantity(minReplicas, resource.DecimalSI),
			MaxReplicas: *resource.NewQuantity(maxReplicas, resource.DecimalSI),
		},
	}

	// Translate triggers to metrics
	triggers, found, err := getNestedSlice(scaledObject, "spec", "triggers")
	if err != nil || !found {
		// No triggers is valid, just return without metrics
		return kpa, nil
	}

	metrics := []kpav1alpha1.MetricSpec{}
	for i, trigger := range triggers {
		triggerMap, ok := trigger.(map[string]interface{})
		if !ok {
			continue
		}

		metric, err := r.translateTrigger(triggerMap, i, scaledObject.GetName())
		if err != nil {
			return nil, fmt.Errorf("failed to translate trigger %d: %w", i, err)
		}
		if metric != nil {
			metrics = append(metrics, *metric)
		}
	}
	kpa.Spec.Metrics = metrics

	return kpa, nil
}

// translateTrigger converts a KEDA trigger to a KPodAutoscaler MetricSpec
func (r *ScaledObjectReconciler) translateTrigger(trigger map[string]interface{}, index int, scaledObjectName string) (*kpav1alpha1.MetricSpec, error) {
	triggerType, ok := trigger["type"].(string)
	if !ok {
		return nil, fmt.Errorf("trigger missing type")
	}

	// Extract metadata
	metadata, ok := trigger["metadata"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("trigger missing metadata")
	}

	// Convert metadata to string map
	metadataStrings := make(map[string]string)
	for k, v := range metadata {
		if s, ok := v.(string); ok {
			metadataStrings[k] = s
		}
	}

	// Extract trigger name if provided
	triggerName, _ := trigger["name"].(string)
	if triggerName == "" {
		triggerName = fmt.Sprintf("%s-%d", triggerType, index)
	}

	// Route to appropriate handler based on trigger type
	switch triggerType {
	case "prometheus":
		return r.translatePrometheusTrigger(metadataStrings, triggerName, scaledObjectName)
	case "cpu":
		return r.translateCPUTrigger(metadataStrings)
	case "memory":
		return r.translateMemoryTrigger(metadataStrings)
	case "kubernetes-workload":
		return r.translateKubernetesWorkloadTrigger(metadataStrings, scaledObjectName)
	case "external":
		return r.translateExternalTrigger(metadataStrings, triggerName, scaledObjectName)
	case "redis", "kafka", "rabbitmq", "azure-queue", "aws-sqs":
		// These are external scalers that need to be mapped to external metrics
		return r.translateExternalScalerTrigger(triggerType, metadataStrings, scaledObjectName)
	default:
		// Skip unsupported trigger types
		return nil, nil
	}
}

// translatePrometheusTrigger handles Prometheus trigger translation
func (r *ScaledObjectReconciler) translatePrometheusTrigger(metadata map[string]string, triggerName string, scaledObjectName string) (*kpav1alpha1.MetricSpec, error) {
	// Validate required fields
	if _, ok := metadata["serverAddress"]; !ok {
		return nil, fmt.Errorf("prometheus trigger missing serverAddress")
	}

	if _, ok := metadata["query"]; !ok {
		return nil, fmt.Errorf("prometheus trigger missing query")
	}

	metricName, ok := metadata["metricName"]
	if !ok {
		metricName = triggerName
	}

	// Parse threshold
	threshold, ok := metadata["threshold"]
	if !ok {
		return nil, fmt.Errorf("prometheus trigger missing threshold")
	}

	thresholdQuantity, err := resource.ParseQuantity(threshold)
	if err != nil {
		return nil, fmt.Errorf("failed to parse threshold %q: %w", threshold, err)
	}

	// Determine metric type (default to AverageValue)
	metricType := metadata["metricType"]
	if metricType == "" {
		metricType = string(kpav1alpha1.AverageValueMetricType)
	}

	// Create the MetricSpec
	metric := &kpav1alpha1.MetricSpec{
		Type: kpav1alpha1.ExternalMetricType,
		External: &kpav1alpha1.ExternalMetricSource{
			Metric: kpav1alpha1.MetricIdentifier{
				Name: metricName,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"scaledobject.keda.sh/name": scaledObjectName,
					},
				},
			},
		},
	}

	// Set the target based on metric type
	switch metricType {
	case string(kpav1alpha1.AverageValueMetricType):
		metric.External.Target = kpav1alpha1.MetricTarget{
			Type:         kpav1alpha1.AverageValueMetricType,
			AverageValue: &thresholdQuantity,
		}
	case string(kpav1alpha1.ValueMetricType):
		metric.External.Target = kpav1alpha1.MetricTarget{
			Type:  kpav1alpha1.ValueMetricType,
			Value: &thresholdQuantity,
		}
	default:
		return nil, fmt.Errorf("unsupported metricType: %s", metricType)
	}

	return metric, nil
}

// translateCPUTrigger handles CPU resource trigger translation
func (r *ScaledObjectReconciler) translateCPUTrigger(metadata map[string]string) (*kpav1alpha1.MetricSpec, error) {
	// Extract value or averageValue
	cpuType := metadata["type"]
	if cpuType == "" {
		cpuType = string(kpav1alpha1.UtilizationMetricType)
	}

	metric := &kpav1alpha1.MetricSpec{
		Type: kpav1alpha1.ResourceMetricType,
		Resource: &kpav1alpha1.ResourceMetricSource{
			Name: "cpu",
		},
	}

	switch cpuType {
	case string(kpav1alpha1.UtilizationMetricType):
		utilization, ok := metadata["value"]
		if !ok {
			utilization = "80" // Default to 80%
		}
		utilizationInt, err := strconv.Atoi(utilization)
		if err != nil {
			return nil, fmt.Errorf("invalid CPU utilization value: %w", err)
		}
		utilizationInt32 := int32(utilizationInt)
		metric.Resource.Target = kpav1alpha1.MetricTarget{
			Type:               kpav1alpha1.UtilizationMetricType,
			AverageUtilization: &utilizationInt32,
		}
	case string(kpav1alpha1.AverageValueMetricType):
		avgValue, ok := metadata["value"]
		if !ok {
			return nil, fmt.Errorf("cpu trigger missing value for AverageValue type")
		}
		avgQuantity, err := resource.ParseQuantity(avgValue)
		if err != nil {
			return nil, fmt.Errorf("failed to parse CPU average value: %w", err)
		}
		metric.Resource.Target = kpav1alpha1.MetricTarget{
			Type:         kpav1alpha1.AverageValueMetricType,
			AverageValue: &avgQuantity,
		}
	default:
		return nil, fmt.Errorf("unsupported CPU metric type: %s", cpuType)
	}

	return metric, nil
}

// translateMemoryTrigger handles Memory resource trigger translation
func (r *ScaledObjectReconciler) translateMemoryTrigger(metadata map[string]string) (*kpav1alpha1.MetricSpec, error) {
	// Extract value or averageValue
	memoryType := metadata["type"]
	if memoryType == "" {
		memoryType = string(kpav1alpha1.UtilizationMetricType)
	}

	metric := &kpav1alpha1.MetricSpec{
		Type: kpav1alpha1.ResourceMetricType,
		Resource: &kpav1alpha1.ResourceMetricSource{
			Name: "memory",
		},
	}

	switch memoryType {
	case string(kpav1alpha1.UtilizationMetricType):
		utilization, ok := metadata["value"]
		if !ok {
			utilization = "80" // Default to 80%
		}
		utilizationInt, err := strconv.Atoi(utilization)
		if err != nil {
			return nil, fmt.Errorf("invalid memory utilization value: %w", err)
		}
		utilizationInt32 := int32(utilizationInt)
		metric.Resource.Target = kpav1alpha1.MetricTarget{
			Type:               kpav1alpha1.UtilizationMetricType,
			AverageUtilization: &utilizationInt32,
		}
	case string(kpav1alpha1.AverageValueMetricType):
		avgValue, ok := metadata["value"]
		if !ok {
			return nil, fmt.Errorf("memory trigger missing value for AverageValue type")
		}
		avgQuantity, err := resource.ParseQuantity(avgValue)
		if err != nil {
			return nil, fmt.Errorf("failed to parse memory average value: %w", err)
		}
		metric.Resource.Target = kpav1alpha1.MetricTarget{
			Type:         kpav1alpha1.AverageValueMetricType,
			AverageValue: &avgQuantity,
		}
	default:
		return nil, fmt.Errorf("unsupported memory metric type: %s", memoryType)
	}

	return metric, nil
}

// translateKubernetesWorkloadTrigger handles Kubernetes workload trigger translation
func (r *ScaledObjectReconciler) translateKubernetesWorkloadTrigger(metadata map[string]string, scaledObjectName string) (*kpav1alpha1.MetricSpec, error) {
	name := metadata["name"]
	value := metadata["value"]

	if name == "" || value == "" {
		return nil, fmt.Errorf("kubernetes-workload trigger missing required fields")
	}

	targetValue, err := resource.ParseQuantity(value)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubernetes-workload value: %w", err)
	}

	metric := &kpav1alpha1.MetricSpec{
		Type: kpav1alpha1.ObjectMetricType,
		Object: &kpav1alpha1.ObjectMetricSource{
			DescribedObject: kpav1alpha1.CrossVersionObjectReference{
				Kind:       "Pod",
				Name:       name,
				APIVersion: "v1",
			},
			Metric: kpav1alpha1.MetricIdentifier{
				Name: "pending_pods",
			},
			Target: kpav1alpha1.MetricTarget{
				Type:  kpav1alpha1.ValueMetricType,
				Value: &targetValue,
			},
		},
	}

	// Use simple selector referencing the ScaledObject
	metric.Object.Metric.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"scaledobject.keda.sh/name": scaledObjectName,
		},
	}

	return metric, nil
}

// translateExternalTrigger handles generic external trigger translation
func (r *ScaledObjectReconciler) translateExternalTrigger(metadata map[string]string, triggerName string, scaledObjectName string) (*kpav1alpha1.MetricSpec, error) {
	metricName := metadata["metricName"]
	if metricName == "" {
		metricName = triggerName
	}

	targetValue := metadata["targetValue"]
	if targetValue == "" {
		return nil, fmt.Errorf("external trigger missing targetValue")
	}

	targetQuantity, err := resource.ParseQuantity(targetValue)
	if err != nil {
		return nil, fmt.Errorf("failed to parse targetValue: %w", err)
	}

	metric := &kpav1alpha1.MetricSpec{
		Type: kpav1alpha1.ExternalMetricType,
		External: &kpav1alpha1.ExternalMetricSource{
			Metric: kpav1alpha1.MetricIdentifier{
				Name: metricName,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"scaledobject.keda.sh/name": scaledObjectName,
					},
				},
			},
			Target: kpav1alpha1.MetricTarget{
				Type:  kpav1alpha1.ValueMetricType,
				Value: &targetQuantity,
			},
		},
	}

	return metric, nil
}

// translateExternalScalerTrigger handles specific external scaler triggers (redis, kafka, etc.)
func (r *ScaledObjectReconciler) translateExternalScalerTrigger(scalerType string, metadata map[string]string, scaledObjectName string) (*kpav1alpha1.MetricSpec, error) {
	// Generic handling for external scalers
	// Each scaler type would have its specific metadata requirements

	// Generate metric name similar to KEDA pattern
	var metricName string
	var targetValue string

	switch scalerType {
	case "redis":
		targetValue = metadata["listLength"]
		listName := metadata["listName"]
		if listName != "" {
			metricName = fmt.Sprintf("s0-%s-%s", scalerType, listName)
		} else {
			metricName = fmt.Sprintf("s0-%s", scalerType)
		}
	case "kafka":
		targetValue = metadata["lagThreshold"]
		topic := metadata["topic"]
		if topic != "" {
			metricName = fmt.Sprintf("s0-%s-%s", scalerType, topic)
		} else {
			metricName = fmt.Sprintf("s0-%s", scalerType)
		}
	case "rabbitmq":
		targetValue = metadata["queueLength"]
		queueName := metadata["queueName"]
		if queueName != "" {
			metricName = fmt.Sprintf("s0-%s-%s", scalerType, queueName)
		} else {
			metricName = fmt.Sprintf("s0-%s", scalerType)
		}
	case "azure-queue", "aws-sqs":
		targetValue = metadata["queueLength"]
		queueName := metadata["queueName"]
		if queueName != "" {
			metricName = fmt.Sprintf("s0-%s-%s", scalerType, queueName)
		} else {
			metricName = fmt.Sprintf("s0-%s", scalerType)
		}
	default:
		targetValue = metadata["threshold"]
		metricName = fmt.Sprintf("s0-%s", scalerType)
	}

	if targetValue == "" {
		return nil, fmt.Errorf("%s trigger missing target value", scalerType)
	}

	targetQuantity, err := resource.ParseQuantity(targetValue)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s target value: %w", scalerType, err)
	}

	// Use a simple selector like KEDA - just reference the parent ScaledObject
	// The actual scaler configuration would be handled by the metrics adapter
	// which would need to look up the ScaledObject to get the full metadata
	selector := map[string]string{
		"scaledobject.keda.sh/name": scaledObjectName,
	}

	metric := &kpav1alpha1.MetricSpec{
		Type: kpav1alpha1.ExternalMetricType,
		External: &kpav1alpha1.ExternalMetricSource{
			Metric: kpav1alpha1.MetricIdentifier{
				Name: metricName,
				Selector: &metav1.LabelSelector{
					MatchLabels: selector,
				},
			},
			Target: kpav1alpha1.MetricTarget{
				Type:         kpav1alpha1.AverageValueMetricType,
				AverageValue: &targetQuantity,
			},
		},
	}

	return metric, nil
}

// originalTarget stores the original scaleTargetRef information
type originalTarget struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
}

// ensureDummyDeployment creates or updates a minimal dummy deployment for KEDA to manage
func (r *ScaledObjectReconciler) ensureDummyDeployment(ctx context.Context, scaledObject *unstructured.Unstructured) error {
	log := logf.FromContext(ctx)

	dummyName := dummyDeploymentPrefix + scaledObject.GetName()
	namespace := scaledObject.GetNamespace()

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dummyName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kpodautoscaler",
				"app.kubernetes.io/component":  "dummy-deployment",
				"scaledobject.keda.sh/name":    scaledObject.GetName(),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": dummyName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": dummyName,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "pause",
							Image: "gcr.io/google_containers/pause:3.1",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1m"),
									corev1.ResourceMemory: resource.MustParse("1Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1m"),
									corev1.ResourceMemory: resource.MustParse("1Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	// Set ScaledObject as the owner of the dummy deployment
	if err := controllerutil.SetControllerReference(scaledObject, deployment, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on dummy deployment: %w", err)
	}

	// Create or update the deployment
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		// Update is a no-op since we want a minimal deployment
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to ensure dummy deployment: %w", err)
	}

	log.Info("Ensured dummy deployment", "operation", op, "deployment", dummyName)
	return nil
}

// storeOriginalValues stores the original ScaledObject values in annotations
func (r *ScaledObjectReconciler) storeOriginalValues(ctx context.Context, scaledObject *unstructured.Unstructured) error {
	log := logf.FromContext(ctx)
	annotations := scaledObject.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Check if we already stored the values
	if _, hasMin := annotations[minReplicasAnnotation]; hasMin {
		log.V(1).Info("Original values already stored")
		return nil
	}

	// Get original values
	minReplicas, hasMin, err := getNestedInt64(scaledObject, "spec", "minReplicaCount")
	if err != nil || !hasMin {
		minReplicas = 1 // KEDA default
	}

	maxReplicas, hasMax, err := getNestedInt64(scaledObject, "spec", "maxReplicaCount")
	if err != nil || !hasMax {
		maxReplicas = 100 // KEDA default
	}

	// Get original target
	targetName, _, _ := getNestedString(scaledObject, "spec", "scaleTargetRef", "name")
	targetKind, _, _ := getNestedString(scaledObject, "spec", "scaleTargetRef", "kind")
	targetAPIVersion, _, _ := getNestedString(scaledObject, "spec", "scaleTargetRef", "apiVersion")

	if targetKind == "" {
		targetKind = "Deployment"
	}
	if targetAPIVersion == "" {
		targetAPIVersion = "apps/v1"
	}

	originalTargetData := originalTarget{
		Name:       targetName,
		Kind:       targetKind,
		APIVersion: targetAPIVersion,
	}

	targetJSON, err := json.Marshal(originalTargetData)
	if err != nil {
		return fmt.Errorf("failed to marshal original target: %w", err)
	}

	// Store in annotations
	annotations[minReplicasAnnotation] = strconv.FormatInt(minReplicas, 10)
	annotations[maxReplicasAnnotation] = strconv.FormatInt(maxReplicas, 10)
	annotations[originalTargetAnnotation] = string(targetJSON)

	scaledObject.SetAnnotations(annotations)

	log.Info("Stored original values in annotations",
		"minReplicas", minReplicas,
		"maxReplicas", maxReplicas,
		"originalTarget", originalTargetData)

	return nil
}

// modifyScaledObjectForDummy modifies the ScaledObject to point to the dummy deployment
func (r *ScaledObjectReconciler) modifyScaledObjectForDummy(scaledObject *unstructured.Unstructured) error {
	dummyName := dummyDeploymentPrefix + scaledObject.GetName()

	// Set minReplicaCount and maxReplicaCount to 1
	if err := unstructured.SetNestedField(scaledObject.Object, int64(1), "spec", "minReplicaCount"); err != nil {
		return fmt.Errorf("failed to set minReplicaCount: %w", err)
	}

	if err := unstructured.SetNestedField(scaledObject.Object, int64(1), "spec", "maxReplicaCount"); err != nil {
		return fmt.Errorf("failed to set maxReplicaCount: %w", err)
	}

	// Update scaleTargetRef to point to dummy deployment
	scaleTargetRef := map[string]interface{}{
		"name":       dummyName,
		"kind":       "Deployment",
		"apiVersion": "apps/v1",
	}

	if err := unstructured.SetNestedMap(scaledObject.Object, scaleTargetRef, "spec", "scaleTargetRef"); err != nil {
		return fmt.Errorf("failed to set scaleTargetRef: %w", err)
	}

	return nil
}

// getOriginalValuesFromAnnotations retrieves the original values from annotations
func (r *ScaledObjectReconciler) getOriginalValuesFromAnnotations(scaledObject *unstructured.Unstructured) (minReplicas, maxReplicas int64, target originalTarget, err error) {
	annotations := scaledObject.GetAnnotations()

	// Get min replicas
	if minStr, ok := annotations[minReplicasAnnotation]; ok {
		minReplicas, err = strconv.ParseInt(minStr, 10, 64)
		if err != nil {
			return 0, 0, target, fmt.Errorf("failed to parse min replicas: %w", err)
		}
	} else {
		minReplicas = 1 // Default
	}

	// Get max replicas
	if maxStr, ok := annotations[maxReplicasAnnotation]; ok {
		maxReplicas, err = strconv.ParseInt(maxStr, 10, 64)
		if err != nil {
			return 0, 0, target, fmt.Errorf("failed to parse max replicas: %w", err)
		}
	} else {
		maxReplicas = 100 // Default
	}

	// Get original target
	if targetStr, ok := annotations[originalTargetAnnotation]; ok {
		if err = json.Unmarshal([]byte(targetStr), &target); err != nil {
			return 0, 0, target, fmt.Errorf("failed to unmarshal original target: %w", err)
		}
	}

	return minReplicas, maxReplicas, target, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScaledObjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create an unstructured object for ScaledObject
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ScaledObjectGVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(u).
		Owns(&kpav1alpha1.KPodAutoscaler{}).
		Named("scaledobject").
		Complete(r)
}
