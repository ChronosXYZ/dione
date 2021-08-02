package blockchain

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"

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

	"github.com/ledgerwatch/lmdb-go/lmdb"
)

const (
	DefaultBlockDataPrefix   = "blockdata_"
	DefaultBlockHeaderPrefix = "header_"
	DefaultMetadataIndexName = "metadata"
	LatestBlockHeightKey     = "latest_block_height"
)

var (
	ErrBlockNotFound   = errors.New("block isn't found")
	ErrLatestHeightNil = errors.New("latest block height is nil")
)

type BlockChain struct {
	// db-related
	dbEnv         *lmdb.Env
	db            lmdb.DBI
	metadataIndex *utils.Index
	heightIndex   *utils.Index

	bus         EventBus.Bus
	miner       *Miner
	drandBeacon *drand2.DrandBeacon
}

func NewBlockChain(path string, bus EventBus.Bus, miner *Miner, db *drand2.DrandBeacon) (*BlockChain, error) {
	chain := &BlockChain{
		bus:         bus,
		miner:       miner,
		drandBeacon: db,
	}

	// configure lmdb env
	env, err := lmdb.NewEnv()
	if err != nil {
		return nil, err
	}

	err = env.SetMaxDBs(1)
	if err != nil {
		return nil, err
	}
	err = env.SetMapSize(100 * 1024 * 1024 * 1024) // 100 GB
	if err != nil {
		return nil, err
	}

	err = os.MkdirAll(path, 0755)
	if err != nil {
		return nil, err
	}

	err = env.Open(path, 0, 0755)
	if err != nil {
		return nil, err
	}

	chain.dbEnv = env

	var dbi lmdb.DBI
	err = env.Update(func(txn *lmdb.Txn) error {
		dbi, err = txn.OpenDBI("blocks", lmdb.Create)
		return err
	})
	if err != nil {
		return nil, err
	}

	chain.db = dbi

	// create index instances
	metadataIndex := utils.NewIndex(DefaultMetadataIndexName, env, dbi)
	heightIndex := utils.NewIndex("height", env, dbi)
	chain.metadataIndex = metadataIndex
	chain.heightIndex = heightIndex

	return chain, nil
}

func (bc *BlockChain) setLatestBlockHeight(height uint64) error {
	err := bc.metadataIndex.PutUint64([]byte(LatestBlockHeightKey), height)
	if err != nil {
		return err
	}
	return nil
}

func (bc *BlockChain) GetLatestBlockHeight() (uint64, error) {
	height, err := bc.metadataIndex.GetUint64([]byte(LatestBlockHeightKey))
	if err != nil {
		if err == utils.ErrIndexKeyNotFound {
			return 0, ErrLatestHeightNil
		}
		return 0, err
	}
	return height, nil
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

	err := bc.dbEnv.Update(func(txn *lmdb.Txn) error {
		data, err := cbor.Marshal(block.Data)
		if err != nil {
			return err
		}
		headerData, err := cbor.Marshal(block.Header)
		if err != nil {
			return err
		}
		blockHash := hex.EncodeToString(block.Header.Hash)
		err = txn.Put(bc.db, []byte(DefaultBlockDataPrefix+blockHash), data, 0)
		if err != nil {
			return err
		}
		err = txn.Put(bc.db, []byte(DefaultBlockHeaderPrefix+blockHash), headerData, 0) // store header separately for easy fetching
		return err
	})
	if err != nil {
		return err
	}

	// update index "height -> block hash"
	heightBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(heightBytes, block.Header.Height)
	err = bc.heightIndex.PutBytes(heightBytes, block.Header.Hash)
	if err != nil {
		return err
	}

	// update latest block height
	height, err := bc.GetLatestBlockHeight()
	if err != nil && err != ErrLatestHeightNil {
		return err
	}

	if err == ErrLatestHeightNil || block.Header.Height > height {
		if err = bc.setLatestBlockHeight(block.Header.Height); err != nil {
			return err
		}
		bc.bus.Publish("blockchain:latestBlockHeightUpdated", block)
	}
	bc.bus.Publish("blockchain:blockCommitted", block)
	return nil
}

func (bc *BlockChain) HasBlock(blockHash []byte) (bool, error) {
	var blockExists bool
	err := bc.dbEnv.View(func(txn *lmdb.Txn) error {
		h := hex.EncodeToString(blockHash)
		_, err := txn.Get(bc.db, []byte(DefaultBlockHeaderPrefix+h)) // try to fetch block header
		if err != nil {
			if lmdb.IsNotFound(err) {
				blockExists = false
				return nil
			}
			return err
		}
		blockExists = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return blockExists, nil
}

func (bc *BlockChain) FetchBlockData(blockHash []byte) ([]*types2.Transaction, error) {
	var data []*types2.Transaction
	err := bc.dbEnv.View(func(txn *lmdb.Txn) error {
		h := hex.EncodeToString(blockHash)
		blockData, err := txn.Get(bc.db, []byte(DefaultBlockDataPrefix+h))
		if err != nil {
			if lmdb.IsNotFound(err) {
				return ErrBlockNotFound
			}
			return err
		}
		err = cbor.Unmarshal(blockData, &data)
		return err
	})
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (bc *BlockChain) FetchBlockHeader(blockHash []byte) (*types2.BlockHeader, error) {
	var blockHeader types2.BlockHeader
	err := bc.dbEnv.View(func(txn *lmdb.Txn) error {
		h := hex.EncodeToString(blockHash)
		data, err := txn.Get(bc.db, []byte(DefaultBlockHeaderPrefix+h))
		if err != nil {
			if lmdb.IsNotFound(err) {
				return ErrBlockNotFound
			}
			return err
		}
		err = cbor.Unmarshal(data, &blockHeader)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &blockHeader, nil
}

func (bc *BlockChain) FetchBlock(blockHash []byte) (*types2.Block, error) {
	var block types2.Block
	header, err := bc.FetchBlockHeader(blockHash)
	if err != nil {
		return nil, err
	}
	block.Header = header

	data, err := bc.FetchBlockData(blockHash)
	if err != nil {
		return nil, err
	}
	block.Data = data

	return &block, nil
}

func (bc *BlockChain) FetchBlockByHeight(height uint64) (*types2.Block, error) {
	var heightBytes = make([]byte, 8)
	binary.LittleEndian.PutUint64(heightBytes, height)
	blockHash, err := bc.heightIndex.GetBytes(heightBytes)
	if err != nil {
		if err == utils.ErrIndexKeyNotFound {
			return nil, ErrBlockNotFound
		}
	}
	block, err := bc.FetchBlock(blockHash)
	if err != nil {
		return nil, err
	}
	return block, nil
}

func (bc *BlockChain) FetchBlockHeaderByHeight(height uint64) (*types2.BlockHeader, error) {
	var heightBytes = make([]byte, 8)
	binary.LittleEndian.PutUint64(heightBytes, height)
	blockHash, err := bc.heightIndex.GetBytes(heightBytes)
	if err != nil {
		if err == utils.ErrIndexKeyNotFound {
			return nil, ErrBlockNotFound
		}
	}
	blockHeader, err := bc.FetchBlockHeader(blockHash)
	if err != nil {
		return nil, err
	}
	return blockHeader, nil
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
