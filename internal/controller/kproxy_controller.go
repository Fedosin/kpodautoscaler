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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	autoscalingv1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// KProxyReconciler reconciles a KProxy object
type KProxyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	condTargetsDiscovered = "TargetsDiscovered"
	condKProxyAvailable   = "KProxyAvailable"
	hashAnnotationKey     = "kproxy.kpodautoscaler.io/config-hash"
	appLabelKey           = "app"
)

//+kubebuilder:rbac:groups=autoscaling.kpodautoscaler.io,resources=kproxies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling.kpodautoscaler.io,resources=kproxies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=autoscaling.kpodautoscaler.io,resources=kproxies/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=services;configmaps;events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=endpoints;pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="discovery.k8s.io",resources=endpointslices,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *KProxyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var kproxy autoscalingv1alpha1.KProxy
	if err := r.Get(ctx, req.NamespacedName, &kproxy); err != nil {
		if kerrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Defaults
	defaultKProxy(&kproxy)

	// Resolve target Deployment
	targetNS := kproxy.Spec.TargetRef.Namespace
	if targetNS == "" {
		targetNS = kproxy.Namespace
	}
	var targetDep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: targetNS, Name: kproxy.Spec.TargetRef.Name}, &targetDep); err != nil {
		r.setCondition(ctx, &kproxy, condTargetsDiscovered, metav1.ConditionFalse, "TargetMissing", fmt.Sprintf("Deployment %s/%s not found", targetNS, kproxy.Spec.TargetRef.Name))
		return ctrl.Result{RequeueAfter: 10 * time.Second}, client.IgnoreNotFound(err)
	}

	// Compute selector for headless service
	selector := map[string]string{}
	if len(kproxy.Spec.TargetRef.Selector) > 0 {
		for k, v := range kproxy.Spec.TargetRef.Selector {
			selector[k] = v
		}
	} else if targetDep.Spec.Selector != nil {
		for k, v := range targetDep.Spec.Selector.MatchLabels {
			selector[k] = v
		}
	}
	if len(selector) == 0 {
		r.setCondition(ctx, &kproxy, condTargetsDiscovered, metav1.ConditionFalse, "NoSelector", "No labels available to select target pods")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Count target endpoints (for status)
	endpointsCount, err := r.countReadyEndpoints(ctx, targetNS, selector, int(kproxy.Spec.TargetRef.Port))
	if err != nil {
		logger.Error(err, "countReadyEndpoints failed")
	}

	// Ensure headless service
	headlessName := kproxy.Spec.HeadlessService.Name
	if err := r.reconcileHeadlessService(ctx, &kproxy, headlessName, targetNS, selector, kproxy.Spec.TargetRef.Port); err != nil {
		return ctrl.Result{}, err
	}

	// Build Envoy config
	fqdn := fmt.Sprintf("%s.%s.svc.cluster.local", headlessName, targetNS)
	envoyYAML := buildEnvoyYAML(kproxy.Spec, fqdn)

	// Config hash
	configHash := hashStrings([]string{
		envoyYAML,
		fmt.Sprintf("%d", kproxy.Spec.BufferBytes),
		fmt.Sprintf("%d", kproxy.Spec.MaxPendingRequests),
		kproxy.Spec.Retry.RetryOn,
		fmt.Sprintf("%d", kproxy.Spec.Retry.NumRetries),
		fmt.Sprintf("%d", kproxy.Spec.Retry.BaseIntervalMs),
		fmt.Sprintf("%d", kproxy.Spec.Retry.MaxIntervalMs),
		targetNS, kproxy.Spec.TargetRef.Name,
	})

	// Ensure ConfigMap
	cmName := kproxy.Name + "-envoy"
	if err := r.reconcileConfigMap(ctx, &kproxy, cmName, envoyYAML); err != nil {
		return ctrl.Result{}, err
	}

	// Ensure kproxy Deployment (2 replicas, HA)
	deployName := kproxy.Name + "-kproxy"
	if err := r.reconcileKProxyDeployment(ctx, &kproxy, deployName, cmName, configHash); err != nil {
		return ctrl.Result{}, err
	}

	// Ensure external Service pointing to kproxy
	if err := r.reconcileExternalService(ctx, &kproxy, deployName); err != nil {
		return ctrl.Result{}, err
	}

	// Update status
	r.setCondition(ctx, &kproxy, condTargetsDiscovered, metav1.ConditionTrue, "OK", fmt.Sprintf("Found %d target endpoints", endpointsCount))
	r.setCondition(ctx, &kproxy, condKProxyAvailable, metav1.ConditionTrue, "Reconciled", "KProxy resources are up to date")
	kproxy.Status.ObservedGeneration = kproxy.Generation
	kproxy.Status.TargetEndpointCount = endpointsCount
	kproxy.Status.KProxyDeploymentName = deployName
	kproxy.Status.ExternalServiceName = kproxy.Spec.ExternalService.Name
	kproxy.Status.ConfigHash = configHash

	if err := r.Status().Update(ctx, &kproxy); err != nil {
		// In case of conflict, requeue
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *KProxyReconciler) reconcileHeadlessService(ctx context.Context, kproxy *autoscalingv1alpha1.KProxy, name, ns string, selector map[string]string, port int32) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if svc.Labels == nil {
			svc.Labels = map[string]string{}
		}
		for k, v := range labels.Set(map[string]string{
			"kproxy.kpodautoscaler.io/owner": kproxy.Name,
			"kproxy.kpodautoscaler.io/type":  "headless",
		}) {
			svc.Labels[k] = v
		}
		svc.Spec.ClusterIP = corev1.ClusterIPNone
		svc.Spec.Selector = selector
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       port,
			TargetPort: intstr.FromInt(int(port)),
			Protocol:   corev1.ProtocolTCP,
		}}
		return controllerutil.SetControllerReference(kproxy, svc, r.Scheme)
	})
	return err
}

