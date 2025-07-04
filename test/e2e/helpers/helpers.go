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

package helpers

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/gomega"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kpav1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

// TestConstants contains common timeout and interval values for tests
var TestConstants = struct {
	DefaultTimeout       time.Duration
	DefaultInterval      time.Duration
	ResourceReadyTimeout time.Duration
	ScaleTimeout         time.Duration
	MetricsTimeout       time.Duration
}{
	DefaultTimeout:       2 * time.Minute,
	DefaultInterval:      2 * time.Second,
	ResourceReadyTimeout: 5 * time.Minute,
	ScaleTimeout:         3 * time.Minute,
	MetricsTimeout:       2 * time.Minute,
}

// E2ETestHelper contains helper methods for E2E tests
type E2ETestHelper struct {
	K8sClient  client.Client
	ClientSet  *kubernetes.Clientset
	RestConfig *rest.Config
	TestID     string
	Namespace  string
}

// NewE2ETestHelper creates a new E2E test helper
func NewE2ETestHelper(k8sClient client.Client, clientSet *kubernetes.Clientset, config *rest.Config, testID string) *E2ETestHelper {
	return &E2ETestHelper{
		K8sClient:  k8sClient,
		ClientSet:  clientSet,
		RestConfig: config,
		TestID:     testID,
		Namespace:  fmt.Sprintf("e2e-test-%s", testID),
	}
}

// CreateTestNamespace creates a namespace for the test
func (h *E2ETestHelper) CreateTestNamespace(ctx context.Context) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: h.Namespace,
			Labels: map[string]string{
				"test-id":   h.TestID,
				"test-type": "e2e",
			},
		},
	}
	return h.K8sClient.Create(ctx, ns)
}

// CleanupTestNamespace deletes the test namespace
func (h *E2ETestHelper) CleanupTestNamespace(ctx context.Context) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: h.Namespace,
		},
	}
	return h.K8sClient.Delete(ctx, ns)
}

// CreateDeployment creates a deployment for testing
func (h *E2ETestHelper) CreateDeployment(ctx context.Context, name string, replicas int32, image string,
	labels map[string]string, resources corev1.ResourceRequirements) (*v1.Deployment, error) {

	deployment := &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
			Labels:    labels,
		},
		Spec: v1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:      "app",
							Image:     image,
							Resources: resources,
						},
					},
				},
			},
		},
	}

	err := h.K8sClient.Create(ctx, deployment)
	return deployment, err
}

// CreateService creates a service for the deployment
func (h *E2ETestHelper) CreateService(ctx context.Context, name string, selector map[string]string, port int32) (*corev1.Service, error) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
			Labels:    selector,
		},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports: []corev1.ServicePort{
				{
					Name:       "metrics",
					Port:       port,
					TargetPort: intstr.FromInt(int(port)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	err := h.K8sClient.Create(ctx, service)
	return service, err
}

// CreateKPodAutoscaler creates a KPodAutoscaler resource
func (h *E2ETestHelper) CreateKPodAutoscaler(ctx context.Context, name string, targetRef kpav1alpha1.ScaleTargetRef,
	minReplicas, maxReplicas int32, metrics []kpav1alpha1.MetricSpec) (*kpav1alpha1.KPodAutoscaler, error) {

	kpa := &kpav1alpha1.KPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
		},
		Spec: kpav1alpha1.KPodAutoscalerSpec{
			ScaleTargetRef: targetRef,
			MinReplicas:    *resource.NewQuantity(int64(minReplicas), resource.DecimalSI),
			MaxReplicas:    *resource.NewQuantity(int64(maxReplicas), resource.DecimalSI),
			Metrics:        metrics,
		},
	}

	var err error
	for range 5 {
		err = h.K8sClient.Create(ctx, kpa)
		if err == nil {
			break
		}
		fmt.Printf("failed to create KPodAutoscaler: %v\n", err)
		time.Sleep(3 * time.Second)
	}
	return kpa, err
}

