package blockchain

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

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
	address      peer.ID
	ethAddress   common.Address
	mutex        sync.Mutex
	ethClient    *ethclient.EthereumClient
	minerStake   *big.Int
	networkStake *big.Int
	privateKey   crypto.PrivKey
	mempool      *pool.Mempool
}

func NewMiner(
	address peer.ID,
	ethAddress common.Address,
	ethClient *ethclient.EthereumClient,
	privateKey crypto.PrivKey,
	mempool *pool.Mempool,
) *Miner {
	return &Miner{
		address:    address,
		ethAddress: ethAddress,
		ethClient:  ethClient,
		privateKey: privateKey,
		mempool:    mempool,
	}
}

func (m *Miner) UpdateCurrentStakeInfo() error {
	mStake, err := m.ethClient.GetMinerStake(m.ethAddress)

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

func (m *Miner) MineBlock(randomness []byte, randomnessRound uint64, lastBlockHeader *types2.BlockHeader) (*types2.Block, error) {
	logrus.WithField("height", lastBlockHeader.Height+1).Debug("Trying to mine new block...")

	if err := m.UpdateCurrentStakeInfo(); err != nil {
		return nil, fmt.Errorf("failed to update miner stake: %w", err)
	}

	winner, err := isRoundWinner(
		lastBlockHeader.Height+1,
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
		logrus.WithField("height", lastBlockHeader.Height+1).Debug("Block is not mined because we are not leader in consensus round")
		return nil, nil
	}

	txs := m.mempool.GetTransactionsForNewBlock()
	if txs == nil {
		return nil, ErrNoTxForBlock // skip new consensus round because there is no transaction for processing
	}

	newBlock, err := types2.CreateBlock(lastBlockHeader, txs, m.ethAddress, m.privateKey, winner)
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
