# KPodAutoscaler E2E Testing Framework

This directory contains a comprehensive end-to-end (E2E) testing framework for the KPodAutoscaler operator, built with Kubebuilder.

## Overview

The E2E testing framework provides automated testing for the KPodAutoscaler operator across different scenarios:

1. **CPU-based autoscaling** with metrics-server
2. **Custom metrics autoscaling** with Prometheus Adapter
3. **External metrics autoscaling** with KEDA

## Architecture

```
test/e2e/
├── README.md                    # This file
├── e2e_suite_test.go           # Test suite setup
├── e2e_test.go                 # Existing basic tests
├── e2e_test_scenarios.go       # Comprehensive test scenarios
├── helpers/                    # Test helper utilities
│   └── helpers.go
└── manifests/                  # Test manifests
```

## Prerequisites

Before running E2E tests, ensure you have the following installed:

- **Docker** - For running containers
- **KinD** (v0.25.0+) - For creating local Kubernetes clusters
- **kubectl** (v1.29+) - For interacting with Kubernetes
- **Helm** (v3.16+) - For installing dependencies
- **Go** (1.23+) - For running tests

## Dependencies

The E2E tests require the following components to be installed in the cluster:

| Component | Version | Purpose |
|-----------|---------|---------|
| metrics-server | 3.12.2 | Resource metrics (CPU/Memory) |
| kube-prometheus-stack | 75.7.0 | Prometheus for custom metrics |
| prometheus-adapter | 4.14.1 | Custom metrics API provider |
| KEDA | 2.17.2 | External metrics provider |
| Redis | 21.2.6 | External metrics source for testing |

## Running Tests Locally

### Quick Start

Run all E2E tests with a single command:

```bash
make test-e2e
```

### Step-by-Step Manual Execution

1. **Create KinD cluster:**
   ```bash
   kind create cluster --name kpodautoscaler-e2e --config scripts/kind-config.yaml
   ```

2. **Install dependencies:**
   ```bash
   scripts/install-deps.sh
   ```

3. **Build and load operator:**
   ```bash
   make docker-build IMG=localhost/kpodautoscaler:e2e-test
   kind load docker-image localhost/kpodautoscaler:e2e-test --name kpodautoscaler-e2e
   ```

4. **Deploy operator:**
   ```bash
   make install
   make deploy IMG=localhost/kpodautoscaler:e2e-test
   ```

5. **Run tests:**
   ```bash
   go test ./test/e2e/... -v -ginkgo.v -timeout 30m
   ```

### Environment Variables

Control test execution with these environment variables:

- `KIND_CLUSTER_NAME` - Name of the KinD cluster (default: `kpodautoscaler-e2e`)
- `SKIP_CLUSTER_CREATION` - Skip cluster creation if `true`
- `SKIP_DEPS_INSTALL` - Skip dependency installation if `true`
- `SKIP_CLEANUP` - Skip cleanup after tests if `true`

Example:
```bash
# Use existing cluster and skip cleanup for debugging
SKIP_CLUSTER_CREATION=true SKIP_CLEANUP=true scripts/e2e-local.sh
```

## Test Scenarios

### Scenario 1: CPU-based Autoscaling

Tests scaling based on CPU utilization using metrics-server.

**Flow:**
1. Creates a deployment with CPU requests (busybox load generator)
2. Creates KPodAutoscaler targeting 50% CPU utilization
3. Generates CPU load to trigger scale-up
4. Verifies deployment scales up when CPU exceeds threshold

### Scenario 2: Custom Metrics Autoscaling

Tests scaling based on custom metrics from Prometheus.

**Flow:**
1. Creates HTTP metrics exporter (node-exporter)
2. Configures ServiceMonitor for Prometheus scraping
3. Creates KPodAutoscaler using custom metric (node_load1)
4. Verifies scaling based on custom metric values

### Scenario 3: External Metrics Autoscaling

Tests scaling based on external metrics via KEDA.

**Flow:**
1. Creates Redis consumer deployment
2. Configures KEDA ScaledObject for Redis queue length
3. Creates KPodAutoscaler using external metrics
4. Adds items to Redis queue to trigger scale-up
5. Verifies deployment scales based on queue length

