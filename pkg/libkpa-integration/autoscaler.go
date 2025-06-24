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

package libkpa

import (
	"time"

	"github.com/Fedosin/libkpa/algorithm"
	"github.com/Fedosin/libkpa/api"

	"github.com/Fedosin/kpodautoscaler/api/v1alpha1"
)

// MetricAutoscaler wraps libkpa autoscaler for a specific metric
type MetricAutoscaler struct {
	Metric     v1alpha1.MetricSpec
	autoscaler *algorithm.SlidingWindowAutoscaler
}

// NewMetricAutoscaler creates a new autoscaler for the given metric
func NewMetricAutoscaler(kpa *v1alpha1.KPodAutoscaler, metric v1alpha1.MetricSpec) (*MetricAutoscaler, error) {
	cfg := GetAutoscalerConfig(metric.Config)

	// Set default target based on metric type
	if cfg.TargetValue == 0 {
		switch metric.Type {
		case v1alpha1.ResourceMetricType:
			if metric.Resource != nil && metric.Resource.Target.AverageUtilization != nil {
				cfg.TargetValue = float64(*metric.Resource.Target.AverageUtilization)
			}
		case v1alpha1.PodsMetricType:
			if metric.Pods != nil && metric.Pods.Target.AverageValue != nil {
				// This will be handled by the ratio calculation in the controller
				cfg.TargetValue = 1.0
			}
		case v1alpha1.ObjectMetricType:
			if metric.Object != nil && metric.Object.Target.Value != nil {
				// This will be handled by the ratio calculation in the controller
				cfg.TargetValue = 1.0
			}
		case v1alpha1.ExternalMetricType:
			if metric.External != nil {
				// This will be handled by the ratio calculation in the controller
				cfg.TargetValue = 1.0
			}
		}
	}

	// Create the autoscaler
	// Using initial scale of 1
	initialScale := int32(1)
	if metric.Config != nil && metric.Config.ActivationScale > 0 {
		initialScale = metric.Config.ActivationScale
	}

	autoscaler := algorithm.NewSlidingWindowAutoscaler(cfg, initialScale)

	return &MetricAutoscaler{
		Metric:     metric,
		autoscaler: autoscaler,
	}, nil
}

// Update updates the autoscaler with a new metric value and returns the recommended replica count
func (ma *MetricAutoscaler) Update(metricValue float64, currentReplicas int32, timestamp time.Time) int32 {
	// For simplicity, use the same value for stable and panic windows
	// The actual windowing is handled inside the libkpa algorithm
	// snapshot := &api.MetricSnapshot{
	// 	StableValue: metricValue,
	// 	PanicValue:  metricValue,
	// 	ReadyPodCount:   currentReplicas,
	// 	Timestamp:   timestamp,
	// }
	// recommendation := ma.autoscaler.Scale(snapshot, timestamp)
	// return recommendation.DesiredPodCount

	return 0
}

// GetAutoscalerConfig converts KPA MetricConfig to libkpa AutoscalerConfig
func GetAutoscalerConfig(metricConfig *v1alpha1.MetricConfig) api.AutoscalerConfig {
	// Create default config
	cfg := api.AutoscalerConfig{
		MaxScaleUpRate:        1000.0,
		MaxScaleDownRate:      2.0,
		TargetValue:           100.0,
		TotalValue:            1000.0,
		TargetBurstCapacity:   211.0,
		PanicThreshold:        200.0,
		PanicWindowPercentage: 10.0,
		StableWindow:          60 * time.Second,
		ScaleDownDelay:        0 * time.Second,
		MinScale:              0,
		MaxScale:              0,
		ActivationScale:       1,
	}

	if metricConfig == nil {
		return cfg
	}

	// Window configuration
	if metricConfig.StableWindow > 0 {
		cfg.StableWindow = metricConfig.StableWindow
	}
	if metricConfig.PanicWindowPercentage != nil {
		cfg.PanicWindowPercentage = metricConfig.PanicWindowPercentage.AsApproximateFloat64()
	}

	// Scaling rates
	if metricConfig.MaxScaleUpRate != nil {
		cfg.MaxScaleUpRate = metricConfig.MaxScaleUpRate.AsApproximateFloat64()
	}
	if metricConfig.MaxScaleDownRate != nil {
		cfg.MaxScaleDownRate = metricConfig.MaxScaleDownRate.AsApproximateFloat64()
	}

	// Panic configuration
	if metricConfig.PanicThreshold != nil {
		cfg.PanicThreshold = metricConfig.PanicThreshold.AsApproximateFloat64()
	}

	// Target and total values
	if metricConfig.TargetValue != nil {
		cfg.TargetValue = metricConfig.TargetValue.AsApproximateFloat64()
	}
	if metricConfig.TotalValue != nil {
		cfg.TotalValue = metricConfig.TotalValue.AsApproximateFloat64()
	}
	if metricConfig.TargetBurstCapacity != nil {
		cfg.TargetBurstCapacity = metricConfig.TargetBurstCapacity.AsApproximateFloat64()
	}

	// Scale down delay
	if metricConfig.ScaleDownDelay > 0 {
		cfg.ScaleDownDelay = metricConfig.ScaleDownDelay
	}

	// Activation scale - use for scaling from zero
	if metricConfig.ActivationScale > 0 {
		cfg.ActivationScale = metricConfig.ActivationScale
	}

	return cfg
}
