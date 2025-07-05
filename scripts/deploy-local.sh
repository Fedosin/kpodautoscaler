#!/bin/bash
set -euo pipefail

# Configuration
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kpodautoscaler-local}"
KIND_CONFIG="${SCRIPT_DIR}/kind-config.yaml"
SKIP_CLUSTER_CREATION="${SKIP_CLUSTER_CREATION:-false}"
DEPS_INSTALL="${DEPS_INSTALL:-false}"
SKIP_CLEANUP="${SKIP_CLEANUP:-false}"

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
    exit 1
}

log_section() {
    echo -e "\n${BLUE}=== $1 ===${NC}\n"
}

# Check prerequisites
check_prerequisites() {
    log_section "Checking Prerequisites"
    
    # Check for required tools
    local required_tools=("docker" "kind" "kubectl" "helm" "go")
    for tool in "${required_tools[@]}"; do
        if ! command -v "$tool" &> /dev/null; then
            log_error "$tool is not installed. Please install $tool first."
        fi
        log_info "✓ $tool is installed"
    done
    
    # Check Docker is running
    if ! docker info &> /dev/null; then
        log_error "Docker is not running. Please start Docker."
    fi
    log_info "✓ Docker is running"
}

# Create KinD cluster
create_kind_cluster() {
    if [[ "$SKIP_CLUSTER_CREATION" == "true" ]]; then
        log_info "Skipping cluster creation (SKIP_CLUSTER_CREATION=true)"
        return
    fi
    
    log_section "Creating KinD Cluster"
    
    # Check if cluster already exists
    if kind get clusters | grep -q "^${KIND_CLUSTER_NAME}$"; then
        log_warn "Cluster '${KIND_CLUSTER_NAME}' already exists"
        read -p "Do you want to delete and recreate it? (y/N) " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            log_info "Deleting existing cluster..."
            kind delete cluster --name "${KIND_CLUSTER_NAME}"
        else
            log_info "Using existing cluster"
            return
        fi
    fi
    
    # Create cluster
    log_info "Creating KinD cluster '${KIND_CLUSTER_NAME}'..."
    kind create cluster \
        --name "${KIND_CLUSTER_NAME}" \
        --config "${KIND_CONFIG}" \
        --wait 5m
    
    # Set kubectl context
    kubectl cluster-info --context "kind-${KIND_CLUSTER_NAME}"
    
    log_info "KinD cluster created successfully"
}

# Install dependencies
install_dependencies() {
    if [[ "$DEPS_INSTALL" == "false" ]]; then
        log_info "Skipping dependencies installation (DEPS_INSTALL=false)"
        return
    fi
    
    log_section "Installing Dependencies"
    
    # Run the install-deps.sh script
    "${SCRIPT_DIR}/install-deps.sh"
}

# Build and load operator image
build_and_load_operator() {
    log_section "Building and Loading Operator Image"
    
    cd "${PROJECT_ROOT}"
    
    # Build the operator image
    log_info "Building operator image..."
    make docker-build IMG=localhost/kpodautoscaler:local-test
    
    # Load image into KinD cluster
    log_info "Loading operator image into KinD cluster..."
    docker save localhost/kpodautoscaler:local-test -o kpodautoscaler.tar
    kind load image-archive kpodautoscaler.tar --name "${KIND_CLUSTER_NAME}"
    rm kpodautoscaler.tar
}

# Deploy operator
deploy_operator() {
    log_section "Deploying Operator"
    
    cd "${PROJECT_ROOT}"
    
    # Install CRDs
    log_info "Installing CRDs..."
    make install
    
    # Wait for CRD to be established
    log_info "Waiting for CRD to be established..."
    kubectl wait --for condition=established --timeout=60s crd/kpodautoscalers.autoscaling.kpodautoscaler.io
    
    # Deploy operator
    log_info "Deploying operator..."
    make deploy IMG=localhost/kpodautoscaler:local-test
    
    # Wait for operator to be ready
    log_info "Waiting for operator to be ready..."
    kubectl wait --for=condition=ready pod \
        -l control-plane=controller-manager \
        -n kpodautoscaler-system \
        --timeout=300s
    
    log_info "Operator deployed successfully"
}

