package ethclient

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/sirupsen/logrus"

	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"

	"github.com/Secured-Finance/dione/config"

	"github.com/Secured-Finance/dione/contracts/dioneDispute"
	"github.com/Secured-Finance/dione/contracts/dioneOracle"
	"github.com/Secured-Finance/dione/contracts/dioneStaking"
	stakingContract "github.com/Secured-Finance/dione/contracts/dioneStaking"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/event"
)

type ethereumClient struct {
	client          *ethclient.Client
	ethAddress      *common.Address
	authTransactor  *bind.TransactOpts
	dioneStaking    *stakingContract.DioneStakingSession
	disputeContract *dioneDispute.DioneDisputeSession
	dioneOracle     *dioneOracle.DioneOracleSession
}

type EthereumSideAPI interface {
	SubmitRequestAnswer(reqID *big.Int, data []byte) error
	BeginDispute(miner common.Address, requestID *big.Int) error
	VoteDispute(dhash [32]byte, voteStatus bool) error
	FinishDispute(dhash [32]byte) error
	SubscribeOnOracleEvents(ctx context.Context) (chan *dioneOracle.DioneOracleNewOracleRequest, event.Subscription, error)
	SubscribeOnNewDisputes(ctx context.Context) (chan *dioneDispute.DioneDisputeNewDispute, event.Subscription, error)
	SubscribeOnNewSubmissions(ctx context.Context) (chan *dioneOracle.DioneOracleSubmittedOracleRequest, event.Subscription, error)
	GetEthAddress() *common.Address
	GetTotalStake() (*big.Int, error)
	GetMinerStake(minerAddress common.Address) (*big.Int, error)
}

func NewEthereumClient(cfg *config.Config) (EthereumSideAPI, error) {
	c := &ethereumClient{}

	client, err := ethclient.Dial(cfg.Ethereum.GatewayAddress)
	if err != nil {
		return nil, err
	}
	c.client = client

	key, err := c.getPrivateKey(cfg)
	if err != nil {
		return nil, err
	}
	authTransactor, err := bind.NewKeyedTransactorWithChainID(key, big.NewInt(int64(cfg.Ethereum.ChainID)))
	if err != nil {
		return nil, err
	}
	c.authTransactor = authTransactor
	c.ethAddress = &c.authTransactor.From

	stakingContract, err := dioneStaking.NewDioneStaking(common.HexToAddress(cfg.Ethereum.DioneStakingContractAddress), client)
	if err != nil {
		return nil, err
	}
	oracleContract, err := dioneOracle.NewDioneOracle(common.HexToAddress(cfg.Ethereum.DioneOracleContractAddress), client)
	if err != nil {
		return nil, err
	}
	disputeContract, err := dioneDispute.NewDioneDispute(common.HexToAddress(cfg.Ethereum.DisputeContractAddress), client)
	if err != nil {
		return nil, err
	}
	c.dioneStaking = &dioneStaking.DioneStakingSession{
		Contract: stakingContract,
		CallOpts: bind.CallOpts{
			Pending: true,
			From:    authTransactor.From,
			Context: context.Background(),
		},
		TransactOpts: bind.TransactOpts{
			From:     authTransactor.From,
			Signer:   authTransactor.Signer,
			GasLimit: 0,   // 0 automatically estimates gas limit
			GasPrice: nil, // nil automatically suggests gas price
			Context:  context.Background(),
		},
	}
	c.disputeContract = &dioneDispute.DioneDisputeSession{
		Contract: disputeContract,
		CallOpts: bind.CallOpts{
			Pending: true,
			From:    authTransactor.From,
			Context: context.Background(),
		},
		TransactOpts: bind.TransactOpts{
			From:     authTransactor.From,
			Signer:   authTransactor.Signer,
			GasLimit: 0,   // 0 automatically estimates gas limit
			GasPrice: nil, // nil automatically suggests gas price
			Context:  context.Background(),
		},
	}
	c.dioneOracle = &dioneOracle.DioneOracleSession{
		Contract: oracleContract,
		CallOpts: bind.CallOpts{
			Pending: true,
			From:    authTransactor.From,
			Context: context.Background(),
		},
		TransactOpts: bind.TransactOpts{
			From:     authTransactor.From,
			Signer:   authTransactor.Signer,
			GasLimit: 0,   // 0 automatically estimates gas limit
			GasPrice: nil, // nil automatically suggests gas price
			Context:  context.Background(),
		},
	}

	logrus.WithField("ethAddress", c.GetEthAddress().Hex()).Info("Ethereum client has been initialized!")

	return c, nil
}

