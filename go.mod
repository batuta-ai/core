module github.com/batuta-ai/core

go 1.26.4

require (
	github.com/pelletier/go-toml/v2 v2.4.3
	gopkg.in/yaml.v3 v3.0.1
)

// v1.0.0 and v1.0.1 were published before the beta line; the module stays
// pre-release (v1.1.0-beta.N) until the API stabilizes.
retract [v1.0.0, v1.0.1]
