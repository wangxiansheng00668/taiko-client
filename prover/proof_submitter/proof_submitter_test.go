package submitter

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/suite"

	"github.com/taikoxyz/taiko-client/bindings"
	"github.com/taikoxyz/taiko-client/bindings/encoding"
	"github.com/taikoxyz/taiko-client/driver/chain_syncer/beaconsync"
	"github.com/taikoxyz/taiko-client/driver/chain_syncer/calldata"
	"github.com/taikoxyz/taiko-client/driver/state"
	"github.com/taikoxyz/taiko-client/internal/testutils"
	"github.com/taikoxyz/taiko-client/pkg/rpc"
	"github.com/taikoxyz/taiko-client/pkg/sender"
	"github.com/taikoxyz/taiko-client/proposer"
	producer "github.com/taikoxyz/taiko-client/prover/proof_producer"
	"github.com/taikoxyz/taiko-client/prover/proof_submitter/transaction"
)

type ProofSubmitterTestSuite struct {
	testutils.ClientTestSuite
	submitter      *ProofSubmitter
	contester      *ProofContester
	calldataSyncer *calldata.Syncer
	proposer       *proposer.Proposer
	proofCh        chan *producer.ProofWithHeader
}

func (s *ProofSubmitterTestSuite) SetupTest() {
	s.ClientTestSuite.SetupTest()

	l1ProverPrivKey, err := crypto.ToECDSA(common.FromHex(os.Getenv("L1_PROVER_PRIVATE_KEY")))
	s.Nil(err)

	s.proofCh = make(chan *producer.ProofWithHeader, 1024)

	sender, err := sender.NewSender(context.Background(), &sender.Config{}, s.RPCClient.L1, l1ProverPrivKey)
	s.Nil(err)

	builder := transaction.NewProveBlockTxBuilder(s.RPCClient)

	s.submitter, err = NewProofSubmitter(
		s.RPCClient,
		&producer.OptimisticProofProducer{},
		s.proofCh,
		common.HexToAddress(os.Getenv("TAIKO_L2_ADDRESS")),
		"test",
		sender,
		builder,
	)
	s.Nil(err)
	s.contester = NewProofContester(
		s.RPCClient,
		sender,
		"test",
		builder,
	)

	// Init calldata syncer
	testState, err := state.New(context.Background(), s.RPCClient)
	s.Nil(err)
	s.Nil(testState.ResetL1Current(context.Background(), common.Big0))

	tracker := beaconsync.NewSyncProgressTracker(s.RPCClient.L2, 30*time.Second)

	s.calldataSyncer, err = calldata.NewSyncer(
		context.Background(),
		s.RPCClient,
		testState,
		tracker,
	)
	s.Nil(err)

	// Init proposer
	prop := new(proposer.Proposer)
	l1ProposerPrivKey, err := crypto.ToECDSA(common.FromHex(os.Getenv("L1_PROPOSER_PRIVATE_KEY")))
	s.Nil(err)

	s.Nil(prop.InitFromConfig(context.Background(), &proposer.Config{
		ClientConfig: &rpc.ClientConfig{
			L1Endpoint:        os.Getenv("L1_NODE_WS_ENDPOINT"),
			L2Endpoint:        os.Getenv("L2_EXECUTION_ENGINE_WS_ENDPOINT"),
			TaikoL1Address:    common.HexToAddress(os.Getenv("TAIKO_L1_ADDRESS")),
			TaikoL2Address:    common.HexToAddress(os.Getenv("TAIKO_L2_ADDRESS")),
			TaikoTokenAddress: common.HexToAddress(os.Getenv("TAIKO_TOKEN_ADDRESS")),
		},
		AssignmentHookAddress:      common.HexToAddress(os.Getenv("ASSIGNMENT_HOOK_ADDRESS")),
		L1ProposerPrivKey:          l1ProposerPrivKey,
		L2SuggestedFeeRecipient:    common.HexToAddress(os.Getenv("L2_SUGGESTED_FEE_RECIPIENT")),
		ProposeInterval:            1024 * time.Hour,
		MaxProposedTxListsPerEpoch: 1,
		WaitReceiptTimeout:         12 * time.Second,
		ProverEndpoints:            s.ProverEndpoints,
		OptimisticTierFee:          common.Big256,
		SgxTierFee:                 common.Big256,
		MaxTierFeePriceBumps:       3,
		TierFeePriceBump:           common.Big2,
		L1BlockBuilderTip:          common.Big0,
	}))

	s.proposer = prop
}

func (s *ProofSubmitterTestSuite) TestProofSubmitterRequestProofDeadlineExceeded() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	s.ErrorContains(
		s.submitter.RequestProof(
			ctx, &bindings.TaikoL1ClientBlockProposed{BlockId: common.Big256}), "context deadline exceeded",
	)
}

func (s *ProofSubmitterTestSuite) TestProofSubmitterSubmitProofMetadataNotFound() {
	s.Error(
		s.submitter.SubmitProof(
			context.Background(), &producer.ProofWithHeader{
				BlockID: common.Big256,
				Meta:    &bindings.TaikoDataBlockMetadata{},
				Header:  &types.Header{},
				Opts:    &producer.ProofRequestOptions{},
				Proof:   bytes.Repeat([]byte{0xff}, 100),
			},
		),
	)
}

func (s *ProofSubmitterTestSuite) TestSubmitProofs() {
	events := s.ProposeAndInsertEmptyBlocks(s.proposer, s.calldataSyncer)

	for _, e := range events {
		s.Nil(s.submitter.RequestProof(context.Background(), e))
		proofWithHeader := <-s.proofCh
		s.Nil(s.submitter.SubmitProof(context.Background(), proofWithHeader))
	}
}

func (s *ProofSubmitterTestSuite) TestGuardianSubmitProofs() {
	events := s.ProposeAndInsertEmptyBlocks(s.proposer, s.calldataSyncer)

	for _, e := range events {
		s.Nil(s.submitter.RequestProof(context.Background(), e))
		proofWithHeader := <-s.proofCh
		proofWithHeader.Tier = encoding.TierGuardianID
		s.Nil(s.submitter.SubmitProof(context.Background(), proofWithHeader))
	}
}

func (s *ProofSubmitterTestSuite) TestProofSubmitterRequestProofCancelled() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.AfterFunc(2*time.Second, func() { cancel() }) }()

	s.ErrorContains(
		s.submitter.RequestProof(
			ctx, &bindings.TaikoL1ClientBlockProposed{BlockId: common.Big256}), "context canceled",
	)
}

func TestProofSubmitterTestSuite(t *testing.T) {
	suite.Run(t, new(ProofSubmitterTestSuite))
}
