package consensus

import (
	"encoding/hex"

	drand2 "github.com/Secured-Finance/dione/beacon/drand"

	"github.com/sirupsen/logrus"

	"github.com/Secured-Finance/dione/blockchain"
	types2 "github.com/Secured-Finance/dione/consensus/types"
)

type ConsensusValidator struct {
	validationFuncMap map[types2.ConsensusMessageType]func(msg *types2.ConsensusMessage) bool
	miner             *blockchain.Miner
	beacon            *drand2.DrandBeacon
	blockchain        *blockchain.BlockChain
}

func NewConsensusValidator(miner *blockchain.Miner, bc *blockchain.BlockChain, db *drand2.DrandBeacon) *ConsensusValidator {
	cv := &ConsensusValidator{
		miner:      miner,
		blockchain: bc,
		beacon:     db,
	}

	cv.validationFuncMap = map[types2.ConsensusMessageType]func(msg *types2.ConsensusMessage) bool{
		types2.ConsensusMessageTypePrePrepare: func(msg *types2.ConsensusMessage) bool {
			if err := cv.blockchain.ValidateBlock(msg.Block); err != nil {
				logrus.WithFields(logrus.Fields{
					"blockHash": hex.EncodeToString(msg.Block.Header.Hash),
					"err":       err.Error(),
				}).Error("failed to validate block from PrePrepare message")
				return false
			}
			return true
		},
		types2.ConsensusMessageTypePrepare: checkSignatureForBlockhash,
		types2.ConsensusMessageTypeCommit:  checkSignatureForBlockhash,
	}

	return cv
}

func (cv *ConsensusValidator) Valid(msg *types2.ConsensusMessage) bool {
	return cv.validationFuncMap[msg.Type](msg)
}

func checkSignatureForBlockhash(msg *types2.ConsensusMessage) bool {
	pubKey, err := msg.From.ExtractPublicKey()
	if err != nil {
		// TODO logging
		return false
	}
	ok, err := pubKey.Verify(msg.Blockhash, msg.Signature)
	if err != nil {
		// TODO logging
		return false
	}
	return ok
}
