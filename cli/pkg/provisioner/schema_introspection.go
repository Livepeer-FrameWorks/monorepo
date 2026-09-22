package provisioner

// PostgresSchemaIntrospectionQuery dumps every deploy-relevant logical object as sorted text. Definitions that can
// contain newlines are hashed so one object remains one comparison row. Migration bookkeeping and baseline provenance
// are excluded because they intentionally differ by install path. Physical YugabyteDB placement is not included.
const PostgresSchemaIntrospectionQuery = `
SELECT 'col|' || n.nspname || '|' || c.relname || '|' || a.attname || '|' ||
       format_type(a.atttypid, a.atttypmod) || '|' || CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END || '|' ||
       coalesce(pg_get_expr(d.adbin, d.adrelid), '')
  FROM pg_attribute a
  JOIN pg_class c ON c.oid = a.attrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind IN ('r','p','v','m','f')
   AND c.relname NOT IN ('_migrations', '_schema_baseline')
   AND a.attnum > 0
   AND NOT a.attisdropped
UNION ALL
SELECT 'idx|' || schemaname || '|' || indexname || '|' || indexdef
  FROM pg_indexes
 WHERE schemaname NOT IN ('pg_catalog','information_schema')
   AND tablename NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'con|' || n.nspname || '|' || t.relname || '|' || c.contype::text || '|' || c.conname || '|' || pg_get_constraintdef(c.oid)
  FROM pg_constraint c
  JOIN pg_class t ON t.oid = c.conrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND t.relname NOT IN ('_migrations', '_schema_baseline')
   -- Exclude Postgres's synthesized NOT NULL check constraints: their names embed
   -- table OIDs (e.g. 21489_21843_3_not_null) so they differ between the two DBs
   -- as pure noise. NOT NULL is already compared via is_nullable in the column rows.
   AND NOT (c.contype = 'c' AND c.conname LIKE '%_not_null')
UNION ALL
SELECT 'trg|' || n.nspname || '|' || t.relname || '|' || tg.tgname || '|' || pg_get_triggerdef(tg.oid)
  FROM pg_trigger tg
  JOIN pg_class t ON t.oid = tg.tgrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
 WHERE NOT tg.tgisinternal
   AND n.nspname NOT IN ('pg_catalog','information_schema')
   AND t.relname NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'routine|' || n.nspname || '|' || p.prokind::text || '|' || p.proname || '|' ||
       pg_get_function_identity_arguments(p.oid) || '|' || md5(pg_get_functiondef(p.oid)) || '|' || pg_get_userbyid(p.proowner)
  FROM pg_proc p
  JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND p.prokind IN ('f', 'p')
UNION ALL
SELECT 'view|' || n.nspname || '|' || c.relname || '|' || c.relkind::text || '|' || md5(pg_get_viewdef(c.oid, true)) || '|' || pg_get_userbyid(c.relowner)
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind IN ('v', 'm')
UNION ALL
SELECT 'seq|' || sequence_schema || '|' || sequence_name || '|' || data_type || '|' ||
       start_value || '|' || minimum_value || '|' || maximum_value || '|' || increment || '|' || cycle_option
  FROM information_schema.sequences
 WHERE sequence_schema NOT IN ('pg_catalog','information_schema')
UNION ALL
SELECT 'ext|' || e.extname || '|' || e.extversion || '|' || n.nspname
  FROM pg_extension e
  JOIN pg_namespace n ON n.oid = e.extnamespace
 WHERE e.extname <> 'plpgsql'
UNION ALL
SELECT 'enum|' || n.nspname || '|' || t.typname || '|' || string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder) || '|' || pg_get_userbyid(t.typowner)
  FROM pg_type t
  JOIN pg_namespace n ON n.oid = t.typnamespace
  JOIN pg_enum e ON e.enumtypid = t.oid
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
 GROUP BY n.nspname, t.typname, t.typowner
UNION ALL
SELECT 'domain|' || n.nspname || '|' || t.typname || '|' || format_type(t.typbasetype, t.typtypmod) || '|' ||
       t.typnotnull::text || '|' || coalesce(t.typdefault, '') || '|' || pg_get_userbyid(t.typowner)
  FROM pg_type t
  JOIN pg_namespace n ON n.oid = t.typnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND t.typtype = 'd'
UNION ALL
SELECT 'composite|' || n.nspname || '|' || c.relname || '|' || a.attnum::text || '|' || a.attname || '|' ||
       format_type(a.atttypid, a.atttypmod) || '|' || a.attnotnull::text || '|' || pg_get_userbyid(c.relowner)
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_attribute a ON a.attrelid = c.oid
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind = 'c'
   AND a.attnum > 0
   AND NOT a.attisdropped
UNION ALL
SELECT 'schema-owner|' || n.nspname || '|' || pg_get_userbyid(n.nspowner)
  FROM pg_namespace n
 WHERE n.nspname NOT IN ('pg_catalog','information_schema','public')
UNION ALL
SELECT 'relation-owner|' || n.nspname || '|' || c.relname || '|' || c.relkind::text || '|' || pg_get_userbyid(c.relowner)
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind IN ('r','p','v','m','S','f')
   AND c.relname NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'table-grant|' || table_schema || '|' || table_name || '|' || grantor || '|' || grantee || '|' || privilege_type || '|' || is_grantable
  FROM information_schema.role_table_grants
 WHERE table_schema NOT IN ('pg_catalog','information_schema')
   AND table_name NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'routine-grant|' || routine_schema || '|' || routine_name || '|' || grantor || '|' || grantee || '|' || privilege_type || '|' || is_grantable
  FROM information_schema.role_routine_grants
 WHERE routine_schema NOT IN ('pg_catalog','information_schema')
UNION ALL
SELECT 'usage-grant|' || object_type || '|' || object_schema || '|' || object_name || '|' || grantor || '|' || grantee || '|' || privilege_type || '|' || is_grantable
  FROM information_schema.role_usage_grants
 WHERE object_schema NOT IN ('pg_catalog','information_schema')
UNION ALL
SELECT 'default-acl|' || coalesce(n.nspname, '') || '|' || pg_get_userbyid(d.defaclrole) || '|' || d.defaclobjtype::text || '|' || d.defaclacl::text
  FROM pg_default_acl d
  LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace
UNION ALL
SELECT 'rls-table|' || n.nspname || '|' || c.relname || '|' || c.relrowsecurity::text || '|' || c.relforcerowsecurity::text
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog','information_schema')
   AND c.relkind IN ('r','p')
   AND c.relname NOT IN ('_migrations', '_schema_baseline')
UNION ALL
SELECT 'rls-policy|' || schemaname || '|' || tablename || '|' || policyname || '|' || permissive || '|' ||
       array_to_string(ARRAY(SELECT role_name FROM unnest(roles) AS role_name ORDER BY role_name), ',') ||
       '|' || cmd || '|' || coalesce(qual, '') || '|' || coalesce(with_check, '')
  FROM pg_policies
 ORDER BY 1`
