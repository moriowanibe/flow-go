package sealing

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/suite"

	"github.com/onflow/flow-go/engine"
	"github.com/onflow/flow-go/engine/common/fifoqueue"
	"github.com/onflow/flow-go/model/flow"
	"github.com/onflow/flow-go/module/mempool/stdmap"
	"github.com/onflow/flow-go/module/metrics"
	mockmodule "github.com/onflow/flow-go/module/mock"
	"github.com/onflow/flow-go/module/trace"
	"github.com/onflow/flow-go/utils/unittest"
)

func TestSealingEngineContext(t *testing.T) {
	suite.Run(t, new(SealingEngineSuite))
}

type SealingEngineSuite struct {
	unittest.BaseChainSuite
	// misc SERVICE COMPONENTS which are injected into Sealing Core
	requester         *mockmodule.Requester
	receiptValidator  *mockmodule.ReceiptValidator
	approvalValidator *mockmodule.ApprovalValidator

	// Sealing Engine
	engine *Engine
}

func (ms *SealingEngineSuite) SetupTest() {
	// ~~~~~~~~~~~~~~~~~~~~~~~~~~ SETUP SUITE ~~~~~~~~~~~~~~~~~~~~~~~~~~ //
	ms.SetupChain()

	log := zerolog.New(os.Stderr)
	metrics := metrics.NewNoopCollector()
	tracer := trace.NewNoopTracer()

	// ~~~~~~~~~~~~~~~~~~~~~~~ SETUP MATCHING ENGINE ~~~~~~~~~~~~~~~~~~~~~~~ //
	ms.requester = new(mockmodule.Requester)
	ms.receiptValidator = &mockmodule.ReceiptValidator{}
	ms.approvalValidator = &mockmodule.ApprovalValidator{}

	approvalsProvider := make(chan *Event)
	approvalResponseProvider := make(chan *Event)
	receiptsProvider := make(chan *Event)

	ms.engine = &Engine{
		log:  log,
		unit: engine.NewUnit(),
		core: &Core{
			tracer:                               tracer,
			log:                                  log,
			coreMetrics:                          metrics,
			mempool:                              metrics,
			metrics:                              metrics,
			state:                                ms.State,
			receiptRequester:                     ms.requester,
			receiptsDB:                           ms.ReceiptsDB,
			headersDB:                            ms.HeadersDB,
			indexDB:                              ms.IndexDB,
			incorporatedResults:                  ms.ResultsPL,
			receipts:                             ms.ReceiptsPL,
			approvals:                            ms.ApprovalsPL,
			seals:                                ms.SealsPL,
			pendingReceipts:                      stdmap.NewPendingReceipts(100),
			sealingThreshold:                     10,
			maxResultsToRequest:                  200,
			assigner:                             ms.Assigner,
			receiptValidator:                     ms.receiptValidator,
			approvalValidator:                    ms.approvalValidator,
			requestTracker:                       NewRequestTracker(1, 3),
			approvalRequestsThreshold:            10,
			requiredApprovalsForSealConstruction: RequiredApprovalsForSealConstructionTestingValue,
			emergencySealingActive:               false,
		},
		approvalSink:                         approvalsProvider,
		requestedApprovalSink:                approvalResponseProvider,
		receiptSink:                          receiptsProvider,
		pendingEventSink:                     make(chan *Event),
		engineMetrics:                        metrics,
		cacheMetrics:                         metrics,
		requiredApprovalsForSealConstruction: RequiredApprovalsForSealConstructionTestingValue,
	}

	ms.engine.pendingReceipts, _ = fifoqueue.NewFifoQueue()
	ms.engine.pendingApprovals, _ = fifoqueue.NewFifoQueue()
	ms.engine.pendingRequestedApprovals, _ = fifoqueue.NewFifoQueue()

	<-ms.engine.Ready()
}

