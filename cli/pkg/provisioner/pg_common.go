package provisioner

// staticSeeds maps database names to their static seed file paths. Reserved
// for catalogs that genuinely cannot be owned by a service binary; today the
// map is empty — Purser owns its tier catalog through `purser bootstrap` and
// the binary-embedded YAML.
var staticSeeds = map[string]string{}

// demoSeeds maps database names to their demo seed file paths.
var demoSeeds = map[string]string{
	"quartermaster": "seeds/demo/demo_data.sql",
}
