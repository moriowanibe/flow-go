package vertestutils

import (
	"context"
	"math/rand"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/onflow/flow-go/engine/execution/computation/committer"
	"github.com/onflow/flow-go/engine/execution/computation/computer"
	"github.com/onflow/flow-go/engine/execution/state"
	"github.com/onflow/flow-go/engine/execution/state/bootstrap"
	"github.com/onflow/flow-go/engine/execution/state/delta"
	"github.com/onflow/flow-go/engine/execution/testutil"
	"github.com/onflow/flow-go/fvm"
	"github.com/onflow/flow-go/fvm/programs"
	"github.com/onflow/flow-go/ledger"
	completeLedger "github.com/onflow/flow-go/ledger/complete"
	"github.com/onflow/flow-go/ledger/complete/wal/fixtures"
	"github.com/onflow/flow-go/model/messages"

	fvmMock "github.com/onflow/flow-go/fvm/mock"
	"github.com/onflow/flow-go/model/flow"
	"github.com/onflow/flow-go/module/mempool/entity"
	"github.com/onflow/flow-go/module/metrics"
	"github.com/onflow/flow-go/module/trace"
	"github.com/onflow/flow-go/utils/unittest"
)

// ExecutionReceiptData is a test helper struct that represents all data required
// to verify the result of an execution receipt.
type ExecutionReceiptData struct {
	ReferenceBlock *flow.Block // block that execution receipt refers to
	Collections    []*flow.Collection
	ChunkDataPacks []*flow.ChunkDataPack
	SpockSecrets   [][]byte
}

// CompleteExecutionReceipt is a test helper struct that represents a container block accompanied with all
// data required to verify its execution receipts.
// TODO update this as needed based on execution requirements
type CompleteExecutionReceipt struct {
	ContainerBlock *flow.Block // block that contains execution receipt of reference block

	// TODO: this is a temporary field to support finder engine logic
	// It should be removed once we replace finder engine.
	Receipts     []*flow.ExecutionReceipt // copy of execution receipts in container block
	ReceiptsData []*ExecutionReceiptData  // execution receipts data of the container block
}

type CompleteExecutionReceiptList []*CompleteExecutionReceipt

// ChunkDataResponseOf is a test helper method that returns a chunk data pack response message for the specified chunk ID that
// should belong to this complete execution receipt list.
//
// It fails the test if no chunk with specified chunk ID is found in this complete execution receipt list.
func (c CompleteExecutionReceiptList) ChunkDataResponseOf(t *testing.T, chunkID flow.Identifier) *messages.ChunkDataResponse {
	result, chunkIndex := c.resultOf(t, chunkID)
	receiptData := c.ReceiptDataOf(t, chunkID)

	// publishes the chunk data pack response to the network
	res := &messages.ChunkDataResponse{
		ChunkDataPack: *receiptData.ChunkDataPacks[chunkIndex],
		Nonce:         rand.Uint64(),
	}

	// only non-system chunks have a collection
	if !isSystemChunk(chunkIndex, len(result.Chunks)) {
		res.Collection = *receiptData.Collections[chunkIndex]
	}

	return res
}

// ChunkOf is a test helper method that returns the chunk of the specified index from the specified result that
// should belong to this complete execution receipt list.
//
// It fails the test if no execution result with the specified identifier is found in this complete execution receipt list.
func (c CompleteExecutionReceiptList) ChunkOf(t *testing.T, resultID flow.Identifier, chunkIndex uint64) *flow.Chunk {
	for _, completeER := range c {
		for _, result := range completeER.ContainerBlock.Payload.Results {
			if result.ID() == resultID {
				return result.Chunks[chunkIndex]
			}
		}
	}

	require.Fail(t, "could not find specified chunk in the complete execution result list")
	return nil
}

// ReceiptDataOf is a test helper method that returns the receipt data of the specified chunk ID that
// should belong to this complete execution receipt list.
//
// It fails the test if no chunk with specified chunk ID is found in this complete execution receipt list.
func (c CompleteExecutionReceiptList) ReceiptDataOf(t *testing.T, chunkID flow.Identifier) *ExecutionReceiptData {
	for _, completeER := range c {
		for _, receiptData := range completeER.ReceiptsData {
			for _, cdp := range receiptData.ChunkDataPacks {
				if cdp.ChunkID == chunkID {
					return receiptData
				}
			}
		}
	}

	require.Fail(t, "could not find receipt data of specified chunk in the complete execution result list")
	return nil
}

