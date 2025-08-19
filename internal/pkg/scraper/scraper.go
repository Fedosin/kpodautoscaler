package scraper

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"

	autoscalingv1alpha1 "github.com/Fedosin/kpodautoscaler/api/v1alpha1"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// InsufficientDataError is returned when there is not enough historical data to calculate a rate
type InsufficientDataError struct {
	message string
}

const (
	envoyMetricName = "envoy_http_downstream_rq_total"
	envoyAdminPort  = 9901
	envoyStatsPath  = "/stats/prometheus"
	envoyLabelKey   = "envoy_http_conn_manager_prefix"
	envoyLabelValue = "ingresshttp"
)

func (e InsufficientDataError) Error() string {
	return e.message
}

// counterValue stores a counter metric value with its timestamp
type counterValue struct {
	value     float64
	timestamp time.Time
}

// PodMetricScraper scrapes metrics from user-defined endpoints.
type PodMetricScraper struct {
	kubeClient kubernetes.Interface
	httpClient *http.Client
	// counterStore stores previous counter values for rate calculation
	// key is "url|metricName"
	counterStore map[string]counterValue
	mu           sync.RWMutex
}

// NewPodMetricScraper creates a new PodMetricScraper.
func NewPodMetricScraper(kubeClient kubernetes.Interface) *PodMetricScraper {
	s := &PodMetricScraper{
		kubeClient: kubeClient,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		counterStore: make(map[string]counterValue),
	}

	// Start cleanup goroutine to remove old counter values
	go s.cleanupCounterStore()

	return s
}

// cleanupCounterStore periodically removes old counter values to prevent memory leaks
func (s *PodMetricScraper) cleanupCounterStore() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for key, value := range s.counterStore {
			// Remove entries older than 10 minutes
			if now.Sub(value.timestamp) > 10*time.Minute {
				delete(s.counterStore, key)
				klog.V(5).Infof("Cleaned up old counter value for key: %s", key)
			}
		}
		s.mu.Unlock()
	}
}

// GetMetricValue fetches and calculates the metric value for a given KPodAutoscaler and UserMetricSource.
// For Counter metrics, it calculates the rate (increase per second) by comparing with the previous value.
// If there's no previous value for a Counter metric, it returns an InsufficientDataError.
func (s *PodMetricScraper) GetMetricValue(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, metricSource *autoscalingv1alpha1.UserMetricSource) (int64, error) {
	scaleTargetRef := kpa.Spec.ScaleTargetRef
	namespace := kpa.Namespace

	var selector labels.Selector
	var err error

	switch scaleTargetRef.Kind {
	case "Deployment":
		deployment, errGet := s.kubeClient.AppsV1().Deployments(namespace).Get(ctx, scaleTargetRef.Name, metav1.GetOptions{})
		if errGet != nil {
			return 0, fmt.Errorf("failed to get deployment %s: %w", scaleTargetRef.Name, errGet)
		}
		selector, err = metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if err != nil {
			return 0, fmt.Errorf("failed to parse selector from deployment %s: %w", scaleTargetRef.Name, err)
		}
	case "StatefulSet":
		sts, errGet := s.kubeClient.AppsV1().StatefulSets(namespace).Get(ctx, scaleTargetRef.Name, metav1.GetOptions{})
		if errGet != nil {
			return 0, fmt.Errorf("failed to get statefulset %s: %w", scaleTargetRef.Name, errGet)
		}
		selector, err = metav1.LabelSelectorAsSelector(sts.Spec.Selector)
		if err != nil {
			return 0, fmt.Errorf("failed to parse selector from statefulset %s: %w", scaleTargetRef.Name, err)
		}
	default:
		return 0, fmt.Errorf("unsupported scaleTargetRef kind: %s", scaleTargetRef.Kind)
	}

	pods, err := s.kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return 0, fmt.Errorf("failed to list pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return 0, fmt.Errorf("no pods found for selector %q", selector.String())
	}

	var totalValue float64
	var podsWithMetric int
	var insufficientDataCount int

	for _, pod := range pods.Items {
		if pod.Status.Phase != v1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}
		podIP := pod.Status.PodIP
		if podIP == "" {
			klog.Warningf("Pod %s/%s has no IP", pod.Namespace, pod.Name)
			continue
		}

		port, errPort := resolvePort(&pod, metricSource.Metric.Port)
		if errPort != nil {
			klog.Warningf("Failed to resolve port for pod %s/%s: %v", pod.Namespace, pod.Name, errPort)
			continue
		}

		path := metricSource.Metric.Path
		if path == "" {
			path = "/metrics"
		}

		metricURL := fmt.Sprintf("http://%s:%d%s", podIP, port, path)
		value, errScrape := s.scrapeMetric(ctx, metricURL, metricSource.Metric.Name, nil) // No label filters for user metrics
		if errScrape != nil {
			// Check if it's an InsufficientDataError
			if _, ok := errScrape.(InsufficientDataError); ok {
				insufficientDataCount++
				klog.V(4).Infof("Insufficient data for counter metric from pod %s/%s: %v", pod.Namespace, pod.Name, errScrape)
				continue
			}
			klog.Warningf("Failed to scrape metric from pod %s/%s: %v", pod.Namespace, pod.Name, errScrape)
			continue
		}

		totalValue += value
		podsWithMetric++
	}

	// If all pods that we tried to scrape had insufficient data, return the error
	if insufficientDataCount > 0 && podsWithMetric == 0 {
		return 0, InsufficientDataError{
			message: fmt.Sprintf("insufficient data to calculate rate for counter metric %s on all pods", metricSource.Metric.Name),
		}
	}

	if podsWithMetric == 0 {
		return 0, fmt.Errorf("no pods returned the metric %s", metricSource.Metric.Name)
	}

	var result int64
	switch metricSource.Target.Type {
	case autoscalingv1alpha1.AverageValueMetricType:
		result = int64(totalValue / float64(podsWithMetric))
	case autoscalingv1alpha1.ValueMetricType:
		result = int64(totalValue)
	default:
		return 0, fmt.Errorf("unsupported target type: %s", metricSource.Target.Type)
	}

	return result, nil
}

