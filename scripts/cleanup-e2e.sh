#!/bin/bash
set -euo pipefail

# Configuration
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kpodautoscaler-e2e}"

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
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
}

log_section() {
    echo -e "\n${BLUE}=== $1 ===${NC}\n"
}

# Cleanup E2E test namespaces
cleanup_test_namespaces() {
    log_section "Cleaning up test namespaces"
    
    # Find all E2E test namespaces
    namespaces=$(kubectl get namespaces -o json | jq -r '.items[] | select(.metadata.name | startswith("e2e-test-")) | .metadata.name')
    
    if [ -z "$namespaces" ]; then
        log_info "No E2E test namespaces found"
    else
        for ns in $namespaces; do
            log_info "Deleting namespace: $ns"
            kubectl delete namespace "$ns" --wait=false || true
        done
        
        log_info "Waiting for namespace deletion..."
        for ns in $namespaces; do
            kubectl wait --for=delete namespace/"$ns" --timeout=120s || true
        done
    fi
}

# Cleanup operator deployment
cleanup_operator() {
    log_section "Cleaning up operator deployment"
    
    cd "$PROJECT_ROOT"
    
    # Undeploy operator
    if kubectl get namespace kpodautoscaler-system &> /dev/null; then
        log_info "Undeploying operator..."
        make undeploy || true
    else
        log_info "Operator namespace not found"
    fi
    
    # Uninstall CRDs
    log_info "Uninstalling CRDs..."
    make uninstall || true
}

# Cleanup Helm releases
cleanup_helm_releases() {
    log_section "Cleaning up Helm releases"
    
    # List of Helm releases to uninstall
    releases=(
        "redis:redis"
        "keda:keda"
        "prometheus-adapter:monitoring"
        "prometheus-stack:monitoring"
        "metrics-server:kube-system"
    )
    
    for release_ns in "${releases[@]}"; do
        release="${release_ns%%:*}"
        namespace="${release_ns##*:}"
        
        if helm list -n "$namespace" | grep -q "^$release"; then
            log_info "Uninstalling Helm release: $release (namespace: $namespace)"
            helm uninstall "$release" -n "$namespace" || true
        fi
    done
    
    # Delete custom namespaces
    for ns in monitoring keda redis; do
        if kubectl get namespace "$ns" &> /dev/null; then
            log_info "Deleting namespace: $ns"
            kubectl delete namespace "$ns" --wait=false || true
        fi
    done
}

# Delete KinD cluster
delete_kind_cluster() {
    log_section "Deleting KinD cluster"
    
    if kind get clusters | grep -q "^${KIND_CLUSTER_NAME}$"; then
        log_info "Deleting KinD cluster: ${KIND_CLUSTER_NAME}"
        kind delete cluster --name "${KIND_CLUSTER_NAME}"
    else
        log_info "KinD cluster '${KIND_CLUSTER_NAME}' not found"
    fi
}

# Clean up local artifacts
cleanup_artifacts() {
    log_section "Cleaning up local artifacts"
    
    cd "$PROJECT_ROOT"
    
    # Remove E2E artifacts directory
    if [ -d "e2e-artifacts" ]; then
        log_info "Removing e2e-artifacts directory"
        rm -rf e2e-artifacts
    fi
    
    # Remove temporary files
    rm -f /tmp/metrics-server-values.yaml
    rm -f /tmp/prometheus-stack-values.yaml
    rm -f /tmp/prometheus-adapter-values.yaml
    rm -f /tmp/keda-values.yaml
    rm -f /tmp/redis-values.yaml
}

# Main cleanup function
main() {
    log_section "E2E Test Cleanup"
    
    # Check if cluster exists
    if ! kubectl cluster-info &> /dev/null; then
        log_warn "No active Kubernetes cluster found"
        delete_kind_cluster
        cleanup_artifacts
        exit 0
    fi
    
    # Confirm cleanup
    echo -e "${YELLOW}This will delete all E2E test resources including:${NC}"
    echo "- All e2e-test-* namespaces"
    echo "- KPodAutoscaler operator"
    echo "- All Helm releases (metrics-server, Prometheus, KEDA, Redis)"
    echo "- KinD cluster: ${KIND_CLUSTER_NAME}"
    echo ""
    read -p "Are you sure you want to continue? (y/N) " -n 1 -r
    echo
    
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log_info "Cleanup cancelled"
        exit 0
    fi
    
    # Perform cleanup
    cleanup_test_namespaces
    cleanup_operator
    cleanup_helm_releases
    delete_kind_cluster
    cleanup_artifacts
    
    log_section "Cleanup Complete!"
    log_info "All E2E test resources have been removed"
}

# Parse command line arguments
FORCE=false
while [[ $# -gt 0 ]]; do
    case $1 in
        -f|--force)
            FORCE=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  -f, --force     Skip confirmation prompt"
            echo "  -h, --help      Show this help message"
            echo ""
            echo "Environment Variables:"
            echo "  KIND_CLUSTER_NAME    Name of the KinD cluster (default: kpodautoscaler-e2e)"
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Skip confirmation if force flag is set
if [ "$FORCE" = true ]; then
    cleanup_test_namespaces
    cleanup_operator
    cleanup_helm_releases
    delete_kind_cluster
    cleanup_artifacts
    log_section "Cleanup Complete!"
else
    main
fi 