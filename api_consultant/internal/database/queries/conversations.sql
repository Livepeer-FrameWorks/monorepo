-- name: CreateConversation :one
INSERT INTO skipper.skipper_conversations (tenant_id, user_id)
VALUES (sqlc.arg(tenant_id), sqlc.narg(user_id))
RETURNING id;

-- name: AddMessage :one
WITH input AS (
    SELECT sqlc.arg(conversation_id)::uuid AS conversation_id,
           sqlc.arg(role)::text AS role,
           sqlc.arg(content)::text AS content,
           sqlc.arg(confidence)::text AS confidence,
           sqlc.arg(sources)::text::jsonb AS sources,
           sqlc.arg(tools_used)::text::jsonb AS tools_used,
           sqlc.arg(confidence_blocks)::text::jsonb AS confidence_blocks,
           sqlc.arg(token_count_input)::integer AS token_count_input,
           sqlc.arg(token_count_output)::integer AS token_count_output,
           sqlc.arg(tenant_id)::uuid AS tenant_id
)
INSERT INTO skipper.skipper_messages (
    conversation_id,
    role,
    content,
    confidence,
    sources,
    tools_used,
    confidence_blocks,
    token_count_input,
    token_count_output
)
SELECT c.id, input.role, input.content, input.confidence,
       input.sources, input.tools_used, input.confidence_blocks,
       input.token_count_input, input.token_count_output
FROM skipper.skipper_conversations c
CROSS JOIN input
WHERE c.id = input.conversation_id
  AND c.tenant_id = input.tenant_id
RETURNING id;

-- name: TouchConversation :exec
UPDATE skipper.skipper_conversations
SET updated_at = NOW()
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: GetConversation :one
SELECT id,
       tenant_id,
       user_id,
       title,
       COALESCE(summary, ''),
       created_at,
       updated_at
FROM skipper.skipper_conversations
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: GetConversationForUser :one
SELECT id,
       tenant_id,
       user_id,
       title,
       COALESCE(summary, ''),
       created_at,
       updated_at
FROM skipper.skipper_conversations
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id)
  AND user_id = sqlc.arg(user_id)::uuid;

-- name: ListConversations :many
SELECT c.id,
       c.tenant_id,
       c.user_id,
       c.title,
       c.created_at,
       c.updated_at,
       MAX(m.created_at) AS last_message_at,
       COUNT(m.id) AS message_count
FROM skipper.skipper_conversations c
LEFT JOIN skipper.skipper_messages m ON m.conversation_id = c.id
WHERE c.tenant_id = sqlc.arg(tenant_id)
GROUP BY c.id, c.tenant_id, c.user_id, c.title, c.created_at, c.updated_at
ORDER BY COALESCE(MAX(m.created_at), c.created_at) DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: ListConversationsForUser :many
SELECT c.id,
       c.tenant_id,
       c.user_id,
       c.title,
       c.created_at,
       c.updated_at,
       MAX(m.created_at) AS last_message_at,
       COUNT(m.id) AS message_count
FROM skipper.skipper_conversations c
LEFT JOIN skipper.skipper_messages m ON m.conversation_id = c.id
WHERE c.tenant_id = sqlc.arg(tenant_id)
  AND c.user_id = sqlc.arg(user_id)::uuid
GROUP BY c.id, c.tenant_id, c.user_id, c.title, c.created_at, c.updated_at
ORDER BY COALESCE(MAX(m.created_at), c.created_at) DESC
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: UpdateConversationTitle :execrows
UPDATE skipper.skipper_conversations
SET title = sqlc.arg(title)::text, updated_at = NOW()
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: UpdateConversationTitleForUser :execrows
UPDATE skipper.skipper_conversations
SET title = sqlc.arg(title)::text, updated_at = NOW()
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id)
  AND user_id = sqlc.arg(user_id)::uuid;

-- name: DeleteConversationMessages :exec
DELETE FROM skipper.skipper_messages
WHERE conversation_id = sqlc.arg(conversation_id)
  AND conversation_id IN (
      SELECT id
      FROM skipper.skipper_conversations
      WHERE tenant_id = sqlc.arg(tenant_id)
  );

-- name: DeleteConversationMessagesForUser :exec
DELETE FROM skipper.skipper_messages
WHERE conversation_id = sqlc.arg(conversation_id)
  AND conversation_id IN (
      SELECT id
      FROM skipper.skipper_conversations
      WHERE tenant_id = sqlc.arg(tenant_id)
        AND user_id = sqlc.arg(user_id)::uuid
  );

-- name: DeleteConversation :execrows
DELETE FROM skipper.skipper_conversations
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: DeleteConversationForUser :execrows
DELETE FROM skipper.skipper_conversations
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id)
  AND user_id = sqlc.arg(user_id)::uuid;

-- name: ListConversationMessages :many
SELECT m.id,
       m.conversation_id,
       m.role,
       m.content,
       m.confidence,
       COALESCE(m.sources, 'null') AS sources,
       COALESCE(m.tools_used, 'null') AS tools_used,
       COALESCE(m.confidence_blocks, 'null') AS confidence_blocks,
       m.token_count_input,
       m.token_count_output,
       m.created_at
FROM skipper.skipper_messages m
JOIN skipper.skipper_conversations c ON m.conversation_id = c.id
WHERE m.conversation_id = sqlc.arg(conversation_id)
  AND c.tenant_id = sqlc.arg(tenant_id)
ORDER BY m.created_at ASC;

-- name: ListRecentConversationMessages :many
SELECT *
FROM (
    SELECT m.id,
           m.conversation_id,
           m.role,
           m.content,
           m.confidence,
           COALESCE(m.sources, 'null') AS sources,
           COALESCE(m.tools_used, 'null') AS tools_used,
           COALESCE(m.confidence_blocks, 'null') AS confidence_blocks,
           m.token_count_input,
           m.token_count_output,
           m.created_at
    FROM skipper.skipper_messages m
    JOIN skipper.skipper_conversations c ON m.conversation_id = c.id
    WHERE m.conversation_id = sqlc.arg(conversation_id)
      AND c.tenant_id = sqlc.arg(tenant_id)
    ORDER BY m.created_at DESC
    LIMIT sqlc.arg(row_limit)
) recent
ORDER BY created_at ASC;

-- name: GetConversationSummary :one
SELECT summary
FROM skipper.skipper_conversations
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: UpdateConversationSummary :exec
UPDATE skipper.skipper_conversations
SET summary = sqlc.arg(summary)::text, updated_at = NOW()
WHERE id = sqlc.arg(conversation_id)
  AND tenant_id = sqlc.arg(tenant_id);

-- name: CountConversationMessages :one
SELECT COUNT(*)
FROM skipper.skipper_messages m
JOIN skipper.skipper_conversations c ON m.conversation_id = c.id
WHERE m.conversation_id = sqlc.arg(conversation_id)
  AND c.tenant_id = sqlc.arg(tenant_id);