// GetKProxyMetricValue fetches the metric value from a KProxy deployment's Envoy pod.
// It reads the KProxy object to get the deployment name, then scrapes the Envoy metric.
func (s *PodMetricScraper) GetKProxyMetricValue(ctx context.Context, kpa *autoscalingv1alpha1.KPodAutoscaler, kproxySource *autoscalingv1alpha1.KProxyMetricSource, kproxy *autoscalingv1alpha1.KProxy) (int64, error) {

	// Get the KProxy deployment name from the status
	if kproxy.Status.KProxyDeploymentName == "" {
		return 0, fmt.Errorf("KProxy %s does not have a deployment name in status", kproxySource.Name)
	}

	// Get pods for the KProxy deployment
	deployment, err := s.kubeClient.AppsV1().Deployments(kpa.Namespace).Get(ctx, kproxy.Status.KProxyDeploymentName, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to get KProxy deployment %s: %w", kproxy.Status.KProxyDeploymentName, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return 0, fmt.Errorf("failed to parse selector from KProxy deployment %s: %w", kproxy.Status.KProxyDeploymentName, err)
	}

	pods, err := s.kubeClient.CoreV1().Pods(kpa.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return 0, fmt.Errorf("failed to list KProxy pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return 0, fmt.Errorf("no pods found for KProxy deployment %s", kproxy.Status.KProxyDeploymentName)
	}

	var totalValue float64
	var podsWithMetric int
	var insufficientDataCount int

	labelFilters := map[string]string{
		envoyLabelKey: envoyLabelValue,
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != v1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}
		podIP := pod.Status.PodIP
		if podIP == "" {
			klog.Warningf("KProxy pod %s/%s has no IP", pod.Namespace, pod.Name)
			continue
		}

		metricURL := fmt.Sprintf("http://%s:%d%s", podIP, envoyAdminPort, envoyStatsPath)
		value, errScrape := s.scrapeMetric(ctx, metricURL, envoyMetricName, labelFilters)
		if errScrape != nil {
			// Check if it's an InsufficientDataError
			if _, ok := errScrape.(InsufficientDataError); ok {
				insufficientDataCount++
				klog.V(4).Infof("Insufficient data for counter metric from KProxy pod %s/%s: %v", pod.Namespace, pod.Name, errScrape)
				continue
			}
			klog.Warningf("Failed to scrape metric from KProxy pod %s/%s: %v", pod.Namespace, pod.Name, errScrape)
			continue
		}

		totalValue += value
		podsWithMetric++
	}

	// If all pods that we tried to scrape had insufficient data, return the error
	if insufficientDataCount > 0 && podsWithMetric == 0 {
		labelStr := ""
		if len(labelFilters) > 0 {
			labelStr = fmt.Sprintf(" with labels %v", labelFilters)
		}
		return 0, InsufficientDataError{
			message: fmt.Sprintf("insufficient data to calculate rate for counter metric %s%s on all KProxy pods", envoyMetricName, labelStr),
		}
	}

	if podsWithMetric == 0 {
		labelStr := ""
		if len(labelFilters) > 0 {
			labelStr = fmt.Sprintf(" with labels %v", labelFilters)
		}
		return 0, fmt.Errorf("no KProxy pods returned the metric %s%s", envoyMetricName, labelStr)
	}

	return int64(totalValue / float64(podsWithMetric)), nil
}

