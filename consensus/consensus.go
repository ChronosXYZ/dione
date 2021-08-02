package consensus

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	drand2 "github.com/Secured-Finance/dione/beacon/drand"

	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/fxamacker/cbor/v2"

	"github.com/asaskevich/EventBus"

	"github.com/Secured-Finance/dione/blockchain"

	types3 "github.com/Secured-Finance/dione/blockchain/types"

	"github.com/libp2p/go-libp2p-core/crypto"

	"github.com/Secured-Finance/dione/blockchain/pool"

	"github.com/Secured-Finance/dione/consensus/types"
	types2 "github.com/Secured-Finance/dione/types"

	"github.com/Secured-Finance/dione/ethclient"
	"github.com/sirupsen/logrus"

	"github.com/Secured-Finance/dione/pubsub"
)

var (
	ErrNoAcceptedBlocks = errors.New("there is no accepted blocks")
)

type PBFTConsensusManager struct {
	bus                 EventBus.Bus
	psb                 *pubsub.PubSubRouter
	privKey             crypto.PrivKey
	validator           *ConsensusValidator
	ethereumClient      *ethclient.EthereumClient
	miner               *blockchain.Miner
	consensusRoundPool  *ConsensusStatePool
	mempool             *pool.Mempool
	blockchain          *blockchain.BlockChain
	address             peer.ID
	stateChangeChannels map[string]map[State][]chan bool
}

func NewPBFTConsensusManager(
	bus EventBus.Bus,
	psb *pubsub.PubSubRouter,
	privKey crypto.PrivKey,
	ethereumClient *ethclient.EthereumClient,
	miner *blockchain.Miner,
	bc *blockchain.BlockChain,
	bp *ConsensusStatePool,
	db *drand2.DrandBeacon,
	mempool *pool.Mempool,
	address peer.ID,
) *PBFTConsensusManager {
	pcm := &PBFTConsensusManager{
		psb:                 psb,
		miner:               miner,
		validator:           NewConsensusValidator(miner, bc, db),
		privKey:             privKey,
		ethereumClient:      ethereumClient,
		bus:                 bus,
		consensusRoundPool:  bp,
		mempool:             mempool,
		blockchain:          bc,
		address:             address,
		stateChangeChannels: map[string]map[State][]chan bool{},
	}

	pcm.psb.Hook(pubsub.PrePrepareMessageType, pcm.handlePrePrepare)
	pcm.psb.Hook(pubsub.PrepareMessageType, pcm.handlePrepare)
	pcm.psb.Hook(pubsub.CommitMessageType, pcm.handleCommit)

	pcm.bus.SubscribeAsync("beacon:newEntry", func(entry types2.BeaconEntry) {
		pcm.onNewBeaconEntry(entry)
	}, true)

	pcm.bus.SubscribeAsync("consensus:newState", func(block *types3.Block, newStateNumber int) {
		newState := State(newStateNumber) // hacky, because reflection panics if we pass int to a handler which has type-alias for int

		consensusMessageType := types.ConsensusMessageTypeUnknown

		switch newState {
		case StateStatusPrePrepared:
			{
				logrus.WithField("blockHash", fmt.Sprintf("%x", block.Header.Hash)).Debugf("Entered into PREPREPARED state")
				if *block.Header.Proposer == pcm.address {
					return
				}
				consensusMessageType = types.ConsensusMessageTypePrepare
				break
			}
		case StateStatusPrepared:
			{
				consensusMessageType = types.ConsensusMessageTypeCommit
				logrus.WithField("blockHash", fmt.Sprintf("%x", block.Header.Hash)).Debugf("Entered into PREPARED state")
				break
			}
		case StateStatusCommited:
			{
				logrus.WithField("blockHash", fmt.Sprintf("%x", block.Header.Hash)).Debugf("Entered into COMMITTED state")
				break
			}
		}

		if consensusMessageType == types.ConsensusMessageTypeUnknown {
			return
		}

		message, err := NewMessage(&types.ConsensusMessage{
			Type:      consensusMessageType,
			Blockhash: block.Header.Hash,
		}, pcm.privKey)
		if err != nil {
			logrus.Errorf("Failed to create consensus message: %v", err)
			return
		}
		if err = pcm.psb.BroadcastToServiceTopic(message); err != nil {
			logrus.Errorf("Failed to send consensus message: %s", err.Error())
			return
		}
	}, true)

	return pcm
}

