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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	autoscalingv1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

var _ = Describe("KPodAutoscaler Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When reconciling a KPodAutoscaler resource", func() {
		var (
			namespace   string
			deployment  *appsv1.Deployment
			statefulSet *appsv1.StatefulSet
		)

		BeforeEach(func() {
			// Create a unique namespace for each test
			namespace = fmt.Sprintf("test-namespace-%d", time.Now().UnixNano())
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			// Create a test deployment
			deployment = &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(2),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "test-deployment",
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "test-deployment",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "nginx:latest",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("100m"),
											corev1.ResourceMemory: resource.MustParse("128Mi"),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			// Create a test statefulset
			statefulSet = &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-statefulset",
					Namespace: namespace,
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: int32Ptr(2),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "test-statefulset",
						},
					},
					ServiceName: "test-statefulset",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "test-statefulset",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "nginx:latest",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("100m"),
											corev1.ResourceMemory: resource.MustParse("128Mi"),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up namespace (this will delete all resources in it)
			ns := &corev1.Namespace{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)
			if err == nil {
				Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			}
		})

		It("should create and reconcile a KPodAutoscaler for a Deployment", func() {
			By("Creating a KPodAutoscaler targeting the deployment")
			kpa := &autoscalingv1alpha1.KPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kpa-deployment",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.KPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv1alpha1.ScaleTargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deployment.Name,
					},
					MinReplicas: *resource.NewQuantity(1, resource.DecimalSI),
					MaxReplicas: *resource.NewQuantity(10, resource.DecimalSI),
					Metrics: []autoscalingv1alpha1.MetricSpec{
						{
							Type: autoscalingv1alpha1.ResourceMetricType,
							Resource: &autoscalingv1alpha1.ResourceMetricSource{
								Name: corev1.ResourceCPU,
								Target: autoscalingv1alpha1.MetricTarget{
									Type:               autoscalingv1alpha1.UtilizationMetricType,
									AverageUtilization: int32Ptr(50),
								},
							},
							Config: &autoscalingv1alpha1.MetricConfig{
								StableWindow:          30 * time.Second,
								PanicThreshold:        resource.NewQuantity(200, resource.DecimalSI),
								PanicWindowPercentage: resource.NewQuantity(10, resource.DecimalSI),
								MaxScaleUpRate:        resource.NewQuantity(1000, resource.DecimalSI),
								MaxScaleDownRate:      resource.NewQuantity(2, resource.DecimalSI),
								ScaleDownDelay:        5 * time.Second,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kpa)).To(Succeed())

			By("Checking that the KPodAutoscaler was created")
			createdKPA := &autoscalingv1alpha1.KPodAutoscaler{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpa.Name,
					Namespace: kpa.Namespace,
				}, createdKPA)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Checking that the KPodAutoscaler has correct spec")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpa.Name,
					Namespace: kpa.Namespace,
				}, createdKPA)
				if err != nil {
					return false
				}
				return createdKPA.Spec.ScaleTargetRef.Name == deployment.Name &&
					createdKPA.Spec.MinReplicas.Value() == 1 &&
					createdKPA.Spec.MaxReplicas.Value() == 10
			}, timeout, interval).Should(BeTrue())

			By("Verifying the KPodAutoscaler properties")
			Expect(createdKPA.Spec.ScaleTargetRef.Kind).To(Equal("Deployment"))
			Expect(createdKPA.Spec.ScaleTargetRef.APIVersion).To(Equal("apps/v1"))
			Expect(len(createdKPA.Spec.Metrics)).To(Equal(1))
			Expect(createdKPA.Spec.Metrics[0].Type).To(Equal(autoscalingv1alpha1.ResourceMetricType))
			Expect(createdKPA.Spec.Metrics[0].Resource.Name).To(Equal(corev1.ResourceCPU))

			// Verify panic mode configuration
			config := createdKPA.Spec.Metrics[0].Config
			Expect(config).NotTo(BeNil())
			Expect(config.StableWindow).To(Equal(30 * time.Second))
			Expect(config.PanicThreshold.Value()).To(Equal(int64(200)))
			Expect(config.PanicWindowPercentage.Value()).To(Equal(int64(10)))

			By("Deleting the KPodAutoscaler")
			Expect(k8sClient.Delete(ctx, kpa)).To(Succeed())

			By("Checking that the KPodAutoscaler is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpa.Name,
					Namespace: kpa.Namespace,
				}, createdKPA)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})

		It("should create and reconcile a KPodAutoscaler for a StatefulSet", func() {
			By("Creating a KPodAutoscaler targeting the statefulset")
			kpa := &autoscalingv1alpha1.KPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kpa-statefulset",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.KPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv1alpha1.ScaleTargetRef{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
						Name:       statefulSet.Name,
					},
					MinReplicas: *resource.NewQuantity(1, resource.DecimalSI),
					MaxReplicas: *resource.NewQuantity(10, resource.DecimalSI),
					Metrics: []autoscalingv1alpha1.MetricSpec{
						{
							Type: autoscalingv1alpha1.ResourceMetricType,
							Resource: &autoscalingv1alpha1.ResourceMetricSource{
								Name: corev1.ResourceCPU,
								Target: autoscalingv1alpha1.MetricTarget{
									Type:               autoscalingv1alpha1.UtilizationMetricType,
									AverageUtilization: int32Ptr(50),
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kpa)).To(Succeed())

			By("Checking that the KPodAutoscaler was created")
			createdKPA := &autoscalingv1alpha1.KPodAutoscaler{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpa.Name,
					Namespace: kpa.Namespace,
				}, createdKPA)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Deleting the KPodAutoscaler")
			Expect(k8sClient.Delete(ctx, kpa)).To(Succeed())
		})

		It("should handle different metric types", func() {
			By("Creating a KPodAutoscaler with memory metric")
			kpaMemory := &autoscalingv1alpha1.KPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kpa-memory",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.KPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv1alpha1.ScaleTargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deployment.Name,
					},
					MinReplicas: *resource.NewQuantity(1, resource.DecimalSI),
					MaxReplicas: *resource.NewQuantity(5, resource.DecimalSI),
					Metrics: []autoscalingv1alpha1.MetricSpec{
						{
							Type: autoscalingv1alpha1.ResourceMetricType,
							Resource: &autoscalingv1alpha1.ResourceMetricSource{
								Name: corev1.ResourceMemory,
								Target: autoscalingv1alpha1.MetricTarget{
									Type:               autoscalingv1alpha1.UtilizationMetricType,
									AverageUtilization: int32Ptr(80),
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kpaMemory)).To(Succeed())

			By("Creating a KPodAutoscaler with custom metric")
			kpaCustom := &autoscalingv1alpha1.KPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kpa-custom",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.KPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv1alpha1.ScaleTargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deployment.Name,
					},
					MinReplicas: *resource.NewQuantity(2, resource.DecimalSI),
					MaxReplicas: *resource.NewQuantity(8, resource.DecimalSI),
					Metrics: []autoscalingv1alpha1.MetricSpec{
						{
							Type: autoscalingv1alpha1.PodsMetricType,
							Pods: &autoscalingv1alpha1.PodsMetricSource{
								Metric: autoscalingv1alpha1.MetricIdentifier{
									Name: "requests_per_second",
								},
								Target: autoscalingv1alpha1.MetricTarget{
									Type:         autoscalingv1alpha1.AverageValueMetricType,
									AverageValue: resource.NewQuantity(100, resource.DecimalSI),
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kpaCustom)).To(Succeed())

			By("Verifying both KPAs were created")
			Eventually(func() bool {
				err1 := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpaMemory.Name,
					Namespace: kpaMemory.Namespace,
				}, &autoscalingv1alpha1.KPodAutoscaler{})

				err2 := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpaCustom.Name,
					Namespace: kpaCustom.Namespace,
				}, &autoscalingv1alpha1.KPodAutoscaler{})

				return err1 == nil && err2 == nil
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, kpaMemory)).To(Succeed())
			Expect(k8sClient.Delete(ctx, kpaCustom)).To(Succeed())
		})

		It("should respect min and max replica limits in the spec", func() {
			By("Creating a KPodAutoscaler with specific min/max limits")
			kpa := &autoscalingv1alpha1.KPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kpa-limits",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.KPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv1alpha1.ScaleTargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deployment.Name,
					},
					MinReplicas: *resource.NewQuantity(3, resource.DecimalSI),
					MaxReplicas: *resource.NewQuantity(7, resource.DecimalSI),
					Metrics: []autoscalingv1alpha1.MetricSpec{
						{
							Type: autoscalingv1alpha1.ResourceMetricType,
							Resource: &autoscalingv1alpha1.ResourceMetricSource{
								Name: corev1.ResourceCPU,
								Target: autoscalingv1alpha1.MetricTarget{
									Type:               autoscalingv1alpha1.UtilizationMetricType,
									AverageUtilization: int32Ptr(70),
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kpa)).To(Succeed())

			By("Verifying the min/max values are stored correctly")
			createdKPA := &autoscalingv1alpha1.KPodAutoscaler{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpa.Name,
					Namespace: kpa.Namespace,
				}, createdKPA)
				if err != nil {
					return false
				}
				return createdKPA.Spec.MinReplicas.Value() == 3 &&
					createdKPA.Spec.MaxReplicas.Value() == 7
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, kpa)).To(Succeed())
		})

		It("should handle panic mode configuration", func() {
			By("Creating a KPodAutoscaler with panic mode settings")
			kpa := &autoscalingv1alpha1.KPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kpa-panic",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.KPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv1alpha1.ScaleTargetRef{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
						Name:       statefulSet.Name,
					},
					MinReplicas: *resource.NewQuantity(1, resource.DecimalSI),
					MaxReplicas: *resource.NewQuantity(20, resource.DecimalSI),
					Metrics: []autoscalingv1alpha1.MetricSpec{
						{
							Type: autoscalingv1alpha1.ResourceMetricType,
							Resource: &autoscalingv1alpha1.ResourceMetricSource{
								Name: corev1.ResourceCPU,
								Target: autoscalingv1alpha1.MetricTarget{
									Type:               autoscalingv1alpha1.UtilizationMetricType,
									AverageUtilization: int32Ptr(50),
								},
							},
							Config: &autoscalingv1alpha1.MetricConfig{
								StableWindow:          60 * time.Second,
								PanicThreshold:        resource.NewQuantity(150, resource.DecimalSI),  // 150% threshold
								PanicWindowPercentage: resource.NewQuantity(5, resource.DecimalSI),    // 5% of stable window
								MaxScaleUpRate:        resource.NewQuantity(2000, resource.DecimalSI), // Can double quickly
								MaxScaleDownRate:      resource.NewQuantity(3, resource.DecimalSI),    // Can scale down by 1/3
								ScaleDownDelay:        10 * time.Second,
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kpa)).To(Succeed())

			By("Verifying panic mode configuration is stored correctly")
			createdKPA := &autoscalingv1alpha1.KPodAutoscaler{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpa.Name,
					Namespace: kpa.Namespace,
				}, createdKPA)
				if err != nil {
					return false
				}
				config := createdKPA.Spec.Metrics[0].Config
				return config != nil &&
					config.PanicThreshold.Value() == 150 &&
					config.PanicWindowPercentage.Value() == 5 &&
					config.MaxScaleUpRate.Value() == 2000 &&
					config.MaxScaleDownRate.Value() == 3
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, kpa)).To(Succeed())
		})

		It("should handle multiple metrics configuration", func() {
			By("Creating a KPodAutoscaler with multiple metrics")
			kpa := &autoscalingv1alpha1.KPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-kpa-multi-metrics",
					Namespace: namespace,
				},
				Spec: autoscalingv1alpha1.KPodAutoscalerSpec{
					ScaleTargetRef: autoscalingv1alpha1.ScaleTargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deployment.Name,
					},
					MinReplicas: *resource.NewQuantity(2, resource.DecimalSI),
					MaxReplicas: *resource.NewQuantity(15, resource.DecimalSI),
					Metrics: []autoscalingv1alpha1.MetricSpec{
						{
							Type: autoscalingv1alpha1.ResourceMetricType,
							Resource: &autoscalingv1alpha1.ResourceMetricSource{
								Name: corev1.ResourceCPU,
								Target: autoscalingv1alpha1.MetricTarget{
									Type:               autoscalingv1alpha1.UtilizationMetricType,
									AverageUtilization: int32Ptr(60),
								},
							},
						},
						{
							Type: autoscalingv1alpha1.ResourceMetricType,
							Resource: &autoscalingv1alpha1.ResourceMetricSource{
								Name: corev1.ResourceMemory,
								Target: autoscalingv1alpha1.MetricTarget{
									Type:               autoscalingv1alpha1.UtilizationMetricType,
									AverageUtilization: int32Ptr(70),
								},
							},
						},
						{
							Type: autoscalingv1alpha1.PodsMetricType,
							Pods: &autoscalingv1alpha1.PodsMetricSource{
								Metric: autoscalingv1alpha1.MetricIdentifier{
									Name: "queue_depth",
								},
								Target: autoscalingv1alpha1.MetricTarget{
									Type:         autoscalingv1alpha1.AverageValueMetricType,
									AverageValue: resource.NewQuantity(50, resource.DecimalSI),
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kpa)).To(Succeed())

			By("Verifying all metrics are stored correctly")
			createdKPA := &autoscalingv1alpha1.KPodAutoscaler{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      kpa.Name,
					Namespace: kpa.Namespace,
				}, createdKPA)
				if err != nil {
					return false
				}
				return len(createdKPA.Spec.Metrics) == 3
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, kpa)).To(Succeed())
		})
	})
})

func int32Ptr(i int32) *int32 {
	return &i
}