## CI/CD Integration

### GitHub Actions

The repository includes a GitHub Actions workflow (`.github/workflows/e2e-comprehensive.yml`) that:

- Runs tests across multiple Kubernetes versions (1.32, 1.33)
- Builds and tests the operator on each PR and push to main
- Collects and uploads artifacts on failure
- Provides a summary of test results

### Running in CI

Tests automatically run on:
- Push to `main` or `release-*` branches
- Pull requests targeting `main` or `release-*`
- Manual workflow dispatch

## Debugging Failed Tests

### Local Debugging

1. **Keep resources after failure:**
   ```bash
   SKIP_CLEANUP=true scripts/e2e-local.sh
   ```

2. **Check operator logs:**
   ```bash
   kubectl logs -n kpodautoscaler-system -l control-plane=controller-manager
   ```

3. **Check test namespaces:**
   ```bash
   kubectl get namespaces | grep e2e-test
   kubectl get all -n <test-namespace>
   ```

4. **Check KPodAutoscaler status:**
   ```bash
   kubectl get kpodautoscalers -A
   kubectl describe kpodautoscaler <name> -n <namespace>
   ```

### CI Debugging

Failed CI runs automatically collect and upload artifacts:

- Operator logs
- Kubernetes events
- Pod descriptions
- KPodAutoscaler resources
- Metrics data

Download artifacts from the GitHub Actions run page.

## Extending Tests

### Adding New Test Scenarios

1. **Create test in `e2e_test_scenarios.go`:**
   ```go
   Context("Scenario: Your New Test", func() {
       It("should do something", func() {
           // Test implementation
       })
   })
   ```

2. **Use helper functions from `helpers/helpers.go`:**
   ```go
   helper := helpers.NewE2ETestHelper(k8sClient, clientSet, restConfig, testID)
   err := helper.CreateTestNamespace(ctx)
   ```

3. **Follow the pattern:**
   - Create resources
   - Wait for ready state
   - Trigger scaling condition
   - Verify scaling behavior
   - Clean up (automatic via namespace deletion)

### Adding New Metrics Types

To test new metric types:

1. Update Prometheus Adapter configuration in `scripts/install-deps.sh`
2. Add new metric source in test scenarios
3. Configure appropriate metric provider (Prometheus, KEDA, etc.)

## Troubleshooting

### Common Issues

1. **Metrics not available:**
   - Wait longer for metrics to be scraped (30-60s)
   - Check ServiceMonitor is created correctly
   - Verify Prometheus is scraping the target

2. **Scaling not triggered:**
   - Check KPodAutoscaler conditions
   - Verify metric values are being reported
   - Check operator logs for errors

3. **Tests timeout:**
   - Increase timeout values in `helpers/helpers.go`
   - Check if cluster has sufficient resources
   - Verify all dependencies are running

### Useful Commands

```bash
# Check all metrics APIs
kubectl get apiservices | grep metrics

# Test metrics API directly
kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes
kubectl get --raw /apis/custom.metrics.k8s.io/v1beta1
kubectl get --raw /apis/external.metrics.k8s.io/v1beta1

# Check Prometheus targets
kubectl port-forward -n monitoring svc/prometheus-stack-kube-prom-prometheus 9090:9090
# Visit http://localhost:9090/targets

# Check KEDA metrics
kubectl get scaledobjects -A
kubectl get hpa -A
```

## Performance Considerations

- Tests create multiple namespaces and resources
- Each scenario runs in isolation with its own namespace
- Cleanup happens automatically after each test
- Total test execution time: ~15-20 minutes

## Contributing

When contributing to E2E tests:

1. Ensure tests are idempotent
2. Use unique resource names (include testID)
3. Always clean up resources
4. Add appropriate timeouts
5. Include helpful debug output on failures
6. Test locally before submitting PR

## References

- [Kubernetes E2E Testing Guide](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/e2e-tests.md)
- [Ginkgo Documentation](https://onsi.github.io/ginkgo/)
- [Gomega Matchers](https://onsi.github.io/gomega/)
- [KinD Documentation](https://kind.sigs.k8s.io/)
- [KEDA Documentation](https://keda.sh/) 
