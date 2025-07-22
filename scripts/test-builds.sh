#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color
YELLOW='\033[1;33m'
BLUE='\033[0;34m'

# Find all Go services (directories containing go.mod)
GO_SERVICES=$(find . -name "go.mod" -exec dirname {} \;)

# Function to test a single service
test_service() {
    local service_dir=$1
    local service_name=$(basename $service_dir)
    local log_file="/tmp/build_${service_name}.log"
    local status_file="/tmp/build_${service_name}.status"
    
    echo -e "${YELLOW}Testing ${service_name}...${NC}"
    
    # Run tests in sequence
    (
        cd $service_dir && \
        echo -e "${BLUE}[${service_name}]${NC} → Running go mod tidy..." && \
        go mod tidy >> $log_file 2>&1 && \
        echo -e "${BLUE}[${service_name}]${NC} → Running go build..." && \
        go build ./... >> $log_file 2>&1 && \
        if [ -f "Dockerfile" ]; then
            echo -e "${BLUE}[${service_name}]${NC} → Building Docker image..." && \
            cd .. && \
            docker build -t frameworks-${service_name}:test -f ${service_dir}/Dockerfile . >> $log_file 2>&1
        else
            echo -e "${BLUE}[${service_name}]${NC} → Skipping Docker build (no Dockerfile found)"
        fi && \
        echo -e "${GREEN}✓ ${service_name} passed all tests${NC}" && \
        echo "success" > $status_file \
    ) || (
        echo -e "${RED}✗ ${service_name} failed${NC}"
        echo -e "${RED}Last few lines of log:${NC}"
        tail -n 5 $log_file
        echo "failed" > $status_file
        return 1
    )
}

# Main execution
echo "Found services:"
for service in $GO_SERVICES; do
    echo -e "  - ${BLUE}$(basename $service)${NC}"
done
echo ""

# Run all tests in parallel
echo "Starting build tests..."
for service in $GO_SERVICES; do
    test_service $service &
done

# Wait for all background processes
wait

# Check if any failed
echo ""
echo "Build Test Summary:"
failed=0
failed_services=()

for service in $GO_SERVICES; do
    service_name=$(basename $service)
    status_file="/tmp/build_${service_name}.status"
    
    if [ -f "$status_file" ] && [ "$(cat $status_file)" = "success" ]; then
        echo -e "${GREEN}✓ ${service_name}${NC}"
    else
        echo -e "${RED}✗ ${service_name}${NC}"
        failed=1
        failed_services+=($service_name)
    fi
done

# Exit with status
if [ $failed -eq 0 ]; then
    echo -e "\n${GREEN}All builds passed!${NC}"
    exit 0
else
    echo -e "\n${RED}The following services failed:${NC}"
    for service in "${failed_services[@]}"; do
        echo -e "${RED}  - ${service}${NC}"
        echo -e "${YELLOW}Log file: /tmp/build_${service}.log${NC}"
    done
    exit 1
fi 