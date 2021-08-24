package blockchain

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/Secured-Finance/dione/blockchain/database"

	drand2 "github.com/Secured-Finance/dione/beacon/drand"

	"github.com/Secured-Finance/dione/beacon"

	"github.com/Secured-Finance/dione/consensus/validation"
	"github.com/Secured-Finance/dione/types"
	"github.com/sirupsen/logrus"
	"github.com/wealdtech/go-merkletree"
	"github.com/wealdtech/go-merkletree/keccak256"

	"github.com/asaskevich/EventBus"

	"github.com/Secured-Finance/dione/blockchain/utils"

	types2 "github.com/Secured-Finance/dione/blockchain/types"
	"github.com/fxamacker/cbor/v2"
)

type BlockChain struct {
	db          database.Database
	bus         EventBus.Bus
	miner       *Miner
	drandBeacon *drand2.DrandBeacon
}

func NewBlockChain(db database.Database, bus EventBus.Bus, miner *Miner, drand *drand2.DrandBeacon) (*BlockChain, error) {
	chain := &BlockChain{
		db:          db,
		bus:         bus,
		miner:       miner,
		drandBeacon: drand,
	}

	logrus.Info("Blockchain has been successfully initialized!")

	return chain, nil
}

func (bc *BlockChain) GetLatestBlockHeight() (uint64, error) {
	return bc.db.GetLatestBlockHeight()
}

func (bc *BlockChain) StoreBlock(block *types2.Block) error {
	if exists, err := bc.HasBlock(block.Header.Hash); err != nil {
		return err
	} else if exists {
		//return fmt.Errorf("block already exists in blockchain")
		return nil
	}

	if block.Header.Height != 0 {
		err := bc.ValidateBlock(block)
		if err != nil {
			return fmt.Errorf("failed to store block: %w", err)
		}
	}

	if err := bc.db.StoreBlock(block); err != nil {
		return err
	}

	// update latest block height
	height, err := bc.GetLatestBlockHeight()
	if err != nil && err != database.ErrLatestHeightNil {
		return err
	}

	if err == database.ErrLatestHeightNil || block.Header.Height > height {
		if err = bc.db.SetLatestBlockHeight(block.Header.Height); err != nil {
			return err
		}
		bc.bus.Publish("blockchain:latestBlockHeightUpdated", block)
	}
	bc.bus.Publish("blockchain:blockCommitted", block)
	return nil
}

func (bc *BlockChain) HasBlock(blockHash []byte) (bool, error) {
	return bc.db.HasBlock(blockHash)
}

func (bc *BlockChain) FetchBlockData(blockHash []byte) ([]*types2.Transaction, error) {
	return bc.db.FetchBlockData(blockHash)
}

func (bc *BlockChain) FetchBlockHeader(blockHash []byte) (*types2.BlockHeader, error) {
	return bc.db.FetchBlockHeader(blockHash)
}

func (bc *BlockChain) FetchBlock(blockHash []byte) (*types2.Block, error) {
	return bc.db.FetchBlock(blockHash)
}

func (bc *BlockChain) FetchBlockByHeight(height uint64) (*types2.Block, error) {
	return bc.db.FetchBlockByHeight(height)
}

func (bc *BlockChain) FetchBlockHeaderByHeight(height uint64) (*types2.BlockHeader, error) {
	return bc.db.FetchBlockHeaderByHeight(height)
}

