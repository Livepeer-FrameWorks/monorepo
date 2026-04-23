package provisioner

// staticSeeds maps database names to their static seed file paths.
var staticSeeds = map[string]string{
	"purser": "seeds/static/purser_tiers.sql",
}

// demoSeeds maps database names to their demo seed file paths.
var demoSeeds = map[string]string{
	"quartermaster": "seeds/demo/demo_data.sql",
}
