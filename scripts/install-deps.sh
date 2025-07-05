#!/bin/bash
set -euo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
    exit 1
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    # Check kubectl
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
    fi
    
    # Check helm
    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed. Please install helm first."
    fi
    
    # Check cluster connection
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot connect to Kubernetes cluster. Please ensure cluster is running."
    fi
    
    log_info "Prerequisites check passed"
}

# Add Helm repositories
add_helm_repos() {
    log_info "Adding Helm repositories..."
    
    helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
    helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
    helm repo add kedacore https://kedacore.github.io/charts
    helm repo add bitnami https://charts.bitnami.com/bitnami
    
    log_info "Updating Helm repositories..."
    helm repo update
}

# Install metrics-server
install_metrics_server() {
    log_info "Installing metrics-server..."
    
    # Create values file for metrics-server
    cat <<EOF > /tmp/metrics-server-values.yaml
args:
  - --cert-dir=/tmp
  - --secure-port=10250
  - --kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname
  - --kubelet-use-node-status-port
  - --metric-resolution=15s
  - --kubelet-insecure-tls
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi
service:
  type: ClusterIP
  port: 443
EOF
    
    helm upgrade --install metrics-server metrics-server/metrics-server \
        --namespace kube-system \
        --version 3.12.2 \
        --values /tmp/metrics-server-values.yaml \
        --wait \
        --timeout 5m
    
    # Wait for metrics-server to be ready
    log_info "Waiting for metrics-server to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=metrics-server -n kube-system --timeout=300s
    
    # Verify metrics-server is working
    sleep 30  # Give it time to collect metrics
    if kubectl top nodes &> /dev/null; then
        log_info "metrics-server is working correctly"
    else
        log_warn "metrics-server might not be fully ready yet"
    fi
}

# Install Prometheus stack
install_prometheus_stack() {
    log_info "Installing Prometheus stack..."
    
    # Create namespace
    kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
    
    # Create values file for kube-prometheus-stack
    cat <<EOF > /tmp/prometheus-stack-values.yaml
prometheus:
  prometheusSpec:
    retention: 6h
    resources:
      requests:
        cpu: 200m
        memory: 1Gi
      limits:
        cpu: 1
        memory: 2Gi
    serviceMonitorSelectorNilUsesHelmValues: false
    podMonitorSelectorNilUsesHelmValues: false
    ruleSelectorNilUsesHelmValues: false
  service:
    type: NodePort
    nodePort: 30090
grafana:
  enabled: false
alertmanager:
  enabled: false
kubeStateMetrics:
  enabled: true
nodeExporter:
  enabled: true
prometheusOperator:
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 512Mi
EOF
    
    helm upgrade --install prometheus-stack prometheus-community/kube-prometheus-stack \
        --namespace monitoring \
        --version 75.7.0 \
        --values /tmp/prometheus-stack-values.yaml \
        --wait \
        --timeout 10m
    
    # Wait for Prometheus to be ready
    log_info "Waiting for Prometheus to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=prometheus -n monitoring --timeout=300s
}

# Install Prometheus Adapter
install_prometheus_adapter() {
    log_info "Installing Prometheus Adapter..."
    
    # Create values file for prometheus-adapter
    cat <<EOF > /tmp/prometheus-adapter-values.yaml
prometheus:
  url: http://prometheus-stack-kube-prom-prometheus.monitoring.svc.cluster.local
  port: 9090
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi
rules:
  default: true
  custom:
  - seriesQuery: 'http_requests_total{namespace!="",pod!=""}'
    resources:
      overrides:
        namespace: {resource: "namespace"}
        pod: {resource: "pod"}
    name:
      matches: "^(.*)"
      as: "http_requests_per_second"
    metricsQuery: 'sum(rate(<<.Series>>{<<.LabelMatchers>>}[2m])) by (<<.GroupBy>>)'
  - seriesQuery: 'custom_metric_gauge{namespace!="",pod!=""}'
    resources:
      overrides:
        namespace: {resource: "namespace"}
        pod: {resource: "pod"}
    name:
      matches: "^(.*)"
      as: "custom_metric_gauge"
    metricsQuery: 'avg_over_time(<<.Series>>{<<.LabelMatchers>>}[2m])'
EOF
    
    helm upgrade --install prometheus-adapter prometheus-community/prometheus-adapter \
        --namespace monitoring \
        --version 4.14.1 \
        --values /tmp/prometheus-adapter-values.yaml \
        --wait \
        --timeout 5m
    
    # Wait for prometheus-adapter to be ready
    log_info "Waiting for Prometheus Adapter to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=prometheus-adapter -n monitoring --timeout=300s
    
    # Verify custom metrics API is available
    sleep 10
    if kubectl get --raw /apis/custom.metrics.k8s.io/v1beta1 &> /dev/null; then
        log_info "Custom metrics API is available"
    else
        log_warn "Custom metrics API might not be fully ready yet"
    fi
}

