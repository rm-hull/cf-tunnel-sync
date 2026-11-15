package config

import (
	"github.com/alecthomas/jsonschema"
)

// Schema generates the JSON schema for Config.
// Field descriptions come from `descr` struct tags collected by buildCommentMap().
func Schema() *jsonschema.Schema {
	reflector := reflectorWithComments()
	schema := reflector.Reflect(&Config{})
	schema.Description = "cf-tunnel-sync configuration schema"
	return schema
}
