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

	"github.com/Fedosin/kpodautoscaler/api/v1alpha1"
	"github.com/Fedosin/libkpa/algorithm"
	"github.com/Fedosin/libkpa/api"
)

// SimpleMetricSnapshot implements api.MetricSnapshot for our use case
type SimpleMetricSnapshot struct {
	stableValue float64
	panicValue  float64
	readyPods   int32
	timestamp   time.Time
}

func (s *SimpleMetricSnapshot) StableValue() float64 {
	return s.stableValue
}

func (s *SimpleMetricSnapshot) PanicValue() float64 {
	return s.panicValue
}

func (s *SimpleMetricSnapshot) ReadyPodCount() int32 {
	return s.readyPods
}

func (s *SimpleMetricSnapshot) Timestamp() time.Time {
	return s.timestamp
}

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
	if metric.Config != nil && metric.Config.InitialScale != nil {
		initialScale = *metric.Config.InitialScale
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
	snapshot := &SimpleMetricSnapshot{
		stableValue: metricValue,
		panicValue:  metricValue,
		readyPods:   currentReplicas,
		timestamp:   timestamp,
	}
	recommendation := ma.autoscaler.Scale(snapshot, timestamp)
	return recommendation.DesiredPodCount
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
	if metricConfig.WindowSize != nil {
		cfg.StableWindow = metricConfig.WindowSize.Duration
	}
	if metricConfig.StableWindow != nil {
		cfg.StableWindow = metricConfig.StableWindow.Duration
	}
	if metricConfig.PanicWindow != nil {
		cfg.PanicWindowPercentage = metricConfig.PanicWindow.Duration.Seconds() / cfg.StableWindow.Seconds() * 100
	}

	// Scaling rates
	if metricConfig.ScaleUpRate != nil {
		cfg.MaxScaleUpRate = *metricConfig.ScaleUpRate
	}
	if metricConfig.ScaleDownRate != nil {
		cfg.MaxScaleDownRate = *metricConfig.ScaleDownRate
	}
	if metricConfig.MaxScaleUpRate != nil {
		cfg.MaxScaleUpRate = *metricConfig.MaxScaleUpRate
	}
	if metricConfig.MaxScaleDownRate != nil {
		cfg.MaxScaleDownRate = *metricConfig.MaxScaleDownRate
	}

	// Panic configuration
	if metricConfig.PanicThreshold != nil {
		cfg.PanicThreshold = *metricConfig.PanicThreshold
	}

	// Target utilization
	if metricConfig.TargetUtilization != nil {
		cfg.TargetValue = *metricConfig.TargetUtilization
	}

	// Initial scale - use ActivationScale for scaling from zero
	if metricConfig.InitialScale != nil {
		cfg.ActivationScale = max(1, *metricConfig.InitialScale)
	}

	return cfg
}
