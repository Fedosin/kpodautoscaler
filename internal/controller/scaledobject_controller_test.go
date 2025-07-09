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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kpav1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

var _ = Describe("ScaledObject Controller", func() {
	Context("translateSpec", func() {
		var (
			reconciler   *ScaledObjectReconciler
			scaledObject *unstructured.Unstructured
		)

		BeforeEach(func() {
			reconciler = &ScaledObjectReconciler{}

			// Create a sample ScaledObject using unstructured
			scaledObject = &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "keda.sh/v1alpha1",
					"kind":       "ScaledObject",
					"metadata": map[string]interface{}{
						"name":      "test-scaledobject",
						"namespace": "default",
					},
					"spec": map[string]interface{}{
						"scaleTargetRef": map[string]interface{}{
							"name":       "test-deployment",
							"kind":       "Deployment",
							"apiVersion": "apps/v1",
						},
						"minReplicaCount": int64(2),
						"maxReplicaCount": int64(10),
						"triggers": []interface{}{
							map[string]interface{}{
								"type": "prometheus",
								"metadata": map[string]interface{}{
									"serverAddress": "http://prometheus:9090",
									"metricName":    "http_requests_per_second",
									"query":         "sum(rate(http_requests_total[1m]))",
									"threshold":     "100",
									"metricType":    "AverageValue",
								},
							},
						},
					},
				},
			}
		})

		It("should correctly translate a ScaledObject with Prometheus trigger", func() {
			kpa, err := reconciler.translateSpec(scaledObject)
			Expect(err).NotTo(HaveOccurred())
			Expect(kpa).NotTo(BeNil())

			// Check basic fields
			Expect(kpa.Name).To(Equal("keda-kpa-test-scaledobject"))
			Expect(kpa.Namespace).To(Equal("default"))

			// Check ScaleTargetRef
			Expect(kpa.Spec.ScaleTargetRef.Name).To(Equal("test-deployment"))
			Expect(kpa.Spec.ScaleTargetRef.Kind).To(Equal("Deployment"))
			Expect(kpa.Spec.ScaleTargetRef.APIVersion).To(Equal("apps/v1"))

			// Check replica counts
			Expect(kpa.Spec.MinReplicas).To(Equal(*resource.NewQuantity(2, resource.DecimalSI)))
			Expect(kpa.Spec.MaxReplicas).To(Equal(*resource.NewQuantity(10, resource.DecimalSI)))

			// Check metrics
			Expect(kpa.Spec.Metrics).To(HaveLen(1))
			metric := kpa.Spec.Metrics[0]
			Expect(metric.Type).To(Equal(kpav1alpha1.ExternalMetricType))
			Expect(metric.External).NotTo(BeNil())
			Expect(metric.External.Metric.Name).To(Equal("http_requests_per_second"))
			Expect(metric.External.Metric.Selector.MatchLabels).To(HaveKeyWithValue("scaledobject.keda.sh/name", "test-scaledobject"))
			Expect(metric.External.Target.Type).To(Equal(kpav1alpha1.AverageValueMetricType))
			expectedValue := resource.MustParse("100")
			Expect(metric.External.Target.AverageValue.Cmp(expectedValue)).To(Equal(0))
		})

		It("should handle missing MinReplicaCount", func() {
			// Remove minReplicaCount
			spec := scaledObject.Object["spec"].(map[string]interface{})
			delete(spec, "minReplicaCount")

			kpa, err := reconciler.translateSpec(scaledObject)
			Expect(err).NotTo(HaveOccurred())
			Expect(kpa).NotTo(BeNil())

			// MinReplicas should default to 1 when not specified (KEDA default)
			Expect(kpa.Spec.MinReplicas).To(Equal(*resource.NewQuantity(1, resource.DecimalSI)))
		})

		It("should translate CPU trigger", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			spec["triggers"] = []interface{}{
				map[string]interface{}{
					"type": "cpu",
					"metadata": map[string]interface{}{
						"type":  "Utilization",
						"value": "60",
					},
				},
			}

			kpa, err := reconciler.translateSpec(scaledObject)
			Expect(err).NotTo(HaveOccurred())
			Expect(kpa).NotTo(BeNil())

			// Should have one CPU metric
			Expect(kpa.Spec.Metrics).To(HaveLen(1))
			metric := kpa.Spec.Metrics[0]
			Expect(metric.Type).To(Equal(kpav1alpha1.ResourceMetricType))
			Expect(metric.Resource).NotTo(BeNil())
			Expect(string(metric.Resource.Name)).To(Equal("cpu"))
			Expect(metric.Resource.Target.Type).To(Equal(kpav1alpha1.UtilizationMetricType))
			Expect(*metric.Resource.Target.AverageUtilization).To(Equal(int32(60)))
		})

		It("should handle Value metric type", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			trigger := triggers[0].(map[string]interface{})
			metadata := trigger["metadata"].(map[string]interface{})
			metadata["metricType"] = "Value"

			kpa, err := reconciler.translateSpec(scaledObject)
			Expect(err).NotTo(HaveOccurred())
			Expect(kpa).NotTo(BeNil())

			metric := kpa.Spec.Metrics[0]
			Expect(metric.External.Target.Type).To(Equal(kpav1alpha1.ValueMetricType))
			Expect(metric.External.Target.Value).NotTo(BeNil())
			expectedValue := resource.MustParse("100")
			Expect(metric.External.Target.Value.Cmp(expectedValue)).To(Equal(0))
			Expect(metric.External.Target.AverageValue).To(BeNil())
		})

		It("should generate default metric name when not provided", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			trigger := triggers[0].(map[string]interface{})
			metadata := trigger["metadata"].(map[string]interface{})
			delete(metadata, "metricName")

			kpa, err := reconciler.translateSpec(scaledObject)
			Expect(err).NotTo(HaveOccurred())
			Expect(kpa).NotTo(BeNil())

			metric := kpa.Spec.Metrics[0]
			Expect(metric.External.Metric.Name).To(Equal("prometheus-0"))
		})

		It("should return error for missing Prometheus serverAddress", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			trigger := triggers[0].(map[string]interface{})
			metadata := trigger["metadata"].(map[string]interface{})
			delete(metadata, "serverAddress")

			_, err := reconciler.translateSpec(scaledObject)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prometheus trigger missing serverAddress"))
		})

		It("should return error for missing Prometheus query", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			trigger := triggers[0].(map[string]interface{})
			metadata := trigger["metadata"].(map[string]interface{})
			delete(metadata, "query")

			_, err := reconciler.translateSpec(scaledObject)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prometheus trigger missing query"))
		})

		It("should return error for missing threshold", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			trigger := triggers[0].(map[string]interface{})
			metadata := trigger["metadata"].(map[string]interface{})
			delete(metadata, "threshold")

			_, err := reconciler.translateSpec(scaledObject)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("prometheus trigger missing threshold"))
		})

		It("should return error for invalid threshold", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			trigger := triggers[0].(map[string]interface{})
			metadata := trigger["metadata"].(map[string]interface{})
			metadata["threshold"] = "invalid"

			_, err := reconciler.translateSpec(scaledObject)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to parse threshold"))
		})

		It("should return error for unsupported metricType", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			trigger := triggers[0].(map[string]interface{})
			metadata := trigger["metadata"].(map[string]interface{})
			metadata["metricType"] = "Utilization"

			_, err := reconciler.translateSpec(scaledObject)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unsupported metricType: Utilization"))
		})

		It("should handle multiple triggers", func() {
			spec := scaledObject.Object["spec"].(map[string]interface{})
			triggers := spec["triggers"].([]interface{})
			spec["triggers"] = append(triggers,
				map[string]interface{}{
					"type": "prometheus",
					"metadata": map[string]interface{}{
						"serverAddress": "http://prometheus2:9090",
						"metricName":    "cpu_usage",
						"query":         "avg(cpu_usage)",
						"threshold":     "80",
						"metricType":    "Value",
					},
				},
			)

			kpa, err := reconciler.translateSpec(scaledObject)
			Expect(err).NotTo(HaveOccurred())
			Expect(kpa).NotTo(BeNil())

			// Should have two metrics
			Expect(kpa.Spec.Metrics).To(HaveLen(2))

			// Check second metric
			metric2 := kpa.Spec.Metrics[1]
			Expect(metric2.External.Metric.Name).To(Equal("cpu_usage"))
			Expect(metric2.External.Metric.Selector.MatchLabels).To(HaveKeyWithValue("scaledobject.keda.sh/name", "test-scaledobject"))
			Expect(metric2.External.Target.Type).To(Equal(kpav1alpha1.ValueMetricType))
			expectedValue := resource.MustParse("80")
			Expect(metric2.External.Target.Value.Cmp(expectedValue)).To(Equal(0))
		})
	})

	Context("Reconcile Integration Tests", func() {
		var (
			ctx        context.Context
			reconciler *ScaledObjectReconciler
			k8sClient  client.Client
			scheme     *runtime.Scheme
		)

		BeforeEach(func() {
			ctx = context.TODO()
			scheme = runtime.NewScheme()
			// We don't need to add KEDA scheme since we're using unstructured
			Expect(kpav1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(appsv1.AddToScheme(scheme)).To(Succeed())

			k8sClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			reconciler = &ScaledObjectReconciler{
				Client: k8sClient,
				Scheme: scheme,
			}
		})

		It("should add finalizer and create KPodAutoscaler when annotation is present", func() {
			// Create a ScaledObject with the management annotation
			scaledObject := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "keda.sh/v1alpha1",
					"kind":       "ScaledObject",
					"metadata": map[string]interface{}{
						"name":      "test-scaledobject",
						"namespace": "default",
						"annotations": map[string]interface{}{
							managedByAnnotation: "true",
						},
					},
					"spec": map[string]interface{}{
						"scaleTargetRef": map[string]interface{}{
							"name":       "test-deployment",
							"kind":       "Deployment",
							"apiVersion": "apps/v1",
						},
						"minReplicaCount": int64(1),
						"maxReplicaCount": int64(5),
						"triggers": []interface{}{
							map[string]interface{}{
								"type": "prometheus",
								"metadata": map[string]interface{}{
									"serverAddress": "http://prometheus:9090",
									"metricName":    "test_metric",
									"query":         "test_query",
									"threshold":     "50",
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, scaledObject)).To(Succeed())

			// Reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-scaledobject",
					Namespace: "default",
				},
			}

			// First reconciliation should add finalizer
			result, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			// Check finalizer was added
			var updatedSO unstructured.Unstructured
			updatedSO.SetGroupVersionKind(ScaledObjectGVK)
			Expect(k8sClient.Get(ctx, req.NamespacedName, &updatedSO)).To(Succeed())
			Expect(updatedSO.GetFinalizers()).To(ContainElement(kpaFinalizer))

			// Second reconciliation should store original values and create dummy deployment
			result, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// Third reconciliation should modify ScaledObject to point to dummy
			result, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			// Fourth reconciliation should create KPodAutoscaler
			result, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// Check KPodAutoscaler was created
			var kpa kpav1alpha1.KPodAutoscaler
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "keda-kpa-test-scaledobject",
				Namespace: "default",
			}, &kpa)).To(Succeed())

			// Verify KPodAutoscaler fields
			Expect(kpa.Spec.ScaleTargetRef.Name).To(Equal("test-deployment"))
			expectedMin := resource.MustParse("1")
			expectedMax := resource.MustParse("5")
			Expect(kpa.Spec.MinReplicas.Cmp(expectedMin)).To(Equal(0))
			Expect(kpa.Spec.MaxReplicas.Cmp(expectedMax)).To(Equal(0))
			Expect(kpa.Spec.Metrics).To(HaveLen(1))

			// Verify owner reference
			Expect(kpa.OwnerReferences).To(HaveLen(1))
			Expect(kpa.OwnerReferences[0].Name).To(Equal("test-scaledobject"))
			Expect(kpa.OwnerReferences[0].Kind).To(Equal("ScaledObject"))
			Expect(*kpa.OwnerReferences[0].Controller).To(BeTrue())
		})

		It("should handle ScaledObject not found", func() {
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "non-existent",
					Namespace: "default",
				},
			}

			result, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})
})

