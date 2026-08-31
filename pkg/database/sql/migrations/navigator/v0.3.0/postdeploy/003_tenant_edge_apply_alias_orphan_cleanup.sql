DELETE FROM navigator.tenant_edge_apply_state AS edge
WHERE NOT EXISTS (
    SELECT 1
    FROM navigator.tenant_aliases AS alias
    WHERE alias.tenant_id = edge.tenant_id
);
