package consensus

import (
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/Secured-Finance/dione/config"

	types2 "github.com/Secured-Finance/dione/consensus/types"

	"github.com/Secured-Finance/dione/blockchain/pool"

	"github.com/asaskevich/EventBus"

	"github.com/sirupsen/logrus"

	"github.com/Secured-Finance/dione/blockchain/types"
)

type State uint8

const (
	StateStatusUnknown = iota

	StateStatusPrePrepared
	StateStatusPrepared
	StateStatusCommited
)

type ConsensusManager struct {
	mempool          *pool.Mempool
	consensusInfoMap map[string]*ConsensusInfo
	mapMutex         sync.Mutex
	bus              EventBus.Bus
	minApprovals     int // FIXME
}

func NewConsensusManager(mp *pool.Mempool, bus EventBus.Bus, cfg *config.Config) (*ConsensusManager, error) {
	bp := &ConsensusManager{
		consensusInfoMap: map[string]*ConsensusInfo{},
		mempool:          mp,
		bus:              bus,
		minApprovals:     cfg.ConsensusMinApprovals,
	}

	return bp, nil
}

type ConsensusInfo struct {
	Blockhash  []byte
	Block      *types.Block
	State      State
	MessageLog *ConsensusMessageLog
}

func (crp *ConsensusManager) InsertMessageIntoLog(cmsg *types2.ConsensusMessage) error {
	crp.mapMutex.Lock()
	defer crp.mapMutex.Unlock()
	consensusInfo, ok := crp.consensusInfoMap[hex.EncodeToString(cmsg.Blockhash)]
	if !ok {
		consensusInfo = &ConsensusInfo{
			Block:      cmsg.Block,
			Blockhash:  cmsg.Blockhash,
			State:      StateStatusUnknown,
			MessageLog: NewConsensusMessageLog(),
		}
		crp.consensusInfoMap[hex.EncodeToString(cmsg.Blockhash)] = consensusInfo
	}

	added := consensusInfo.MessageLog.AddMessage(cmsg)
	if !added {
		return fmt.Errorf("consensus message already exists in message log")
	}

	crp.maybeUpdateConsensusState(consensusInfo, cmsg)

	return nil
}

func (crp *ConsensusManager) maybeUpdateConsensusState(ci *ConsensusInfo, cmsg *types2.ConsensusMessage) {
	if ci.State == StateStatusUnknown && cmsg.Type == types2.ConsensusMessageTypePrePrepare && cmsg.Block != nil {
		ci.Block = cmsg.Block
		logrus.WithField("hash", fmt.Sprintf("%x", cmsg.Block.Header.Hash)).Debug("New block discovered")
		ci.State = StateStatusPrePrepared
		crp.bus.Publish("consensus:newState", ci.Block, StateStatusPrePrepared)
	}

	if len(ci.MessageLog.Get(types2.ConsensusMessageTypePrepare, cmsg.Blockhash)) >= crp.minApprovals-1 && ci.State == StateStatusPrePrepared { // FIXME approval across 2f nodes
		ci.State = StateStatusPrepared
		crp.bus.Publish("consensus:newState", ci.Block, StateStatusPrepared)
	}

	if len(ci.MessageLog.Get(types2.ConsensusMessageTypeCommit, cmsg.Blockhash)) >= crp.minApprovals && ci.State == StateStatusPrepared { // FIXME approval across 2f+1 nodes
		ci.State = StateStatusCommited
		crp.bus.Publish("consensus:newState", ci.Block, StateStatusCommited)
	}
}

// Prune cleans known blocks list. It is called when new consensus round starts.
func (crp *ConsensusManager) Prune() {
	for k := range crp.consensusInfoMap {
		delete(crp.consensusInfoMap, k)
	}
	crp.bus.Publish("blockpool:pruned")
}

func (crp *ConsensusManager) GetAllBlocksWithCommit() []*ConsensusInfo {
	crp.mapMutex.Lock()
	defer crp.mapMutex.Unlock()
	var consensusInfos []*ConsensusInfo
	for _, v := range crp.consensusInfoMap {
		if v.State == StateStatusCommited {
			consensusInfos = append(consensusInfos, v)
		}
	}
	return consensusInfos
}

//func containsTx(s []*types.Transaction, e *types.Transaction) bool {
//	for _, a := range s {
//		if bytes.Equal(a.Hash, e.Hash) {
//			return true
//		}
//	}
//	return false
//}