// resultOf is a test helper method that returns the execution result and chunk index of the specified chunk ID that
// should belong to this complete execution receipt list.
//
// It fails the test if no chunk with specified chunk ID is found in this complete execution receipt list.
func (c CompleteExecutionReceiptList) resultOf(t *testing.T, chunkID flow.Identifier) (*flow.ExecutionResult, uint64) {
	for _, completeER := range c {
		for _, result := range completeER.ContainerBlock.Payload.Results {
			for _, chunk := range result.Chunks {
				if chunk.ID() == chunkID {
					return result, chunk.Index
				}
			}
		}
	}

	require.Fail(t, "could not find specified chunk in the complete execution result list")
	return nil, uint64(0)
}

// CompleteExecutionReceiptBuilder is a test helper struct that specifies the parameters to build a CompleteExecutionReceipt.
type CompleteExecutionReceiptBuilder struct {
	resultsCount int // number of execution results in the container block.
	copyCount    int // number of times each execution result is copied in a block (by different receipts).
	chunksCount  int // number of chunks in each execution result.
	chain        flow.Chain
	executorIDs  flow.IdentifierList // identifier of execution nodes in the test.
}

type CompleteExecutionReceiptBuilderOpt func(builder *CompleteExecutionReceiptBuilder)

func WithResults(count int) CompleteExecutionReceiptBuilderOpt {
	return func(builder *CompleteExecutionReceiptBuilder) {
		builder.resultsCount = count
	}
}

func WithChunksCount(count int) CompleteExecutionReceiptBuilderOpt {
	return func(builder *CompleteExecutionReceiptBuilder) {
		builder.chunksCount = count
	}
}

func WithCopies(count int) CompleteExecutionReceiptBuilderOpt {
	return func(builder *CompleteExecutionReceiptBuilder) {
		builder.copyCount = count
	}
}

func WithChain(chain flow.Chain) CompleteExecutionReceiptBuilderOpt {
	return func(builder *CompleteExecutionReceiptBuilder) {
		builder.chain = chain
	}
}

func WithExecutorIDs(executorIDs flow.IdentifierList) CompleteExecutionReceiptBuilderOpt {
	return func(builder *CompleteExecutionReceiptBuilder) {
		builder.executorIDs = executorIDs
	}
}

// CompleteExecutionReceiptFixture returns complete execution receipt with an
// execution receipt referencing the block collections.
//
// chunks determines the number of chunks inside each receipt.
// The output is an execution result with chunks+1 chunks, where the last chunk accounts
// for the system chunk.
// TODO: remove this function once new verification architecture is in place.
func CompleteExecutionReceiptFixture(t *testing.T, chunks int, chain flow.Chain, root *flow.Header) *CompleteExecutionReceipt {
	return CompleteExecutionReceiptChainFixture(t, root, 1, WithChunksCount(chunks), WithChain(chain))[0]
}

