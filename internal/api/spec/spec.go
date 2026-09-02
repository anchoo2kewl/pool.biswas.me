// Package spec embeds the API's OpenAPI document.
//
// It lives in its own package so the document is compiled into the binary and
// travels with the deploy: a spec served from disk can drift from the code it
// describes, or go missing in a container that never copied it.
package spec

import (
	_ "embed"

	goapi "github.com/anchoo2kewl/go-api"
)

//go:embed openapi.yaml
var document []byte

// Document is the served OpenAPI document.
//
// MustSpec validates at init, so a build that embedded the wrong file — or
// nothing at all — fails on startup rather than serving a confident 200 of
// garbage to whatever is relying on it.
var Document = goapi.MustSpec(document, goapi.SpecYAML)
