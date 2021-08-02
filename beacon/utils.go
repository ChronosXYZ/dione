package beacon

import (
	"encoding/binary"

	"github.com/minio/blake2b-simd"
	"golang.org/x/xerrors"
)

func DrawRandomness(rbase []byte, randomnessType RandomnessType, round uint64, entropy []byte) ([]byte, error) {
	h := blake2b.New256()
	if err := binary.Write(h, binary.BigEndian, int64(randomnessType)); err != nil {
		return nil, xerrors.Errorf("deriving randomness: %v", err)
	}
	VRFDigest := blake2b.Sum256(rbase)
	_, err := h.Write(VRFDigest[:])
	if err != nil {
		return nil, xerrors.Errorf("hashing VRFDigest: %w", err)
	}
	if err := binary.Write(h, binary.BigEndian, round); err != nil {
		return nil, xerrors.Errorf("deriving randomness: %v", err)
	}
	_, err = h.Write(entropy)
	if err != nil {
		return nil, xerrors.Errorf("hashing entropy: %v", err)
	}

	return h.Sum(nil), nil
}