func (r *KProxyReconciler) reconcileConfigMap(ctx context.Context, kproxy *autoscalingv1alpha1.KProxy, cmName, envoyYAML string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: kproxy.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["envoy.yaml"] = envoyYAML
		return controllerutil.SetControllerReference(kproxy, cm, r.Scheme)
	})
	return err
}

func (r *KProxyReconciler) reconcileKProxyDeployment(ctx context.Context, kproxy *autoscalingv1alpha1.KProxy, deployName, cmName, configHash string) error {
	replicas := int32(2)
	env := effectiveEnvoy(kproxy.Spec.Envoy)
	labelsMap := map[string]string{
		appLabelKey:                           deployName,
		"kproxy.kpodautoscaler.io/owner":      kproxy.Name,
		"kproxy.kpodautoscaler.io/controller": "true",
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: kproxy.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		dep.Labels = mergeStringMap(dep.Labels, labelsMap)

		dep.Spec.Replicas = &replicas
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{appLabelKey: deployName}}
		dep.Spec.Template.Labels = mergeStringMap(dep.Spec.Template.Labels, map[string]string{appLabelKey: deployName})
		if kproxy.Spec.Envoy.DisableIstioInjection {
			if dep.Spec.Template.Annotations == nil {
				dep.Spec.Template.Annotations = map[string]string{}
			}
			dep.Spec.Template.Annotations["sidecar.istio.io/inject"] = "false"
		}
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = map[string]string{}
		}
		dep.Spec.Template.Annotations[hashAnnotationKey] = configHash

		dep.Spec.Template.Spec.TerminationGracePeriodSeconds = ptr.To[int64](int64(env.DrainSeconds + 5))
		dep.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:  "envoy",
			Image: "envoyproxy/envoy:v1.30.1",
			Args:  []string{"-c", "/etc/envoy/envoy.yaml", "--service-cluster", deployName},
			Ports: []corev1.ContainerPort{
				{Name: "http", ContainerPort: env.ListenerPort},
				{Name: "admin", ContainerPort: env.AdminPort},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("admin")},
				},
				InitialDelaySeconds: 2, PeriodSeconds: 3,
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("admin")},
				},
				InitialDelaySeconds: 10, PeriodSeconds: 10,
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "envoy-config", MountPath: "/etc/envoy"}},
			Lifecycle: &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{Command: []string{"/bin/sh", "-c",
					fmt.Sprintf("curl -s -X POST http://127.0.0.1:%d/healthcheck/fail; sleep %d", env.AdminPort, env.DrainSeconds)}},
			}},
		}}
		dep.Spec.Template.Spec.Volumes = []corev1.Volume{{
			Name: "envoy-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}},
			},
		}}
		return controllerutil.SetControllerReference(kproxy, dep, r.Scheme)
	})
	return err
}