// WaitForDeploymentReady waits for a deployment to be ready
func (h *E2ETestHelper) WaitForDeploymentReady(ctx context.Context, name string) error {
	Eventually(func(g Gomega) {
		deployment := &v1.Deployment{}
		err := h.K8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, deployment)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(deployment.Status.ReadyReplicas).To(Equal(*deployment.Spec.Replicas))
	}, TestConstants.ResourceReadyTimeout, TestConstants.DefaultInterval).Should(Succeed())
	return nil
}

// WaitForKPAActive waits for KPodAutoscaler to become active
func (h *E2ETestHelper) WaitForKPAActive(ctx context.Context, name string) error {
	Eventually(func(g Gomega) {
		kpa := &kpav1alpha1.KPodAutoscaler{}
		err := h.K8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, kpa)
		g.Expect(err).NotTo(HaveOccurred())

		// Check for ScalingActive condition
		for _, condition := range kpa.Status.Conditions {
			if condition.Type == kpav1alpha1.ScalingActive {
				g.Expect(condition.Status).To(Equal(corev1.ConditionTrue))
				break
			}
		}
	}, TestConstants.ResourceReadyTimeout, TestConstants.DefaultInterval).Should(Succeed())
	return nil
}

// WaitForScale waits for the deployment to scale to the expected number of replicas
func (h *E2ETestHelper) WaitForScale(ctx context.Context, deploymentName string, expectedReplicas int32) error {
	Eventually(func(g Gomega) {
		deployment := &v1.Deployment{}
		err := h.K8sClient.Get(ctx, types.NamespacedName{
			Name:      deploymentName,
			Namespace: h.Namespace,
		}, deployment)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(deployment.Status.ReadyReplicas).To(Equal(expectedReplicas))
	}, TestConstants.ScaleTimeout, TestConstants.DefaultInterval).Should(Succeed())
	return nil
}

// VerifyKPAMetrics verifies that KPodAutoscaler has metrics populated
func (h *E2ETestHelper) VerifyKPAMetrics(ctx context.Context, name string) error {
	Eventually(func(g Gomega) {
		kpa := &kpav1alpha1.KPodAutoscaler{}
		err := h.K8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: h.Namespace,
		}, kpa)
		g.Expect(err).NotTo(HaveOccurred())
	}, TestConstants.MetricsTimeout, TestConstants.DefaultInterval).Should(Succeed())
	return nil
}

// CreateBusyboxLoadGenerator creates a deployment that generates CPU load
func (h *E2ETestHelper) CreateBusyboxLoadGenerator(ctx context.Context, name string, cpuRequest string) (*v1.Deployment, error) {
	labels := map[string]string{"app": name}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpuRequest),
		},
	}

	deployment := &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
			Labels:    labels,
		},
		Spec: v1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "load-generator",
							Image: "busybox:1.36.1",
							Command: []string{
								"/bin/sh",
								"-c",
								"while true; do dd if=/dev/zero of=/dev/null bs=1M count=1000; sleep 0.1; done",
							},
							Resources: resources,
						},
					},
				},
			},
		},
	}

	err := h.K8sClient.Create(ctx, deployment)
	return deployment, err
}

// CreateHTTPMetricsExporter creates a deployment that exports custom metrics
func (h *E2ETestHelper) CreateHTTPMetricsExporter(ctx context.Context, name string) (*v1.Deployment, error) {
	labels := map[string]string{"app": name}

	deployment := &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
			Labels:    labels,
		},
		Spec: v1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/port":   "8080",
						"prometheus.io/path":   "/metrics",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "metrics-exporter",
							Image: "quay.io/brancz/prometheus-example-app:v0.3.0",
							Args: []string{
								"-bind=:8080",
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "metrics",
									ContainerPort: 8080,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err := h.K8sClient.Create(ctx, deployment)
	return deployment, err
}

// Helper function to get int32 pointer
func int32Ptr(i int32) *int32 {
	return &i
}
