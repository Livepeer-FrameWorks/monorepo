# RFC: Bulk Operations API

## Status

Draft

## TL;DR

- Add async GraphQL operations for large data exports (Shopify-style `bulkOperationRunQuery`)
- Results stored as JSONL in object storage with expiring download URLs
- Not subject to normal rate limits; concurrency-limited per tenant

## Current State

- All GraphQL queries are synchronous with pagination via Relay connections
- Large exports require many paginated requests, hitting rate limits
- No background job system for tenant-initiated async operations
- Tenants needing bulk exports (analytics, audit logs) must build custom polling loops

## Problem / Motivation

Tenants with large stream libraries or high-volume analytics need to export data for:

- Auditing and compliance
- Backup and migration
- BI tool integration
- Large API queries that don't need real-time response

Current pagination-based queries are slow, complex, and rate-limited. A single export of 100K viewer sessions requires hundreds of requests.

## Goals

- Async GraphQL query execution for large datasets
- JSONL results with expiring download URLs
- Progress tracking and cancellation
- Exempt from normal rate limits during execution
- Concurrency limits per tenant

## Non-Goals

- Bulk mutations (writes) - focus on reads only for v1
- Real-time streaming results
- Custom output formats (CSV, Parquet) in v1

## Proposal

### GraphQL Schema

```graphql
type BulkOperation {
  id: ID!
  status: BulkOperationStatus!
  query: String!
  createdAt: DateTime!
  completedAt: DateTime
  url: String # JSONL download URL, available when COMPLETED
  errorMessage: String
  objectCount: Int
  fileSize: Int
}

enum BulkOperationStatus {
  CREATED
  RUNNING
  COMPLETED
  FAILED
  CANCELED
}

type BulkOperationRunQueryPayload {
  bulkOperation: BulkOperation
  userErrors: [UserError!]!
}

type BulkOperationCancelPayload {
  bulkOperation: BulkOperation
  userErrors: [UserError!]!
}

extend type Mutation {
  bulkOperationRunQuery(query: String!): BulkOperationRunQueryPayload!
  bulkOperationCancel(id: ID!): BulkOperationCancelPayload!
}

extend type Query {
  bulkOperation(id: ID!): BulkOperation
  currentBulkOperation: BulkOperation
}
```

### Architecture

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Bridge    │────▶│   Kafka     │────▶│   Worker    │
│  (GraphQL)  │     │             │     │  Service    │
└─────────────┘     └─────────────┘     └──────┬──────┘
                                               │
                                               ▼
                                        ┌─────────────┐
                                        │  S3 / GCS   │
                                        │  (JSONL)    │
                                        └─────────────┘
```

1. **Submission**: Tenant calls `bulkOperationRunQuery` with a GraphQL query string
2. **Validation**: Bridge validates query syntax and permissions
3. **Queuing**: Job created in DB, message sent to Kafka
4. **Execution**: Worker service executes query with cursor-based iteration
5. **Storage**: Results streamed to JSONL file in object storage
6. **Completion**: Status updated, signed URL generated (expires in 7 days)

### Constraints

- Max 3 concurrent bulk operations per tenant
- Query must be valid GraphQL against the schema
- Results expire after 7 days
- Max file size TBD (suggest 10GB)

### JSONL Output Format

Each line is a JSON object representing one node:

```jsonl
{"id":"stream_123","title":"My Stream","viewerCount":1500}
{"id":"stream_124","title":"Another Stream","viewerCount":800}
```

## Impact / Dependencies

- **Bridge**: New mutations/queries, job submission
- **Kafka**: New topic for bulk operation jobs
- **New Worker Service** (or extension of existing): Job execution
- **Object Storage**: S3/GCS bucket for results
- **Purser Schema**: Job tracking table

## Alternatives Considered

- **Streaming GraphQL subscriptions**: More complex, doesn't solve rate limiting
- **Dedicated export endpoints**: Loses GraphQL flexibility
- **Higher rate limits**: Doesn't solve pagination complexity

## Risks & Mitigations

- **Resource exhaustion**: Mitigate with concurrency limits, query complexity limits
- **Long-running jobs blocking**: Mitigate with separate worker pool, timeouts
- **Storage costs**: Mitigate with expiration, size limits

## Migration / Rollout

1. Add schema and job tracking tables
2. Implement worker service
3. Add Bridge mutations/queries
4. Beta with select tenants
5. GA with documentation

## Open Questions

- Should webhook notifications be supported on completion?
- What's the maximum query complexity allowed?
- Should we support filtering which fields are exported?

## References, Sources & Evidence

- [Reference] https://shopify.dev/docs/api/usage/bulk-operations
- [Reference] https://shopify.dev/docs/api/admin-graphql/latest/objects/BulkOperation
- [Evidence] Shopify uses 7-day URL expiration, JSONL format
- [Evidence] Shopify allows up to 5 concurrent operations (API version 2026-01+)
