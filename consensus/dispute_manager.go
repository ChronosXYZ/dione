package consensus

import (
	"context"
	"encoding/hex"
	"math/big"
	"time"

	"github.com/asaskevich/EventBus"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Secured-Finance/dione/config"

	"github.com/Secured-Finance/dione/cache"

	"github.com/ethereum/go-ethereum/event"

	types2 "github.com/Secured-Finance/dione/blockchain/types"

	"github.com/Secured-Finance/dione/types"
	"github.com/fxamacker/cbor/v2"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"

	"github.com/Secured-Finance/dione/blockchain"

	"github.com/Secured-Finance/dione/contracts/dioneDispute"
	"github.com/Secured-Finance/dione/contracts/dioneOracle"
	"github.com/Secured-Finance/dione/ethclient"
)

type DisputeManager struct {
	ctx        context.Context
	bus        EventBus.Bus
	ethClient  *ethclient.EthereumClient
	pcm        *PBFTConsensusManager
	voteWindow time.Duration
	blockchain *blockchain.BlockChain

	submissionChan            chan *dioneOracle.DioneOracleSubmittedOracleRequest
	submissionEthSubscription event.Subscription
	submissionCache           cache.Cache

	disputesChan           chan *dioneDispute.DioneDisputeNewDispute
	disputeEthSubscription event.Subscription
	disputeCache           cache.Cache
}

type Dispute struct {
	Dhash            [32]byte
	RequestID        *big.Int
	Miner            common.Address
	DisputeInitiator common.Address
	Timestamp        int64
	Voted            bool
	Finished         bool // if we are dispute initiator
}

type Submission struct {
	ReqID     *big.Int
	Data      []byte
	Timestamp int64
	Checked   bool
}

func NewDisputeManager(bus EventBus.Bus, ethClient *ethclient.EthereumClient, bc *blockchain.BlockChain, cfg *config.Config, cm cache.CacheManager) (*DisputeManager, error) {
	ctx := context.TODO()

	submissionChan, submSubscription, err := ethClient.SubscribeOnNewSubmissions(ctx)
	if err != nil {
		return nil, err
	}

	disputesChan, dispSubscription, err := ethClient.SubscribeOnNewDisputes(ctx)
	if err != nil {
		return nil, err
	}

	dm := &DisputeManager{
		ethClient:                 ethClient,
		ctx:                       ctx,
		bus:                       bus,
		voteWindow:                time.Duration(cfg.Ethereum.DisputeVoteWindow) * time.Second,
		blockchain:                bc,
		submissionChan:            submissionChan,
		submissionEthSubscription: submSubscription,
		submissionCache:           cm.Cache("submissions"),
		disputesChan:              disputesChan,
		disputeEthSubscription:    dispSubscription,
		disputeCache:              cm.Cache("disputes"),
	}

	return dm, nil
}

func (dm *DisputeManager) Run(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				{
					dm.submissionEthSubscription.Unsubscribe()
					dm.disputeEthSubscription.Unsubscribe()
					return
				}
			case s := <-dm.submissionChan:
				{
					dm.onNewSubmission(s)
				}
			case d := <-dm.disputesChan:
				{
					dm.onNewDispute(d)
				}
			}
		}
	}()
}

