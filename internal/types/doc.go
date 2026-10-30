// Package types defines the shared type contracts used across InferencePath's
// trust zones (Interior, Membrane, Exterior). It contains no logic.
//
// Identity-bearing types here are Interior-only and must not be embedded in
// Exterior-facing types.
package types