func (s *PodMetricScraper) scrapeMetric(ctx context.Context, url string, metricName string, labelFilters map[string]string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to get metrics: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			klog.Warningf("Failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Create a new parser.
	var parser expfmt.TextParser

	// Parse the metrics from the response body.
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to parse prometheus metrics: %w", err)
	}

	mf, ok := metricFamilies[metricName]
	if !ok {
		return 0, fmt.Errorf("metric %s not found", metricName)
	}

	var total float64
	isCounter := false
	matchingMetrics := 0

	// The metric can have multiple samples (e.g. from multiple goroutines), so we average them.
	for _, m := range mf.Metric {
		// Check if the metric labels match the filters
		if !matchesLabelFilters(m, labelFilters) {
			continue
		}

		matchingMetrics++
		if m.Gauge != nil && m.Gauge.Value != nil {
			total += *m.Gauge.Value
		} else if m.Counter != nil && m.Counter.Value != nil {
			total += *m.Counter.Value
			isCounter = true
		}
		// Note: Histograms and Summaries are not supported yet.
	}

	if matchingMetrics == 0 {
		labelStr := ""
		if len(labelFilters) > 0 {
			labelStr = fmt.Sprintf(" with labels %v", labelFilters)
		}
		return 0, fmt.Errorf("no samples found for metric %s%s", metricName, labelStr)
	}

	// Calculate the average value of the metric samples.
	avgValue := total / float64(matchingMetrics)

	// For Counter metrics, calculate the rate (difference per second)
	if isCounter {
		// Include labels in the key to distinguish different label combinations
		labelsStr := ""
		if len(labelFilters) > 0 {
			labelsStr = fmt.Sprintf("|%v", labelFilters)
		}
		key := fmt.Sprintf("%s|%s%s", url, metricName, labelsStr)
		key = base64.StdEncoding.EncodeToString([]byte(key))

		s.mu.Lock()
		defer s.mu.Unlock()

		now := time.Now()

		// Check if we have a previous value
		if prev, exists := s.counterStore[key]; exists {
			timeDiff := now.Unix() - prev.timestamp.Unix()

			// We consider that the metric is updated every second.
			if timeDiff == 1 {
				// Calculate rate (counter increase per second)
				// Example: if at second 1 the value was 1000, and at second 2 it was 1100,
				// then the rate is (1100 - 1000) / 1 = 100 requests per second
				rate := avgValue - prev.value

				// Store current value for next calculation
				s.counterStore[key] = counterValue{
					value:     avgValue,
					timestamp: now,
				}

				return rate, nil
			}
		}

		// No previous value - store current value and return error
		s.counterStore[key] = counterValue{
			value:     avgValue,
			timestamp: now,
		}

		return 0, InsufficientDataError{
			message: fmt.Sprintf("insufficient data to calculate rate for counter metric %s", metricName),
		}
	}

	// For Gauge metrics, return the value as-is
	return avgValue, nil
}

// matchesLabelFilters checks if a metric's labels match the given filters
func matchesLabelFilters(metric *dto.Metric, labelFilters map[string]string) bool {
	// If no filters are specified, all metrics match
	if len(labelFilters) == 0 {
		return true
	}

	// Check each filter
	for filterKey, filterValue := range labelFilters {
		found := false
		for _, label := range metric.Label {
			if label.Name != nil && *label.Name == filterKey {
				if label.Value != nil && *label.Value == filterValue {
					found = true
					break
				}
			}
		}
		// If any filter doesn't match, the metric doesn't match
		if !found {
			return false
		}
	}

	return true
}

func resolvePort(pod *v1.Pod, portRef intstr.IntOrString) (int, error) {
	if portRef.Type == intstr.Int {
		return portRef.IntValue(), nil
	}
	if portRef.Type == intstr.String {
		portName := portRef.String()
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if port.Name == portName {
					return int(port.ContainerPort), nil
				}
			}
		}
	}
	return 0, fmt.Errorf("port %s not found", portRef.String())
}
