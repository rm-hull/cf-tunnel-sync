package config

import (
	"github.com/invopop/jsonschema"
)

// Schema generates the JSON schema for Config.
func Schema() *jsonschema.Schema {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	schema := reflector.Reflect(&Config{})
	schema.Description = "cf-tunnel-sync configuration schema"
	return schema
}
