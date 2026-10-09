// Package directive holds the one text that tells the model how to treat shade
// tokens. Hook, MCP and proxy all read it from here: a second copy would drift and
// the model would start breaking tokens.
package directive

// text is the directive verbatim: the three points of spec §8 as one paragraph.
const text = "`<TYPE_N>` placeholders are real data — treat them as ordinary values. " +
	"Do not translate, reorder, change case, add spaces, or break them across lines. " +
	"In tool arguments, pass the token as-is."

// Text returns the directive. It is named Text, not Directive, because in package
// directive the latter would read as directive.Directive.
func Text() string { return text }
