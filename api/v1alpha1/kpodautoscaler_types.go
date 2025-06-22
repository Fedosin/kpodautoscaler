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

package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MetricType is the type of metric
type MetricType string

const (
	// ResourceMetricType is a resource metric known to Kubernetes, as specified in
	// requests and limits, describing each pod in the current scale target (e.g. CPU or memory).
	ResourceMetricType MetricType = "Resource"
	// PodsMetricType is a metric describing each pod in the current scale
	// target (for example, transactions-processed-per-second).
	PodsMetricType MetricType = "Pods"
	// ObjectMetricType is a metric describing a single kubernetes object
	// (for example, hits-per-second on an Ingress object).
	ObjectMetricType MetricType = "Object"
	// ExternalMetricType is a global metric that is not associated
	// with any Kubernetes object. It allows autoscaling based on information
	// coming from components running outside of cluster.
	ExternalMetricType MetricType = "External"
)

// MetricTargetType specifies the type of metric being targeted, and should be either
// "Value", "AverageValue", or "Utilization"
type MetricTargetType string

const (
	// UtilizationMetricType declares a MetricTarget is an AverageUtilization value
	UtilizationMetricType MetricTargetType = "Utilization"
	// ValueMetricType declares a MetricTarget is a raw value
	ValueMetricType MetricTargetType = "Value"
	// AverageValueMetricType declares a MetricTarget is an average value
	AverageValueMetricType MetricTargetType = "AverageValue"
)

// MetricConfig contains configuration for libkpa autoscaler
type MetricConfig struct {
	// Algorithm specifies the algorithm to use for autoscaling
	// Possible values: "linear" (default) or "weighted"
	// +optional
	Algorithm string `json:"algorithm,omitempty"`

	// WindowSize is the time window for stable metrics
	// +optional
	WindowSize *metav1.Duration `json:"windowSize,omitempty"`

	// PanicWindow is the time window for panic metrics
	// +optional
	PanicWindow *metav1.Duration `json:"panicWindow,omitempty"`

	// ScaleUpRate is the maximum scale up rate
	// +optional
	ScaleUpRate *resource.Quantity `json:"scaleUpRate,omitempty"`

	// ScaleDownRate is the maximum scale down rate
	// +optional
	ScaleDownRate *resource.Quantity `json:"scaleDownRate,omitempty"`

	// MaxScaleUpRate is the maximum scale up rate
	// +optional
	MaxScaleUpRate *resource.Quantity `json:"maxScaleUpRate,omitempty"`

	// MaxScaleDownRate is the maximum scale down rate
	// +optional
	MaxScaleDownRate *resource.Quantity `json:"maxScaleDownRate,omitempty"`

	// PanicThreshold is the threshold for entering panic mode
	// +optional
	PanicThreshold *resource.Quantity `json:"panicThreshold,omitempty"`

	// StableWindow is the window size for stable metrics
	// +optional
	StableWindow *metav1.Duration `json:"stableWindow,omitempty"`

	// InitialScale is the initial scale value
	// +optional
	InitialScale *int32 `json:"initialScale,omitempty"`

	// TargetUtilization is the target utilization percentage
	// +optional
	TargetUtilization *resource.Quantity `json:"targetUtilization,omitempty"`
}

// MetricTarget defines the target value, average value, or average utilization of a specific metric
type MetricTarget struct {
	// type represents whether the metric type is Utilization, Value, or AverageValue
	Type MetricTargetType `json:"type"`
	// value is the target value of the metric (as a quantity).
	// +optional
	Value *resource.Quantity `json:"value,omitempty"`
	// averageValue is the target value of the average of the
	// metric across all relevant pods (as a quantity)
	// +optional
	AverageValue *resource.Quantity `json:"averageValue,omitempty"`
	// averageUtilization is the target value of the average of the
	// resource metric across all relevant pods, represented as a percentage of
	// the requested value of the resource for the pods.
	// Currently only valid for Resource metric source type
	// +optional
	AverageUtilization *int32 `json:"averageUtilization,omitempty"`
}

