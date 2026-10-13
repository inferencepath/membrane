// Package types defines the shared, behavior-free type contracts used across
// InferencePath's trust zones (Interior, Membrane, Exterior).
//
// It deliberately contains no logic: it is the vocabulary other packages depend
// on. Identity-bearing types defined here are Interior-only and must never be
// embedded into Exterior-facing types.
package types