# Collect logs and artifacts
collect_artifacts() {
    log_section "Collecting Artifacts"
    
    local artifacts_dir="${PROJECT_ROOT}/local-artifacts"
    mkdir -p "${artifacts_dir}"
    
    # Collect operator logs
    log_info "Collecting operator logs..."
    kubectl logs -n kpodautoscaler-system -l control-plane=controller-manager --all-containers=true > "${artifacts_dir}/operator-logs.txt" 2>&1 || true
    
    # Collect events
    log_info "Collecting Kubernetes events..."
    kubectl get events --all-namespaces --sort-by='.lastTimestamp' > "${artifacts_dir}/events.txt" 2>&1 || true
    
    # Collect pod descriptions
    log_info "Collecting pod descriptions..."
    kubectl describe pods --all-namespaces > "${artifacts_dir}/pod-descriptions.txt" 2>&1 || true
    
    # Collect KPodAutoscaler resources
    log_info "Collecting KPodAutoscaler resources..."
    kubectl get kpodautoscalers --all-namespaces -o yaml > "${artifacts_dir}/kpodautoscalers.yaml" 2>&1 || true
    
    # Collect metrics
    log_info "Collecting metrics information..."
    kubectl top nodes > "${artifacts_dir}/node-metrics.txt" 2>&1 || true
    kubectl top pods --all-namespaces > "${artifacts_dir}/pod-metrics.txt" 2>&1 || true
    
    # Collect custom and external metrics
    kubectl get --raw /apis/custom.metrics.k8s.io/v1beta1 > "${artifacts_dir}/custom-metrics.json" 2>&1 || true
    kubectl get --raw /apis/external.metrics.k8s.io/v1beta1 > "${artifacts_dir}/external-metrics.json" 2>&1 || true
    
    log_info "Artifacts collected in ${artifacts_dir}"
}

# Cleanup function
cleanup() {
    if [[ "$SKIP_CLEANUP" == "true" ]]; then
        log_info "Skipping cleanup (SKIP_CLEANUP=true)"
        return
    fi
    
    log_section "Cleanup"
    
    # Undeploy operator
    log_info "Undeploying operator..."
    cd "${PROJECT_ROOT}"
    make undeploy || true
    
    # Delete KinD cluster
    if [[ "$SKIP_CLUSTER_CREATION" != "true" ]]; then
        log_info "Deleting KinD cluster..."
        kind delete cluster --name "${KIND_CLUSTER_NAME}" || true
    fi
}

# Main execution
main() {
    log_section "KPodAutoscaler local deployer"
    log_info "Project root: ${PROJECT_ROOT}"
    log_info "KinD cluster name: ${KIND_CLUSTER_NAME}"
    
    # Execute steps
    check_prerequisites
    create_kind_cluster
    install_dependencies
    build_and_load_operator
    deploy_operator
}

# Print usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -h, --help              Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  KIND_CLUSTER_NAME       Name of the KinD cluster (default: kpodautoscaler-local)"
    echo "  SKIP_CLUSTER_CREATION   Skip KinD cluster creation if set to 'true'"
    echo "  DEPS_INSTALL            Install dependencies if set to 'true' (default: false)"
    echo "  SKIP_CLEANUP            Skip cleanup on exit if set to 'true'"
    echo ""
    echo "Examples:"
    echo "  # Deploy operator to KinD cluster"
    echo "  $0"
    echo ""
    echo "  # Skip cluster creation (use existing cluster)"
    echo "  SKIP_CLUSTER_CREATION=true $0"
    echo ""
    echo "  # Skip cleanup (keep cluster and resources for debugging)"
    echo "  SKIP_CLEANUP=true $0"
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
    shift
done

# Run main function
main
