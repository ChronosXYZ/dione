package beacon

// RandomnessType specifies a type of randomness.
type RandomnessType int64

const (
	RandomnessTypeElectionProofProduction RandomnessType = 1 + iota
)
