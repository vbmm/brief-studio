// Package viewer embeds the offline reader shared with the macOS build.
package viewer

import _ "embed"

//go:embed index.html
var Template string