// ExecutionResultFixture is a test helper that returns an execution result for the reference block header as well as the execution receipt data
// for that result.
func ExecutionResultFixture(t *testing.T, chunkCount int, chain flow.Chain, refBlkHeader *flow.Header) (*flow.ExecutionResult,
	*ExecutionReceiptData) {
	// setups up the first collection of block consists of three transactions
	tx1 := testutil.DeployCounterContractTransaction(chain.ServiceAddress(), chain)
	err := testutil.SignTransactionAsServiceAccount(tx1, 0, chain)
	require.NoError(t, err)
	tx2 := testutil.CreateCounterTransaction(chain.ServiceAddress(), chain.ServiceAddress())
	err = testutil.SignTransactionAsServiceAccount(tx2, 1, chain)
	require.NoError(t, err)
	tx3 := testutil.CreateCounterPanicTransaction(chain.ServiceAddress(), chain.ServiceAddress())
	err = testutil.SignTransactionAsServiceAccount(tx3, 2, chain)
	require.NoError(t, err)
	transactions := []*flow.TransactionBody{tx1, tx2, tx3}
	collection := flow.Collection{Transactions: transactions}
	collections := []*flow.Collection{&collection}
	guarantee := unittest.CollectionGuaranteeFixture(unittest.WithCollection(&collection), unittest.WithCollRef(refBlkHeader.ParentID))
	guarantees := []*flow.CollectionGuarantee{guarantee}

	metricsCollector := &metrics.NoopCollector{}
	log := zerolog.Nop()

	// setups execution outputs:
	spockSecrets := make([][]byte, 0)
	chunks := make([]*flow.Chunk, 0)
	chunkDataPacks := make([]*flow.ChunkDataPack, 0)

	var payload flow.Payload
	var referenceBlock flow.Block

	unittest.RunWithTempDir(t, func(dir string) {

		w := &fixtures.NoopWAL{}

		led, err := completeLedger.NewLedger(w, 100, metricsCollector, zerolog.Nop(), completeLedger.DefaultPathFinderVersion)
		require.NoError(t, err)
		defer led.Done()

		startStateCommitment, err := bootstrap.NewBootstrapper(log).BootstrapLedger(
			led,
			unittest.ServiceAccountPublicKey,
			chain,
			fvm.WithInitialTokenSupply(unittest.GenesisTokenSupply),
		)
		require.NoError(t, err)

		rt := fvm.NewInterpreterRuntime()

		vm := fvm.NewVirtualMachine(rt)

		blocks := new(fvmMock.Blocks)

		execCtx := fvm.NewContext(
			log,
			fvm.WithChain(chain),
			fvm.WithBlocks(blocks),
		)

		// create state.View
		view := delta.NewView(state.LedgerGetRegister(led, startStateCommitment))
		committer := committer.NewLedgerViewCommitter(led, trace.NewNoopTracer())
		programs := programs.NewEmptyPrograms()

		// create BlockComputer
		bc, err := computer.NewBlockComputer(vm, execCtx, metrics.NewNoopCollector(), trace.NewNoopTracer(), log, committer)
		require.NoError(t, err)

		completeColls := make(map[flow.Identifier]*entity.CompleteCollection)
		completeColls[guarantee.ID()] = &entity.CompleteCollection{
			Guarantee:    guarantee,
			Transactions: collection.Transactions,
		}

		for i := 1; i < chunkCount; i++ {
			tx := testutil.CreateCounterTransaction(chain.ServiceAddress(), chain.ServiceAddress())
			err = testutil.SignTransactionAsServiceAccount(tx, 3+uint64(i), chain)
			require.NoError(t, err)

			collection := flow.Collection{Transactions: []*flow.TransactionBody{tx}}
			guarantee := unittest.CollectionGuaranteeFixture(unittest.WithCollection(&collection), unittest.WithCollRef(refBlkHeader.ParentID))
			collections = append(collections, &collection)
			guarantees = append(guarantees, guarantee)

			completeColls[guarantee.ID()] = &entity.CompleteCollection{
				Guarantee:    guarantee,
				Transactions: collection.Transactions,
			}
		}

		payload = flow.Payload{
			Guarantees: guarantees,
		}
		referenceBlock = flow.Block{
			Header: refBlkHeader,
		}
		referenceBlock.SetPayload(payload)

		executableBlock := &entity.ExecutableBlock{
			Block:               &referenceBlock,
			CompleteCollections: completeColls,
			StartState:          &startStateCommitment,
		}
		computationResult, err := bc.ExecuteBlock(context.Background(), executableBlock, view, programs)
		require.NoError(t, err)

		for i, stateSnapshot := range computationResult.StateSnapshots {

			ids, values := view.Delta().RegisterUpdates()
			keys := state.RegisterIDSToKeys(ids)
			flowValues := state.RegisterValuesToValues(values)

			update, err := ledger.NewUpdate(ledger.State(startStateCommitment), keys, flowValues)
			require.NoError(t, err)

			// TODO: update CommitDelta to also return proofs
			endStateCommitment, err := led.Set(update)
			require.NoError(t, err, "error updating registers")

			var collectionID flow.Identifier

			// account for system chunk being last
			if i < len(computationResult.StateSnapshots)-1 {
				collectionGuarantee := executableBlock.Block.Payload.Guarantees[i]
				completeCollection := executableBlock.CompleteCollections[collectionGuarantee.ID()]
				collectionID = completeCollection.Collection().ID()
			} else {
				collectionID = flow.ZeroID
			}

			chunk := &flow.Chunk{
				ChunkBody: flow.ChunkBody{
					CollectionIndex: uint(i),
					StartState:      startStateCommitment,
					// TODO: include real, event collection hash, currently using the collection ID to generate a different Chunk ID
					// Otherwise, the chances of there being chunks with the same ID before all these TODOs are done is large, since
					// startState stays the same if blocks are empty
					EventCollection: collectionID,
					BlockID:         executableBlock.ID(),
					// TODO: record gas used
					TotalComputationUsed: 0,
					// TODO: record number of txs
					NumberOfTransactions: 0,
				},
				Index:    uint64(i),
				EndState: flow.StateCommitment(endStateCommitment),
			}

			// chunkDataPack
			allRegisters := view.Interactions().AllRegisters()
			allKeys := state.RegisterIDSToKeys(allRegisters)

			query, err := ledger.NewQuery(ledger.State(chunk.StartState), allKeys)
			require.NoError(t, err)

			//values, proofs, err := led.GetRegistersWithProof(allRegisters, chunk.StartState)
			proof, err := led.Prove(query)
			require.NoError(t, err, "error reading registers with proofs from ledger")

			chunkDataPack := &flow.ChunkDataPack{
				ChunkID:      chunk.ID(),
				StartState:   chunk.StartState,
				Proof:        proof,
				CollectionID: collectionID,
			}

			chunks = append(chunks, chunk)
			chunkDataPacks = append(chunkDataPacks, chunkDataPack)
			spockSecrets = append(spockSecrets, stateSnapshot.SpockSecret)
			startStateCommitment = flow.StateCommitment(endStateCommitment)
		}

	})

	// makes sure all chunks are referencing the correct block id.
	blockID := referenceBlock.ID()
	for _, chunk := range chunks {
		require.Equal(t, blockID, chunk.BlockID, "inconsistent block id in chunk fixture")
	}

	result := &flow.ExecutionResult{
		BlockID: blockID,
		Chunks:  chunks,
	}

	return result, &ExecutionReceiptData{
		ReferenceBlock: &referenceBlock,
		Collections:    collections,
		ChunkDataPacks: chunkDataPacks,
		SpockSecrets:   spockSecrets,
	}
}