// TestProcessValidReceipt tests if valid receipt gets recorded into mempool when send through `Engine`.
// Tests the whole processing pipeline.
func (ms *SealingEngineSuite) TestProcessValidReceipt() {
	originID := ms.ExeID
	receipt := unittest.ExecutionReceiptFixture(
		unittest.WithExecutorID(originID),
		unittest.WithResult(unittest.ExecutionResultFixture(unittest.WithBlock(&ms.UnfinalizedBlock))),
	)

	ms.receiptValidator.On("Validate", receipt).Return(nil).Once()
	// we expect that receipt is persisted in storage
	ms.ReceiptsDB.On("Store", receipt).Return(nil).Once()
	// we expect that receipt is added to mempool
	ms.ReceiptsPL.On("AddReceipt", receipt, ms.UnfinalizedBlock.Header).Return(true, nil).Once()
	// setup the results mempool to check if we attempted to add the incorporated result
	ms.ResultsPL.
		On("Add", incorporatedResult(receipt.ExecutionResult.BlockID, &receipt.ExecutionResult)).
		Return(true, nil).Once()

	err := ms.engine.Process(originID, receipt)
	ms.Require().NoError(err, "should add receipt and result to mempool if valid")

	// sealing engine has at least 100ms ticks for processing events
	time.Sleep(1 * time.Second)

	ms.receiptValidator.AssertExpectations(ms.T())
	ms.ReceiptsPL.AssertExpectations(ms.T())
	ms.ResultsPL.AssertExpectations(ms.T())
}

// TestMultipleProcessingItems tests that the engine queues multiple receipts and approvals
// and eventually feeds them into sealing.Core for processing
func (ms *SealingEngineSuite) TestMultipleProcessingItems() {
	originID := ms.ExeID

	receipts := make([]*flow.ExecutionReceipt, 20)
	for i := range receipts {
		receipt := unittest.ExecutionReceiptFixture(
			unittest.WithExecutorID(originID),
			unittest.WithResult(unittest.ExecutionResultFixture(unittest.WithBlock(&ms.UnfinalizedBlock))),
		)
		ms.receiptValidator.On("Validate", receipt).Return(nil).Once()
		// we expect that receipt is persisted in storage
		ms.ReceiptsDB.On("Store", receipt).Return(nil).Once()
		// we expect that receipt is added to mempool
		ms.ReceiptsPL.On("AddReceipt", receipt, ms.UnfinalizedBlock.Header).Return(true, nil).Once()
		// setup the results mempool to check if we attempted to add the incorporated result
		ms.ResultsPL.
			On("Add", incorporatedResult(receipt.ExecutionResult.BlockID, &receipt.ExecutionResult)).
			Return(true, nil).Once()
		receipts[i] = receipt
	}

	numApprovalsPerReceipt := 1
	approvals := make([]*flow.ResultApproval, 0, len(receipts)*numApprovalsPerReceipt)
	approverID := ms.VerID
	for _, receipt := range receipts {
		for j := 0; j < numApprovalsPerReceipt; j++ {
			approval := unittest.ResultApprovalFixture(unittest.WithExecutionResultID(receipt.ID()),
				unittest.WithApproverID(approverID))
			ms.approvalValidator.On("Validate", approval).Return(nil).Once()
			approvals = append(approvals, approval)
			ms.ApprovalsPL.On("Add", approval).Return(true, nil).Once()
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, receipt := range receipts {
			err := ms.engine.Process(originID, receipt)
			ms.Require().NoError(err, "should add receipt and result to mempool if valid")
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, approval := range approvals {
			err := ms.engine.Process(approverID, approval)
			ms.Require().NoError(err, "should process approval")
		}
	}()

	wg.Wait()

	// sealing engine has at least 100ms ticks for processing events
	time.Sleep(1 * time.Second)

	ms.receiptValidator.AssertExpectations(ms.T())
	ms.ReceiptsPL.AssertExpectations(ms.T())
	ms.ResultsPL.AssertExpectations(ms.T())
	ms.ApprovalsPL.AssertExpectations(ms.T())
}
