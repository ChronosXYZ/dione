package consensus

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"

	"github.com/Secured-Finance/dione/pubsub"

	types2 "github.com/Secured-Finance/dione/consensus/types"

	"github.com/libp2p/go-libp2p-core/crypto"
)

func NewMessage(cmsg *types2.ConsensusMessage, privKey crypto.PrivKey) (*pubsub.PubSubMessage, error) {
	var message pubsub.PubSubMessage
	switch cmsg.Type {
	case types2.ConsensusMessageTypePrePrepare:
		{
			message.Type = pubsub.PrePrepareMessageType
			msg := types2.PrePrepareMessage{
				Block: cmsg.Block,
			}
			data, err := cbor.Marshal(msg)
			if err != nil {
				return nil, fmt.Errorf("failed to convert message to map: %s", err.Error())
			}
			message.Payload = data
			break
		}
	case types2.ConsensusMessageTypePrepare:
		{
			message.Type = pubsub.PrepareMessageType
			signature, err := privKey.Sign(cmsg.Blockhash)
			if err != nil {
				return nil, fmt.Errorf("failed to create signature: %v", err)
			}
			pm := types2.PrepareMessage{
				Blockhash: cmsg.Blockhash,
				Signature: signature,
			}
			data, err := cbor.Marshal(pm)
			if err != nil {
				return nil, fmt.Errorf("failed to convert message to map: %s", err.Error())
			}
			message.Payload = data
			break
		}
	case types2.ConsensusMessageTypeCommit:
		{
			message.Type = pubsub.CommitMessageType
			signature, err := privKey.Sign(cmsg.Blockhash)
			if err != nil {
				return nil, fmt.Errorf("failed to create signature: %v", err)
			}
			pm := types2.CommitMessage{
				Blockhash: cmsg.Blockhash,
				Signature: signature,
			}
			data, err := cbor.Marshal(pm)
			if err != nil {
				return nil, fmt.Errorf("failed to convert message to map: %s", err.Error())
			}
			message.Payload = data
			break
		}
	}

	return &message, nil
}
