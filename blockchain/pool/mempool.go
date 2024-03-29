package pool

import (
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/asaskevich/EventBus"

	"github.com/sirupsen/logrus"

	types2 "github.com/Secured-Finance/dione/blockchain/types"

	"github.com/Secured-Finance/dione/consensus/policy"

	"github.com/Secured-Finance/dione/cache"
)

const (
	DefaultTxTTL    = 10 * time.Minute
	DefaultTxPrefix = "tx_"
)

var (
	ErrTxNotFound = errors.New("tx isn't found in mempool")
)

type Mempool struct {
	cache cache.Cache
	bus   EventBus.Bus
}

func NewMempool(bus EventBus.Bus, cm cache.CacheManager) (*Mempool, error) {
	mp := &Mempool{
		cache: cm.Cache("mempool"),
		bus:   bus,
	}

	logrus.Info("Mempool has been successfully initialized!")

	return mp, nil
}

func (mp *Mempool) StoreTx(tx *types2.Transaction) error {
	hashStr := hex.EncodeToString(tx.Hash)
	err := mp.cache.StoreWithTTL(DefaultTxPrefix+hashStr, tx, DefaultTxTTL)
	logrus.WithField("txHash", hex.EncodeToString(tx.Hash)).Info("Submitted new transaction in mempool")
	mp.bus.Publish("mempool:transactionAdded", tx)
	return err
}

func (mp *Mempool) DeleteTx(txHash []byte) error {
	hashStr := hex.EncodeToString(txHash)
	var tx types2.Transaction
	err := mp.cache.Get(DefaultTxPrefix+hashStr, &tx)
	if err != nil {
		return err
	}
	mp.cache.Delete(DefaultTxPrefix + hashStr)
	logrus.WithField("txHash", hex.EncodeToString(txHash)).Debugf("Deleted transaction from mempool")
	mp.bus.Publish("mempool:transactionRemoved", tx)
	return nil
}

func (mp *Mempool) GetTransactionsForNewBlock() []*types2.Transaction {
	var txForBlock []*types2.Transaction
	allTxs := mp.GetAllTransactions()
	sort.Slice(allTxs, func(i, j int) bool {
		return allTxs[i].Timestamp.Before(allTxs[j].Timestamp)
	})

	for i := 0; i < policy.BlockMaxTransactionCount; i++ {
		if len(allTxs) == 0 {
			break
		}
		tx := allTxs[0]     // get oldest tx
		allTxs = allTxs[1:] // pop tx
		txForBlock = append(txForBlock, tx)
	}

	return txForBlock
}

func (mp *Mempool) GetAllTransactions() []*types2.Transaction {
	var allTxs []*types2.Transaction

	for _, v := range mp.cache.Keys() {
		var tx types2.Transaction
		err := mp.cache.Get(v, &tx)
		if err != nil {
			logrus.Error(err)
			continue
		}
		allTxs = append(allTxs, &tx)
	}
	return allTxs
}

func (mp *Mempool) GetTransaction(hash []byte) (*types2.Transaction, error) {
	hashStr := hex.EncodeToString(hash)
	var tx types2.Transaction
	err := mp.cache.Get(DefaultTxPrefix+hashStr, &tx)

	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			return nil, ErrTxNotFound
		} else {
			return nil, err
		}
	}

	return &tx, nil
}
