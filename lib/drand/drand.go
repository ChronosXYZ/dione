package drand

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/drand/drand/chain"
	"github.com/drand/drand/client"
	httpClient "github.com/drand/drand/client/http"
	libp2pClient "github.com/drand/drand/lp2p/client"
	"github.com/drand/kyber"
	"github.com/sirupsen/logrus"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/Secured-Finance/dione/config"
	"github.com/Secured-Finance/dione/lib"
)

type DrandRes struct {
	// PreviousSig is the previous signature generated
	PreviousSig []byte
	// Round is the round number this beacon is tied to
	Round uint64
	// Signature is the BLS deterministic signature over Round || PreviousRand
	Signature []byte
	// Randomness for specific round generated by Drand
	Randomness []byte
}

type Beacon struct {
	DrandResponse DrandRes
	Error         error
}

type DrandBeacon struct {
	DrandClient      client.Client
	PublicKey        kyber.Point
	Interval         time.Duration
	drandGenesisTime uint64
	cacheLock        sync.Mutex
	localCache       map[uint64]DrandRes
}

func NewDrandBeacon(ps *pubsub.PubSub) (*DrandBeacon, error) {
	cfg := config.NewDrandConfig()

	drandChain, err := chain.InfoFromJSON(bytes.NewReader([]byte(cfg.ChainInfo)))
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal drand chain info: %w", err)
	}

	var clients []client.Client
	for _, url := range cfg.Servers {
		client, err := httpClient.NewWithInfo(url, drandChain, nil)
		if err != nil {
			return nil, fmt.Errorf("could not create http drand client: %w", err)
		}
		clients = append(clients, client)
	}

	opts := []client.Option{
		client.WithChainInfo(drandChain),
		client.WithCacheSize(1024),
		client.WithAutoWatch(),
	}

	if ps != nil {
		opts = append(opts, libp2pClient.WithPubsub(ps))
	} else {
		logrus.Info("Initiated drand with PubSub")
	}

	drandClient, err := client.Wrap(clients, opts...)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create Drand clients")
	}

	db := &DrandBeacon{
		DrandClient: drandClient,
		localCache:  make(map[uint64]DrandRes),
	}

	db.PublicKey = drandChain.PublicKey
	db.Interval = drandChain.Period
	db.drandGenesisTime = uint64(drandChain.GenesisTime)

	return db, nil
}

func (db *DrandBeacon) Entry(ctx context.Context, round uint64) <-chan Beacon {
	out := make(chan Beacon, 1)
	if round != 0 {
		res := db.getCachedValue(round)
		if res != nil {
			out <- Beacon{DrandResponse: *res}
			close(out)
			return out
		}
	}

	go func() {
		start := lib.Clock.Now()
		logrus.Info("start fetching randomness", "round", round)
		resp, err := db.DrandClient.Get(ctx, round)

		var res Beacon
		if err != nil {
			res.Error = fmt.Errorf("drand failed Get request: %w", err)
		} else {
			res.DrandResponse.Round = resp.Round()
			res.DrandResponse.Signature = resp.Signature()
		}
		logrus.Info("done fetching randomness", "round", round, "took", lib.Clock.Since(start))
		out <- res
		close(out)
	}()

	return out
}
func (db *DrandBeacon) cacheValue(res DrandRes) {
	db.cacheLock.Lock()
	defer db.cacheLock.Unlock()
	db.localCache[res.Round] = res
}

func (db *DrandBeacon) getCachedValue(round uint64) *DrandRes {
	db.cacheLock.Lock()
	defer db.cacheLock.Unlock()
	v, ok := db.localCache[round]
	if !ok {
		return nil
	}
	return &v
}

func (db *DrandBeacon) VerifyEntry(curr DrandRes, prev DrandRes) error {
	if prev.Round == 0 {
		return nil
	}
	if be := db.getCachedValue(curr.Round); be != nil {
		return nil
	}
	b := &chain.Beacon{
		PreviousSig: prev.PreviousSig,
		Round:       curr.Round,
		Signature:   curr.Signature,
	}
	err := chain.VerifyBeacon(db.PublicKey, b)
	if err == nil {
		db.cacheValue(curr)
	}
	return err
}
