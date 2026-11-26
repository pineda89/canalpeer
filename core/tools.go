//go:build tools

// Package tools keeps in go.mod what the build needs but no package imports:
// gomobile bind refuses a module that does not require golang.org/x/mobile.
package tools

import _ "golang.org/x/mobile/bind"
