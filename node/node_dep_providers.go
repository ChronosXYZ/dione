package node

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"runtime"

	"github.com/Secured-Finance/dione/blockchain/database/memory"

	"github.com/Secured-Finance/dione/blockchain/database/lmdb"

	"github.com/Secured-Finance/dione/blockchain/database"

	"github.com/Secured-Finance/dione/cache/inmemory"

	"github.com/Secured-Finance/dione/cache/redis"

	types2 "github.com/Secured-Finance/dione/blockchain/types"

	"github.com/Secured-Finance/dione/rpc"
	"github.com/Secured-Finance/dione/rpc/filecoin"
	solana2 "github.com/Secured-Finance/dione/rpc/solana"
	rtypes "github.com/Secured-Finance/dione/rpc/types"

	"github.com/sirupsen/logrus"

	"github.com/Secured-Finance/dione/blockchain"

	"github.com/libp2p/go-libp2p-core/protocol"

	gorpc "github.com/libp2p/go-libp2p-gorpc"

	"github.com/Secured-Finance/dione/cache"
	"github.com/Secured-Finance/dione/config"
	"github.com/Secured-Finance/dione/ethclient"
	"github.com/Secured-Finance/dione/pubsub"
	pex "github.com/Secured-Finance/go-libp2p-pex"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/discovery"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub2 "github.com/libp2p/go-libp2p-pubsub"
	"github.com/multiformats/go-multiaddr"
)

const (
	DioneProtocolID = protocol.ID("/dione/1.0")
)

func provideCacheManager(cfg *config.Config) cache.CacheManager {
	var backend cache.CacheManager
	switch cfg.CacheType {
	case config.CacheTypeInMemory:
		backend = inmemory.NewCacheManager()
	case config.CacheTypeRedis:
		backend = redis.NewCacheManager(cfg)
	default:
		backend = inmemory.NewCacheManager()
	}
	return backend
}

func provideBlockchainDatabase(cfg *config.Config) (database.Database, error) {
	var db database.Database
	switch cfg.Blockchain.DatabaseType {
	case config.BlockchainDatabaseInMemory:
		db = memory.NewDatabase()
	case config.BlockChainDatabaseLMDB:
		{
			if cfg.Blockchain.LMDB.DatabasePath == "" {
				return nil, fmt.Errorf("database path for lmdb database is empty")
			}
			l, err := lmdb.NewDatabase(cfg.Blockchain.LMDB.DatabasePath)
			if err != nil {
				return nil, err
			}
			db = l
		}
	default:
		db = memory.NewDatabase()
	}

	return db, nil
}

// FIXME: do we really need this?
//func provideWallet(peerID peer.ID, privKey []byte) (*wallet.LocalWallet, error) {
//	// TODO make persistent keystore
//	kstore := wallet.NewMemKeyStore()
//	keyInfo := types.KeyInfo{
//		Type:       types.KTEd25519,
//		PrivateKey: privKey,
//	}
//
//	kstore.Put(wallet.KNamePrefix+peerID.String(), keyInfo)
//	w, err := wallet.NewWallet(kstore)
//	if err != nil {
//		return nil, xerrors.Errorf("failed to setup wallet: %w", err)
//	}
//	return w, nil
//}

func provideEthereumClient(config *config.Config) *ethclient.EthereumClient {
	ethereum := ethclient.NewEthereumClient()
	err := ethereum.Initialize(&config.Ethereum)
	if err != nil {
		logrus.Fatal(err)
	}

	logrus.WithField("ethAddress", ethereum.GetEthAddress().Hex()).Info("Ethereum client has been initialized!")

	return ethereum
}

func providePubsub(h host.Host) (*pubsub2.PubSub, error) {
	return pubsub2.NewFloodSub(
		context.TODO(),
		h,
	)
}

func providePubsubRouter(h host.Host, ps *pubsub2.PubSub, config *config.Config) *pubsub.PubSubRouter {
	psb := pubsub.NewPubSubRouter(h, ps, config.PubSub.ServiceTopicName, config.IsBootstrap)
	logrus.Info("PubSub subsystem has been initialized!")
	return psb
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

func provideDirectRPCClient(h host.Host) *gorpc.Client {
	return gorpc.NewClient(h, DioneProtocolID)
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

		dirName := filepath.Dir(cfg.PrivateKeyPath)
		if _, err := os.Stat(dirName); os.IsNotExist(err) {
			err := os.MkdirAll(dirName, 0755)
			if err != nil {
				logrus.Fatalf("Cannot create private key file: %s", err.Error())
			}
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

func configureMiner(m *blockchain.Miner, b *blockchain.BlockChain) {
	m.SetBlockchainInstance(b)
}

func initializeBlockchain(bc *blockchain.BlockChain) {
	_, err := bc.GetLatestBlockHeight()
	if err == database.ErrLatestHeightNil {
		gBlock := types2.GenesisBlock()
		err = bc.StoreBlock(gBlock) // commit genesis block
		if err != nil {
			logrus.Fatal(err)
		}
		logrus.Info("Committed genesis block")
	}
}