// LightExecutionResultFixture returns a light mocked version of execution result with an
// execution receipt referencing the block/collections. In the light version of execution result,
// everything is wired properly, but with the minimum viable content provided. This version is basically used
// for profiling.
// TODO: remove this once new architecture of verification node is in place.
func LightExecutionResultFixture(chunkCount int) *CompleteExecutionReceipt {
	collections := make([]*flow.Collection, 0, chunkCount)
	guarantees := make([]*flow.CollectionGuarantee, 0, chunkCount)
	chunkDataPacks := make([]*flow.ChunkDataPack, 0, chunkCount)

	// creates collections and guarantees
	for i := 0; i < chunkCount; i++ {
		coll := unittest.CollectionFixture(1)
		guarantee := coll.Guarantee()
		collections = append(collections, &coll)
		guarantees = append(guarantees, &guarantee)
	}

	payload := flow.Payload{
		Guarantees: guarantees,
	}

	header := unittest.BlockHeaderFixture()
	header.Height = 0
	header.PayloadHash = payload.Hash()

	referenceBlock := flow.Block{
		Header:  &header,
		Payload: &payload,
	}
	blockID := referenceBlock.ID()

	// creates chunks
	chunks := make([]*flow.Chunk, 0)
	for i := 0; i < chunkCount; i++ {
		chunk := &flow.Chunk{
			ChunkBody: flow.ChunkBody{
				CollectionIndex: uint(i),
				BlockID:         blockID,
				EventCollection: unittest.IdentifierFixture(),
			},
			Index: uint64(i),
		}
		chunks = append(chunks, chunk)

		// creates a light (quite empty) chunk data pack for the chunk at bare minimum
		chunkDataPack := flow.ChunkDataPack{
			ChunkID: chunk.ID(),
		}
		chunkDataPacks = append(chunkDataPacks, &chunkDataPack)
	}

	result := flow.ExecutionResult{
		BlockID: blockID,
		Chunks:  chunks,
	}

	receipt := &flow.ExecutionReceipt{
		ExecutionResult: result,
	}

	// container block contains the execution receipt and points back to reference block
	// as its parent.
	containerBlock := unittest.BlockWithParentFixture(referenceBlock.Header)
	containerBlock.Payload.Receipts = []*flow.ExecutionReceiptMeta{receipt.Meta()}
	containerBlock.Payload.Results = []*flow.ExecutionResult{&receipt.ExecutionResult}

	return &CompleteExecutionReceipt{
		ContainerBlock: &containerBlock,
		Receipts:       []*flow.ExecutionReceipt{receipt},
		ReceiptsData: []*ExecutionReceiptData{
			{
				ReferenceBlock: &referenceBlock,
				Collections:    collections,
				ChunkDataPacks: chunkDataPacks,
			},
		},
	}
}

