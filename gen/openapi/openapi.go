// Package openapi holds the OpenAPI document `make gen` writes from the protos.
package openapi

import _ "embed"

// Document describes the whole REST API as inaba-server serves it.
//
//go:embed openapi.yaml
var Document []byte