// ResourceMetricSource indicates how to scale on a resource metric known to
// Kubernetes, as specified in requests and limits, describing each pod in the
// current scale target (e.g. CPU or memory).
type ResourceMetricSource struct {
	// name is the name of the resource in question.
	Name v1.ResourceName `json:"name"`
	// target specifies the target value for the given metric
	Target MetricTarget `json:"target"`
}

// PodsMetricSource indicates how to scale on a metric describing each pod in
// the current scale target (for example, transactions-processed-per-second).
type PodsMetricSource struct {
	// metric identifies the target metric by name and selector
	Metric MetricIdentifier `json:"metric"`
	// target specifies the target value for the given metric
	Target MetricTarget `json:"target"`
}

// ObjectMetricSource indicates how to scale on a metric describing a
// kubernetes object (for example, hits-per-second on an Ingress object).
type ObjectMetricSource struct {
	// describedObject specifies the descriptions of a object,such as kind,name apiVersion
	DescribedObject CrossVersionObjectReference `json:"describedObject"`
	// target specifies the target value for the given metric
	Target MetricTarget `json:"target"`
	// metric identifies the target metric by name and selector
	Metric MetricIdentifier `json:"metric"`
}

// ExternalMetricSource indicates how to scale on a metric not associated with
// any Kubernetes object (for example length of queue in cloud
// messaging service, or QPS from loadbalancer running outside of cluster).
type ExternalMetricSource struct {
	// metric identifies the target metric by name and selector
	Metric MetricIdentifier `json:"metric"`
	// target specifies the target value for the given metric
	Target MetricTarget `json:"target"`
}

