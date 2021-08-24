package node

import (
	"context"
	"time"

	"github.com/Secured-Finance/dione/blockchain"

	drand2 "github.com/Secured-Finance/dione/beacon/drand"

	"github.com/Secured-Finance/dione/pubsub"

	"github.com/Secured-Finance/dione/consensus"

	"github.com/Secured-Finance/dione/blockchain/sync"

	"go.uber.org/fx"

	"github.com/fxamacker/cbor/v2"

	"github.com/Secured-Finance/dione/types"

	types2 "github.com/Secured-Finance/dione/blockchain/types"

	"github.com/Secured-Finance/dione/blockchain/pool"

	"github.com/libp2p/go-libp2p-core/discovery"

	"github.com/Secured-Finance/dione/rpc"

	"golang.org/x/xerrors"

	"github.com/Secured-Finance/dione/config"
	"github.com/Secured-Finance/dione/ethclient"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/sirupsen/logrus"
)

const (
	DefaultPEXUpdateTime = 6 * time.Second
)

func runNode(
	lc fx.Lifecycle,
	cfg *config.Config,
	disco discovery.Discovery,
	ethClient *ethclient.EthereumClient,
	h host.Host,
	mp *pool.Mempool,
	syncManager sync.SyncManager,
	consensusManager *consensus.PBFTConsensusManager,
	pubSubRouter *pubsub.PubSubRouter,
	disputeManager *consensus.DisputeManager,
	db *drand2.DrandBeacon,
) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			err := runLibp2pAsync(context.TODO(), h, cfg, disco)
			if err != nil {
				return err
			}

			err = db.Run(context.TODO())
			if err != nil {
				return err
			}

			// Run pubsub router
			pubSubRouter.Run()

			// Subscribe on new requests event channel from Ethereum
			err = subscribeOnEthContractsAsync(context.TODO(), ethClient, mp)
			if err != nil {
				return err
			}

			// Run blockchain sync manager
			syncManager.Run()

			// Run dispute manager
			disputeManager.Run(context.TODO())

			return nil
		},
		OnStop: func(ctx context.Context) error {
			// TODO
			return nil
		},
	})
}

func runLibp2pAsync(ctx context.Context, h host.Host, cfg *config.Config, disco discovery.Discovery) error {
	logrus.Info("Announcing ourselves...")
	_, err := disco.Advertise(context.TODO(), cfg.Rendezvous)
	if err != nil {
		return xerrors.Errorf("failed to announce this node to the network: %v", err)
	}
	logrus.Info("Successfully announced!")

	// Discover unbounded count of peers
	logrus.Info("Searching for other peers...")
	peerChan, err := disco.FindPeers(context.TODO(), cfg.Rendezvous)
	if err != nil {
		return xerrors.Errorf("failed to find new peers: %v", err)
	}
	go func() {
	MainLoop:
		for {
			select {
			case <-ctx.Done():
				break MainLoop
			case newPeer := <-peerChan:
				{
					if len(newPeer.Addrs) == 0 {
						continue
					}
					if newPeer.ID.String() == h.ID().String() {
						continue
					}
					logrus.WithField("peer", newPeer.ID).Info("Discovered new peer, connecting...")
					// Connect to the peer
					if err := h.Connect(ctx, newPeer); err != nil {
						logrus.WithFields(logrus.Fields{
							"peer": newPeer.ID,
							"err":  err.Error(),
						}).Warn("Connection with newly discovered peer has been failed")
					}
					logrus.WithField("peer", newPeer.ID).Info("Connected to newly discovered peer")
				}
			}
		}
	}()
	return nil
}

func subscribeOnEthContractsAsync(ctx context.Context, ethClient *ethclient.EthereumClient, mp *pool.Mempool) error {
	eventChan, subscription, err := ethClient.SubscribeOnOracleEvents(ctx)
	if err != nil {
		return err
	}

	go func() {
	EventLoop:
		for {
			select {
			case event := <-eventChan:
				{
					rpcMethod := rpc.GetRPCMethod(event.OriginChain, event.RequestType)
					if rpcMethod == nil {
						logrus.Errorf("Invalid RPC method name/type %d/%s for oracle request %s", event.OriginChain, event.RequestType, event.ReqID.String())
						continue
					}
					res, err := rpcMethod(event.RequestParams)
					if err != nil {
						logrus.Errorf("Failed to invoke RPC method for oracle request %s: %s", event.ReqID.String(), err.Error())
						continue
					}
					task := &types.DioneTask{
						OriginChain:   event.OriginChain,
						RequestType:   event.RequestType,
						RequestParams: event.RequestParams,
						Payload:       res,
						RequestID:     event.ReqID.String(),
					}
					data, err := cbor.Marshal(task)
					if err != nil {
						logrus.Errorf("Failed to marshal RPC response for oracle request %s: %s", event.ReqID.String(), err.Error())
						continue
					}
					tx := types2.CreateTransaction(data)
					err = mp.StoreTx(tx)
					if err != nil {
						logrus.Errorf("Failed to store tx in mempool: %s", err.Error())
						continue
					}
				}
			case <-ctx.Done():
				break EventLoop
			case err := <-subscription.Err():
				logrus.Fatalf("Error has occurred in subscription to Ethereum event channel: %s", err.Error())
			}
		}
	}()

	return nil
}

func Start() {
	fx.New(
		fx.Provide(
			provideEventBus,
			provideAppFlags,
			provideConfig,
			providePrivateKey,
			provideLibp2pHost,
			provideEthereumClient,
			providePubsubRouter,
			provideBootstrapAddrs,
			providePeerDiscovery,
			provideDrandBeacon,
			provideMempool,
			blockchain.NewMiner,
			provideBlockChain,
			provideBlockPool,
			provideSyncManager,
			provideNetworkRPCHost,
			provideNetworkService,
			provideDirectRPCClient,
			provideConsensusManager,
			consensus.NewDisputeManager,
			provideCacheManager,
		),
		fx.Invoke(
			configureLogger,
			configureDirectRPC,
			configureForeignBlockchainRPC,
			initializeBlockchain,
			configureMiner,
			runNode,
		),
		fx.NopLogger,
	).Run()
}
