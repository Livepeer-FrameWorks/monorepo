# Kafka Schema Registry Migration Plan

## Executive Summary

This document outlines the migration strategy from our current JSON-based Kafka event serialization to a typed, schema-enforced approach using Apache Kafka Schema Registry with Protobuf. This migration will provide stronger type safety, better data governance, schema evolution capabilities, and improved performance.

## Current State Analysis

### Current Implementation
- **Serialization**: JSON with `map[string]interface{}` for event data
- **Schema Management**: None - implicit schema in code
- **Type Safety**: Limited - runtime validation only
- **Performance**: Suboptimal - JSON parsing overhead
- **Schema Evolution**: Manual, error-prone

### Key Files and Components
- `pkg/kafka/producer.go`: JSON marshaling in PublishBatch (lines 79-102)
- `api_firehose/internal/grpc/server.go`: Proto-to-map conversion for Kafka (lines 199-260)
- `pkg/kafka/events.go`: Event struct with generic map[string]interface{} Data field

### Pain Points
1. No compile-time type checking for event data
2. Schema drift between producers and consumers
3. JSON serialization overhead (~5x larger than Protobuf)
4. No centralized schema governance
5. Difficulty tracking schema changes across services

## Target Architecture

### Technology Stack
- **Serialization Format**: Protobuf (already defined in `pkg/proto/decklog.proto`)
- **Schema Registry**: Apicurio Registry or Karapace (FOSS options)
- **Wire Format**: Confluent wire format (magic byte + schema ID + protobuf bytes)
- **Client Libraries**: Confluent Kafka Go client with Schema Registry support

### Benefits
1. **Type Safety**: Compile-time validation of event structures
2. **Schema Evolution**: Backward/forward compatibility guarantees
3. **Performance**: ~80% reduction in message size, faster serialization
4. **Data Governance**: Centralized schema management and versioning
5. **Developer Experience**: Auto-generated code from proto definitions

## Implementation Plan

### Phase 1: Infrastructure Setup (Week 1-2)

#### 1.1 Schema Registry Deployment
Choose between:

**Option A: Apicurio Registry (Recommended)**
```yaml
# docker-compose.yml addition
apicurio:
  image: apicurio/apicurio-registry-sql:2.5.11.Final
  environment:
    REGISTRY_DATASOURCE_URL: 'jdbc:postgresql://postgres:5432/registry'
    REGISTRY_DATASOURCE_USERNAME: 'apicurio'
    REGISTRY_DATASOURCE_PASSWORD: 'apicurio'
  ports:
    - "8081:8080"
  depends_on:
    - postgres
```

**Option B: Karapace**
```yaml
# docker-compose.yml addition
karapace:
  image: ghcr.io/aiven-open/karapace:3.11.0
  environment:
    KARAPACE_BOOTSTRAP_URI: kafka:9092
    KARAPACE_HOST: 0.0.0.0
    KARAPACE_PORT: 8081
  ports:
    - "8081:8081"
  depends_on:
    - kafka
```

#### 1.2 Update Dependencies
```go
// pkg/go.mod additions
require (
    github.com/confluentinc/confluent-kafka-go/v2 v2.3.0
    github.com/riferrei/srclient v0.6.0  // Schema Registry client
)
```

### Phase 2: Producer Migration (Week 3-4)

#### 2.1 Create Schema Registry Client Wrapper
```go
// pkg/kafka/schema_registry.go
package kafka

import (
    "github.com/riferrei/srclient"
    pb "frameworks/pkg/proto"
)

type SchemaRegistryClient struct {
    client *srclient.SchemaRegistryClient
    cache  map[string]int // topic -> schema ID cache
}

func NewSchemaRegistryClient(url string) (*SchemaRegistryClient, error) {
    client := srclient.CreateSchemaRegistryClient(url)
    return &SchemaRegistryClient{
        client: client,
        cache:  make(map[string]int),
    }, nil
}

func (s *SchemaRegistryClient) RegisterEventSchema() error {
    schema, err := pb.GetEventProtoSchema() // Generated helper
    if err != nil {
        return err
    }
    
    _, err = s.client.CreateSchema(
        "analytics_events-value",
        schema,
        srclient.Protobuf,
    )
    return err
}
```

#### 2.2 Update Producer with Protobuf Serialization
```go
// pkg/kafka/producer_v2.go
func (p *KafkaProducer) PublishProtobufBatch(batch *pb.Event) error {
    // Serialize with schema registry wire format
    value, err := p.serializeWithSchema(batch)
    if err != nil {
        return fmt.Errorf("failed to serialize: %w", err)
    }
    
    record := &kgo.Record{
        Topic: "analytics_events",
        Key:   []byte(batch.BatchId),
        Value: value,
        Headers: []kgo.RecordHeader{
            {Key: "content-type", Value: []byte("application/x-protobuf")},
            {Key: "schema-version", Value: []byte("2.0")},
        },
    }
    
    return p.client.ProduceSync(context.Background(), record).FirstErr()
}
```

### Phase 3: Consumer Migration (Week 5-6)

#### 3.1 Create Dual-Mode Consumer
```go
// pkg/kafka/consumer_v2.go
func (c *KafkaConsumer) processMessage(record *kgo.Record) error {
    // Check content type header
    contentType := c.getHeader(record.Headers, "content-type")
    
    switch contentType {
    case "application/x-protobuf":
        return c.processProtobufMessage(record)
    case "application/json", "":
        return c.processJSONMessage(record) // Legacy support
    default:
        return fmt.Errorf("unsupported content type: %s", contentType)
    }
}

func (c *KafkaConsumer) processProtobufMessage(record *kgo.Record) error {
    // Deserialize from wire format
    event := &pb.Event{}
    if err := c.deserializeWithSchema(record.Value, event); err != nil {
        return err
    }
    
    // Process typed event
    return c.handler.HandleProtobufEvent(event)
}
```