func (r *KProxyReconciler) reconcileExternalService(ctx context.Context, kproxy *autoscalingv1alpha1.KProxy, deployName string) error {
	spec := kproxy.Spec.ExternalService
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Namespace: kproxy.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if svc.Labels == nil {
			svc.Labels = map[string]string{}
		}
		svc.Labels["kproxy.kpodautoscaler.io/owner"] = kproxy.Name
		svc.Labels["kproxy.kpodautoscaler.io/type"] = "external"

		svc.Spec.Selector = map[string]string{appLabelKey: deployName}
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       spec.Port,
			TargetPort: intstr.FromString("http"),
			Protocol:   corev1.ProtocolTCP,
		}}

		switch spec.Type {
		case "ClusterIP":
			svc.Spec.Type = corev1.ServiceTypeClusterIP
		case "NodePort":
			svc.Spec.Type = corev1.ServiceTypeNodePort
		case "LoadBalancer":
			svc.Spec.Type = corev1.ServiceTypeLoadBalancer
		default:
			svc.Spec.Type = corev1.ServiceTypeClusterIP
		}

		return controllerutil.SetControllerReference(kproxy, svc, r.Scheme)
	})
	return err
}

func (r *KProxyReconciler) countReadyEndpoints(ctx context.Context, ns string, selector map[string]string, port int) (int32, error) {
	var slices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slices, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(selector)}); err != nil {
		return 0, err
	}
	var ready int32
	for _, es := range slices.Items {
		for _, ep := range es.Endpoints {
			if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
				ready++
			}
		}
	}
	return ready, nil
}

func defaultKProxy(px *autoscalingv1alpha1.KProxy) {
	if px.Spec.BufferBytes == 0 {
		px.Spec.BufferBytes = 1048576
	}
	if px.Spec.MaxPendingRequests == 0 {
		px.Spec.MaxPendingRequests = 1024
	}
	if px.Spec.Retry.RetryOn == "" {
		px.Spec.Retry.RetryOn = "5xx,connect-failure,refused-stream"
	}
	if px.Spec.Retry.NumRetries == 0 {
		px.Spec.Retry.NumRetries = 3
	}
	if px.Spec.Retry.BaseIntervalMs == 0 {
		px.Spec.Retry.BaseIntervalMs = 25
	}
	if px.Spec.Retry.MaxIntervalMs == 0 {
		px.Spec.Retry.MaxIntervalMs = 250
	}
	if px.Spec.ExternalService.Port == 0 {
		px.Spec.ExternalService.Port = 80
	}
	if px.Spec.ExternalService.Type == "" {
		px.Spec.ExternalService.Type = "ClusterIP"
	}
	if px.Spec.Envoy.AdminPort == 0 {
		px.Spec.Envoy.AdminPort = 9901
	}
	if px.Spec.Envoy.DrainSeconds == 0 {
		px.Spec.Envoy.DrainSeconds = 25
	}
	if px.Spec.Envoy.ListenerPort == 0 {
		px.Spec.Envoy.ListenerPort = 8080
	}
	if px.Spec.Envoy.StatPrefix == "" {
		px.Spec.Envoy.StatPrefix = "ingresshttp"
	}
	if !px.Spec.Envoy.DisableIstioInjection {
		px.Spec.Envoy.DisableIstioInjection = true
	}
}

