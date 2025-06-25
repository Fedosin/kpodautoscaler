# K Pod Autoscaler (KPA)

A Kubernetes controller that provides advanced pod autoscaling using algorithms from [libkpa](https://github.com/Fedosin/libkpa). KPA is compatible with the HorizontalPodAutoscaler CRD pattern but uses more sophisticated scaling algorithms including sliding window and weighted time window approaches.

## Features

- **Advanced Scaling Algorithms**: Uses libkpa's sliding window algorithms for more stable and predictable scaling
- **Multiple Metric Support**: Supports Resource, Pods, Object, and External metrics
- **Per-Metric Configuration**: Each metric can have its own window size, panic threshold, and scaling rates
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
      algorithm: "linear"  # or "weighted"
      windowSize: 60s
      panicWindow: 6s
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
      algorithm: "weighted"
      windowSize: 120s
      panicWindow: 10s
      scaleUpRate: 2.0
      scaleDownRate: 0.5
      panicThreshold: 200.0
  
  # Memory metric
  - type: Resource
    resource:
      name: memory
      target:
        type: AverageValue
        averageValue: "1Gi"
    config:
      windowSize: 90s
  
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
      algorithm: "linear"
      windowSize: 60s
      targetUtilization: 80.0
  
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
      windowSize: 120s
      panicThreshold: 150.0
```

## Configuration

### MetricConfig Options

Each metric can have a `config` section with the following options:

| Field | Description | Default |
|-------|-------------|---------|
| `algorithm` | Scaling algorithm: "linear" or "weighted" | "linear" |
| `windowSize` | Time window for stable metrics | 60s |
| `panicWindow` | Time window for panic mode metrics | 6s (10% of windowSize) |
| `scaleUpRate` | Maximum scale up rate | 1000.0 |
| `scaleDownRate` | Maximum scale down rate | 2.0 |
| `maxScaleUpRate` | Absolute maximum scale up rate | 1000.0 |
| `maxScaleDownRate` | Absolute maximum scale down rate | 2.0 |
| `panicThreshold` | Threshold for entering panic mode (% of target) | 200.0 |
| `stableWindow` | Window size for stable metrics | 60s |
| `initialScale` | Initial scale when creating autoscaler | 1 |
| `targetUtilization` | Target utilization percentage | Based on metric target |

## Development

### Project Structure

```
├── api/v1alpha1/          # CRD types
├── controllers/           # Reconciliation logic
├── cmd/manager/          # Controller entrypoint
├── pkg/
│   ├── metrics/          # Metrics API wrappers
│   └── libkpa-integration/ # libkpa integration
├── charts/kpodautoscaler/ # Helm chart
└── tests/
    ├── unit/             # Unit tests
    └── e2e/              # End-to-end tests
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
go test ./pkg/libkpa-integration/...

# Run with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

#### E2E Tests

The E2E tests use KIND (Kubernetes in Docker) to spin up a test cluster:

```bash
# Run E2E tests
cd tests/e2e
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

The Helm chart is located in `charts/kpodautoscaler/` and includes:

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