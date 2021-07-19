package beacon

import (
	"fmt"

	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
)

func ComputeVRF(privKey crypto.PrivKey, sigInput []byte) ([]byte, error) {
	return privKey.Sign(sigInput)
}

func VerifyVRF(worker peer.ID, vrfBase, vrfproof []byte) error {
	pk, err := worker.ExtractPublicKey()
	if err != nil {
		return err
	}
	ok, err := pk.Verify(vrfBase, vrfproof)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("vrf was invalid")
	}

	return nil
}
