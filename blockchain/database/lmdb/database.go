package lmdb

import (
	"encoding/binary"
	"encoding/hex"
	"os"

	"github.com/Secured-Finance/dione/blockchain/database"
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

type Database struct {
	dbEnv         *lmdb.Env
	db            lmdb.DBI
	metadataIndex *Index
	heightIndex   *Index
}

func NewDatabase(path string) (*Database, error) {
	db := &Database{}

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

	db.dbEnv = env

	var dbi lmdb.DBI
	err = env.Update(func(txn *lmdb.Txn) error {
		dbi, err = txn.OpenDBI("blocks", lmdb.Create)
		return err
	})
	if err != nil {
		return nil, err
	}

	db.db = dbi

	// create index instances
	metadataIndex := NewIndex(DefaultMetadataIndexName, env, dbi)
	heightIndex := NewIndex("height", env, dbi)
	db.metadataIndex = metadataIndex
	db.heightIndex = heightIndex

	return db, nil
}

func (d *Database) StoreBlock(block *types2.Block) error {
	err := d.dbEnv.Update(func(txn *lmdb.Txn) error {
		data, err := cbor.Marshal(block.Data)
		if err != nil {
			return err
		}
		headerData, err := cbor.Marshal(block.Header)
		if err != nil {
			return err
		}
		blockHash := hex.EncodeToString(block.Header.Hash)
		err = txn.Put(d.db, []byte(DefaultBlockDataPrefix+blockHash), data, 0)
		if err != nil {
			return err
		}
		err = txn.Put(d.db, []byte(DefaultBlockHeaderPrefix+blockHash), headerData, 0) // store header separately for easy fetching
		return err
	})
	if err != nil {
		return err
	}

	// update index "height -> block hash"
	heightBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(heightBytes, block.Header.Height)
	err = d.heightIndex.PutBytes(heightBytes, block.Header.Hash)
	if err != nil {
		return err
	}

	return nil
}

func (d *Database) HasBlock(blockHash []byte) (bool, error) {
	var blockExists bool
	err := d.dbEnv.View(func(txn *lmdb.Txn) error {
		h := hex.EncodeToString(blockHash)
		_, err := txn.Get(d.db, []byte(DefaultBlockHeaderPrefix+h)) // try to fetch block header
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

func (d *Database) FetchBlockData(blockHash []byte) ([]*types2.Transaction, error) {
	var data []*types2.Transaction
	err := d.dbEnv.View(func(txn *lmdb.Txn) error {
		h := hex.EncodeToString(blockHash)
		blockData, err := txn.Get(d.db, []byte(DefaultBlockDataPrefix+h))
		if err != nil {
			if lmdb.IsNotFound(err) {
				return database.ErrBlockNotFound
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

func (d *Database) FetchBlockHeader(blockHash []byte) (*types2.BlockHeader, error) {
	var blockHeader types2.BlockHeader
	err := d.dbEnv.View(func(txn *lmdb.Txn) error {
		h := hex.EncodeToString(blockHash)
		data, err := txn.Get(d.db, []byte(DefaultBlockHeaderPrefix+h))
		if err != nil {
			if lmdb.IsNotFound(err) {
				return database.ErrBlockNotFound
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

func (d *Database) FetchBlock(blockHash []byte) (*types2.Block, error) {
	var block types2.Block
	header, err := d.FetchBlockHeader(blockHash)
	if err != nil {
		return nil, err
	}
	block.Header = header

	data, err := d.FetchBlockData(blockHash)
	if err != nil {
		return nil, err
	}
	block.Data = data

	return &block, nil
}

func (d *Database) FetchBlockByHeight(height uint64) (*types2.Block, error) {
	var heightBytes = make([]byte, 8)
	binary.LittleEndian.PutUint64(heightBytes, height)
	blockHash, err := d.heightIndex.GetBytes(heightBytes)
	if err != nil {
		if err == ErrIndexKeyNotFound {
			return nil, database.ErrBlockNotFound
		}
	}
	block, err := d.FetchBlock(blockHash)
	if err != nil {
		return nil, err
	}
	return block, nil
}

func (d *Database) FetchBlockHeaderByHeight(height uint64) (*types2.BlockHeader, error) {
	var heightBytes = make([]byte, 8)
	binary.LittleEndian.PutUint64(heightBytes, height)
	blockHash, err := d.heightIndex.GetBytes(heightBytes)
	if err != nil {
		if err == ErrIndexKeyNotFound {
			return nil, database.ErrBlockNotFound
		}
	}
	blockHeader, err := d.FetchBlockHeader(blockHash)
	if err != nil {
		return nil, err
	}
	return blockHeader, nil
}

func (d *Database) GetLatestBlockHeight() (uint64, error) {
	height, err := d.metadataIndex.GetUint64([]byte(LatestBlockHeightKey))
	if err != nil {
		if err == ErrIndexKeyNotFound {
			return 0, database.ErrLatestHeightNil
		}
		return 0, err
	}
	return height, nil
}

func (d *Database) SetLatestBlockHeight(height uint64) error {
	err := d.metadataIndex.PutUint64([]byte(LatestBlockHeightKey), height)
	if err != nil {
		return err
	}
	return nil
}