// CompleteExecutionReceiptChainFixture is a test fixture that creates a chain of blocks of size `count`.
// The chain is in the form of root <- R1,1 <- R1,2 <- ... <- C1 <- R2,1 <- R2,2 <- ... <- C2 <- ...
// In this chain R refers to reference blocks that contain guarantees.
// C refers to a container block that contains an execution receipt for its preceding reference blocks.
// e.g., C1 contains an execution receipt for R1,1, R1,2, etc, and C2 contains a receipt for R2,1, R2,2, etc.
// For sake of simplicity and test, container blocks (i.e., C) do not contain any guarantee.
//
// It returns a slice of complete execution receipt fixtures that contains a container block as well as all data to verify its contained receipts.
func CompleteExecutionReceiptChainFixture(t *testing.T, root *flow.Header, count int, opts ...CompleteExecutionReceiptBuilderOpt) []*CompleteExecutionReceipt {
	completeERs := make([]*CompleteExecutionReceipt, 0, count)
	parent := root

	builder := &CompleteExecutionReceiptBuilder{
		resultsCount: 1,
		copyCount:    1,
		chunksCount:  1,
		chain:        flow.Testnet.Chain(),
	}

	for _, apply := range opts {
		apply(builder)
	}

	if len(builder.executorIDs) == 0 {
		builder.executorIDs = unittest.IdentifierListFixture(builder.copyCount)
	}

	require.GreaterOrEqual(t, len(builder.executorIDs), builder.copyCount,
		"number of executors in the tests should be greater than or equal to the number of receipts per block")

	for i := 0; i < count; i++ {
		// Generates two blocks as parent <- R <- C where R is a reference block containing guarantees,
		// and C is a container block containing execution receipt for R.
		receipts, allData, head := ExecutionReceiptsFromParentBlockFixture(t, parent, builder)
		containerBlock := ContainerBlockFixture(head, receipts)
		completeERs = append(completeERs, &CompleteExecutionReceipt{
			ContainerBlock: containerBlock,
			Receipts:       receipts,
			ReceiptsData:   allData,
		})

		parent = containerBlock.Header
	}
	return completeERs
}

// ExecutionReceiptsFromParentBlockFixture creates a chain of receipts from a parent block.
//
// By default each result refers to a distinct reference block, and it extends the block chain after generating each
// result (i.e., for the next result).
//
// Each result may appear in more than one receipt depending on the builder parameters.
func ExecutionReceiptsFromParentBlockFixture(t *testing.T, parent *flow.Header, builder *CompleteExecutionReceiptBuilder) (
	[]*flow.ExecutionReceipt,
	[]*ExecutionReceiptData, *flow.Header) {

	allData := make([]*ExecutionReceiptData, 0, builder.resultsCount*builder.copyCount)
	allReceipts := make([]*flow.ExecutionReceipt, 0, builder.resultsCount*builder.copyCount)

	for i := 0; i < builder.resultsCount; i++ {
		result, data := ExecutionResultFromParentBlockFixture(t, parent, builder)

		// makes several copies of the same result
		for cp := 0; cp < builder.copyCount; cp++ {
			allReceipts = append(allReceipts, &flow.ExecutionReceipt{
				ExecutorID:      builder.executorIDs[cp],
				ExecutionResult: *result,
			})

			allData = append(allData, data)
		}
		parent = data.ReferenceBlock.Header
	}

	return allReceipts, allData, parent
}

// ExecutionResultFromParentBlockFixture is a test helper that creates a child (reference) block from the parent, as well as an execution for it.
func ExecutionResultFromParentBlockFixture(t *testing.T, parent *flow.Header, builder *CompleteExecutionReceiptBuilder) (*flow.ExecutionResult,
	*ExecutionReceiptData) {
	refBlkHeader := unittest.BlockHeaderWithParentFixture(parent)
	return ExecutionResultFixture(t, builder.chunksCount, builder.chain, &refBlkHeader)
}

// ContainerBlockFixture builds and returns a block that contains input execution receipts.
func ContainerBlockFixture(parent *flow.Header, receipts []*flow.ExecutionReceipt) *flow.Block {
	// container block is the block that contains the execution receipt of reference block
	containerBlock := unittest.BlockWithParentFixture(parent)
	containerBlock.SetPayload(unittest.PayloadFixture(unittest.WithReceipts(receipts...)))

	return &containerBlock
}