func (dm *DisputeManager) onNewSubmission(submission *dioneOracle.DioneOracleSubmittedOracleRequest) {
	s := wrapSubmission(submission)
	s.Timestamp = time.Now().Unix()
	dm.submissionCache.Store(submission.ReqID.String(), s)

	// find a block that contains the dione task with specified request id
	task, block, err := dm.findTaskAndBlockWithRequestID(submission.ReqID.String())
	if err != nil {
		logrus.Error(err)
		return
	}

	submHashBytes := sha3.Sum256(s.Data)
	localHashBytes := sha3.Sum256(task.Payload)
	submHash := hex.EncodeToString(submHashBytes[:])
	localHash := hex.EncodeToString(localHashBytes[:])
	if submHash != localHash {
		logrus.Debugf("submission of request id %s isn't valid - beginning dispute", s.ReqID)
		err := dm.ethClient.BeginDispute(block.Header.ProposerEth, s.ReqID)
		if err != nil {
			logrus.Errorf(err.Error())
			return
		}
		disputeFinishTimer := time.NewTimer(dm.voteWindow)
		go func() {
			for {
				select {
				case <-dm.ctx.Done():
					return
				case <-disputeFinishTimer.C:
					{
						var d Dispute
						err := dm.disputeCache.Get(s.ReqID.String(), &d)
						if err != nil {
							logrus.Errorf(err.Error())
							return
						}
						err = dm.ethClient.FinishDispute(d.Dhash)
						if err != nil {
							logrus.Errorf(err.Error())
							disputeFinishTimer.Stop()
							return
						}
						disputeFinishTimer.Stop()

						d.Finished = true
						dm.disputeCache.Store(d.RequestID.String(), d)
						return
					}
				}
			}
		}()
	}
	s.Checked = true
	dm.submissionCache.Store(s.ReqID.String(), s)
}

func (dm *DisputeManager) findTaskAndBlockWithRequestID(requestID string) (*types.DioneTask, *types2.Block, error) {
	height, err := dm.blockchain.GetLatestBlockHeight()
	if err != nil {
		return nil, nil, err
	}

	for {
		block, err := dm.blockchain.FetchBlockByHeight(height)
		if err != nil {
			return nil, nil, err
		}

		for _, v := range block.Data {
			var task types.DioneTask
			err := cbor.Unmarshal(v.Data, &task)
			if err != nil {
				logrus.Error(err)
				continue
			}

			if task.RequestID == requestID {
				return &task, block, nil
			}
		}

		height--
	}
}

func (dm *DisputeManager) onNewDispute(dispute *dioneDispute.DioneDisputeNewDispute) {
	d := wrapDispute(dispute)
	d.Timestamp = time.Now().Unix()
	dm.disputeCache.Store(d.RequestID.String(), d)

	task, _, err := dm.findTaskAndBlockWithRequestID(d.RequestID.String())
	if err != nil {
		logrus.Error(err)
		return
	}

	var s Submission
	err = dm.submissionCache.Get(d.RequestID.String(), &s)
	if err != nil {
		logrus.Warnf("submission of request id %s isn't found in cache", d.RequestID.String())
		return
	}

	if dispute.DisputeInitiator.Hex() == dm.ethClient.GetEthAddress().Hex() {
		d.Voted = true
		dm.disputeCache.Store(d.RequestID.String(), d)
		return
	}

	submHashBytes := sha3.Sum256(s.Data)
	localHashBytes := sha3.Sum256(task.Payload)
	submHash := hex.EncodeToString(submHashBytes[:])
	localHash := hex.EncodeToString(localHashBytes[:])
	if submHash == localHash {
		err := dm.ethClient.VoteDispute(d.Dhash, false)
		if err != nil {
			logrus.Errorf(err.Error())
			return
		}
	} else {
		err = dm.ethClient.VoteDispute(d.Dhash, true)
		if err != nil {
			logrus.Errorf(err.Error())
			return
		}
	}

	d.Voted = true
	dm.disputeCache.Store(dispute.RequestID.String(), d)
}

func wrapDispute(d *dioneDispute.DioneDisputeNewDispute) *Dispute {
	return &Dispute{
		Dhash:            d.Dhash,
		RequestID:        d.RequestID,
		Miner:            d.Miner,
		DisputeInitiator: d.DisputeInitiator,
	}
}

func wrapSubmission(s *dioneOracle.DioneOracleSubmittedOracleRequest) *Submission {
	return &Submission{
		ReqID: s.ReqID,
		Data:  s.Data,
	}
}
