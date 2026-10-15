// Package web holds the templates and static assets, embedded into the binary.
package web

import "embed"

//go:embed templates static i18n
var FS embed.FS
