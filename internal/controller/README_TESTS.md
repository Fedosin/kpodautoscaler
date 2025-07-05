# KPodAutoscaler Controller Tests

This document describes the comprehensive test suite for the KPodAutoscaler controller.

## Overview

The test suite validates the core functionality of the KPodAutoscaler controller, including:
- Basic resource creation and deletion
- Support for both Deployments and StatefulSets
- Different metric types (CPU, Memory, Custom metrics)
- Min/Max replica limits
- Panic mode configuration
- Multiple metrics support

## Test Structure

The tests are organized using Ginkgo BDD-style testing framework and use envtest for creating a test Kubernetes environment.

## Test Coverage

### 1. Basic Resource Management
- **Test**: "should create and reconcile a KPodAutoscaler for a Deployment"
- **Test**: "should create and reconcile a KPodAutoscaler for a StatefulSet"
- **Coverage**: 
  - Creates KPodAutoscaler resources
  - Verifies correct spec configuration
  - Tests resource deletion

### 2. Metric Types
- **Test**: "should handle different metric types"
- **Coverage**:
  - Resource metrics (CPU, Memory)
  - Custom pod metrics
  - Verifies metric configuration is stored correctly

### 3. Scaling Limits
- **Test**: "should respect min and max replica limits in the spec"
- **Coverage**:
  - Validates min/max replica configuration
  - Ensures limits are properly stored

### 4. Panic Mode Configuration
- **Test**: "should handle panic mode configuration"
- **Coverage**:
  - Panic threshold settings
  - Panic window percentage
  - Scale up/down rates
  - Scale down delay

### 5. Multiple Metrics
- **Test**: "should handle multiple metrics configuration"
- **Coverage**:
  - Multiple metric types in single KPA
  - Combination of resource and custom metrics

## Running the Tests

```bash
# Run all controller tests
go test -v ./internal/controller/...

# Run with specific timeout
go test -v ./internal/controller/... -timeout 2m

# Run with coverage
go test -v ./internal/controller/... -cover
```

## Test Environment

The tests use:
- **envtest**: Provides a test Kubernetes API server
- **Ginkgo/Gomega**: BDD-style test framework
- **Unique namespaces**: Each test runs in isolation

## Future Enhancements

For more comprehensive testing with actual scaling behavior, consider:

1. **Mock Metrics Client**: Create a proper mock for the MetricsClient to test scaling decisions
2. **Integration Tests**: Test with actual metrics server
3. **E2E Tests**: Deploy to a real cluster with metrics-server
4. **Performance Tests**: Test scaling behavior under load

## Example: Testing Scaling Behavior with Mock Metrics

To test actual scaling behavior, you would need to:

1. Create an interface for MetricsClient
2. Implement a mock that returns controlled metric values
3. Test scaling decisions based on different metric values

Example approach:
```go
type MetricsInterface interface {
    GetResourceMetric(ctx context.Context, pods []corev1.Pod, resourceName corev1.ResourceName) ([]*resource.Quantity, error)
    // ... other methods
}
```

Then modify the controller to accept this interface instead of the concrete type.

## Notes

- The current tests focus on resource management and configuration validation
- Actual scaling behavior testing requires metrics mocking (see future enhancements)
- Tests run in isolated namespaces to prevent interference
- Each test creates its own deployment/statefulset targets 