func (bc *BlockChain) ValidateBlock(block *types2.Block) error {
	// === verify block signature ===
	pubkey, err := block.Header.Proposer.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("unable to extract public key from block proposer's peer id: %w", err)
	}

	ok, err := pubkey.Verify(block.Header.Hash, block.Header.Signature)
	if err != nil {
		return fmt.Errorf("failed to verify block signature: %w", err)
	}
	if !ok {
		return fmt.Errorf("signature of block %x is invalid", block.Header.Hash)
	}
	/////////////////////////////////

	// === check last hash merkle proof ===
	latestHeight, err := bc.GetLatestBlockHeight()
	if err != nil {
		return err
	}
	previousBlockHeader, err := bc.FetchBlockHeaderByHeight(latestHeight)
	if err != nil {
		return err
	}
	if !bytes.Equal(block.Header.LastHash, previousBlockHeader.Hash) {
		return fmt.Errorf("block header has invalid last block hash (expected: %x, actual %x)", previousBlockHeader.Hash, block.Header.LastHash)
	}

	verified, err := merkletree.VerifyProofUsing(previousBlockHeader.Hash, true, block.Header.LastHashProof, [][]byte{block.Header.Hash}, keccak256.New())
	if err != nil {
		return fmt.Errorf("failed to verify last block hash merkle proof: %w", err)
	}
	if !verified {
		return fmt.Errorf("merkle hash of block doesn't contain hash of previous block")
	}
	/////////////////////////////////

	// === verify election proof wincount preliminarily ===
	if block.Header.ElectionProof.WinCount < 1 {
		return fmt.Errorf("block proposer %s is not a winner", block.Header.Proposer.String())
	}
	/////////////////////////////////

	// === verify miner's eligibility to propose this task ===
	err = bc.miner.IsMinerEligibleToProposeBlock(block.Header.ProposerEth)
	if err != nil {
		return fmt.Errorf("block proposer is not eligible to propose block: %w", err)
	}
	/////////////////////////////////

	// === verify election proof vrf ===
	proposerBuf, err := block.Header.Proposer.MarshalBinary()
	if err != nil {
		return err
	}

	res, err := bc.drandBeacon.Entry(context.TODO(), block.Header.ElectionProof.RandomnessRound)
	if err != nil {
		return err
	}
	eproofRandomness, err := beacon.DrawRandomness(
		res.Data,
		beacon.RandomnessTypeElectionProofProduction,
		block.Header.Height,
		proposerBuf,
	)
	if err != nil {
		return fmt.Errorf("failed to draw ElectionProof randomness: %w", err)
	}

	err = beacon.VerifyVRF(*block.Header.Proposer, eproofRandomness, block.Header.ElectionProof.VRFProof)
	if err != nil {
		return fmt.Errorf("failed to verify election proof vrf: %w", err)
	}
	//////////////////////////////////////

	// === compute wincount locally and verify values ===
	mStake, nStake, err := bc.miner.GetStakeInfo(block.Header.ProposerEth)
	if err != nil {
		return fmt.Errorf("failed to get miner stake: %w", err)
	}

	actualWinCount := block.Header.ElectionProof.ComputeWinCount(mStake, nStake)
	if block.Header.ElectionProof.WinCount != actualWinCount {
		return fmt.Errorf("locally computed wincount of block is not matching to the received value")
	}
	//////////////////////////////////////

	// === validate block transactions ===
	result := make(chan error)
	var wg sync.WaitGroup
	for _, v := range block.Data {
		wg.Add(1)
		go func(v *types2.Transaction, c chan error) {
			defer wg.Done()
			if err := utils.VerifyTx(block.Header, v); err != nil {
				c <- fmt.Errorf("failed to verify tx: %w", err)
				return
			}

			var task types.DioneTask
			err = cbor.Unmarshal(v.Data, &task)
			if err != nil {
				c <- fmt.Errorf("failed to unmarshal transaction payload: %w", err)
				return
			}

			if validationFunc := validation.GetValidationMethod(task.OriginChain, task.RequestType); validationFunc != nil {
				if err := validationFunc(&task); err != nil {
					c <- fmt.Errorf("payload validation has been failed: %w", err)
					return
				}
			} else {
				logrus.WithFields(logrus.Fields{
					"originChain": task.OriginChain,
					"requestType": task.RequestType,
				}).Debug("This origin chain/request type doesn't have any payload validation!")
			}
		}(v, result)
	}
	go func() {
		wg.Wait()
		close(result)
	}()
	for err := range result {
		if err != nil {
			return err
		}
	}
	/////////////////////////////////

	return nil
}
