package consensus

import (
	"encoding/hex"
	"errors"
	"math/big"
	"sync"

	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/Secured-Finance/dione/beacon"

	"github.com/fxamacker/cbor/v2"

	"github.com/Secured-Finance/dione/cache"

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

type StateStatus uint8

const (
	StateStatusUnknown = iota

	StateStatusPrePrepared
	StateStatusPrepared
	StateStatusCommited
)

type PBFTConsensusManager struct {
	bus            EventBus.Bus
	psb            *pubsub.PubSubRouter
	minApprovals   int // FIXME
	privKey        crypto.PrivKey
	msgLog         *ConsensusMessageLog
	validator      *ConsensusValidator
	ethereumClient *ethclient.EthereumClient
	miner          *blockchain.Miner
	blockPool      *pool.BlockPool
	mempool        *pool.Mempool
	blockchain     *blockchain.BlockChain
	state          *State
	address        peer.ID
}

type State struct {
	mutex       sync.Mutex
	drandRound  uint64
	randomness  []byte
	blockHeight uint64
	status      StateStatus
	ready       bool
}

func NewPBFTConsensusManager(
	bus EventBus.Bus,
	psb *pubsub.PubSubRouter,
	minApprovals int,
	privKey crypto.PrivKey,
	ethereumClient *ethclient.EthereumClient,
	miner *blockchain.Miner,
	bc *blockchain.BlockChain,
	bp *pool.BlockPool,
	b beacon.BeaconNetwork,
	mempool *pool.Mempool,
	address peer.ID,
) *PBFTConsensusManager {
	pcm := &PBFTConsensusManager{
		psb:            psb,
		miner:          miner,
		validator:      NewConsensusValidator(miner, bc, b),
		msgLog:         NewConsensusMessageLog(),
		minApprovals:   minApprovals,
		privKey:        privKey,
		ethereumClient: ethereumClient,
		state: &State{
			ready:  false,
			status: StateStatusUnknown,
		},
		bus:        bus,
		blockPool:  bp,
		mempool:    mempool,
		blockchain: bc,
		address:    address,
	}

	pcm.psb.Hook(pubsub.PrePrepareMessageType, pcm.handlePrePrepare)
	pcm.psb.Hook(pubsub.PrepareMessageType, pcm.handlePrepare)
	pcm.psb.Hook(pubsub.CommitMessageType, pcm.handleCommit)
	bus.SubscribeAsync("beacon:newEntry", func(entry types2.BeaconEntry) {
		pcm.onNewBeaconEntry(entry)
	}, true)
	height, _ := pcm.blockchain.GetLatestBlockHeight()
	pcm.state.blockHeight = height + 1
	return pcm
}

func (pcm *PBFTConsensusManager) propose(blk *types3.Block) error {
	prePrepareMsg, err := NewMessage(types.ConsensusMessage{Block: blk}, types.ConsensusMessageTypePrePrepare, pcm.privKey)
	if err != nil {
		return err
	}
	pcm.psb.BroadcastToServiceTopic(prePrepareMsg)
	pcm.blockPool.AddBlock(blk)
	pcm.state.status = StateStatusPrePrepared
	return nil
}

func (pcm *PBFTConsensusManager) handlePrePrepare(message *pubsub.PubSubMessage) {
	//pcm.state.mutex.Lock()
	//defer pcm.state.mutex.Unlock()
	var prePrepare types.PrePrepareMessage
	err := cbor.Unmarshal(message.Payload, &prePrepare)
	if err != nil {
		logrus.Errorf("failed to convert payload to PrePrepare message: %s", err.Error())
		return
	}

	if *prePrepare.Block.Header.Proposer == pcm.address {
		return
	}

	cmsg := types.ConsensusMessage{
		Type:      types.ConsensusMessageTypePrePrepare,
		From:      message.From,
		Block:     prePrepare.Block,
		Blockhash: prePrepare.Block.Header.Hash,
	}

	if pcm.msgLog.Exists(cmsg) {
		logrus.Tracef("received existing pre_prepare msg for block %x", cmsg.Block.Header.Hash)
		return
	}
	if !pcm.validator.Valid(cmsg) {
		logrus.Warnf("received invalid pre_prepare msg for block %x", cmsg.Block.Header.Hash)
		return
	}

	pcm.msgLog.AddMessage(cmsg)
	pcm.blockPool.AddBlock(cmsg.Block)

	prepareMsg, err := NewMessage(cmsg, types.ConsensusMessageTypePrepare, pcm.privKey)
	if err != nil {
		logrus.Errorf("failed to create prepare message: %v", err)
		return
	}

	pcm.psb.BroadcastToServiceTopic(prepareMsg)
	pcm.state.status = StateStatusPrePrepared
}

func (pcm *PBFTConsensusManager) handlePrepare(message *pubsub.PubSubMessage) {
	//pcm.state.mutex.Lock()
	//defer pcm.state.mutex.Unlock()
	var prepare types.PrepareMessage
	err := cbor.Unmarshal(message.Payload, &prepare)
	if err != nil {
		logrus.Errorf("failed to convert payload to Prepare message: %s", err.Error())
		return
	}

	cmsg := types.ConsensusMessage{
		Type:      types.ConsensusMessageTypePrepare,
		From:      message.From,
		Blockhash: prepare.Blockhash,
		Signature: prepare.Signature,
	}

	if _, err := pcm.blockPool.GetBlock(cmsg.Blockhash); errors.Is(err, cache.ErrNotFound) {
		logrus.Warnf("received unknown block %x", cmsg.Blockhash)
		return
	}

	if pcm.msgLog.Exists(cmsg) {
		logrus.Tracef("received existing prepare msg for block %x", cmsg.Blockhash)
		return
	}

	if !pcm.validator.Valid(cmsg) {
		logrus.Warnf("received invalid prepare msg for block %x", cmsg.Blockhash)
		return
	}

	pcm.msgLog.AddMessage(cmsg)

	if len(pcm.msgLog.Get(types.ConsensusMessageTypePrepare, cmsg.Blockhash)) >= pcm.minApprovals {
		commitMsg, err := NewMessage(cmsg, types.ConsensusMessageTypeCommit, pcm.privKey)
		if err != nil {
			logrus.Errorf("failed to create commit message: %v", err)
			return
		}
		pcm.psb.BroadcastToServiceTopic(commitMsg)
		pcm.state.status = StateStatusPrepared
	}
}

func (pcm *PBFTConsensusManager) handleCommit(message *pubsub.PubSubMessage) {
	//pcm.state.mutex.Lock()
	//defer pcm.state.mutex.Unlock()
	var commit types.CommitMessage
	err := cbor.Unmarshal(message.Payload, &commit)
	if err != nil {
		logrus.Errorf("failed to convert payload to Commit message: %s", err.Error())
		return
	}

	cmsg := types.ConsensusMessage{
		Type:      types.ConsensusMessageTypeCommit,
		From:      message.From,
		Blockhash: commit.Blockhash,
		Signature: commit.Signature,
	}

	if _, err := pcm.blockPool.GetBlock(cmsg.Blockhash); errors.Is(err, cache.ErrNotFound) {
		logrus.Warnf("received unknown block %x", cmsg.Blockhash)
		return
	}

	if pcm.msgLog.Exists(cmsg) {
		logrus.Tracef("received existing commit msg for block %x", cmsg.Blockhash)
		return
	}
	if !pcm.validator.Valid(cmsg) {
		logrus.Warnf("received invalid commit msg for block %x", cmsg.Blockhash)
		return
	}

	pcm.msgLog.AddMessage(cmsg)

	if len(pcm.msgLog.Get(types.ConsensusMessageTypeCommit, cmsg.Blockhash)) >= pcm.minApprovals {
		block, err := pcm.blockPool.GetBlock(cmsg.Blockhash)
		if err != nil {
			logrus.Error(err)
			return
		}
		pcm.blockPool.AddAcceptedBlock(block)
		pcm.state.status = StateStatusCommited
	}
}

func (pcm *PBFTConsensusManager) onNewBeaconEntry(entry types2.BeaconEntry) {
	block, err := pcm.commitAcceptedBlocks()
	if err != nil {
		if errors.Is(err, ErrNoAcceptedBlocks) {
			logrus.WithFields(logrus.Fields{
				"height": pcm.state.blockHeight,
			}).Infof("No accepted blocks in the current consensus round")
		} else {
			logrus.WithFields(logrus.Fields{
				"height": pcm.state.blockHeight,
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

		pcm.state.blockHeight = pcm.state.blockHeight + 1
	}

	// get latest block
	height, err := pcm.blockchain.GetLatestBlockHeight()
	if err != nil {
		logrus.Error(err)
		return
	}
	blockHeader, err := pcm.blockchain.FetchBlockHeaderByHeight(height)
	if err != nil {
		logrus.Error(err)
		return
	}

	pcm.state.drandRound = entry.Round
	pcm.state.randomness = entry.Data

	minedBlock, err := pcm.miner.MineBlock(entry.Data, entry.Round, blockHeader)
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
		logrus.WithField("height", pcm.state.blockHeight).Infof("We have been elected in the current consensus round")
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
	blocks := pcm.blockPool.GetAllAcceptedBlocks()
	if blocks == nil {
		return nil, ErrNoAcceptedBlocks
	}
	var maxStake *big.Int
	var selectedBlock *types3.Block
	for _, v := range blocks {
		stake, err := pcm.ethereumClient.GetMinerStake(v.Header.ProposerEth)
		if err != nil {
			return nil, err
		}

		if maxStake != nil {
			if stake.Cmp(maxStake) == -1 {
				continue
			}
		}
		maxStake = stake
		selectedBlock = v
	}
	logrus.WithFields(logrus.Fields{
		"hash":   hex.EncodeToString(selectedBlock.Header.Hash),
		"height": selectedBlock.Header.Height,
		"miner":  selectedBlock.Header.Proposer.String(),
	}).Info("Committed new block")
	pcm.blockPool.PruneAcceptedBlocks(selectedBlock)
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
