package node

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"runtime"

	"github.com/Secured-Finance/dione/rpc"
	"github.com/Secured-Finance/dione/rpc/filecoin"
	solana2 "github.com/Secured-Finance/dione/rpc/solana"
	rtypes "github.com/Secured-Finance/dione/rpc/types"

	"github.com/sirupsen/logrus"

	"github.com/asaskevich/EventBus"

	"github.com/Secured-Finance/dione/blockchain"

	drand2 "github.com/Secured-Finance/dione/beacon/drand"

	"github.com/libp2p/go-libp2p-core/protocol"

	gorpc "github.com/libp2p/go-libp2p-gorpc"

	"github.com/Secured-Finance/dione/blockchain/sync"

	"github.com/Secured-Finance/dione/blockchain/pool"

	"github.com/Secured-Finance/dione/cache"
	"github.com/Secured-Finance/dione/config"
	"github.com/Secured-Finance/dione/consensus"
	"github.com/Secured-Finance/dione/ethclient"
	"github.com/Secured-Finance/dione/pubsub"
	"github.com/Secured-Finance/dione/types"
	"github.com/Secured-Finance/dione/wallet"
	pex "github.com/Secured-Finance/go-libp2p-pex"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/discovery"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/xerrors"
)

const (
	DioneProtocolID = protocol.ID("/dione/1.0")
)

func provideCache(config *config.Config) cache.Cache {
	var backend cache.Cache
	switch config.CacheType {
	case "in-memory":
		backend = cache.NewInMemoryCache()
	case "redis":
		backend = cache.NewRedisCache(config)
	default:
		backend = cache.NewInMemoryCache()
	}
	return backend
}

func provideDisputeManager(ethClient *ethclient.EthereumClient, pcm *consensus.PBFTConsensusManager, cfg *config.Config, bc *blockchain.BlockChain) *consensus.DisputeManager {
	dm, err := consensus.NewDisputeManager(context.TODO(), ethClient, pcm, cfg.Ethereum.DisputeVoteWindow, bc)
	if err != nil {
		logrus.Fatal(err)
	}

	logrus.Info("Dispute subsystem has been initialized!")

	return dm
}

func provideMiner(h host.Host, ethClient *ethclient.EthereumClient, privateKey crypto.PrivKey, mempool *pool.Mempool) *blockchain.Miner {
	miner := blockchain.NewMiner(h.ID(), *ethClient.GetEthAddress(), ethClient, privateKey, mempool)
	logrus.Info("Mining subsystem has been initialized!")
	return miner
}

func provideDrandBeacon(ps *pubsub.PubSubRouter, bus EventBus.Bus) *drand2.DrandBeacon {
	db, err := drand2.NewDrandBeacon(ps.Pubsub, bus)
	if err != nil {
		logrus.Fatalf("Failed to setup drand beacon: %s", err)
	}
	logrus.Info("DRAND beacon subsystem has been initialized!")
	return db
}

// FIXME: do we really need this?
func provideWallet(peerID peer.ID, privKey []byte) (*wallet.LocalWallet, error) {
	// TODO make persistent keystore
	kstore := wallet.NewMemKeyStore()
	keyInfo := types.KeyInfo{
		Type:       types.KTEd25519,
		PrivateKey: privKey,
	}

	kstore.Put(wallet.KNamePrefix+peerID.String(), keyInfo)
	w, err := wallet.NewWallet(kstore)
	if err != nil {
		return nil, xerrors.Errorf("failed to setup wallet: %w", err)
	}
	return w, nil
}

func provideEthereumClient(config *config.Config) *ethclient.EthereumClient {
	ethereum := ethclient.NewEthereumClient()
	err := ethereum.Initialize(&config.Ethereum)
	if err != nil {
		logrus.Fatal(err)
	}

	logrus.WithField("ethAddress", ethereum.GetEthAddress().Hex()).Info("Ethereum client has been initialized!")

	return ethereum
}

