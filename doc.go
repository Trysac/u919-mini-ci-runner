// Package minicirunner is a small, dependency-free Go library: Run CI pipelines from a YAML file in containers with job dependencies, matrix expansion, content-keyed caches, artifacts and live logs
//
// The public API is every exported identifier of this root package. Functions are pure: they do
// no I/O, keep no global mutable state and report invalid input through returned errors.
package minicirunner