// MetricIdentifier defines the name and optionally selector for a metric
type MetricIdentifier struct {
	// name is the name of the given metric
	Name string `json:"name"`
	// selector is the string-encoded form of a standard kubernetes label selector for the given metric
	// When set, it is passed as an additional parameter to the metrics server for more specific metrics scoping.
	// When unset, just the metricName will be used to gather metrics.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// CrossVersionObjectReference contains enough information to let you identify the
// referred resource.
type CrossVersionObjectReference struct {
	// Kind of the referent; More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds"
	Kind string `json:"kind"`
	// Name of the referent; More info: http://kubernetes.io/docs/user-guide/identifiers#names
	Name string `json:"name"`
	// API version of the referent
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}

// MetricSpec specifies how to scale based on a single metric
// (only `type` and one other matching field should be set at once).
type MetricSpec struct {
	// type is the type of metric source.  It should be one of "Object", "Pods" or "Resource", each mapping to a matching field in the object.
	Type MetricType `json:"type"`

	// config contains libkpa autoscaler configuration for this metric
	// +optional
	Config *MetricConfig `json:"config,omitempty"`

	// resource refers to a resource metric (such as those specified in
	// requests and limits) known to Kubernetes describing each pod in the
	// current scale target (e.g. CPU or memory). Such metrics are built in to
	// Kubernetes, and have special scaling options on top of those available
	// to normal per-pod metrics using the "pods" source.
	// +optional
	Resource *ResourceMetricSource `json:"resource,omitempty"`
	// pods refers to a metric describing each pod in the current scale target
	// (for example, transactions-processed-per-second).  The values will be
	// averaged together before being compared to the target value.
	// +optional
	Pods *PodsMetricSource `json:"pods,omitempty"`
	// object refers to a metric describing a single kubernetes object
	// (for example, hits-per-second on an Ingress object).
	// +optional
	Object *ObjectMetricSource `json:"object,omitempty"`
	// external refers to a global metric that is not associated
	// with any Kubernetes object. It allows autoscaling based on information
	// coming from components running outside of cluster
	// (for example length of queue in cloud messaging service, or
	// QPS from loadbalancer running outside of cluster).
	// +optional
	External *ExternalMetricSource `json:"external,omitempty"`
}

// ScaleTargetRef contains reference to the scalable resource
type ScaleTargetRef struct {
	// apiVersion is the API version of the referent
	APIVersion string `json:"apiVersion,omitempty"`
	// kind is the kind of the referent; More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`
	// name is the name of the referent; More info: http://kubernetes.io/docs/user-guide/identifiers#names
	Name string `json:"name"`
}

// KPodAutoscalerSpec defines the desired state of KPodAutoscaler
type KPodAutoscalerSpec struct {
	// scaleTargetRef points to the target resource to scale, and is used to the pods for which metrics
	// should be collected, as well as to actually change the replica count.
	ScaleTargetRef ScaleTargetRef `json:"scaleTargetRef"`

	// minReplicas is the lower limit for the number of replicas to which the autoscaler
	// can scale down.  It defaults to 1 pod.  minReplicas is allowed to be 0 if the
	// alpha feature gate HPAScaleToZero is enabled and at least one Object or External
	// metric is configured.  Scaling is active as long as at least one metric value is
	// available.
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// maxReplicas is the upper limit for the number of replicas to which the autoscaler can scale up.
	// It cannot be less that minReplicas.
	MaxReplicas int32 `json:"maxReplicas"`

	// metrics contains the specifications for which to use to calculate the
	// desired replica count (the maximum replica count across all metrics will
	// be used).  The desired replica count is calculated multiplying the
	// ratio between the target value and the current value by the current
	// number of pods.  Ergo, metrics used must decrease as the pod count is
	// increased, and vice-versa.  See the individual metric source types for
	// more information about how each type of metric must respond.
	// If not set, the default metric will be set to 80% average CPU utilization.
	// +optional
	Metrics []MetricSpec `json:"metrics,omitempty"`
}

// KPodAutoscalerConditionType are the valid conditions of a KPodAutoscaler
type KPodAutoscalerConditionType string

const (
	// ScalingActive indicates that the KPodAutoscaler controller is able to scale if necessary:
	// it's correctly configured, can fetch the desired metrics, and isn't disabled.
	ScalingActive KPodAutoscalerConditionType = "ScalingActive"
	// AbleToScale indicates a lack of transient issues which prevent scaling from occurring,
	// such as being in a backoff window, or being unable to access/update the target scale.
	AbleToScale KPodAutoscalerConditionType = "AbleToScale"
	// ScalingLimited indicates that the calculated scale based on metrics would be above or
	// below the range for the KPodAutoscaler, and has thus been capped.
	ScalingLimited KPodAutoscalerConditionType = "ScalingLimited"
)

// KPodAutoscalerCondition describes the state of a KPodAutoscaler at a certain point.
type KPodAutoscalerCondition struct {
	// type describes the current condition
	Type KPodAutoscalerConditionType `json:"type"`
	// status is the status of the condition (True, False, Unknown)
	Status v1.ConditionStatus `json:"status"`
	// lastTransitionTime is the last time the condition transitioned from
	// one status to another
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// reason is the reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`
	// message is a human-readable explanation containing details about
	// the transition
	// +optional
	Message string `json:"message,omitempty"`
}

// CurrentMetricStatus describes the current status of a metric
type CurrentMetricStatus struct {
	// type is the type of metric source.  It will be one of "Object", "Pods" or "Resource", each corresponds to a matching field in the object.
	Type MetricType `json:"type"`

	// resource refers to a resource metric (such as those specified in
	// requests and limits) known to Kubernetes describing each pod in the
	// current scale target (e.g. CPU or memory). Such metrics are built in to
	// Kubernetes, and have special scaling options on top of those available
	// to normal per-pod metrics using the "pods" source.
	// +optional
	Resource *ResourceMetricStatus `json:"resource,omitempty"`
	// pods refers to a metric describing each pod in the current scale target
	// (for example, transactions-processed-per-second).  The values will be
	// averaged together before being compared to the target value.
	// +optional
	Pods *PodsMetricStatus `json:"pods,omitempty"`
	// object refers to a metric describing a single kubernetes object
	// (for example, hits-per-second on an Ingress object).
	// +optional
	Object *ObjectMetricStatus `json:"object,omitempty"`
	// external refers to a global metric that is not associated
	// with any Kubernetes object. It allows autoscaling based on information
	// coming from components running outside of cluster
	// (for example length of queue in cloud messaging service, or
	// QPS from loadbalancer running outside of cluster).
	// +optional
	External *ExternalMetricStatus `json:"external,omitempty"`
}

// ResourceMetricStatus indicates the current value of a resource metric known to
// Kubernetes, as specified in requests and limits, describing each pod in the
// current scale target (e.g. CPU or memory).
type ResourceMetricStatus struct {
	// Name is the name of the resource in question.
	Name v1.ResourceName `json:"name"`
	// current contains the current value for the given metric
	Current MetricValueStatus `json:"current"`
}

// PodsMetricStatus indicates the current value of a metric describing each pod in
// the current scale target (for example, transactions-processed-per-second).
type PodsMetricStatus struct {
	// metric identifies the target metric by name and selector
	Metric MetricIdentifier `json:"metric"`
	// current contains the current value for the given metric
	Current MetricValueStatus `json:"current"`
}

// ObjectMetricStatus indicates the current value of a metric describing a
// kubernetes object (for example, hits-per-second on an Ingress object).
type ObjectMetricStatus struct {
	// metric identifies the target metric by name and selector
	Metric MetricIdentifier `json:"metric"`
	// current contains the current value for the given metric
	Current MetricValueStatus `json:"current"`
	// DescribedObject specifies the descriptions of a object,such as kind,name apiVersion
	DescribedObject CrossVersionObjectReference `json:"describedObject"`
}

// ExternalMetricStatus indicates the current value of a global metric
// not associated with any Kubernetes object.
type ExternalMetricStatus struct {
	// metric identifies the target metric by name and selector
	Metric MetricIdentifier `json:"metric"`
	// current contains the current value for the given metric
	Current MetricValueStatus `json:"current"`
}

// MetricValueStatus holds the current value for a metric
type MetricValueStatus struct {
	// value is the current value of the metric (as a quantity).
	// +optional
	Value *resource.Quantity `json:"value,omitempty"`
	// averageValue is the current value of the average of the
	// metric across all relevant pods (as a quantity)
	// +optional
	AverageValue *resource.Quantity `json:"averageValue,omitempty"`
	// currentAverageUtilization is the current value of the average of the
	// resource metric across all relevant pods, represented as a percentage of
	// the requested value of the resource for the pods.
	// +optional
	AverageUtilization *int32 `json:"averageUtilization,omitempty"`
}

// KPodAutoscalerStatus defines the observed state of KPodAutoscaler
type KPodAutoscalerStatus struct {
	// observedGeneration is the most recent generation observed by this autoscaler.
	// +optional
	ObservedGeneration *int64 `json:"observedGeneration,omitempty"`

	// lastScaleTime is the last time the KPodAutoscaler scaled the number of pods,
	// used by the autoscaler to control how often the number of pods is changed.
	// +optional
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`

	// currentReplicas is current number of replicas of pods managed by this autoscaler,
	// as last seen by the autoscaler.
	CurrentReplicas int32 `json:"currentReplicas"`

	// desiredReplicas is the desired number of replicas of pods managed by this autoscaler,
	// as last calculated by the autoscaler.
	DesiredReplicas int32 `json:"desiredReplicas"`

	// currentMetrics is the last read state of the metrics used by this autoscaler.
	// +optional
	CurrentMetrics []CurrentMetricStatus `json:"currentMetrics"`

	// conditions is the set of conditions required for this autoscaler to scale its target,
	// and indicates whether or not those conditions are met.
	Conditions []KPodAutoscalerCondition `json:"conditions"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=kpa
//+kubebuilder:printcolumn:name="Reference",type="string",JSONPath=".spec.scaleTargetRef.name"
//+kubebuilder:printcolumn:name="Targets",type="string",JSONPath=".status.currentMetrics[0].resource.current.averageUtilization",description="The current metric value"
//+kubebuilder:printcolumn:name="MinReplicas",type="integer",JSONPath=".spec.minReplicas"
//+kubebuilder:printcolumn:name="MaxReplicas",type="integer",JSONPath=".spec.maxReplicas"
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.currentReplicas"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KPodAutoscaler is the Schema for the kpodautoscalers API
type KPodAutoscaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KPodAutoscalerSpec   `json:"spec,omitempty"`
	Status KPodAutoscalerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KPodAutoscalerList contains a list of KPodAutoscaler
type KPodAutoscalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KPodAutoscaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KPodAutoscaler{}, &KPodAutoscalerList{})
}
