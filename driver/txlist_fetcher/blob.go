package txlistdecoder

import (
	"context"
	"crypto/sha256"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/log"

	"github.com/taikoxyz/taiko-client/bindings"
	"github.com/taikoxyz/taiko-client/pkg/rpc"
)

// BlobFetcher is responsible for fetching the txList blob from the L1 block sidecar.
type BlobFetcher struct {
	rpc *rpc.Client
}

// NewBlobTxListFetcher creates a new BlobFetcher instance based on the given rpc client.
func NewBlobTxListFetcher(rpc *rpc.Client) *BlobFetcher {
	return &BlobFetcher{rpc}
}

// Fetch implements the TxListFetcher interface.
func (d *BlobFetcher) Fetch(
	ctx context.Context,
	_ *types.Transaction,
	meta *bindings.TaikoDataBlockMetadata,
) ([]byte, error) {
	if !meta.BlobUsed {
		return nil, errBlobUnused
	}

	// Fetch the L1 block sidecars.
	sidecars, err := d.rpc.L1Beacon.GetBlobs(ctx, new(big.Int).SetUint64(meta.L1Height+1))
	if err != nil {
		return nil, err
	}

	log.Info("Fetch sidecars", "slot", meta.L1Height+1, "sidecars", len(sidecars))

	// Compare the blob hash with the sidecar's kzg commitment.
	for i, sidecar := range sidecars {
		log.Info(
			"Block sidecar",
			"index", i,
			"KzgCommitment", sidecar.KzgCommitment,
			"blobHash", common.Bytes2Hex(meta.BlobHash[:]),
		)

		commitment := kzg4844.Commitment(common.FromHex(sidecar.KzgCommitment))
		if kzg4844.CalcBlobHashV1(
			sha256.New(),
			&commitment,
		) == common.BytesToHash(meta.BlobHash[:]) {
			blob := rpc.Blob(common.FromHex(sidecar.Blob))
			return blob.ToData()
		}
	}

	return nil, errSidecarNotFound
}