type envoyEffective struct {
	AdminPort    int32
	DrainSeconds int32
	ListenerPort int32
	StatPrefix   string
}

func effectiveEnvoy(e autoscalingv1alpha1.KProxyEnvoy) envoyEffective {
	return envoyEffective{
		AdminPort:    e.AdminPort,
		DrainSeconds: e.DrainSeconds,
		ListenerPort: e.ListenerPort,
		StatPrefix:   e.StatPrefix,
	}
}

func buildEnvoyYAML(spec autoscalingv1alpha1.KProxySpec, headlessFQDN string) string {
	env := effectiveEnvoy(spec.Envoy)
	// Convert milliseconds to seconds for Envoy duration format
	baseIntervalSec := float64(spec.Retry.BaseIntervalMs) / 1000.0
	maxIntervalSec := float64(spec.Retry.MaxIntervalMs) / 1000.0

	// STRICT_DNS pointing to headless service; buffer + router with retries; circuit breakers.
	y := fmt.Sprintf(`
static_resources:
  listeners:
  - name: listener_0
    address:
      socket_address: { address: 0.0.0.0, port_value: %d }
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: "%s"
          route_config:
            name: local_route
            virtual_hosts:
            - name: all
              domains: ["*"]
              routes:
              - match: { prefix: "/" }
                route:
                  cluster: "target"
                  retry_policy:
                    retry_on: "%s"
                    num_retries: %d
                    retry_back_off:
                      base_interval: "%.3fs"
                      max_interval: "%.3fs"
          http_filters:
          - name: envoy.filters.http.buffer
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.buffer.v3.Buffer
              max_request_bytes: %d
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router

  clusters:
  - name: "target"
    type: STRICT_DNS
    connect_timeout: 1s
    lb_policy: ROUND_ROBIN
    circuit_breakers:
      thresholds:
      - max_pending_requests: %d
    load_assignment:
      cluster_name: "target"
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: "%s"
                port_value: %d

admin:
  access_log_path: "/dev/null"
  address:
    socket_address: { address: 0.0.0.0, port_value: %d }
`, env.ListenerPort, env.StatPrefix,
		spec.Retry.RetryOn, spec.Retry.NumRetries, baseIntervalSec, maxIntervalSec,
		spec.BufferBytes, spec.MaxPendingRequests, headlessFQDN, spec.TargetRef.Port, env.AdminPort)

	// Trim leading spaces uniformly
	lines := strings.Split(y, "\n")
	var out []string
	for _, l := range lines {
		out = append(out, strings.TrimRight(l, " "))
	}
	return strings.Join(out, "\n")
}

func hashStrings(parts []string) string {
	cp := append([]string(nil), parts...)
	sort.Strings(cp)
	h := sha256.New()
	for _, p := range cp {
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func mergeStringMap(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (r *KProxyReconciler) setCondition(ctx context.Context, px *autoscalingv1alpha1.KProxy, cond string, status metav1.ConditionStatus, reason, msg string) {
	now := metav1.Now()
	newCond := metav1.Condition{
		Type:               cond,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: now,
		ObservedGeneration: px.Generation,
	}
	var changed bool
	var out []metav1.Condition
	for _, c := range px.Status.Conditions {
		if c.Type == cond {
			if !equality.Semantic.DeepEqual(c.Status, status) ||
				c.Reason != reason || c.Message != msg {
				changed = true
			}
		} else {
			out = append(out, c)
		}
	}
	out = append(out, newCond)
	if changed || len(px.Status.Conditions) == 0 {
		px.Status.Conditions = out
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *KProxyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&autoscalingv1alpha1.KProxy{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
