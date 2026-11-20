// Package redact takes data with a fixed shape out of a text, and splits a
// text into sentences for a removal by meaning (ADR 0040, "Fixed shapes" and
// "Removing by meaning"). It needs no model: the same text and kinds give the
// same result in every enclave and release of one Version.
//
// This file is the seam, committed with the design: the version, the bounds
// and the golden cases every implementation must pass
// (testdata/shapes.jsonl). ADR 0040's pull request 2 writes Shapes and
// Sentences here, pure (depguard's redact-is-pure), and runs the cases.
package redact

// Version is the version of the patterns and the sentence split. A change of
// either, a fixed false positive included, is a new Version: a removal by
// meaning decided on the text of one version is never used for another's
// (rules.MeaningRef), and receipts name the version that applied.
const Version = 1

// The bounds of a removal by meaning (ADR 0040, "Removing by meaning",
// "Cost"): the text it reads, after the fixed shapes, the sentences it
// splits it into (the subject counted, when read), and how long one sentence
// may be before it is split at its last space. What is past them is removed,
// never sent.
const (
	MeaningTextMax      = 32 << 10
	MeaningSentencesMax = 128
	SentenceRunesMax    = 1000
)
