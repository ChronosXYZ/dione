package consensus

import "go.uber.org/fx"

var Module = fx.Options(
	fx.Provide(
		NewConsensusManager,
		NewConsensusHandler,
		NewDisputeManager,
	),
)
