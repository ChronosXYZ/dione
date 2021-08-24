package database

import (
	"errors"

	"github.com/Secured-Finance/dione/blockchain/types"
	types2 "github.com/Secured-Finance/dione/blockchain/types"
)

var (
	ErrBlockNotFound   = errors.New("block isn't found")
	ErrLatestHeightNil = errors.New("latest block height is nil")
)

type Database interface {
	StoreBlock(block *types.Block) error
	HasBlock(blockhash []byte) (bool, error)
	FetchBlockData(blockHash []byte) ([]*types2.Transaction, error)
	FetchBlockHeader(blockHash []byte) (*types2.BlockHeader, error)
	FetchBlock(blockHash []byte) (*types2.Block, error)
	FetchBlockByHeight(height uint64) (*types2.Block, error)
	FetchBlockHeaderByHeight(height uint64) (*types2.BlockHeader, error)
	GetLatestBlockHeight() (uint64, error)
	SetLatestBlockHeight(height uint64) error
}
