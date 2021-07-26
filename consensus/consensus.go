package consensus

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	drand2 "github.com/Secured-Finance/dione/beacon/drand"

	"github.com/libp2p/go-libp2p-core/peer"

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

type PBFTConsensusManager struct {
	bus                 EventBus.Bus
	psb                 *pubsub.PubSubRouter
	minApprovals        int // FIXME
	privKey             crypto.PrivKey
	msgLog              *ConsensusMessageLog
	validator           *ConsensusValidator
	ethereumClient      *ethclient.EthereumClient
	miner               *blockchain.Miner
	consensusRoundPool  *ConsensusRoundPool
	mempool             *pool.Mempool
	blockchain          *blockchain.BlockChain
	address             peer.ID
	stateChangeChannels map[string]map[State][]chan bool
}

func NewPBFTConsensusManager(
	bus EventBus.Bus,
	psb *pubsub.PubSubRouter,
	minApprovals int,
	privKey crypto.PrivKey,
	ethereumClient *ethclient.EthereumClient,
	miner *blockchain.Miner,
	bc *blockchain.BlockChain,
	bp *ConsensusRoundPool,
	db *drand2.DrandBeacon,
	mempool *pool.Mempool,
	address peer.ID,
) *PBFTConsensusManager {
	pcm := &PBFTConsensusManager{
		psb:                 psb,
		miner:               miner,
		validator:           NewConsensusValidator(miner, bc, db),
		msgLog:              NewConsensusMessageLog(),
		minApprovals:        minApprovals,
		privKey:             privKey,
		ethereumClient:      ethereumClient,
		bus:                 bus,
		consensusRoundPool:  bp,
		mempool:             mempool,
		blockchain:          bc,
		address:             address,
		stateChangeChannels: map[string]map[State][]chan bool{},
	}

	return pcm
}

func (pcm *PBFTConsensusManager) Run() {
	pcm.psb.Hook(pubsub.PrePrepareMessageType, pcm.handlePrePrepare)
	pcm.psb.Hook(pubsub.PrepareMessageType, pcm.handlePrepare)
	pcm.psb.Hook(pubsub.CommitMessageType, pcm.handleCommit)
	pcm.bus.SubscribeAsync("beacon:newEntry", func(entry types2.BeaconEntry) {
		pcm.onNewBeaconEntry(entry)
	}, true)
}

func (pcm *PBFTConsensusManager) propose(blk *types3.Block) error {
	prePrepareMsg, err := NewMessage(types.ConsensusMessage{Block: blk}, types.ConsensusMessageTypePrePrepare, pcm.privKey)
	if err != nil {
		return err
	}
	pcm.psb.BroadcastToServiceTopic(prePrepareMsg)
	pcm.consensusRoundPool.AddConsensusInfo(blk)
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
	logrus.WithFields(logrus.Fields{
		"blockHash": fmt.Sprintf("%x", cmsg.Block.Header.Hash),
		"from":      message.From.String(),
	}).Debug("Received PREPREPARE message")
	pcm.consensusRoundPool.AddConsensusInfo(cmsg.Block)

	encodedHash := hex.EncodeToString(cmsg.Blockhash)
	if m, ok := pcm.stateChangeChannels[encodedHash]; ok {
		if channels, ok := m[StateStatusPrePrepared]; ok {
			for _, v := range channels {
				v <- true
				close(v)
				delete(pcm.stateChangeChannels, encodedHash)
			}
		}
	}

	prepareMsg, err := NewMessage(cmsg, types.ConsensusMessageTypePrepare, pcm.privKey)
	if err != nil {
		logrus.Errorf("failed to create prepare message: %v", err)
		return
	}

	logrus.WithField("blockHash", fmt.Sprintf("%x", prePrepare.Block.Header.Hash)).Debugf("Entered into PREPREPARED state")
	pcm.psb.BroadcastToServiceTopic(prepareMsg)
}