#### 3.2 Update Consumer Services
Services to update:
- api_signalman
- api_periscope
- api_analytics_query

### Phase 4: Rollout Strategy (Week 7-8)

#### 4.1 Canary Deployment
1. **Stage 1**: Deploy Schema Registry to all environments
2. **Stage 2**: Deploy dual-mode consumers (can read both JSON and Protobuf)
3. **Stage 3**: Deploy one Protobuf producer (api_firehose) with feature flag
4. **Stage 4**: Monitor metrics, validate data integrity
5. **Stage 5**: Gradually enable Protobuf for all producers
6. **Stage 6**: After 30 days, deprecate JSON support

#### 4.2 Feature Flags
```go
// pkg/config/features.go
type Features struct {
    UseProtobufSerialization bool `env:"FEATURE_PROTOBUF_SERIALIZATION" default:"false"`
    SchemaRegistryURL        string `env:"SCHEMA_REGISTRY_URL" default:"http://localhost:8081"`
}
```

#### 4.3 Monitoring and Rollback
- Add metrics for serialization format distribution
- Monitor message size reduction
- Track deserialization errors
- Maintain rollback procedure

### Phase 5: Cleanup (Week 9-10)

1. Remove JSON serialization code
2. Remove dual-mode consumer logic
3. Update documentation
4. Archive migration feature flags

## Risk Mitigation

### Potential Risks and Mitigations

1. **Schema Registry Downtime**
   - Mitigation: Client-side schema caching, multi-region deployment
   
2. **Schema Evolution Conflicts**
   - Mitigation: Strict compatibility rules, automated testing
   
3. **Performance Degradation**
   - Mitigation: Load testing, gradual rollout, monitoring
   
4. **Data Loss During Migration**
   - Mitigation: Dual consumers, message replay capability

## Operational Considerations

### Monitoring
- Schema Registry health metrics
- Schema version distribution
- Serialization/deserialization latency
- Message size reduction metrics

### Backup and Recovery
- Regular schema registry backups
- Point-in-time recovery procedures
- Schema version rollback process

### Development Workflow
1. Update .proto files
2. Run `make proto` to generate code
3. Register schema with registry (automated in CI/CD)
4. Deploy services with new schema

## Timeline and Milestones

| Week | Phase | Milestone |
|------|-------|-----------|
| 1-2  | Infrastructure | Schema Registry deployed to dev/staging |
| 3-4  | Producer Migration | Protobuf producer implemented and tested |
| 5-6  | Consumer Migration | Dual-mode consumers deployed |
| 7    | Canary Rollout | 10% traffic on Protobuf |
| 8    | Full Rollout | 100% traffic on Protobuf |
| 9-10 | Cleanup | JSON code removed, documentation updated |

## Success Criteria

- ✅ 80% reduction in average message size
- ✅ Zero data loss during migration
- ✅ < 1ms added latency for serialization
- ✅ 100% of events validated against schema
- ✅ Successful schema evolution test (adding optional field)

## Appendix

### A. Environment Variables
```bash
# Schema Registry Configuration
SCHEMA_REGISTRY_URL=http://apicurio:8081
SCHEMA_REGISTRY_AUTH_METHOD=none  # or basic, oauth2
SCHEMA_REGISTRY_CACHE_TTL=300     # seconds
SCHEMA_REGISTRY_TIMEOUT=5         # seconds

# Feature Flags
FEATURE_PROTOBUF_SERIALIZATION=true
FEATURE_DUAL_MODE_CONSUMER=true
FEATURE_LEGACY_JSON_SUPPORT=false
```

### B. Proto Schema Registration Script
```bash
#!/bin/bash
# scripts/register-schemas.sh

REGISTRY_URL=${SCHEMA_REGISTRY_URL:-http://localhost:8081}

# Register Event schema
curl -X POST $REGISTRY_URL/apis/registry/v2/groups/default/artifacts \
  -H "Content-Type: application/json" \
  -H "X-Registry-ArtifactType: PROTOBUF" \
  -H "X-Registry-ArtifactId: analytics-events-value" \
  -d @pkg/proto/decklog.proto

echo "Schemas registered successfully"
```

### C. Testing Strategy
1. **Unit Tests**: Serialization/deserialization roundtrip
2. **Integration Tests**: Producer -> Kafka -> Consumer flow
3. **Load Tests**: Performance benchmarks with both formats
4. **Compatibility Tests**: Schema evolution scenarios
5. **Chaos Tests**: Schema Registry failure scenarios

### D. Rollback Procedure
```bash
# 1. Disable Protobuf feature flag
kubectl set env deployment/api-firehose FEATURE_PROTOBUF_SERIALIZATION=false

# 2. Verify consumers still processing JSON
kubectl logs -l app=api-signalman --tail=100

# 3. If needed, replay messages from backup topic
kafka-console-consumer --topic analytics_events_backup \
  --from-beginning | \
kafka-console-producer --topic analytics_events
```

## References

- [Confluent Schema Registry Documentation](https://docs.confluent.io/platform/current/schema-registry/index.html)
- [Apicurio Registry Documentation](https://www.apicur.io/registry/docs/)
- [Karapace Documentation](https://karapace.io/)
- [Protobuf Best Practices](https://developers.google.com/protocol-buffers/docs/proto3)
- [Kafka Schema Evolution](https://www.confluent.io/blog/schema-evolution-in-avro-protocol-buffers-thrift/)