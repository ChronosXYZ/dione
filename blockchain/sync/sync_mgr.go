package sync

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/fxamacker/cbor/v2"

	"github.com/asaskevich/EventBus"

	"github.com/Secured-Finance/dione/blockchain/utils"

	"github.com/Secured-Finance/dione/blockchain"

	"github.com/Secured-Finance/dione/pubsub"

	"github.com/Secured-Finance/dione/consensus/policy"

	"github.com/wealdtech/go-merkletree/keccak256"

	"github.com/wealdtech/go-merkletree"

	"github.com/sirupsen/logrus"

	"github.com/Secured-Finance/dione/node/wire"

	"github.com/libp2p/go-libp2p-core/peer"

	types2 "github.com/Secured-Finance/dione/blockchain/types"

	"github.com/Secured-Finance/dione/blockchain/pool"
	gorpc "github.com/libp2p/go-libp2p-gorpc"
)

type SyncManager interface {
	Run()
}

type syncManager struct {
	blockpool            *blockchain.BlockChain
	mempool              *pool.Mempool
	wg                   sync.WaitGroup
	ctx                  context.Context
	ctxCancelFunc        context.CancelFunc
	initialSyncCompleted bool
	bootstrapPeer        peer.ID
	rpcClient            *gorpc.Client
	psb                  *pubsub.PubSubRouter
	bus                  EventBus.Bus
}

func NewSyncManager(bus EventBus.Bus, bc *blockchain.BlockChain, mp *pool.Mempool, p2pRPCClient *gorpc.Client, bootstrapPeer peer.ID, psb *pubsub.PubSubRouter) SyncManager {
	ctx, cancelFunc := context.WithCancel(context.Background())
	sm := &syncManager{
		bus:                  bus,
		blockpool:            bc,
		mempool:              mp,
		ctx:                  ctx,
		ctxCancelFunc:        cancelFunc,
		initialSyncCompleted: false,
		bootstrapPeer:        bootstrapPeer,
		rpcClient:            p2pRPCClient,
		psb:                  psb,
	}

	return sm
}

func (sm *syncManager) Run() {
	sm.psb.Hook(pubsub.NewTxMessageType, sm.onNewTransaction)
	sm.psb.Hook(pubsub.NewBlockMessageType, sm.onNewBlock)

	go func() {
		if err := sm.initialSync(); err != nil {
			logrus.Error(err)
		}
	}()
}

func (sm *syncManager) initialSync() error {
	if err := sm.doInitialBlockPoolSync(); err != nil {
		return err
	}
	if err := sm.doInitialMempoolSync(); err != nil {
		return err
	}
	sm.bus.Publish("sync:initialSyncCompleted")
	return nil
}

