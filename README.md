# K Pod Autoscaler (KPA)

A Kubernetes controller that provides advanced pod autoscaling using algorithms from [libkpa](https://github.com/Fedosin/libkpa). KPA is compatible with the HorizontalPodAutoscaler CRD pattern but uses more sophisticated scaling algorithms including sliding window and weighted time window approaches.

## Features

- **Advanced Scaling Algorithms**: Uses libkpa's sliding window algorithms for more stable and predictable scaling
- **Multiple Metric Support**: Supports Resource, Pods, Object, and External metrics
- **Per-Metric Configuration**: Each metric can have its own window size, burst threshold, and scaling rates
- **HPA-Compatible**: Similar API to Kubernetes HorizontalPodAutoscaler for easy migration
- **Per-CR Goroutines**: Dedicated goroutine per autoscaler for 1-second metric fetching intervals

## Architecture

KPA follows a controller-runtime pattern with:
- CRD defining KPodAutoscaler resources
- Controller reconciling KPA objects
- Per-CR goroutines fetching metrics every second
- Integration with Kubernetes metrics APIs (Metrics Server, Custom Metrics, External Metrics)

## Prerequisites

- Kubernetes 1.24+
- Metrics Server (for resource metrics)
- Custom Metrics API (optional, for custom metrics)
- External Metrics API (optional, for external metrics)
- Go 1.24+ (for building from source)

## Installation

### Using Helm

```bash
# Add the helm repository (when published)
helm repo add kpa https://fedosin.github.io/kpodautoscaler
helm repo update

# Install KPA
helm install kpa kpa/kpodautoscaler --namespace kpa-system --create-namespace
```

### Using Kustomize

```bash
# Install CRDs
kubectl apply -f config/crd/bases

# Install controller
kubectl apply -k config/default
```

### From Source

```bash
# Clone the repository
git clone https://github.com/Fedosin/kpodautoscaler.git
cd kpodautoscaler

# Install CRDs
make install

# Run locally (for development)
make run

# Or build and deploy to cluster
make docker-build docker-push IMG=<your-registry>/kpodautoscaler:tag
make deploy IMG=<your-registry>/kpodautoscaler:tag
```

## Usage

### Basic Example

```yaml
apiVersion: autoscaling.kpodautoscaler.io/v1alpha1
kind: KPodAutoscaler
metadata:
  name: example-kpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: example-app
  minReplicas: 2
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
    config:
      aggregationAlgorithm: "linear"  # or "weighted"
      stableWindow: 60s
      burstWindowPercentage: 10.0
```

### Advanced Example with Multiple Metrics

```yaml
apiVersion: autoscaling.kpodautoscaler.io/v1alpha1
kind: KPodAutoscaler
metadata:
  name: advanced-kpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: web-app
  minReplicas: 3
  maxReplicas: 50
  metrics:
  # CPU metric with custom window
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 80
    config:
      aggregationAlgorithm: "weighted"
      stableWindow: 120s
      burstWindowPercentage: 8.33
      maxScaleUpRate: 2.0
      maxScaleDownRate: 0.5
      burstThreshold: 200.0
  
  # Memory metric
  - type: Resource
    resource:
      name: memory
      target:
        type: AverageValue
        averageValue: "1Gi"
    config:
      stableWindow: 90s
  
  # Custom metric from Prometheus
  - type: Pods
    pods:
      metric:
        name: http_requests_per_second
        selector:
          matchLabels:
            metric: requests
      target:
        type: AverageValue
        averageValue: "100"
    config:
      aggregationAlgorithm: "linear"
      stableWindow: 60s
  
  # External metric (e.g., queue length)
  - type: External
    external:
      metric:
        name: kafka_consumer_lag
        selector:
          matchLabels:
            topic: orders
      target:
        type: Value
        value: "30"
    config:
      stableWindow: 120s
      burstThreshold: 150.0
```

## Configuration

### MetricConfig Options

Each metric can have a `config` section with the following options:

| Field | Type | Description | Default |
|-------|------|-------------|---------|
| `aggregationAlgorithm` | string | Metrics aggregation algorithm: "linear" or "weighted" | "linear" |
| `maxScaleUpRate` | resource.Quantity | Maximum rate at which the autoscaler will scale up pods (must be > 1.0) | 1000.0 |
| `maxScaleDownRate` | resource.Quantity | Maximum rate at which the autoscaler will scale down pods (must be > 1.0) | 2.0 |
| `burstThreshold` | resource.Quantity | Threshold for entering burst mode (% of desired pod count) | 200 (200%) |
| `burstWindowPercentage` | resource.Quantity | Percentage of stable window used for burst mode calculations (1.0-100.0) | 10.0 |
| `stableWindow` | time.Duration | Time window over which metrics are averaged for scaling decisions (5s-600s) | 60s |
| `scaleDownDelay` | time.Duration | Minimum time that must pass at reduced load before scaling down | 0s |
| `activationScale` | int32 | Minimum scale to use when scaling from zero (must be >= 1) | 1 |
| `scaleToZeroGracePeriod` | time.Duration | Time to wait before scaling to zero after the service becomes idle | 30s |

## Development

### Project Structure

```
├── api/v1alpha1/          # CRD types and API definitions
├── bin/                   # Build output directory
├── cmd/                   # Application entrypoint
│   └── main.go           # Controller manager main
├── config/                # Kubernetes manifests
│   ├── crd/              # CustomResourceDefinition manifests
│   ├── default/          # Default configuration patches
│   ├── manager/          # Controller manager deployment
│   ├── network-policy/   # Network policies
│   ├── prometheus/       # Prometheus monitoring config
│   ├── rbac/             # RBAC roles and bindings
│   └── samples/          # Sample KPodAutoscaler resources
├── helm/                  
│   └── kpodautoscaler/   # Helm chart
├── internal/              # Private application code
│   ├── controller/       # Reconciliation logic
│   └── pkg/              # Internal packages
│       ├── metrics/      # Metrics collection and aggregation
│       ├── resourcerequests/ # Resource request calculations
│       └── scraper/      # User metrics scraping
├── scripts/               # Development and deployment scripts
├── test/                  # Test files
│   ├── e2e/              # End-to-end tests
│   ├── manifests/        # Test manifests
│   └── utils/            # Test utilities
├── Dockerfile             # Container image build
├── Makefile              # Build and development tasks
└── PROJECT               # Kubebuilder project metadata
```

### Building

```bash
# Generate code (CRDs, DeepCopy methods)
make generate

# Generate manifests (RBAC, CRDs)
make manifests

# Run tests
make test

# Build binary
make build

# Build docker image
make docker-build IMG=controller:latest
```

### Testing

#### Unit Tests

```bash
# Run all unit tests
make test

# Run specific package tests
go test ./internal/pkg/...

# Run with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

#### E2E Tests

The E2E tests use KIND (Kubernetes in Docker) to spin up a test cluster:

```bash
# Run E2E tests
cd test/e2e
go test -v ./...
```

The E2E tests will:
1. Create a KIND cluster
2. Install Metrics Server
3. Deploy mock Custom/External Metrics APIs
4. Create test deployments
5. Apply KPA resources
6. Validate scaling behavior

### Local Development

1. Install CRDs:
   ```bash
   make install
   ```

2. Run controller locally:
   ```bash
   make run
   ```

3. In another terminal, apply a KPA resource:
   ```bash
   kubectl apply -f config/samples/autoscaling_v1alpha1_kpodautoscaler.yaml
   ```

## Helm Chart

The Helm chart is located in `helm/kpodautoscaler/` and includes:

- CRD installation
- Controller deployment
- RBAC configuration
- ConfigMap for default settings
- Support for custom metrics intervals

### Values

Key helm values:

```yaml
replicaCount: 1
image:
  repository: ghcr.io/fedosin/kpodautoscaler
  tag: latest
  pullPolicy: IfNotPresent

resources:
  limits:
    cpu: 500m
    memory: 128Mi
  requests:
    cpu: 10m
    memory: 64Mi

# Controller settings
controller:
  metricsBindAddress: ":8080"
  healthProbeBindAddress: ":8081"
  leaderElection:
    enabled: true

# Metrics fetch interval (for all KPAs)
metrics:
  fetchInterval: 1s
```

## Monitoring

KPA exposes Prometheus metrics on `:8080/metrics`:

- `kpa_scaler_active`: Number of active scalers
- `kpa_scaling_decisions_total`: Total scaling decisions made
- `kpa_metric_fetch_duration_seconds`: Metric fetch duration histogram
- `kpa_scaling_errors_total`: Total errors during scaling

## Troubleshooting

### Check controller logs

```bash
kubectl logs -n kpa-system deployment/kpodautoscaler-controller-manager
```

### Check KPA status

```bash
kubectl describe kpa example-kpa
```

### Common issues

1. **Metrics not available**: Ensure Metrics Server is installed and running
2. **Custom metrics failing**: Verify Custom Metrics API is properly configured
3. **No scaling occurring**: Check MinReplicas/MaxReplicas bounds and metric targets

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- [libkpa](https://github.com/Fedosin/libkpa) for the advanced autoscaling algorithms
- Kubernetes HPA for the API design inspiration
- Controller-runtime for the excellent framework 