# Install KEDA
install_keda() {
    log_info "Installing KEDA..."
    
    # Create namespace
    kubectl create namespace keda --dry-run=client -o yaml | kubectl apply -f -
    
    # Create values file for KEDA
    cat <<EOF > /tmp/keda-values.yaml
resources:
  operator:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 512Mi
  metricServer:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 512Mi
  webhooks:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 200m
      memory: 256Mi
prometheus:
  metricServer:
    enabled: true
    port: 8080
    path: /metrics
  operator:
    enabled: true
    port: 8080
    path: /metrics
  webhooks:
    enabled: true
    port: 8080
    path: /metrics
EOF
    
    helm upgrade --install keda kedacore/keda \
        --namespace keda \
        --version 2.17.2 \
        --values /tmp/keda-values.yaml \
        --wait \
        --timeout 5m
    
    # Wait for KEDA to be ready
    log_info "Waiting for KEDA to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=keda-operator -n keda --timeout=300s
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=keda-operator-metrics-apiserver -n keda --timeout=300s
    
    # Verify external metrics API is available
    sleep 10
    if kubectl get --raw /apis/external.metrics.k8s.io/v1beta1 &> /dev/null; then
        log_info "External metrics API is available"
    else
        log_warn "External metrics API might not be fully ready yet"
    fi
}

# Install Redis for external metrics testing
install_redis() {
    log_info "Installing Redis for external metrics testing..."
    
    # Create namespace
    kubectl create namespace redis --dry-run=client -o yaml | kubectl apply -f -
    
    # Create values file for Redis
    cat <<EOF > /tmp/redis-values.yaml
auth:
  enabled: false
replica:
  replicaCount: 0
master:
  persistence:
    enabled: false
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 512Mi
metrics:
  enabled: true
  service:
    type: ClusterIP
    port: 9121
  serviceMonitor:
    enabled: true
    namespace: redis
EOF
    
    helm upgrade --install redis bitnami/redis \
        --namespace redis \
        --version 21.2.6 \
        --values /tmp/redis-values.yaml \
        --wait \
        --timeout 5m
    
    # Wait for Redis to be ready
    log_info "Waiting for Redis to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=redis -n redis --timeout=300s
}

# Verify all installations
verify_installations() {
    log_info "Verifying installations..."
    
    # Check metrics-server
    if kubectl top nodes &> /dev/null; then
        log_info "✓ metrics-server is working"
    else
        log_error "✗ metrics-server is not working"
    fi
    
    # Check Prometheus
    if kubectl get svc -n monitoring prometheus-stack-kube-prom-prometheus &> /dev/null; then
        log_info "✓ Prometheus is installed"
    else
        log_error "✗ Prometheus is not installed"
    fi
    
    # Check custom metrics API
    if kubectl get --raw /apis/custom.metrics.k8s.io/v1beta1 &> /dev/null; then
        log_info "✓ Custom metrics API is available"
    else
        log_error "✗ Custom metrics API is not available"
    fi
    
    # Check external metrics API
    if kubectl get --raw /apis/external.metrics.k8s.io/v1beta1 &> /dev/null; then
        log_info "✓ External metrics API is available"
    else
        log_error "✗ External metrics API is not available"
    fi
    
    # Check Redis
    if kubectl get svc -n redis redis-master &> /dev/null; then
        log_info "✓ Redis is installed"
    else
        log_error "✗ Redis is not installed"
    fi
    
    log_info "All dependencies installed successfully!"
}

# Cleanup function
cleanup() {
    rm -f /tmp/metrics-server-values.yaml
    rm -f /tmp/prometheus-stack-values.yaml
    rm -f /tmp/prometheus-adapter-values.yaml
    rm -f /tmp/keda-values.yaml
    rm -f /tmp/redis-values.yaml
}

# Main execution
main() {
    trap cleanup EXIT
    
    log_info "Starting dependency installation for kpodautoscaler E2E tests..."
    
    check_prerequisites
    add_helm_repos
    install_metrics_server
    install_prometheus_stack
    install_prometheus_adapter
    install_keda
    install_redis
    verify_installations
    
    log_info "Installation complete!"
}

# Run main function
main "$@" 