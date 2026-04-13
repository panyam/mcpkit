package core

import uritemplate "github.com/yosida95/uritemplate/v3"

// URITemplateVars parses uri as an RFC 6570 URI template and returns the
// variable names it contains.  Returns nil when uri is not a valid template
// or contains no variables (i.e. is a concrete URI).
func URITemplateVars(uri string) []string {
	tmpl, err := uritemplate.New(uri)
	if err != nil {
		return nil
	}
	names := tmpl.Varnames()
	if len(names) == 0 {
		return nil
	}
	return names
}

// IsTemplateURI reports whether uri is a valid RFC 6570 URI template with
// at least one variable expression.
func IsTemplateURI(uri string) bool {
	return URITemplateVars(uri) != nil
}