func providePubsubRouter(lhost host.Host, config *config.Config) *pubsub.PubSubRouter {
	psb := pubsub.NewPubSubRouter(lhost, config.PubSub.ServiceTopicName, config.IsBootstrap)
	logrus.Info("PubSub subsystem has been initialized!")
	return psb
}

func provideConsensusManager(
	h host.Host,
	cfg *config.Config,
	bus EventBus.Bus,
	psb *pubsub.PubSubRouter,
	miner *blockchain.Miner,
	bc *blockchain.BlockChain,
	ethClient *ethclient.EthereumClient,
	privateKey crypto.PrivKey,
	bp *pool.BlockPool,
	db *drand2.DrandBeacon,
	mp *pool.Mempool,
) *consensus.PBFTConsensusManager {
	c := consensus.NewPBFTConsensusManager(
		bus,
		psb,
		cfg.ConsensusMinApprovals,
		privateKey,
		ethClient,
		miner,
		bc,
		bp,
		db,
		mp,
		h.ID(),
	)
	logrus.Info("Consensus subsystem has been initialized!")
	return c
}

func provideLibp2pHost(config *config.Config, privateKey crypto.PrivKey) host.Host {
	listenMultiAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", config.ListenAddr, config.ListenPort))
	if err != nil {
		logrus.Fatalf("Failed to parse multiaddress: %s", err.Error())
	}
	libp2pHost, err := libp2p.New(
		context.TODO(),
		libp2p.ListenAddrs(listenMultiAddr),
		libp2p.Identity(privateKey),
	)
	if err != nil {
		logrus.Fatal(err)
	}

	logrus.WithField(
		"multiaddress",
		fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s",
			config.ListenAddr,
			config.ListenPort,
			libp2pHost.ID().Pretty(),
		)).Info("Libp2p host has been initialized!")

	return libp2pHost
}

func provideNetworkRPCHost(h host.Host) *gorpc.Server {
	return gorpc.NewServer(h, DioneProtocolID)
}

func provideBootstrapAddrs(c *config.Config) []multiaddr.Multiaddr {
	if c.IsBootstrap {
		return nil
	}

	var bootstrapMaddrs []multiaddr.Multiaddr
	for _, a := range c.BootstrapNodes {
		maddr, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			logrus.Fatalf("Invalid multiaddress of bootstrap node: %v", err)
		}
		bootstrapMaddrs = append(bootstrapMaddrs, maddr)
	}

	return bootstrapMaddrs
}

func providePeerDiscovery(baddrs []multiaddr.Multiaddr, h host.Host) discovery.Discovery {
	pexDiscovery, err := pex.NewPEXDiscovery(h, baddrs, DefaultPEXUpdateTime)
	if err != nil {
		logrus.Fatalf("Failed to setup libp2p PEX discovery: %s", err.Error())
	}

	logrus.Info("Peer discovery subsystem has been initialized!")

	return pexDiscovery
}

func provideBlockChain(config *config.Config, bus EventBus.Bus, miner *blockchain.Miner, db *drand2.DrandBeacon) *blockchain.BlockChain {
	bc, err := blockchain.NewBlockChain(config.Blockchain.DatabasePath, bus, miner, db)
	if err != nil {
		logrus.Fatalf("Failed to initialize blockchain storage: %s", err.Error())
	}
	logrus.Info("Blockchain storage has been successfully initialized!")

	return bc
}

func provideMempool(bus EventBus.Bus) *pool.Mempool {
	mp, err := pool.NewMempool(bus)
	if err != nil {
		logrus.Fatalf("Failed to initialize mempool: %s", err.Error())
	}

	logrus.Info("Mempool has been successfully initialized!")

	return mp
}

func provideSyncManager(
	bus EventBus.Bus,
	bp *blockchain.BlockChain,
	mp *pool.Mempool,
	c *gorpc.Client,
	bootstrapAddresses []multiaddr.Multiaddr,
	psb *pubsub.PubSubRouter,
) sync.SyncManager {
	bootstrapPeerID := peer.ID("")

	if bootstrapAddresses != nil {
		addr, err := peer.AddrInfoFromP2pAddr(bootstrapAddresses[0]) // FIXME
		if err != nil {
			logrus.Fatal(err)
		}
		bootstrapPeerID = addr.ID
	}

	sm := sync.NewSyncManager(bus, bp, mp, c, bootstrapPeerID, psb)
	logrus.Info("Blockchain sync subsystem has been successfully initialized!")

	return sm
}

