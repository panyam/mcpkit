package generator

import (
	"embed"
	"text/template"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var fileTpl = template.Must(
	template.New("file.go.tmpl").ParseFS(templateFS, "templates/file.go.tmpl"),
)