func (pcm *PBFTConsensusManager) handlePrepare(message *pubsub.PubSubMessage) {
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

	if _, err := pcm.consensusRoundPool.GetConsensusInfo(cmsg.Blockhash); errors.Is(err, cache.ErrNotFound) {
		encodedHash := hex.EncodeToString(cmsg.Blockhash)
		logrus.WithField("blockHash", encodedHash).Warn("received PREPARE for unknown block")
		waitingCh := make(chan bool)
		if _, ok := pcm.stateChangeChannels[encodedHash]; !ok {
			pcm.stateChangeChannels[encodedHash] = map[State][]chan bool{}
		}
		pcm.stateChangeChannels[encodedHash][StateStatusPrePrepared] = append(pcm.stateChangeChannels[encodedHash][StateStatusPrePrepared], waitingCh)
		result := <-waitingCh
		if !result {
			return
		}
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
	logrus.WithFields(logrus.Fields{
		"blockHash": fmt.Sprintf("%x", cmsg.Blockhash),
		"from":      message.From.String(),
	}).Debug("Received PREPARE message")

	if len(pcm.msgLog.Get(types.ConsensusMessageTypePrepare, cmsg.Blockhash)) >= pcm.minApprovals-1 {
		commitMsg, err := NewMessage(cmsg, types.ConsensusMessageTypeCommit, pcm.privKey)
		if err != nil {
			logrus.Errorf("failed to create commit message: %v", err)
			return
		}
		pcm.psb.BroadcastToServiceTopic(commitMsg)
		logrus.WithField("blockHash", fmt.Sprintf("%x", cmsg.Blockhash)).Debugf("Entered into PREPARED state")
		pcm.consensusRoundPool.UpdateConsensusState(cmsg.Blockhash, StateStatusPrepared)

		// pull watchers
		encodedHash := hex.EncodeToString(cmsg.Blockhash)
		if m, ok := pcm.stateChangeChannels[encodedHash]; ok {
			if channels, ok := m[StateStatusPrepared]; ok {
				for _, v := range channels {
					v <- true
					close(v)
					delete(pcm.stateChangeChannels, encodedHash)
				}
			}
		}
	}
}

func (pcm *PBFTConsensusManager) handleCommit(message *pubsub.PubSubMessage) {
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

	ci, err := pcm.consensusRoundPool.GetConsensusInfo(cmsg.Blockhash)

	if errors.Is(err, cache.ErrNotFound) {
		logrus.WithField("blockHash", hex.EncodeToString(cmsg.Blockhash)).Warnf("received COMMIT for unknown block")
		return
	}

	if ci.State < StateStatusPrepared {
		encodedHash := hex.EncodeToString(cmsg.Blockhash)
		logrus.WithField("blockHash", encodedHash).Warnf("incorrect state of block consensus")
		waitingCh := make(chan bool)
		if _, ok := pcm.stateChangeChannels[encodedHash]; !ok {
			pcm.stateChangeChannels[encodedHash] = map[State][]chan bool{}
		}
		pcm.stateChangeChannels[encodedHash][StateStatusPrepared] = append(pcm.stateChangeChannels[encodedHash][StateStatusPrepared], waitingCh)
		result := <-waitingCh
		if !result {
			return
		}
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

	logrus.WithFields(logrus.Fields{
		"blockHash": fmt.Sprintf("%x", cmsg.Blockhash),
		"from":      message.From.String(),
	}).Debug("Received COMMIT message")

	if len(pcm.msgLog.Get(types.ConsensusMessageTypeCommit, cmsg.Blockhash)) >= pcm.minApprovals {
		logrus.WithField("blockHash", fmt.Sprintf("%x", cmsg.Blockhash)).Debugf("Entered into COMMIT state")
		pcm.consensusRoundPool.UpdateConsensusState(cmsg.Blockhash, StateStatusCommited)
	}
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
	var maxStake *big.Int
	var maxWinCount int64 = -1
	var selectedBlock *types3.Block
	for _, v := range blocks {
		stake, err := pcm.ethereumClient.GetMinerStake(v.Block.Header.ProposerEth)
		if err != nil {
			return nil, err
		}

		if maxStake != nil && maxWinCount != -1 {
			if stake.Cmp(maxStake) == -1 || v.Block.Header.ElectionProof.WinCount < maxWinCount {
				continue
			}
		}
		maxStake = stake
		maxWinCount = v.Block.Header.ElectionProof.WinCount
		selectedBlock = v.Block
	}
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
