#!/bin/bash

# FrameWorks Docker Image Build Script
# Builds and pushes all FrameWorks services to Docker registry

set -e  # Exit on any error

# Configuration
DOCKER_REGISTRY=${DOCKER_REGISTRY:-frameworks}
VERSION=${VERSION:-latest}

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v docker &> /dev/null; then
        log_error "Docker is not installed or not in PATH"
        exit 1
    fi
    
    if ! docker info &> /dev/null; then
        log_error "Docker daemon is not running"
        exit 1
    fi
    
    # Check if logged into registry
    if [[ "$DOCKER_REGISTRY" != "frameworks" ]]; then
        log_info "Testing registry access: $DOCKER_REGISTRY"
        if ! docker pull alpine:latest &> /dev/null; then
            log_warn "Cannot pull from registry. Make sure you're logged in: docker login $DOCKER_REGISTRY"
        fi
    fi
    
    log_success "Prerequisites check passed"
}

# Build and push a single service
build_service() {
    local service_name=$1
    local dockerfile_path=$2
    local context_path=${3:-.}
    
    local image_name="${DOCKER_REGISTRY}/${service_name}:${VERSION}"
    local latest_tag="${DOCKER_REGISTRY}/${service_name}:latest"
    
    log_info "Building $service_name..."
    log_info "Image: $image_name"
    log_info "Context: $context_path"
    log_info "Dockerfile: $dockerfile_path"
    
    # Build the image
    if docker build \
        -t "$image_name" \
        -t "$latest_tag" \
        -f "$dockerfile_path" \
        "$context_path"; then
        log_success "Built $service_name"
    else
        log_error "Failed to build $service_name"
        return 1
    fi
    
    # Push the image
    if [[ "$DOCKER_REGISTRY" != "frameworks" ]]; then
        log_info "Pushing $image_name..."
        if docker push "$image_name"; then
            log_success "Pushed $image_name"
        else
            log_error "Failed to push $image_name"
            return 1
        fi
        
        log_info "Pushing $latest_tag..."
        if docker push "$latest_tag"; then
            log_success "Pushed $latest_tag"
        else
            log_error "Failed to push $latest_tag"
            return 1
        fi
    else
        log_warn "Skipping push for local registry 'frameworks'"
    fi
}

# Main build function
main() {
    echo "=================================="
    echo "  FrameWorks Docker Image Build"
    echo "=================================="
    echo "Registry: $DOCKER_REGISTRY"
    echo "Version: $VERSION"
    echo "=================================="
    
    check_prerequisites
    
    # Define services to build
    # Format: "service_name:dockerfile_path:context_path"
    local services=(
        "bridge:api_gateway/Dockerfile"
        "foghorn:api_balancing/Dockerfile"
        "commodore:api_control/Dockerfile"
        "quartermaster:api_tenants/Dockerfile"
        "purser:api_billing/Dockerfile"
        "periscope-query:api_analytics_query/Dockerfile"
        "periscope-ingest:api_analytics_ingest/Dockerfile"
        "helmsman:api_sidecar/Dockerfile"
        "decklog:api_firehose/Dockerfile"
        "signalman:api_realtime/Dockerfile"
        "frontend:website_application/Dockerfile:website_application"
        "website:website_marketing/Dockerfile:website_marketing"
    )
    
    local failed_services=()
    local total_services=${#services[@]}
    local current=0
    
    # Build each service
    for service_def in "${services[@]}"; do
        current=$((current + 1))
        
        # Parse service definition
        IFS=':' read -r service_name dockerfile_path context_path <<< "$service_def"
        if [[ -z "$context_path" ]]; then
            context_path="."
        fi
        
        echo ""
        log_info "Building service $current/$total_services: $service_name"
        
        if build_service "$service_name" "$dockerfile_path" "$context_path"; then
            log_success "✓ $service_name completed"
        else
            log_error "✗ $service_name failed"
            failed_services+=("$service_name")
        fi
    done
    
    # Summary
    echo ""
    echo "=================================="
    echo "           BUILD SUMMARY"
    echo "=================================="
    
    if [[ ${#failed_services[@]} -eq 0 ]]; then
        log_success "All services built successfully!"
        echo "Registry: $DOCKER_REGISTRY"
        echo "Version: $VERSION"
        echo "Services: $total_services"
    else
        log_error "Some services failed to build:"
        for service in "${failed_services[@]}"; do
            echo "  - $service"
        done
        echo ""
        log_info "Successful: $((total_services - ${#failed_services[@]}))/$total_services"
        exit 1
    fi
}

# Help function
show_help() {
    echo "FrameWorks Docker Image Build Script"
    echo ""
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Environment Variables:"
    echo "  DOCKER_REGISTRY    Docker registry URL (default: frameworks)"
    echo "  VERSION            Image version tag (default: latest)"
    echo ""
    echo "Options:"
    echo "  -h, --help         Show this help message"
    echo "  --dry-run          Show what would be built without building"
    echo ""
    echo "Examples:"
    echo "  ./build-images.sh"
    echo "  DOCKER_REGISTRY=myregistry.com/frameworks VERSION=v1.0.0 ./build-images.sh"
    echo "  DOCKER_REGISTRY=localhost:5000/frameworks ./build-images.sh"
}

# Dry run function
dry_run() {
    echo "=================================="
    echo "        DRY RUN MODE"
    echo "=================================="
    echo "Registry: $DOCKER_REGISTRY"
    echo "Version: $VERSION"
    echo ""
    echo "Would build the following images:"
    
    local services=(
        "foghorn" "commodore" "quartermaster" "purser"
        "periscope-query" "periscope-ingest" "helmsman"
        "decklog" "signalman" "frontend" "website"
    )
    
    for service in "${services[@]}"; do
        echo "  - ${DOCKER_REGISTRY}/${service}:${VERSION}"
        echo "  - ${DOCKER_REGISTRY}/${service}:latest"
    done
    
    echo ""
    echo "Total: ${#services[@]} services"
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            show_help
            exit 0
            ;;
        --dry-run)
            dry_run
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Run main function
main