func (sm *syncManager) doInitialBlockPoolSync() error {
	if sm.initialSyncCompleted {
		return nil
	}

	ourLastHeight, _ := sm.blockpool.GetLatestBlockHeight()

	if sm.bootstrapPeer == "" {
		return nil // FIXME
	}

	var reply wire.LastBlockHeightReply
	err := sm.rpcClient.Call(sm.bootstrapPeer, "NetworkService", "LastBlockHeight", nil, &reply)
	if err != nil {
		return err
	}

	if reply.Height > ourLastHeight {
		heightCount := reply.Height - ourLastHeight
		var from uint64
		to := ourLastHeight
		var receivedBlocks []types2.Block
		for heightCount > 0 {
			from = to + 1
			var addedVal uint64
			if heightCount < policy.MaxBlockCountForRetrieving {
				addedVal = heightCount
			} else {
				addedVal = policy.MaxBlockCountForRetrieving
			}
			heightCount -= addedVal
			to += addedVal
			var getBlocksReply wire.GetRangeOfBlocksReply
			arg := wire.GetRangeOfBlocksArg{From: from, To: to}
			err = sm.rpcClient.Call(sm.bootstrapPeer, "NetworkService", "GetRangeOfBlocks", arg, &getBlocksReply)
			if err != nil {
				return err
			}
			receivedBlocks = append(receivedBlocks, getBlocksReply.Blocks...)
			if len(getBlocksReply.FailedBlockHeights) != 0 {
				logrus.Warnf("remote node is unable to retrieve block heights: %s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(getBlocksReply.FailedBlockHeights)), ", "), "[]"))
				// FIXME we definitely need to handle it, because in that case our chain isn't complete!
			}
		}
		for _, b := range receivedBlocks {
			err := sm.processReceivedBlock(b) // it should process the block synchronously
			if err != nil {
				logrus.Warnf("unable to process block %d: %s", b.Header.Height, err.Error())
				continue
			}
		}
	} else {
		// FIXME probably we need to pick up better peer for syncing, because chain of current peer can be out-of-date as well
	}

	return nil
}

func (sm *syncManager) doInitialMempoolSync() error {
	if sm.bootstrapPeer == "" {
		return nil // FIXME
	}

	var reply wire.InvMessage
	err := sm.rpcClient.Call(sm.bootstrapPeer, "NetworkService", "Mempool", nil, &reply)
	if err != nil {
		return err
	}

	var txsToRetrieve [][]byte

	for _, v := range reply.Inventory {
		_, err = sm.mempool.GetTransaction(v.Hash)
		if errors.Is(err, pool.ErrTxNotFound) {
			txsToRetrieve = append(txsToRetrieve, v.Hash)
		}
	}

	for {
		var txHashes [][]byte

		if len(txsToRetrieve) == 0 {
			break
		}

		if len(txsToRetrieve) > policy.MaxTransactionCountForRetrieving {
			txHashes = txsToRetrieve[:policy.MaxTransactionCountForRetrieving]
			txsToRetrieve = txsToRetrieve[policy.MaxTransactionCountForRetrieving:]
		} else {
			txHashes = txsToRetrieve
			txsToRetrieve = nil
		}

		getMempoolTxArg := wire.GetMempoolTxsArg{
			Items: txHashes,
		}
		var getMempoolTxReply wire.GetMempoolTxsReply
		err := sm.rpcClient.Call(sm.bootstrapPeer, "NetworkService", "GetMempoolTxs", getMempoolTxArg, &getMempoolTxReply)
		if err != nil {
			return err
		}
		for _, v := range getMempoolTxReply.Transactions {
			err := sm.mempool.StoreTx(&v)
			if err != nil {
				logrus.Warnf(err.Error())
			}
		}
		// TODO handle not found transactions
	}

	return nil
}

func (sm *syncManager) processReceivedBlock(block types2.Block) error {
	// validate block
	previousBlockHeader, err := sm.blockpool.FetchBlockHeaderByHeight(block.Header.Height - 1)
	if err != nil {
		return fmt.Errorf("failed to retrieve previous block %d", block.Header.Height-1)
	}
	if bytes.Compare(block.Header.LastHash, previousBlockHeader.Hash) != 0 {
		return fmt.Errorf("block header has invalid last block hash")
	}
	verified, err := merkletree.VerifyProofUsing(previousBlockHeader.Hash, false, block.Header.LastHashProof, [][]byte{block.Header.Hash}, keccak256.New())
	if err != nil {
		return fmt.Errorf("failed to verify last block hash merkle proof: %w", err)
	}
	if !verified {
		return fmt.Errorf("merkle hash of current block doesn't contain hash of previous block")
	}

	// check if hashes of block transactions are present in the block hash merkle tree
	for _, tx := range block.Data { // FIXME we need to do something with rejected txs
		if err := utils.VerifyTx(block.Header, tx); err != nil {
			return err
		}
	}

	err = sm.blockpool.StoreBlock(&block)
	if err != nil {
		return fmt.Errorf("failed to store block in blockpool: %s", err.Error())
	}

	return nil
}

func (sm *syncManager) onNewTransaction(message *pubsub.PubSubMessage) {
	var tx types2.Transaction
	err := cbor.Unmarshal(message.Payload, &tx)
	if err != nil {
		logrus.Errorf("failed to convert payload to transaction: %s", err.Error())
		return
	}

	// TODO add more checks on tx
	if !tx.ValidateHash() {
		logrus.WithField("txHash", hex.EncodeToString(tx.Hash)).Warn("failed to validate transaction hash, rejecting it")
		return
	}

	err = sm.mempool.StoreTx(&tx)
	if err != nil {
		logrus.Warnf("failed to store incoming transaction in mempool: %s", err.Error())
	}
}

func (sm *syncManager) onNewBlock(message *pubsub.PubSubMessage) {
	var block types2.Block

	err := cbor.Unmarshal(message.Payload, &block)
	if err != nil {
		logrus.WithField("err", err.Error()).Error("failed to unmarshal payload of NewBlock message")
		return
	}

	err = sm.blockpool.StoreBlock(&block)
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"err":       err.Error(),
			"blockHash": fmt.Sprintf("%x", block.Header.Hash),
		}).Error("failed to store block from NewBlock message")
		return
	}
}
