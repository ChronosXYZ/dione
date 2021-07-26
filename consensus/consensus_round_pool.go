package consensus

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Secured-Finance/dione/blockchain/pool"

	"github.com/asaskevich/EventBus"

	"github.com/sirupsen/logrus"

	"github.com/Secured-Finance/dione/blockchain/types"
	"github.com/Secured-Finance/dione/cache"
)

type State uint8

const (
	StateStatusUnknown = iota

	StateStatusPrePrepared
	StateStatusPrepared
	StateStatusCommited
)

// ConsensusRoundPool is pool for blocks that isn't not validated or committed yet
type ConsensusRoundPool struct {
	mempool              *pool.Mempool
	consensusInfoStorage cache.Cache
	bus                  EventBus.Bus
}

func NewConsensusRoundPool(mp *pool.Mempool, bus EventBus.Bus) (*ConsensusRoundPool, error) {
	bp := &ConsensusRoundPool{
		consensusInfoStorage: cache.NewInMemoryCache(),
		mempool:              mp,
		bus:                  bus,
	}

	return bp, nil
}

type ConsensusInfo struct {
	Block *types.Block
	State State
}

func (crp *ConsensusRoundPool) AddConsensusInfo(block *types.Block) error {
	encodedHash := hex.EncodeToString(block.Header.Hash)

	if crp.consensusInfoStorage.Exists(encodedHash) {
		return nil
	}

	err := crp.consensusInfoStorage.StoreWithTTL(encodedHash, &ConsensusInfo{
		Block: block,
		State: StateStatusPrePrepared,
	}, 10*time.Minute)
	if err != nil {
		return err
	}
	logrus.WithField("hash", fmt.Sprintf("%x", block.Header.Hash)).Debug("New block discovered")
	crp.bus.Publish("blockpool:knownBlockAdded", block)
	return nil
}

func (crp *ConsensusRoundPool) UpdateConsensusState(blockhash []byte, newState State) error {
	encodedHash := hex.EncodeToString(blockhash)

	var consensusInfo ConsensusInfo
	err := crp.consensusInfoStorage.Get(encodedHash, &consensusInfo)
	if err != nil {
		return err
	}

	if newState < consensusInfo.State {
		return fmt.Errorf("attempt to set incorrect state")
	}
	consensusInfo.State = newState
	crp.bus.Publish("blockpool:newConsensusState", blockhash, newState)
	return crp.consensusInfoStorage.StoreWithTTL(encodedHash, &consensusInfo, 10*time.Minute)
}

func (crp *ConsensusRoundPool) GetConsensusInfo(blockhash []byte) (*ConsensusInfo, error) {
	var consensusInfo ConsensusInfo
	err := crp.consensusInfoStorage.Get(hex.EncodeToString(blockhash), &consensusInfo)
	return &consensusInfo, err
}

// Prune cleans known blocks list. It is called when new consensus round starts.
func (crp *ConsensusRoundPool) Prune() {
	for k := range crp.consensusInfoStorage.Items() {
		crp.consensusInfoStorage.Delete(k)
	}
	crp.bus.Publish("blockpool:pruned")
}

func (crp *ConsensusRoundPool) GetAllBlocksWithCommit() []*ConsensusInfo {
	var consensusInfos []*ConsensusInfo
	for _, v := range crp.consensusInfoStorage.Items() {
		ci := v.(*ConsensusInfo)
		if ci.State == StateStatusCommited {
			consensusInfos = append(consensusInfos, ci)
		}
	}
	return consensusInfos
}

func containsTx(s []*types.Transaction, e *types.Transaction) bool {
	for _, a := range s {
		if bytes.Equal(a.Hash, e.Hash) {
			return true
		}
	}
	return false
}
