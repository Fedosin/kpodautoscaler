#!/bin/bash

# KProxy Test Script
# This script helps test the KProxy controller functionality

set -e

echo "=== KProxy Controller Test Script ==="
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to check if resource exists
check_resource() {
    local resource=$1
    local name=$2
    local namespace=${3:-default}
    
    if kubectl get $resource $name -n $namespace &> /dev/null; then
        echo -e "${GREEN}✓${NC} $resource/$name exists"
        return 0
    else
        echo -e "${RED}✗${NC} $resource/$name not found"
        return 1
    fi
}

# Function to wait for deployment to be ready
wait_for_deployment() {
    local name=$1
    local namespace=${2:-default}
    local timeout=${3:-60}
    
    echo "Waiting for deployment/$name to be ready..."
    if kubectl wait --for=condition=available --timeout=${timeout}s deployment/$name -n $namespace; then
        echo -e "${GREEN}✓${NC} Deployment $name is ready"
        return 0
    else
        echo -e "${RED}✗${NC} Deployment $name failed to become ready"
        return 1
    fi
}

echo "1. Checking if CRD is installed..."
if check_resource crd kproxies.autoscaling.kpodautoscaler.io; then
    echo "   CRD is installed"
else
    echo "   Installing CRD..."
    kubectl apply -f config/crd/bases/autoscaling.kpodautoscaler.io_kproxies.yaml
fi

echo ""
echo "2. Creating test web deployment..."
kubectl apply -f test/manifests/test-web-deployment.yaml
wait_for_deployment web

echo ""
echo "3. Creating KProxy resource..."
kubectl apply -f config/samples/autoscaling_v1alpha1_kproxy.yaml

echo ""
echo "4. Waiting for KProxy resources to be created..."
sleep 5

echo ""
echo "5. Checking created resources..."
check_resource service web-internal
check_resource service web-kproxy
check_resource configmap web-kproxy-envoy
check_resource deployment web-kproxy-kproxy

echo ""
echo "6. Checking KProxy status..."
kubectl get kproxy web-kproxy -o jsonpath='{.status}' | jq '.' 2>/dev/null || kubectl get kproxy web-kproxy -o yaml | grep -A 20 "^status:"

echo ""
echo "7. Waiting for Envoy deployment to be ready..."
wait_for_deployment web-kproxy-kproxy

echo ""
echo "8. Testing the proxy (port-forwarding)..."
echo "   Starting port-forward to service/web-kproxy..."
kubectl port-forward svc/web-kproxy 8080:80 &
PF_PID=$!
sleep 3

echo "   Testing HTTP request through proxy..."
if curl -s http://localhost:8080 | grep -q "Hello from pod"; then
    echo -e "${GREEN}✓${NC} Proxy is working! Response received from backend pod"
else
    echo -e "${RED}✗${NC} Failed to get response through proxy"
fi

# Kill port-forward
kill $PF_PID 2>/dev/null || true
wait $PF_PID 2>/dev/null || true

echo ""
echo "9. Checking Envoy admin interface..."
kubectl port-forward deploy/web-kproxy-kproxy 9901:9901 &
PF_PID=$!
sleep 3

echo "   Checking /ready endpoint..."
if curl -s http://localhost:9901/ready | grep -q "LIVE"; then
    echo -e "${GREEN}✓${NC} Envoy admin is accessible and ready"
else
    echo -e "${RED}✗${NC} Envoy admin not responding correctly"
fi

echo "   Checking cluster status..."
curl -s http://localhost:9901/clusters | head -5

# Kill port-forward
kill $PF_PID 2>/dev/null || true
wait $PF_PID 2>/dev/null || true

echo ""
echo "=== Test Summary ==="
echo "KProxy controller has created all expected resources."
echo "To clean up test resources, run:"
echo "  kubectl delete kproxy web-kproxy"
echo "  kubectl delete deployment web"
echo ""
echo "To monitor the proxy:"
echo "  kubectl logs -f deploy/web-kproxy-kproxy"
echo "  kubectl port-forward deploy/web-kproxy-kproxy 9901:9901"
echo "  curl http://localhost:9901/stats/prometheus"
