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

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	kpav1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
	"github.com/Fedosin/kpodautoscaler/test/e2e/helpers"
	"github.com/Fedosin/kpodautoscaler/test/utils"
)

var _ = Describe("KPodAutoscaler E2E Test Scenarios", func() {
	var (
		ctx        context.Context
		k8sClient  client.Client
		clientSet  *kubernetes.Clientset
		restConfig *rest.Config
		helper     *helpers.E2ETestHelper
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Get the Kubernetes config
		var err error
		restConfig, err = config.GetConfig()
		Expect(err).NotTo(HaveOccurred())

		// Create a scheme that includes both Kubernetes built-in types and our custom types
		scheme := runtime.NewScheme()
		err = clientgoscheme.AddToScheme(scheme)
		Expect(err).NotTo(HaveOccurred())
		err = kpav1alpha1.AddToScheme(scheme)
		Expect(err).NotTo(HaveOccurred())

		// Create Kubernetes clients with the scheme
		k8sClient, err = client.New(restConfig, client.Options{
			Scheme: scheme,
		})
		Expect(err).NotTo(HaveOccurred())

		clientSet, err = kubernetes.NewForConfig(restConfig)
		Expect(err).NotTo(HaveOccurred())

		// Generate unique test ID
		testID := fmt.Sprintf("%d", time.Now().Unix())

		// Create test helper
		helper = helpers.NewE2ETestHelper(k8sClient, clientSet, restConfig, testID)

		// Create test namespace
		err = helper.CreateTestNamespace(ctx)
		Expect(err).NotTo(HaveOccurred())

		By(fmt.Sprintf("Created test namespace: %s", helper.Namespace))
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			// Collect debug information
			By("Test failed, collecting debug information")

			// Get KPodAutoscaler status
			cmd := exec.Command("kubectl", "get", "kpodautoscalers", "-n", helper.Namespace, "-o", "wide")
			output, _ := utils.Run(cmd)
			GinkgoWriter.Printf("KPodAutoscalers:\n%s\n", output)

			// Get pod status
			cmd = exec.Command("kubectl", "get", "pods", "-n", helper.Namespace, "-o", "wide")
			output, _ = utils.Run(cmd)
			GinkgoWriter.Printf("Pods:\n%s\n", output)

			// Get events
			cmd = exec.Command("kubectl", "get", "events", "-n", helper.Namespace, "--sort-by='.lastTimestamp'")
			output, _ = utils.Run(cmd)
			GinkgoWriter.Printf("Events:\n%s\n", output)
		}

		// Clean up namespace
		By(fmt.Sprintf("Cleaning up test namespace: %s", helper.Namespace))
		err := helper.CleanupTestNamespace(ctx)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("Scenario 1: CPU-based autoscaling with metrics-server", func() {
		It("should scale deployment based on CPU utilization", func() {
			deploymentName := "cpu-test-app"
			kpaName := "cpu-autoscaler"

			By("Creating a deployment with CPU requests")
			_, err := helper.CreateBusyboxLoadGenerator(ctx, deploymentName, "100m")
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for deployment to be ready")
			err = helper.WaitForDeploymentReady(ctx, deploymentName)
			Expect(err).NotTo(HaveOccurred())

			By("Creating KPodAutoscaler for CPU scaling")
			targetRef := kpav1alpha1.ScaleTargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			}

			metrics := []kpav1alpha1.MetricSpec{
				{
					Type: kpav1alpha1.ResourceMetricType,
					Resource: &kpav1alpha1.ResourceMetricSource{
						Name: corev1.ResourceCPU,
						Target: kpav1alpha1.MetricTarget{
							Type:               kpav1alpha1.UtilizationMetricType,
							AverageUtilization: int32Ptr(50),
						},
					},
					Config: &kpav1alpha1.MetricConfig{
						TargetValue:      resource.NewQuantity(50, resource.DecimalSI),
						TotalValue:       resource.NewQuantity(100, resource.DecimalSI),
						MaxScaleUpRate:   resource.NewQuantity(2, resource.DecimalSI),
						MaxScaleDownRate: resource.NewQuantity(2, resource.DecimalSI),
						StableWindow:     30 * time.Second,
						ScaleDownDelay:   30 * time.Second,
					},
				},
			}

			_, err = helper.CreateKPodAutoscaler(ctx, kpaName, targetRef, 1, 5, metrics)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for KPodAutoscaler to become active")
			err = helper.WaitForKPAActive(ctx, kpaName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying KPodAutoscaler has CPU metrics")
			err = helper.VerifyKPAMetrics(ctx, kpaName)
			Expect(err).NotTo(HaveOccurred())

			By("Generating CPU load to trigger scale up")
			// Update deployment to increase CPU usage
			deployment := &v1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      deploymentName,
				Namespace: helper.Namespace,
			}, deployment)
			Expect(err).NotTo(HaveOccurred())

			// Modify the command to generate more CPU load
			deployment.Spec.Template.Spec.Containers[0].Command = []string{
				"/bin/sh", "-c",
				"while true; do dd if=/dev/zero of=/dev/null; done",
			}
			err = k8sClient.Update(ctx, deployment)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for scale up due to high CPU usage")
			Eventually(func(g Gomega) {
				kpa := &kpav1alpha1.KPodAutoscaler{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpaName,
					Namespace: helper.Namespace,
				}, kpa)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kpa.Status.DesiredReplicas).To(BeNumerically(">", 1))
			}, helpers.TestConstants.ScaleTimeout, helpers.TestConstants.DefaultInterval).Should(Succeed())

			By("Verifying deployment scaled up")
			Eventually(func(g Gomega) {
				deployment := &v1.Deployment{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      deploymentName,
					Namespace: helper.Namespace,
				}, deployment)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">", 1))
			}, helpers.TestConstants.ScaleTimeout, helpers.TestConstants.DefaultInterval).Should(Succeed())

			By("Test completed successfully")
		})
	})

	Context("Scenario 2: Custom metrics autoscaling with Prometheus Adapter", func() {
		It("should scale deployment based on custom metrics", func() {
			deploymentName := "custom-metrics-app"
			serviceName := "custom-metrics-svc"
			kpaName := "custom-metrics-autoscaler"

			By("Creating HTTP metrics exporter deployment")
			_, err := helper.CreateHTTPMetricsExporter(ctx, deploymentName)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for deployment to be ready")
			err = helper.WaitForDeploymentReady(ctx, deploymentName)
			Expect(err).NotTo(HaveOccurred())

			By("Creating service for metrics exporter")
			selector := map[string]string{"app": deploymentName}
			_, err = helper.CreateService(ctx, serviceName, selector, 8080)
			Expect(err).NotTo(HaveOccurred())

			By("Creating ServiceMonitor for Prometheus scraping")
			serviceMonitor := fmt.Sprintf(`
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    matchLabels:
      app: %s
  endpoints:
  - port: metrics
    interval: 15s
    path: /metrics
`, serviceName, helper.Namespace, deploymentName)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(serviceMonitor)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for metrics to be available in Prometheus")
			time.Sleep(30 * time.Second) // Give Prometheus time to scrape metrics

			By("Creating KPodAutoscaler for custom metrics scaling")
			targetRef := kpav1alpha1.ScaleTargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			}

			metrics := []kpav1alpha1.MetricSpec{
				{
					Type: kpav1alpha1.PodsMetricType,
					Pods: &kpav1alpha1.PodsMetricSource{
						Metric: kpav1alpha1.MetricIdentifier{
							Name: "http_requests_total",
						},
						Target: kpav1alpha1.MetricTarget{
							Type:         kpav1alpha1.AverageValueMetricType,
							AverageValue: resource.NewQuantity(100, resource.DecimalSI),
						},
					},
					Config: &kpav1alpha1.MetricConfig{
						TargetValue:      resource.NewQuantity(100, resource.DecimalSI),
						TotalValue:       resource.NewQuantity(500, resource.DecimalSI),
						MaxScaleUpRate:   resource.NewQuantity(2, resource.DecimalSI),
						MaxScaleDownRate: resource.NewQuantity(2, resource.DecimalSI),
						StableWindow:     30 * time.Second,
						ScaleDownDelay:   60 * time.Second,
					},
				},
			}

			_, err = helper.CreateKPodAutoscaler(ctx, kpaName, targetRef, 1, 5, metrics)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for KPodAutoscaler to become active")
			err = helper.WaitForKPAActive(ctx, kpaName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying KPodAutoscaler has custom metrics")
			Eventually(func(g Gomega) {
				kpa := &kpav1alpha1.KPodAutoscaler{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpaName,
					Namespace: helper.Namespace,
				}, kpa)
				g.Expect(err).NotTo(HaveOccurred())
			}, helpers.TestConstants.MetricsTimeout, helpers.TestConstants.DefaultInterval).Should(Succeed())

			By("Test completed successfully")
		})
	})

	Context("Scenario 3: External metrics autoscaling with KEDA", func() {
		It("should scale deployment based on Redis queue length", func() {
			deploymentName := "redis-consumer-app"
			scaledObjectName := "redis-scaledobject"
			kpaName := "keda-kpa-" + scaledObjectName

			By("Creating a simple Redis consumer deployment")
			labels := map[string]string{"app": deploymentName}
			resources := corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			}

			deployment := &v1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: helper.Namespace,
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
									Name:  "redis-consumer",
									Image: "redis:7.4.1-alpine",
									Command: []string{
										"sh", "-c",
										"while true; do redis-cli -h redis-master.redis.svc.cluster.local LPOP myqueue || true; sleep 1; done",
									},
									Resources: resources,
								},
							},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, deployment)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for deployment to be ready")
			err = helper.WaitForDeploymentReady(ctx, deploymentName)
			Expect(err).NotTo(HaveOccurred())

			By("Creating KPA managed KEDA ScaledObject for Redis queue")
			scaledObject := fmt.Sprintf(`
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: %s
  namespace: %s
  annotations:
    autoscaling.kpodautoscaler.io/managed-backend: "true"
spec:
  scaleTargetRef:
    name: %s
  triggers:
  - type: redis
    metadata:
      address: redis-master.redis.svc.cluster.local:6379
      listName: myqueue
      listLength: "5"
`, scaledObjectName, helper.Namespace, deploymentName)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(scaledObject)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for KPodAutoscaler to become active")
			err = helper.WaitForKPAActive(ctx, kpaName)
			Expect(err).NotTo(HaveOccurred())

			By("Adding items to Redis queue to trigger scale up")
			cmd = exec.Command("kubectl", "run", "redis-producer", "--rm", "-i", "--restart=Never",
				"--namespace", helper.Namespace,
				"--image=redis:7.4.1-alpine", "--",
				"sh", "-c",
				"for i in $(seq 1 50); do redis-cli -h redis-master.redis.svc.cluster.local RPUSH myqueue item$i; done")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for scale up due to queue length")
			Eventually(func(g Gomega) {
				kpa := &kpav1alpha1.KPodAutoscaler{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpaName,
					Namespace: helper.Namespace,
				}, kpa)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kpa.Status.DesiredReplicas).To(BeNumerically(">", 1))
			}, helpers.TestConstants.ScaleTimeout, helpers.TestConstants.DefaultInterval).Should(Succeed())

			By("Verifying deployment scaled up")
			Eventually(func(g Gomega) {
				deployment := &v1.Deployment{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      deploymentName,
					Namespace: helper.Namespace,
				}, deployment)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">", 1))
			}, helpers.TestConstants.ScaleTimeout, helpers.TestConstants.DefaultInterval).Should(Succeed())

			By("Test completed successfully")
		})
	})

	Context("Scenario 4: User metrics autoscaling", func() {
		It("should scale deployment based on user metrics scraped from pods", func() {
			deploymentName := "user-metrics-app"
			serviceName := "user-metrics-svc"
			kpaName := "user-metrics-autoscaler"

			By("Creating HTTP metrics exporter deployment")
			_, err := helper.CreateHTTPMetricsExporter(ctx, deploymentName)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for deployment to be ready")
			err = helper.WaitForDeploymentReady(ctx, deploymentName)
			Expect(err).NotTo(HaveOccurred())

			By("Creating service for metrics exporter")
			selector := map[string]string{"app": deploymentName}
			_, err = helper.CreateService(ctx, serviceName, selector, 8080)
			Expect(err).NotTo(HaveOccurred())

			By("Creating KPodAutoscaler for user metrics scaling")
			targetRef := kpav1alpha1.ScaleTargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			}

			metrics := []kpav1alpha1.MetricSpec{
				{
					Type: kpav1alpha1.UserMetricType,
					User: &kpav1alpha1.UserMetricSource{
						Metric: kpav1alpha1.UserMetric{
							Name: "http_requests_total",
							Port: intstr.FromString("metrics"),
							Path: "/metrics",
						},
						Target: kpav1alpha1.MetricTarget{
							Type:         kpav1alpha1.AverageValueMetricType,
							AverageValue: resource.NewQuantity(5, resource.DecimalSI),
						},
					},
				},
			}

			_, err = helper.CreateKPodAutoscaler(ctx, kpaName, targetRef, 1, 3, metrics)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for KPodAutoscaler to become active")
			err = helper.WaitForKPAActive(ctx, kpaName)
			Expect(err).NotTo(HaveOccurred())

			By("Generating HTTP load to trigger scale up")
			loadGeneratorPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "load-generator",
					Namespace: helper.Namespace,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "curl",
							Image: "curlimages/curl:7.84.0",
							Command: []string{
								"/bin/sh",
								"-c",
								fmt.Sprintf("end=$(($(date +%%s) + 180)); while [ $(date +%%s) -lt $end ]; do curl -s http://%s:8080/hello > /dev/null; sleep 0.1; done", serviceName),
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			}
			err = k8sClient.Create(ctx, loadGeneratorPod)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for scale up due to high metric value")
			Eventually(func(g Gomega) {
				kpa := &kpav1alpha1.KPodAutoscaler{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpaName,
					Namespace: helper.Namespace,
				}, kpa)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kpa.Status.DesiredReplicas).To(BeNumerically(">", 1))
			}, helpers.TestConstants.ScaleTimeout, helpers.TestConstants.DefaultInterval).Should(Succeed())

			By("Verifying deployment scaled up")
			Eventually(func(g Gomega) {
				deployment := &v1.Deployment{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      deploymentName,
					Namespace: helper.Namespace,
				}, deployment)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">", 1))
			}, helpers.TestConstants.ScaleTimeout, helpers.TestConstants.DefaultInterval).Should(Succeed())

			By("Test completed successfully")
		})
	})
})

// Helper function to get int32 pointer
func int32Ptr(i int32) *int32 {
	return &i
}
