// Package buildinfo exposes the source revision the binary was built from.
package buildinfo

// Commit is set at build time with
// -ldflags "-X github.com/permaily-org/permaily/internal/buildinfo.Commit=<rev>".
var Commit = "dev"