func (c *ethereumClient) getPrivateKey(cfg *config.Config) (*ecdsa.PrivateKey, error) {
	if cfg.Ethereum.PrivateKey != "" {
		key, err := crypto.HexToECDSA(cfg.Ethereum.PrivateKey)
		return key, err
	}

	if cfg.Ethereum.MnemonicPhrase != "" {
		wallet, err := hdwallet.NewFromMnemonic(cfg.Ethereum.MnemonicPhrase)
		if err != nil {
			return nil, err
		}
		path := hdwallet.DefaultBaseDerivationPath

		if cfg.Ethereum.HDDerivationPath != "" {
			parsedPath, err := hdwallet.ParseDerivationPath(cfg.Ethereum.HDDerivationPath)
			if err != nil {
				return nil, err
			}
			path = parsedPath
		}

		account, err := wallet.Derive(path, true)
		if err != nil {
			return nil, err
		}
		key, err := wallet.PrivateKey(account)
		if err != nil {
			return nil, err
		}
		return key, nil
	}

	return nil, fmt.Errorf("private key or mnemonic phrase isn't specified")
}

func (c *ethereumClient) GetEthAddress() *common.Address {
	return c.ethAddress
}

func (c *ethereumClient) SubscribeOnOracleEvents(ctx context.Context) (chan *dioneOracle.DioneOracleNewOracleRequest, event.Subscription, error) {
	resChan := make(chan *dioneOracle.DioneOracleNewOracleRequest)
	requestsFilter := c.dioneOracle.Contract.DioneOracleFilterer
	subscription, err := requestsFilter.WatchNewOracleRequest(&bind.WatchOpts{
		Start:   nil, //last block
		Context: ctx,
	}, resChan)
	if err != nil {
		return nil, nil, err
	}
	return resChan, subscription, err
}

func (c *ethereumClient) SubmitRequestAnswer(reqID *big.Int, data []byte) error {
	_, err := c.dioneOracle.SubmitOracleRequest(reqID, data)
	if err != nil {
		return err
	}

	return nil
}

func (c *ethereumClient) BeginDispute(miner common.Address, requestID *big.Int) error {
	_, err := c.disputeContract.BeginDispute(miner, requestID)
	if err != nil {
		return err
	}

	return nil
}

func (c *ethereumClient) VoteDispute(dhash [32]byte, voteStatus bool) error {
	_, err := c.disputeContract.Vote(dhash, voteStatus)
	if err != nil {
		return err
	}

	return nil
}

func (c *ethereumClient) FinishDispute(dhash [32]byte) error {
	_, err := c.disputeContract.FinishDispute(dhash)
	if err != nil {
		return err
	}

	return nil
}

func (c *ethereumClient) SubscribeOnNewDisputes(ctx context.Context) (chan *dioneDispute.DioneDisputeNewDispute, event.Subscription, error) {
	resChan := make(chan *dioneDispute.DioneDisputeNewDispute)
	requestsFilter := c.disputeContract.Contract.DioneDisputeFilterer
	subscription, err := requestsFilter.WatchNewDispute(&bind.WatchOpts{
		Start:   nil, //last block
		Context: ctx,
	}, resChan, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return resChan, subscription, err
}

func (c *ethereumClient) SubscribeOnNewSubmissions(ctx context.Context) (chan *dioneOracle.DioneOracleSubmittedOracleRequest, event.Subscription, error) {
	resChan := make(chan *dioneOracle.DioneOracleSubmittedOracleRequest)
	requestsFilter := c.dioneOracle.Contract.DioneOracleFilterer
	subscription, err := requestsFilter.WatchSubmittedOracleRequest(&bind.WatchOpts{
		Start:   nil, // last block
		Context: ctx,
	}, resChan)
	if err != nil {
		return nil, nil, err
	}
	return resChan, subscription, err
}
