-- Self-signup inserted deployment_tier = '' verbatim (CreateTenant bound the
-- unset proto field), which the alias backstop's old <> 'free' predicate
-- counted as paid. Normalize to 'free'; Purser's deployment-tier sweep
-- corrects any tenant whose billing subscription says otherwise. 'global'
-- rows are left alone here — only Purser knows their real tier.
UPDATE quartermaster.tenants
SET deployment_tier = 'free', updated_at = NOW()
WHERE COALESCE(deployment_tier, '') = '';
