package types

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/wealdtech/go-merkletree"

	"github.com/ethereum/go-ethereum/crypto"
)

type Transaction struct {
	Hash        []byte
	MerkleProof *merkletree.Proof // sets when transaction is added to block
	Timestamp   time.Time
	Data        []byte
}

func CreateTransaction(data []byte) *Transaction {
	timestamp := time.Now()
	encodedData := hex.EncodeToString(data)
	hash := crypto.Keccak256([]byte(fmt.Sprintf("%s", encodedData)))
	return &Transaction{
		Hash:      hash,
		Timestamp: timestamp,
		Data:      data,
	}
}

func (tx *Transaction) ValidateHash() bool {
	encodedData := hex.EncodeToString(tx.Data)
	h := crypto.Keccak256([]byte(fmt.Sprintf("%s", encodedData)))
	return bytes.Equal(h, tx.Hash)
}
