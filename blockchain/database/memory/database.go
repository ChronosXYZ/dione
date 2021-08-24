package memory

import (
	"encoding/hex"
	"fmt"

	"github.com/Secured-Finance/dione/blockchain/database"

	types2 "github.com/Secured-Finance/dione/blockchain/types"
	"github.com/patrickmn/go-cache"
)

const (
	LatestBlockHeightKey = "latest_block_height"
)

type Database struct {
	db *cache.Cache
}

func NewDatabase() *Database {
	return &Database{
		db: cache.New(cache.NoExpiration, 0),
	}
}

func (d *Database) StoreBlock(block *types2.Block) error {
	h := hex.EncodeToString(block.Header.Hash)
	d.db.SetDefault(h, block)
	d.db.SetDefault(fmt.Sprintf("height/%d", block.Header.Height), block)
	return nil
}

func (d *Database) HasBlock(blockHash []byte) (bool, error) {
	_, ok := d.db.Get(hex.EncodeToString(blockHash))
	return ok, nil
}

func (d *Database) FetchBlockData(blockHash []byte) ([]*types2.Transaction, error) {
	b, err := d.FetchBlock(blockHash)
	if err != nil {
		return nil, err
	}
	return b.Data, nil
}

func (d *Database) FetchBlockHeader(blockHash []byte) (*types2.BlockHeader, error) {
	b, err := d.FetchBlock(blockHash)
	if err != nil {
		return nil, err
	}
	return b.Header, nil
}

func (d *Database) FetchBlock(blockHash []byte) (*types2.Block, error) {
	b, ok := d.db.Get(hex.EncodeToString(blockHash))
	if !ok {
		return nil, database.ErrBlockNotFound
	}
	return b.(*types2.Block), nil
}

func (d *Database) FetchBlockByHeight(height uint64) (*types2.Block, error) {
	b, ok := d.db.Get(fmt.Sprintf("height/%d", height))
	if !ok {
		return nil, database.ErrBlockNotFound
	}
	return b.(*types2.Block), nil
}

func (d *Database) FetchBlockHeaderByHeight(height uint64) (*types2.BlockHeader, error) {
	b, ok := d.db.Get(fmt.Sprintf("height/%d", height))
	if !ok {
		return nil, database.ErrBlockNotFound
	}
	return b.(*types2.Block).Header, nil
}

func (d *Database) GetLatestBlockHeight() (uint64, error) {
	height, ok := d.db.Get(LatestBlockHeightKey)
	if !ok {
		return 0, database.ErrLatestHeightNil
	}
	return height.(uint64), nil
}

func (d *Database) SetLatestBlockHeight(height uint64) error {
	d.db.SetDefault(LatestBlockHeightKey, height)
	return nil
}
