package drand

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/Secured-Finance/dione/beacon"
	"github.com/asaskevich/EventBus"
	"github.com/drand/drand/chain"
	"github.com/drand/drand/client"
	httpClient "github.com/drand/drand/client/http"
	libp2pClient "github.com/drand/drand/lp2p/client"
	"github.com/drand/kyber"
	logging "github.com/ipfs/go-log"
	"github.com/sirupsen/logrus"
	"go.uber.org/zap/zapcore"

	dlog "github.com/drand/drand/log"
	kzap "github.com/go-kit/kit/log/zap"
	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/Secured-Finance/dione/config"
	"github.com/Secured-Finance/dione/lib"
	types "github.com/Secured-Finance/dione/types"
)

type DrandBeacon struct {
	DrandClient        client.Client
	PublicKey          kyber.Point
	drandResultChannel <-chan client.Result
	cacheLock          sync.Mutex
	localCache         map[uint64]types.BeaconEntry
	latestDrandRound   uint64
	bus                EventBus.Bus
}

func NewDrandBeacon(ps *pubsub.PubSub, bus EventBus.Bus) (*DrandBeacon, error) {
	cfg := config.NewDrandConfig()

	drandChain, err := chain.InfoFromJSON(bytes.NewReader([]byte(cfg.ChainInfo)))
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshal drand chain info: %w", err)
	}

	dlogger := dlog.NewKitLoggerFrom(kzap.NewZapSugarLogger(
		logging.Logger("drand").SugaredLogger.Desugar(), zapcore.InfoLevel))

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
		client.WithLogger(dlogger),
	}

	if ps != nil {
		opts = append(opts, libp2pClient.WithPubsub(ps))
	}

	drandClient, err := client.Wrap(clients, opts...)
	if err != nil {
		logrus.Fatal(fmt.Errorf("cannot create drand client: %w", err))
	}

	db := &DrandBeacon{
		DrandClient: drandClient,
		localCache:  make(map[uint64]types.BeaconEntry),
		bus:         bus,
		PublicKey:   drandChain.PublicKey,
	}

	logrus.Info("DRAND beacon subsystem has been initialized!")

	return db, nil
}

func (db *DrandBeacon) Run(ctx context.Context) error {
	db.drandResultChannel = db.DrandClient.Watch(ctx)
	err := db.getLatestDrandResult()
	if err != nil {
		return err
	}
	go db.loop(ctx)

	return nil
}

func (db *DrandBeacon) getLatestDrandResult() error {
	latestDround, err := db.DrandClient.Get(context.TODO(), 0)
	if err != nil {
		logrus.Errorf("failed to get latest drand round: %v", err)
		return err
	}
	db.cacheValue(newBeaconEntryFromDrandResult(latestDround))
	db.updateLatestDrandRound(latestDround.Round())
	return nil
}

func (db *DrandBeacon) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			{
				logrus.Debug("Stopping watching new DRAND entries...")
				return
			}
		case res := <-db.drandResultChannel:
			{
				db.cacheValue(newBeaconEntryFromDrandResult(res))
				db.updateLatestDrandRound(res.Round())
				db.bus.Publish("beacon:newEntry", types.NewBeaconEntry(res.Round(), res.Randomness(), map[string]interface{}{"signature": res.Signature()}))
			}
		}
	}
}

func (db *DrandBeacon) Entry(ctx context.Context, round uint64) (types.BeaconEntry, error) {
	if round != 0 {
		be := db.getCachedValue(round)
		if be != nil {
			return *be, nil
		}
	}

	start := lib.Clock.Now()
	logrus.Infof("start fetching randomness: round %v", round)
	resp, err := db.DrandClient.Get(ctx, round)
	if err != nil {
		return types.BeaconEntry{}, fmt.Errorf("drand failed Get request: %w", err)
	}
	logrus.Infof("done fetching randomness: round %v, took %v", round, lib.Clock.Since(start))
	return newBeaconEntryFromDrandResult(resp), nil
}
func (db *DrandBeacon) cacheValue(res types.BeaconEntry) {
	db.cacheLock.Lock()
	defer db.cacheLock.Unlock()
	db.localCache[res.Round] = res
}

func (db *DrandBeacon) getCachedValue(round uint64) *types.BeaconEntry {
	db.cacheLock.Lock()
	defer db.cacheLock.Unlock()
	v, ok := db.localCache[round]
	if !ok {
		return nil
	}
	return &v
}

func (db *DrandBeacon) updateLatestDrandRound(round uint64) {
	db.cacheLock.Lock()
	defer db.cacheLock.Unlock()
	db.latestDrandRound = round
}

func (db *DrandBeacon) VerifyEntry(curr, prev types.BeaconEntry) error {
	if prev.Round == 0 {
		return nil
	}
	if be := db.getCachedValue(curr.Round); be != nil {
		return nil
	}
	b := &chain.Beacon{
		PreviousSig: prev.Metadata["signature"].([]byte),
		Round:       curr.Round,
		Signature:   curr.Metadata["signature"].([]byte),
	}
	return chain.VerifyBeacon(db.PublicKey, b)
}

func (db *DrandBeacon) LatestBeaconRound() uint64 {
	db.cacheLock.Lock()
	defer db.cacheLock.Unlock()
	return db.latestDrandRound
}

func newBeaconEntryFromDrandResult(res client.Result) types.BeaconEntry {
	return types.NewBeaconEntry(res.Round(), res.Randomness(), map[string]interface{}{"signature": res.Signature()})
}

var _ beacon.BeaconAPI = (*DrandBeacon)(nil)
