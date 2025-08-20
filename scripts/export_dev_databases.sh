#!/bin/bash

# Script to export all development database data for analysis
# Exports both PostgreSQL and ClickHouse data to JSON format

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_FILE="$ROOT_DIR/database_export_$(date +%Y%m%d_%H%M%S).json"

echo "🔍 Exporting development databases for analysis..."
echo "Output file: $OUTPUT_FILE"

# Function to check if containers are running
check_containers() {
    if ! docker ps | grep -q frameworks-postgres; then
        echo "❌ PostgreSQL container not running. Please start docker-compose first."
        exit 1
    fi
    if ! docker ps | grep -q frameworks-clickhouse; then
        echo "❌ ClickHouse container not running. Please start docker-compose first."
        exit 1
    fi
}

# Check containers
check_containers

echo "📊 Exporting PostgreSQL data..."

# Start JSON output
cat > "$OUTPUT_FILE" << 'EOF'
{
  "export_timestamp": "TIMESTAMP_PLACEHOLDER",
  "postgres": {
EOF

# Replace timestamp
sed -i '' "s/TIMESTAMP_PLACEHOLDER/$(date -u +%Y-%m-%dT%H:%M:%SZ)/" "$OUTPUT_FILE"

# Dynamically get all PostgreSQL tables
POSTGRES_TABLES=$(docker exec frameworks-postgres psql -U frameworks_user -d frameworks -t -A -c \
    "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name;" 2>/dev/null)

# Convert to array, filtering empty lines
POSTGRES_TABLES_ARRAY=()
while IFS= read -r line; do
    [[ -n "$line" ]] && POSTGRES_TABLES_ARRAY+=("$line")
done <<< "$POSTGRES_TABLES"

echo "  Found ${#POSTGRES_TABLES_ARRAY[@]} PostgreSQL tables" >&2

FIRST_TABLE=true
for table in "${POSTGRES_TABLES_ARRAY[@]}"; do
    # Skip empty lines
    [ -z "$table" ] && continue
    
    if [ "$FIRST_TABLE" = false ]; then
        echo "," >> "$OUTPUT_FILE"
    fi
    FIRST_TABLE=false
    
    echo -n "    \"$table\": " >> "$OUTPUT_FILE"
    
    # Export table data as JSON array
    docker exec frameworks-postgres psql -U frameworks_user -d frameworks -t -A -c \
        "SELECT COALESCE(json_agg(row_to_json($table)), '[]'::json) FROM $table;" 2>/dev/null >> "$OUTPUT_FILE" || echo "[]" >> "$OUTPUT_FILE"
done

echo "" >> "$OUTPUT_FILE"
echo "  }," >> "$OUTPUT_FILE"

echo "📈 Exporting ClickHouse data..."

# Export ClickHouse tables
echo "  \"clickhouse\": {" >> "$OUTPUT_FILE"

# Dynamically get all ClickHouse tables
CLICKHOUSE_TABLES=$(docker exec frameworks-clickhouse clickhouse-client \
    --user frameworks --password frameworks_dev \
    --query "SHOW TABLES FROM frameworks" 2>/dev/null)

# Convert to array, filtering empty lines
CLICKHOUSE_TABLES=($(echo "$CLICKHOUSE_TABLES" | grep -v "^$"))

echo "  Found ${#CLICKHOUSE_TABLES[@]} ClickHouse tables" >&2

# Count records in each table first
echo "    \"record_counts\": {" >> "$OUTPUT_FILE"
FIRST_COUNT=true
for table in "${CLICKHOUSE_TABLES[@]}"; do
    if [ "$FIRST_COUNT" = false ]; then
        echo "," >> "$OUTPUT_FILE"
    fi
    FIRST_COUNT=false
    
    COUNT=$(docker exec frameworks-clickhouse clickhouse-client \
        --user frameworks --password frameworks_dev \
        --query "SELECT count(*) FROM frameworks.$table" 2>/dev/null || echo "0")
    
    echo -n "      \"$table\": $COUNT" >> "$OUTPUT_FILE"
done
echo "" >> "$OUTPUT_FILE"
echo "    }," >> "$OUTPUT_FILE"

# Export actual data from tables
echo "    \"data\": {" >> "$OUTPUT_FILE"

FIRST_TABLE=true
for table in "${CLICKHOUSE_TABLES[@]}"; do
    if [ "$FIRST_TABLE" = false ]; then
        echo "," >> "$OUTPUT_FILE"
    fi
    FIRST_TABLE=false
    
    echo "      \"$table\": " >> "$OUTPUT_FILE"
    
    # Export table data (limit to recent 1000 records for large tables)
    docker exec frameworks-clickhouse clickhouse-client \
        --user frameworks --password frameworks_dev \
        --format JSONEachRow \
        --query "SELECT * FROM frameworks.$table ORDER BY timestamp DESC LIMIT 1000" 2>/dev/null | \
        jq -s '.' >> "$OUTPUT_FILE" 2>/dev/null || echo "[]" >> "$OUTPUT_FILE"
done

echo "" >> "$OUTPUT_FILE"
echo "    }" >> "$OUTPUT_FILE"
echo "  }," >> "$OUTPUT_FILE"

echo "📋 Generating analysis summary..."

# Add analysis summary
echo "  \"analysis\": {" >> "$OUTPUT_FILE"

# Check for null/empty fields in PostgreSQL
echo "    \"postgres_null_analysis\": {" >> "$OUTPUT_FILE"

# Analyze streams table for null fields
STREAM_NULLS=$(docker exec frameworks-postgres psql -U frameworks_user -d frameworks -t -A -c "
    SELECT json_build_object(
        'total_streams', COUNT(*),
        'streams_with_null_tenant', COUNT(*) FILTER (WHERE tenant_id IS NULL),
        'streams_with_null_user', COUNT(*) FILTER (WHERE user_id IS NULL),
        'streams_with_null_status', COUNT(*) FILTER (WHERE status IS NULL),
        'streams_with_null_title', COUNT(*) FILTER (WHERE title IS NULL OR title = ''),
        'streams_with_null_internal_name', COUNT(*) FILTER (WHERE internal_name IS NULL)
    ) FROM streams;" 2>/dev/null || echo "{}")

echo "      \"streams\": $STREAM_NULLS," >> "$OUTPUT_FILE"

# Analyze stream_analytics table
ANALYTICS_NULLS=$(docker exec frameworks-postgres psql -U frameworks_user -d frameworks -t -A -c "
    SELECT json_build_object(
        'total_records', COUNT(*),
        'null_tenant_id', COUNT(*) FILTER (WHERE tenant_id IS NULL),
        'null_stream_id', COUNT(*) FILTER (WHERE stream_id IS NULL),
        'null_viewer_count', COUNT(*) FILTER (WHERE current_viewers IS NULL),
        'null_bitrate', COUNT(*) FILTER (WHERE avg_bitrate IS NULL),
        'null_node_id', COUNT(*) FILTER (WHERE primary_node_id IS NULL)
    ) FROM stream_analytics;" 2>/dev/null || echo "{}")

echo "      \"stream_analytics\": $ANALYTICS_NULLS" >> "$OUTPUT_FILE"
echo "    }," >> "$OUTPUT_FILE"

# Check data integrity
echo "    \"data_integrity\": {" >> "$OUTPUT_FILE"

# Check for orphaned records
ORPHANED_ANALYTICS=$(docker exec frameworks-postgres psql -U frameworks_user -d frameworks -t -A -c "
    SELECT COUNT(*) FROM stream_analytics sa
    LEFT JOIN streams s ON sa.stream_id = s.id
    WHERE s.id IS NULL;" 2>/dev/null || echo "0")

echo "      \"orphaned_stream_analytics\": $ORPHANED_ANALYTICS," >> "$OUTPUT_FILE"

# Check for missing tenant associations
MISSING_TENANTS=$(docker exec frameworks-postgres psql -U frameworks_user -d frameworks -t -A -c "
    SELECT json_build_object(
        'streams_without_tenant', (SELECT COUNT(*) FROM streams WHERE tenant_id IS NULL OR tenant_id = '00000000-0000-0000-0000-000000000000'),
        'analytics_without_tenant', (SELECT COUNT(*) FROM stream_analytics WHERE tenant_id IS NULL OR tenant_id = '00000000-0000-0000-0000-000000000000')
    );" 2>/dev/null || echo "{}")

echo "      \"missing_tenant_associations\": $MISSING_TENANTS" >> "$OUTPUT_FILE"
echo "    }," >> "$OUTPUT_FILE"

# ClickHouse data quality
echo "    \"clickhouse_data_quality\": {" >> "$OUTPUT_FILE"

# Check for events with missing tenant_id
EVENTS_NO_TENANT=$(docker exec frameworks-clickhouse clickhouse-client \
    --user frameworks --password frameworks_dev \
    --query "SELECT count(*) FROM frameworks.stream_events WHERE tenant_id = '' OR tenant_id IS NULL" 2>/dev/null || echo "0")

echo "      \"stream_events_without_tenant\": $EVENTS_NO_TENANT," >> "$OUTPUT_FILE"

# Check for health metrics with missing data
HEALTH_MISSING=$(docker exec frameworks-clickhouse clickhouse-client \
    --user frameworks --password frameworks_dev \
    --query "SELECT count(*) FROM frameworks.stream_health_metrics WHERE internal_name = '' OR bitrate = 0" 2>/dev/null || echo "0")

echo "      \"health_metrics_incomplete\": $HEALTH_MISSING," >> "$OUTPUT_FILE"

# Get recent event types distribution
echo "      \"recent_event_types\": " >> "$OUTPUT_FILE"
docker exec frameworks-clickhouse clickhouse-client \
    --user frameworks --password frameworks_dev \
    --format JSON \
    --query "SELECT event_type, count(*) as count FROM frameworks.stream_events GROUP BY event_type ORDER BY count DESC" 2>/dev/null | \
    jq -c '.data' >> "$OUTPUT_FILE" 2>/dev/null || echo "[]" >> "$OUTPUT_FILE"

echo "    }" >> "$OUTPUT_FILE"
echo "  }" >> "$OUTPUT_FILE"
echo "}" >> "$OUTPUT_FILE"

# Pretty-print the JSON
if command -v jq &> /dev/null; then
    jq '.' "$OUTPUT_FILE" > "${OUTPUT_FILE}.tmp" && mv "${OUTPUT_FILE}.tmp" "$OUTPUT_FILE"
fi

echo "✅ Export complete!"
echo "📁 Output saved to: $OUTPUT_FILE"
echo ""
echo "📊 Quick Summary:"
echo "  - PostgreSQL tables exported: ${#POSTGRES_TABLES_ARRAY[@]}"
echo "  - ClickHouse tables exported: ${#CLICKHOUSE_TABLES[@]}"

# Show record counts
echo ""
echo "📈 ClickHouse Record Counts:"
for table in "${CLICKHOUSE_TABLES[@]}"; do
    COUNT=$(docker exec frameworks-clickhouse clickhouse-client \
        --user frameworks --password frameworks_dev \
        --query "SELECT count(*) FROM frameworks.$table" 2>/dev/null || echo "0")
    if [ "$COUNT" -gt 0 ]; then
        echo "  - $table: $COUNT records"
    fi
done

echo ""
echo "💡 Tip: Use 'jq' to analyze the exported data:"
echo "  cat $OUTPUT_FILE | jq '.postgres.streams'"
echo "  cat $OUTPUT_FILE | jq '.clickhouse.record_counts'"
echo "  cat $OUTPUT_FILE | jq '.analysis'"