func provideDirectRPCClient(h host.Host) *gorpc.Client {
	return gorpc.NewClient(h, DioneProtocolID)
}

func provideNetworkService(bp *blockchain.BlockChain, mp *pool.Mempool) *NetworkService {
	ns := NewNetworkService(bp, mp)
	logrus.Info("Direct RPC has been successfully initialized!")
	return ns
}

func provideBlockPool(mp *pool.Mempool, bus EventBus.Bus) *pool.BlockPool {
	bp, err := pool.NewBlockPool(mp, bus)
	if err != nil {
		logrus.Fatalf("Failed to initialize blockpool: %s", err.Error())
	}
	logrus.Info("Blockpool has been successfully initialized!")
	return bp
}

func provideEventBus() EventBus.Bus {
	return EventBus.New()
}

func provideAppFlags() *AppFlags {
	var flags AppFlags

	flag.StringVar(&flags.ConfigPath, "config", "", "Path to config")
	flag.BoolVar(&flags.Verbose, "verbose", false, "Verbose logging")

	flag.Parse()

	return &flags
}

func provideConfig(flags *AppFlags) *config.Config {
	if flags.ConfigPath == "" {
		logrus.Fatal("no config path provided")

	}

	cfg, err := config.NewConfig(flags.ConfigPath)
	if err != nil {
		logrus.Fatalf("failed to load config: %v", err)
	}

	return cfg
}

func providePrivateKey(cfg *config.Config) crypto.PrivKey {
	var privateKey crypto.PrivKey

	if _, err := os.Stat(cfg.PrivateKeyPath); os.IsNotExist(err) {
		privateKey, err = generatePrivateKey()
		if err != nil {
			logrus.Fatal(err)
		}

		f, err := os.Create(cfg.PrivateKeyPath)
		if err != nil {
			logrus.Fatalf("Cannot create private key file: %s, ", err)
		}

		r, err := privateKey.Raw()
		if err != nil {
			logrus.Fatal(err)
		}

		_, err = f.Write(r)
		if err != nil {
			logrus.Fatal(err)
		}
	} else {
		pkey, err := ioutil.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			logrus.Fatal(err)
		}

		privateKey, err = crypto.UnmarshalEd25519PrivateKey(pkey)
		if err != nil {
			logrus.Fatal(err)
		}
	}

	return privateKey
}

func generatePrivateKey() (crypto.PrivKey, error) {
	r := rand.Reader
	// Creates a new RSA key pair for this host.
	prvKey, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, r)
	if err != nil {
		return nil, err
	}
	return prvKey, nil
}

func configureDirectRPC(rpcServer *gorpc.Server, ns *NetworkService) {
	err := rpcServer.Register(ns)
	if err != nil {
		logrus.Fatal(err)
	}
}

func configureLogger(flags *AppFlags) {
	logrus.SetReportCaller(true)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
		CallerPrettyfier: func(f *runtime.Frame) (string, string) {
			filename := path.Base(f.File)
			return "", fmt.Sprintf("%s:%d:", filename, f.Line)
		},
	})

	if flags.Verbose {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}
}

func configureForeignBlockchainRPC() {
	fc := filecoin.NewLotusClient()
	rpc.RegisterRPC(rtypes.RPCTypeFilecoin, map[string]func(string) ([]byte, error){
		"getTransaction": fc.GetTransaction,
		"getBlock":       fc.GetBlock,
	})

	sl := solana2.NewSolanaClient()
	rpc.RegisterRPC(rtypes.RPCTypeSolana, map[string]func(string) ([]byte, error){
		"getTransaction": sl.GetTransaction,
	})

	logrus.Info("Foreign Blockchain RPC clients has been successfully configured!")
}
