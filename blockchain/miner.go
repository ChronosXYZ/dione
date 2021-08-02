package blockchain

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/libp2p/go-libp2p-core/host"

	"github.com/asaskevich/EventBus"

	"github.com/Secured-Finance/dione/beacon"
	"github.com/Secured-Finance/dione/types"

	"github.com/Secured-Finance/dione/blockchain/pool"

	"github.com/libp2p/go-libp2p-core/crypto"

	types2 "github.com/Secured-Finance/dione/blockchain/types"

	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/Secured-Finance/dione/ethclient"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
)

var (
	ErrNoTxForBlock = fmt.Errorf("no transactions for including into block")
)

type Miner struct {
	bus               EventBus.Bus
	address           peer.ID
	ethClient         *ethclient.EthereumClient
	minerStake        *big.Int
	networkStake      *big.Int
	privateKey        crypto.PrivKey
	mempool           *pool.Mempool
	latestBlockHeader *types2.BlockHeader
	blockchain        *BlockChain
}

func NewMiner(
	h host.Host,
	ethClient *ethclient.EthereumClient,
	privateKey crypto.PrivKey,
	mempool *pool.Mempool,
	bus EventBus.Bus,
) *Miner {
	m := &Miner{
		address:    h.ID(),
		ethClient:  ethClient,
		privateKey: privateKey,
		mempool:    mempool,
		bus:        bus,
	}

	return m
}

func (m *Miner) SetBlockchainInstance(b *BlockChain) {
	m.blockchain = b

	m.bus.SubscribeAsync("blockchain:latestBlockHeightUpdated", func(block *types2.Block) {
		m.latestBlockHeader = block.Header
	}, true)

	height, _ := m.blockchain.GetLatestBlockHeight()
	header, err := m.blockchain.FetchBlockHeaderByHeight(height)
	if err != nil {
		logrus.WithField("err", err.Error()).Fatal("Failed to initialize miner subsystem")
	}
	m.latestBlockHeader = header

	logrus.Info("Mining subsystem has been initialized!")
}

func (m *Miner) UpdateCurrentStakeInfo() error {
	mStake, err := m.ethClient.GetMinerStake(*m.ethClient.GetEthAddress())

	if err != nil {
		logrus.Warn("Can't get miner stake", err)
		return err
	}

	nStake, err := m.ethClient.GetTotalStake()

	if err != nil {
		logrus.Warn("Can't get miner stake", err)
		return err
	}

	m.minerStake = mStake
	m.networkStake = nStake

	return nil
}

func (m *Miner) GetStakeInfo(miner common.Address) (*big.Int, *big.Int, error) {
	mStake, err := m.ethClient.GetMinerStake(miner)

	if err != nil {
		logrus.Warn("Can't get miner stake", err)
		return nil, nil, err
	}

	nStake, err := m.ethClient.GetTotalStake()

	if err != nil {
		logrus.Warn("Can't get miner stake", err)
		return nil, nil, err
	}

	return mStake, nStake, nil
}

func (m *Miner) MineBlock(randomness []byte, randomnessRound uint64) (*types2.Block, error) {
	if m.latestBlockHeader == nil {
		return nil, fmt.Errorf("latest block header is null")
	}

	logrus.WithField("height", m.latestBlockHeader.Height+1).Debug("Trying to mine new block...")

	if err := m.UpdateCurrentStakeInfo(); err != nil {
		return nil, fmt.Errorf("failed to update miner stake: %w", err)
	}

	winner, err := isRoundWinner(
		m.latestBlockHeader.Height+1,
		m.address,
		randomness,
		randomnessRound,
		m.minerStake,
		m.networkStake,
		m.privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to check if we winned in next round: %w", err)
	}

	if winner == nil {
		logrus.WithField("height", m.latestBlockHeader.Height+1).Debug("Block is not mined because we are not leader in consensus round")
		return nil, nil
	}

	logrus.WithField("height", m.latestBlockHeader.Height+1).Infof("We have been elected in the current consensus round")

	txs := m.mempool.GetTransactionsForNewBlock()
	if txs == nil {
		return nil, ErrNoTxForBlock // skip new consensus round because there is no transaction for processing
	}

	newBlock, err := types2.CreateBlock(m.latestBlockHeader, txs, *m.ethClient.GetEthAddress(), m.privateKey, winner)
	if err != nil {
		return nil, fmt.Errorf("failed to create new block: %w", err)
	}

	return newBlock, nil
}

func (m *Miner) IsMinerEligibleToProposeBlock(ethAddress common.Address) error {
	mStake, err := m.ethClient.GetMinerStake(ethAddress)
	if err != nil {
		return err
	}
	if mStake.Cmp(big.NewInt(ethclient.MinMinerStake)) == -1 {
		return errors.New("miner doesn't have enough staked tokens")
	}
	return nil
}

func isRoundWinner(round uint64,
	worker peer.ID, randomness []byte, randomnessRound uint64, minerStake, networkStake *big.Int, privKey crypto.PrivKey) (*types.ElectionProof, error) {

	buf, err := worker.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal address: %w", err)
	}

	electionRand, err := beacon.DrawRandomness(randomness, beacon.RandomnessTypeElectionProofProduction, round, buf)
	if err != nil {
		return nil, fmt.Errorf("failed to draw randomness: %w", err)
	}

	vrfout, err := beacon.ComputeVRF(privKey, electionRand)
	if err != nil {
		return nil, fmt.Errorf("failed to compute VRF: %w", err)
	}

	ep := &types.ElectionProof{VRFProof: vrfout, RandomnessRound: randomnessRound}
	j := ep.ComputeWinCount(minerStake, networkStake)
	ep.WinCount = j
	if j < 1 {
		return nil, nil
	}

	return ep, nil
}
