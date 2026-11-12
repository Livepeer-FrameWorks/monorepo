package periscopequerydb

// The payload names the subject; the audit user_id is an actor who can be
// anonymous or an operator. Apply owner, type and time bounds before ranking
// mirrored copies, so the window never ranks unrelated tenant history.
const securityActivityRowsSQL = `
SELECT event_id, event_type, timestamp, details FROM (
  SELECT event_id, event_type, timestamp, details,
    row_number() OVER (PARTITION BY tenant_id, event_id
      ORDER BY position(event_type, '.') = 0) AS dedup_rank
  FROM periscope.api_events
  WHERE tenant_id = toUUID(?)
    AND JSONExtractString(details, 'user_id') = ?
    AND timestamp >= ? AND timestamp <= ?
    AND event_type IN ('auth_login_succeeded', 'auth_login_failed',
      'auth_password_changed', 'auth_password_reset', 'wallet_linked',
      'wallet_unlinked', 'auth_session.revoked', 'user.sessions_revoked')
) WHERE event_id = toUUID('00000000-0000-0000-0000-000000000000') OR dedup_rank = 1
`

const SecurityActivityCountSQL = `SELECT count() FROM (` + securityActivityRowsSQL + `)`

const SecurityActivityPageSQL = `SELECT toString(event_id), event_type, timestamp,
  JSONExtractString(details, 'auth_type'), JSONExtractString(details, 'session_id'),
  JSONExtractString(details, 'reason') FROM (` + securityActivityRowsSQL + `)
WHERE (timestamp, event_id) < (?, toUUID(?))
ORDER BY timestamp DESC, event_id DESC LIMIT ?`
