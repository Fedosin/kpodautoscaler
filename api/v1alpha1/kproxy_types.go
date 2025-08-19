/*
Copyright 2025.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KProxySpec defines the desired state of KProxy
type KProxySpec struct {
	// TargetRef points to the upstream Deployment the kproxy will send traffic to.
	// +kubebuilder:validation:Required
	TargetRef KProxyTargetRef `json:"targetRef"`

	// BufferBytes is the maximum request body bytes Envoy will buffer per request (HTTP buffer filter).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1048576
	BufferBytes int64 `json:"bufferBytes"`

	// MaxPendingRequests bounds the Envoy cluster pending queue (circuit breaker).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1024
	MaxPendingRequests int32 `json:"maxPendingRequests"`

	// Retry policy for upstream routing.
	// +kubebuilder:validation:Optional
	Retry KProxyRetry `json:"retry,omitempty"`

	// ExternalService config for the Service exposing the kproxy.
	// +kubebuilder:validation:Required
	ExternalService KProxyExternalService `json:"externalService"`

	// HeadlessService name for direct-to-pod discovery of the target.
	// +kubebuilder:validation:Required
	HeadlessService KProxyHeadlessService `json:"headlessService"`

	// Envoy runtime settings.
	// +kubebuilder:validation:Optional
	Envoy KProxyEnvoy `json:"envoy,omitempty"`
}

type KProxyTargetRef struct {
	// Name of the target Deployment (in the same namespace as the KProxy unless Namespace set).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the target Deployment. Defaults to KProxy namespace if empty.
	// +kubebuilder:validation:Optional
	Namespace string `json:"namespace,omitempty"`

	// Port that target pods listen on.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// Selector allows overriding or supplementing the target's matchLabels (used for the headless Service).
	// If empty, the operator reads .spec.selector.matchLabels from the Deployment.
	// +kubebuilder:validation:Optional
	Selector map[string]string `json:"selector,omitempty"`
}

type KProxyRetry struct {
	// Comma-separated retry_on conditions.
	// +kubebuilder:default="5xx,connect-failure,refused-stream"
	RetryOn string `json:"retryOn,omitempty"`
	// Number of retries.
	// +kubebuilder:default=3
	NumRetries int32 `json:"numRetries,omitempty"`
	// Base backoff in milliseconds.
	// +kubebuilder:default=25
	BaseIntervalMs int32 `json:"baseIntervalMs,omitempty"`
	// Max backoff in milliseconds.
	// +kubebuilder:default=250
	MaxIntervalMs int32 `json:"maxIntervalMs,omitempty"`
}

type KProxyExternalService struct {
	// Name of the external Service that points at the kproxy.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Type of the external Service (ClusterIP|NodePort|LoadBalancer).
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	Type string `json:"type,omitempty"`
	// Port exposed by the external Service.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=80
	Port int32 `json:"port,omitempty"`
}

type KProxyHeadlessService struct {
	// Name of the headless Service selecting target pods.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type KProxyEnvoy struct {
	// Admin port for Envoy.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9901
	AdminPort int32 `json:"adminPort,omitempty"`
	// Drain seconds before pod termination.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=25
	DrainSeconds int32 `json:"drainSeconds,omitempty"`
	// HTTP listener port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	ListenerPort int32 `json:"listenerPort,omitempty"`
	// Stat prefix used in http_connection_manager.
	// +kubebuilder:default="ingresshttp"
	StatPrefix string `json:"statPrefix,omitempty"`
	// If true, add annotation to disable Istio sidecar injection for the kproxy.
	// +kubebuilder:default=true
	DisableIstioInjection bool `json:"disableIstioInjection,omitempty"`
}

// KProxyStatus defines the observed state of KProxy
type KProxyStatus struct {
	ObservedGeneration   int64              `json:"observedGeneration,omitempty"`
	Conditions           []metav1.Condition `json:"conditions,omitempty"`
	TargetEndpointCount  int32              `json:"targetEndpointCount,omitempty"`
	KProxyDeploymentName string             `json:"kproxyDeploymentName,omitempty"`
	ExternalServiceName  string             `json:"externalServiceName,omitempty"`
	ConfigHash           string             `json:"configHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=kproxies,scope=Namespaced,shortName=kpx

// KProxy is the Schema for the kproxies API
type KProxy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KProxySpec   `json:"spec,omitempty"`
	Status KProxyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KProxyList contains a list of KProxy
type KProxyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KProxy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KProxy{}, &KProxyList{})
}