func (pcm *PBFTConsensusManager) propose(blk *types3.Block) error {
	cmsg := &types.ConsensusMessage{
		Type:      StateStatusPrePrepared,
		Block:     blk,
		Blockhash: blk.Header.Hash,
		From:      pcm.address,
	}
	prePrepareMsg, err := NewMessage(cmsg, pcm.privKey)
	if err != nil {
		return err
	}

	time.Sleep(1 * time.Second) // wait until all nodes will commit previous blocks

	if err = pcm.consensusRoundPool.InsertMessageIntoLog(cmsg); err != nil {
		return err
	}

	if err = pcm.psb.BroadcastToServiceTopic(prePrepareMsg); err != nil {
		return err
	}

	logrus.WithField("blockHash", fmt.Sprintf("%x", blk.Header.Hash)).Debugf("Entered into PREPREPARED state")
	return nil
}

func (pcm *PBFTConsensusManager) handlePrePrepare(message *pubsub.PubSubMessage) {
	var prePrepare types.PrePrepareMessage
	err := cbor.Unmarshal(message.Payload, &prePrepare)
	if err != nil {
		logrus.Errorf("failed to convert payload to PrePrepare message: %s", err.Error())
		return
	}

	if *prePrepare.Block.Header.Proposer == pcm.address {
		return
	}

	cmsg := &types.ConsensusMessage{
		Type:      types.ConsensusMessageTypePrePrepare,
		From:      message.From,
		Block:     prePrepare.Block,
		Blockhash: prePrepare.Block.Header.Hash,
	}

	if !pcm.validator.Valid(cmsg) {
		logrus.WithField("blockHash", hex.EncodeToString(cmsg.Block.Header.Hash)).Warn("Received invalid PREPREPARE for block")
		return
	}

	err = pcm.consensusRoundPool.InsertMessageIntoLog(cmsg)
	if err != nil {
		logrus.WithField("err", err.Error()).Warn("Failed to add PREPARE message to log")
		return
	}

	logrus.WithFields(logrus.Fields{
		"blockHash": fmt.Sprintf("%x", cmsg.Block.Header.Hash),
		"from":      message.From.String(),
	}).Debug("Received PREPREPARE message")
}

func (pcm *PBFTConsensusManager) handlePrepare(message *pubsub.PubSubMessage) {
	var prepare types.PrepareMessage
	err := cbor.Unmarshal(message.Payload, &prepare)
	if err != nil {
		logrus.Errorf("failed to convert payload to Prepare message: %s", err.Error())
		return
	}

	cmsg := &types.ConsensusMessage{
		Type:      types.ConsensusMessageTypePrepare,
		From:      message.From,
		Blockhash: prepare.Blockhash,
		Signature: prepare.Signature,
	}

	if !pcm.validator.Valid(cmsg) {
		logrus.Warnf("received invalid prepare msg for block %x", cmsg.Blockhash)
		return
	}

	err = pcm.consensusRoundPool.InsertMessageIntoLog(cmsg)
	if err != nil {
		logrus.WithField("err", err.Error()).Warn("Failed to add PREPARE message to log")
		return
	}

	logrus.WithFields(logrus.Fields{
		"blockHash": fmt.Sprintf("%x", cmsg.Blockhash),
		"from":      message.From.String(),
	}).Debug("Received PREPARE message")
}

func (pcm *PBFTConsensusManager) handleCommit(message *pubsub.PubSubMessage) {
	var commit types.CommitMessage
	err := cbor.Unmarshal(message.Payload, &commit)
	if err != nil {
		logrus.Errorf("failed to convert payload to Commit message: %s", err.Error())
		return
	}

	cmsg := &types.ConsensusMessage{
		Type:      types.ConsensusMessageTypeCommit,
		From:      message.From,
		Blockhash: commit.Blockhash,
		Signature: commit.Signature,
	}

	if !pcm.validator.Valid(cmsg) {
		logrus.Warnf("received invalid commit msg for block %x", cmsg.Blockhash)
		return
	}

	err = pcm.consensusRoundPool.InsertMessageIntoLog(cmsg)
	if err != nil {
		logrus.WithField("err", err.Error()).Warn("Failed to add COMMIT message to log")
		return
	}

	logrus.WithFields(logrus.Fields{
		"blockHash": fmt.Sprintf("%x", cmsg.Blockhash),
		"from":      message.From.String(),
	}).Debug("Received COMMIT message")
}

