# E2E Testing Guide for KPodAutoscaler

This guide provides instructions for running end-to-end (E2E) tests for the KPodAutoscaler operator.

## Quick Start

Run the complete E2E test suite with a single command:

```bash
make test-e2e
```

This command will:
1. Create a KinD cluster with 3 nodes
2. Install all required dependencies (metrics-server, Prometheus, KEDA, Redis)
3. Build and deploy the KPodAutoscaler operator
4. Run all E2E test scenarios
5. Clean up resources on completion

## Test Scenarios

The E2E test suite includes three comprehensive scenarios:

### 1. CPU-based Autoscaling
- Uses metrics-server for CPU metrics
- Tests scaling based on CPU utilization
- Validates scale-up when CPU load increases

### 2. Custom Metrics Autoscaling
- Uses Prometheus and Prometheus Adapter
- Tests scaling based on custom application metrics
- Validates metrics collection and scaling decisions

### 3. External Metrics Autoscaling
- Uses KEDA for external metrics
- Tests scaling based on Redis queue length
- Validates integration with external systems

## Prerequisites

Ensure you have the following tools installed:

```bash
# Check prerequisites
docker --version  # Docker 20.10+
kind --version    # KinD v0.25.0+
kubectl version   # kubectl v1.29+
helm version      # Helm v3.16+
go version        # Go 1.23+
```

## Running Tests

### Local Development

```bash
# Run full test suite
make test-e2e

# Run with existing cluster (skip creation)
SKIP_CLUSTER_CREATION=true scripts/e2e-local.sh

# Keep resources for debugging (skip cleanup)
SKIP_CLEANUP=true scripts/e2e-local.sh

# Run specific test file
go test ./test/e2e/e2e_test_scenarios.go -v -ginkgo.v
```

### Manual Steps

```bash
# 1. Create cluster
kind create cluster --name kpodautoscaler-e2e --config scripts/kind-config.yaml

# 2. Install dependencies
scripts/install-deps.sh

# 3. Build and deploy operator
make docker-build IMG=localhost/kpodautoscaler:e2e-test
make docker-load IMG=localhost/kpodautoscaler:e2e-test
make deploy-operator IMG=localhost/kpodautoscaler:e2e-test

# 4. Run tests
go test ./test/e2e/... -v -ginkgo.v -timeout 30m

# 5. Cleanup
scripts/cleanup-e2e.sh
```

## CI/CD Integration

The E2E tests run automatically in GitHub Actions:

- **Trigger**: Push/PR to main or release branches
- **Matrix**: Tests against Kubernetes v1.29, v1.30, v1.31
- **Artifacts**: Logs and debug info uploaded on failure

View the workflow: `.github/workflows/e2e-comprehensive.yml`

## Debugging

### View Logs

```bash
# Operator logs
kubectl logs -n kpodautoscaler-system -l control-plane=controller-manager

# Test namespace resources
kubectl get all -n e2e-test-*

# KPodAutoscaler status
kubectl get kpa -A
kubectl describe kpa <name> -n <namespace>
```

### Check Metrics

```bash
# Resource metrics
kubectl top nodes
kubectl top pods -A

# Custom metrics
kubectl get --raw /apis/custom.metrics.k8s.io/v1beta1

# External metrics  
kubectl get --raw /apis/external.metrics.k8s.io/v1beta1
```

### Access Prometheus

```bash
kubectl port-forward -n monitoring svc/prometheus-stack-kube-prom-prometheus 9090:9090
# Open http://localhost:9090
```

## Cleanup

Remove all E2E test resources:

```bash
# Interactive cleanup
scripts/cleanup-e2e.sh

# Force cleanup (no confirmation)
scripts/cleanup-e2e.sh --force
```

## Project Structure

```
kpodautoscaler/
├── scripts/
│   ├── install-deps.sh      # Install test dependencies
│   ├── e2e-local.sh         # Run E2E tests locally
│   ├── cleanup-e2e.sh       # Clean up test resources
│   └── kind-config.yaml     # KinD cluster configuration
├── test/e2e/
│   ├── e2e_suite_test.go    # Test suite setup
│   ├── e2e_test_scenarios.go # Test scenarios implementation
│   ├── helpers/             # Test helper utilities
│   └── README.md            # Detailed E2E documentation
├── .github/workflows/
│   └── e2e-comprehensive.yml # CI workflow
└── Makefile                 # Make targets for E2E tests
```

## Make Targets

```bash
make e2e-local      # Run complete E2E test suite
make e2e-deps       # Install E2E dependencies only
make docker-load    # Load image to KinD cluster
make deploy-operator # Deploy operator (alias for deploy)
```

## Troubleshooting

### Tests Fail to Start

1. Ensure Docker is running
2. Check KinD cluster is healthy: `kubectl cluster-info`
3. Verify dependencies installed: `kubectl get pods -A`

### Metrics Not Available

1. Wait 30-60 seconds for metrics collection
2. Check metrics-server: `kubectl top nodes`
3. Verify Prometheus targets: Port-forward and check UI

### Scaling Not Working

1. Check KPodAutoscaler conditions: `kubectl describe kpa`
2. View operator logs for errors
3. Verify metrics are reporting values

## Contributing

When adding new E2E tests:

1. Add test scenarios to `test/e2e/e2e_test_scenarios.go`
2. Use helpers from `test/e2e/helpers/helpers.go`
3. Follow existing patterns for resource creation and cleanup
4. Test locally before submitting PR
5. Update documentation as needed

For more details, see [test/e2e/README.md](test/e2e/README.md). 