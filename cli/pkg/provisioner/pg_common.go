package provisioner

// staticSeeds maps database names to their static seed file paths. Reserved
// for catalogs that genuinely cannot be owned by a service binary — Purser
// owns its tier catalog through `purser bootstrap` and the binary-embedded
// YAML. The analytics_ro grants qualify: the operator-analytics read-only
// role spans three services' schemas, so no single service can own it.
var staticSeeds = map[string]string{
	"quartermaster": "seeds/static/analytics_ro_quartermaster.sql",
	"commodore":     "seeds/static/analytics_ro_commodore.sql",
	"purser":        "seeds/static/analytics_ro_purser.sql",
}

// demoSeeds maps database names to their demo seed file paths.
var demoSeeds = map[string]string{
	"quartermaster": "seeds/demo/demo_data.sql",
}