func (pcm *PBFTConsensusManager) onNewBeaconEntry(entry types2.BeaconEntry) {
	block, err := pcm.commitAcceptedBlocks()
	height, _ := pcm.blockchain.GetLatestBlockHeight()
	if err != nil {
		if errors.Is(err, ErrNoAcceptedBlocks) {
			logrus.WithFields(logrus.Fields{
				"height": height + 1,
			}).Infof("No accepted blocks in the current consensus round")
		} else {
			logrus.WithFields(logrus.Fields{
				"height": height + 1,
				"err":    err.Error(),
			}).Errorf("Failed to select the block in the current consensus round")
			return
		}
	}

	if block != nil {
		// broadcast new block
		var newBlockMessage pubsub.PubSubMessage
		newBlockMessage.Type = pubsub.NewBlockMessageType
		blockSerialized, err := cbor.Marshal(block)
		if err != nil {
			logrus.Errorf("Failed to serialize block %x for broadcasting!", block.Header.Hash)
		} else {
			newBlockMessage.Payload = blockSerialized
			pcm.psb.BroadcastToServiceTopic(&newBlockMessage)
		}

		// if we are miner of this block
		// then post dione tasks to target chains (currently, only Ethereum)
		if block.Header.Proposer.String() == pcm.address.String() {
			pcm.submitTasksFromBlock(block)
		}
	}

	for k, v := range pcm.stateChangeChannels {
		for k1, j := range v {
			for _, ch := range j {
				ch <- true
				close(ch)
			}
			delete(v, k1)
		}
		delete(pcm.stateChangeChannels, k)
	}

	minedBlock, err := pcm.miner.MineBlock(entry.Data, entry.Round)
	if err != nil {
		if errors.Is(err, blockchain.ErrNoTxForBlock) {
			logrus.Info("Sealing skipped, no transactions in mempool")
		} else {
			logrus.Errorf("Failed to mine the block: %s", err.Error())
		}
		return
	}

	// if we are round winner
	if minedBlock != nil {
		err = pcm.propose(minedBlock)
		if err != nil {
			logrus.Errorf("Failed to propose the block: %s", err.Error())
			return
		}
	}
}

func (pcm *PBFTConsensusManager) submitTasksFromBlock(block *types3.Block) {
	for _, tx := range block.Data {
		var task types2.DioneTask
		err := cbor.Unmarshal(tx.Data, &task)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"err":    err.Error(),
				"txHash": hex.EncodeToString(tx.Hash),
			}).Error("Failed to unmarshal transaction payload")
			continue // FIXME
		}
		reqIDNumber, ok := big.NewInt(0).SetString(task.RequestID, 10)
		if !ok {
			logrus.WithFields(logrus.Fields{
				"txHash": hex.EncodeToString(tx.Hash),
			}).Error("Failed to parse request id number in Dione task")
			continue // FIXME
		}

		err = pcm.ethereumClient.SubmitRequestAnswer(reqIDNumber, task.Payload)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"err":    err.Error(),
				"txHash": hex.EncodeToString(tx.Hash),
				"reqID":  reqIDNumber.String(),
			}).Error("Failed to submit task to ETH chain")
			continue // FIXME
		}
		logrus.WithFields(logrus.Fields{
			"txHash": hex.EncodeToString(tx.Hash),
			"reqID":  reqIDNumber.String(),
		}).Debug("Dione task has been sucessfully submitted to ETH chain (DioneOracle contract)")
	}
}

func (pcm *PBFTConsensusManager) commitAcceptedBlocks() (*types3.Block, error) {
	blocks := pcm.consensusRoundPool.GetAllBlocksWithCommit()
	if blocks == nil {
		return nil, ErrNoAcceptedBlocks
	}
	var selectedBlock *types3.Block

	sort.Slice(blocks, func(i, j int) bool {
		iStake, err := pcm.ethereumClient.GetMinerStake(blocks[i].Block.Header.ProposerEth)
		if err != nil {
			logrus.Error(err)
			return false
		}

		jStake, err := pcm.ethereumClient.GetMinerStake(blocks[j].Block.Header.ProposerEth)
		if err != nil {
			logrus.Error(err)
			return false
		}

		if iStake.Cmp(jStake) == -1 {
			return false
		} else if iStake.Cmp(jStake) == 1 {
			return true
		} else {
			return blocks[i].Block.Header.Timestamp > blocks[i].Block.Header.Timestamp
		}
	})

	selectedBlock = blocks[0].Block

	stake, err := pcm.ethereumClient.GetMinerStake(selectedBlock.Header.ProposerEth)
	if err != nil {
		logrus.Error(err)
		return nil, err
	}

	logrus.WithFields(logrus.Fields{
		"winCount":      selectedBlock.Header.ElectionProof.WinCount,
		"proposerStake": stake.String(),
	}).Debug("Selected the block with maximal win count and proposer's stake.")

	logrus.WithFields(logrus.Fields{
		"hash":   hex.EncodeToString(selectedBlock.Header.Hash),
		"height": selectedBlock.Header.Height,
		"miner":  selectedBlock.Header.Proposer.String(),
	}).Info("Committed new block")
	pcm.consensusRoundPool.Prune()
	for _, v := range selectedBlock.Data {
		err := pcm.mempool.DeleteTx(v.Hash)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"err": err.Error(),
				"tx":  hex.EncodeToString(v.Hash),
			}).Errorf("Failed to delete committed tx from mempool")
			continue
		}
	}
	return selectedBlock, pcm.blockchain.StoreBlock(selectedBlock)
}
