// Package apispec embeds the canonical OpenAPI contract so the running binary
// can serve it at /openapi.yaml without depending on files on disk.
package apispec

import _ "embed"

//go:embed openapi.yaml
var OpenAPIYAML []byte