// Unit test for translateTrigger
func TestTranslateTrigger(t *testing.T) {
	reconciler := &ScaledObjectReconciler{}

	tests := []struct {
		name         string
		trigger      map[string]interface{}
		expectError  bool
		errorMessage string
		validate     func(t *testing.T, metric *kpav1alpha1.MetricSpec)
	}{
		{
			name: "valid prometheus trigger with AverageValue",
			trigger: map[string]interface{}{
				"type": "prometheus",
				"metadata": map[string]interface{}{
					"serverAddress": "http://prometheus:9090",
					"metricName":    "test_metric",
					"query":         "test_query",
					"threshold":     "100",
					"metricType":    "AverageValue",
				},
			},
			expectError: false,
			validate: func(t *testing.T, metric *kpav1alpha1.MetricSpec) {
				if metric == nil {
					t.Fatal("Expected metric to be non-nil")
				}
				if metric.Type != kpav1alpha1.ExternalMetricType {
					t.Errorf("Expected metric type to be External, got %v", metric.Type)
				}
				if metric.External.Metric.Name != "test_metric" {
					t.Errorf("Expected metric name to be test_metric, got %v", metric.External.Metric.Name)
				}
				if metric.External.Target.Type != kpav1alpha1.AverageValueMetricType {
					t.Errorf("Expected target type to be AverageValue, got %v", metric.External.Target.Type)
				}
				expectedQuantity := resource.MustParse("100")
				if metric.External.Target.AverageValue == nil {
					t.Error("Expected AverageValue to be non-nil")
				} else if metric.External.Target.AverageValue.Cmp(expectedQuantity) != 0 {
					t.Errorf("Expected average value to be 100, got %v", metric.External.Target.AverageValue)
				}
			},
		},
		{
			name: "cpu trigger",
			trigger: map[string]interface{}{
				"type": "cpu",
				"metadata": map[string]interface{}{
					"type":  "Utilization",
					"value": "60",
				},
			},
			expectError: false,
			validate: func(t *testing.T, metric *kpav1alpha1.MetricSpec) {
				if metric == nil {
					t.Fatal("Expected metric to be non-nil for CPU trigger")
				}
				if metric.Type != kpav1alpha1.ResourceMetricType {
					t.Errorf("Expected metric type to be Resource, got %v", metric.Type)
				}
				if metric.Resource.Name != "cpu" {
					t.Errorf("Expected resource name to be cpu, got %v", metric.Resource.Name)
				}
				if metric.Resource.Target.Type != kpav1alpha1.UtilizationMetricType {
					t.Errorf("Expected target type to be Utilization, got %v", metric.Resource.Target.Type)
				}
				if *metric.Resource.Target.AverageUtilization != int32(60) {
					t.Errorf("Expected average utilization to be 60, got %v", *metric.Resource.Target.AverageUtilization)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metric, err := reconciler.translateTrigger(tt.trigger, 0, "test-scaledobject")

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if tt.errorMessage != "" && err.Error() != tt.errorMessage {
					t.Errorf("Expected error message %q, got %q", tt.errorMessage, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if tt.validate != nil {
					tt.validate(t, metric)
				}
			}
		})
	}
}
