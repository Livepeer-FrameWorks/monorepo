package grpc

// sqlAliasTierEligible is the SQL form of models.DeploymentTierAliasEligible
// for queries over quartermaster.tenants aliased as t. It MUST enumerate
// exactly models.AliasEligibleDeploymentTiers (asserted by
// TestSQLAliasTierEligibleMatchesModelAllowlist): tenant aliases unlock only
// on the monthly paid tiers — 'free', 'payg', ” and unknown values are all
// ineligible, so the predicate fails closed.
const sqlAliasTierEligible = "t.deployment_tier IN ('supporter', 'developer', 'production', 'enterprise')"
