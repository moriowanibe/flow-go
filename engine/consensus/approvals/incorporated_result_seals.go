package approvals

import (
	"github.com/rs/zerolog/log"

	"github.com/onflow/flow-go/model/flow"
	"github.com/onflow/flow-go/module/mempool"
	"github.com/onflow/flow-go/storage"
	"github.com/onflow/flow-go/utils/logging"
)

// IncorporatedResultSeals implements the incorporated result seals memory pool
// of the consensus nodes.
// ATTENTION: this is a temporary wrapper for `mempool.IncorporatedResultSeals` to support
// a condition that there must be at least 2 receipts from _different_ ENs
// committing to the same incorporated result.
// This wrapper should only be used with `approvalProcessingCore`.
type IncorporatedResultSeals struct {
	seals      mempool.IncorporatedResultSeals // seals mempool that wrapped
	receiptsDB storage.ExecutionReceipts       // receipts DB to decide if we have multiple receipts for same result
}

// NewIncorporatedResults creates a mempool for the incorporated result seals
func NewIncorporatedResultSeals(mempool mempool.IncorporatedResultSeals) *IncorporatedResultSeals {
	return &IncorporatedResultSeals{
		seals: mempool,
	}
}

// Add adds an IncorporatedResultSeal to the mempool
func (ir *IncorporatedResultSeals) Add(seal *flow.IncorporatedResultSeal) (bool, error) {
	return ir.seals.Add(seal)
}

// All returns all the items in the mempool
func (ir *IncorporatedResultSeals) All() []*flow.IncorporatedResultSeal {
	return ir.seals.All()
}

// resultHasMultipleReceipts implements an additional _temporary_ safety measure:
// only consider incorporatedResult sealable if there are at AT LEAST 2 RECEIPTS
// from _different_ ENs committing to the result.
func (ir *IncorporatedResultSeals) resultHasMultipleReceipts(incorporatedResult *flow.IncorporatedResult) bool {
	blockID := incorporatedResult.Result.BlockID // block that was computed
	resultID := incorporatedResult.Result.ID()

	// get all receipts that are known for the block
	receipts, err := ir.receiptsDB.ByBlockID(blockID)
	if err != nil {
		log.Error().Err(err).
			Hex("block_id", logging.ID(blockID)).
			Msg("could not get receipts by block ID")
		return false
	}

	// Index receipts for given incorporatedResult by their executor. In case
	// there are multiple receipts from the same executor, we keep the last one.
	receiptsForIncorporatedResults := receipts.GroupByResultID().GetGroup(resultID)
	return receiptsForIncorporatedResults.GroupByExecutorID().NumberGroups() >= 2
}

// ByID gets an IncorporatedResultSeal by IncorporatedResult ID
func (ir *IncorporatedResultSeals) ByID(id flow.Identifier) (*flow.IncorporatedResultSeal, bool) {
	seal, ok := ir.seals.ByID(id)
	if !ok {
		return nil, false
	}

	// _temporary_ measure, return only receipts that have multiple commitments from different ENs.
	if !ir.resultHasMultipleReceipts(seal.IncorporatedResult) {
		return nil, false
	}

	return seal, true
}

// Rem removes an IncorporatedResultSeal from the mempool
func (ir *IncorporatedResultSeals) Rem(id flow.Identifier) bool {
	return ir.seals.Rem(id)
}

// Clear removes all entities from the pool.
func (ir *IncorporatedResultSeals) Clear() {
	ir.seals.Clear()
}

// RegisterEjectionCallbacks adds the provided OnEjection callbacks
func (ir *IncorporatedResultSeals) RegisterEjectionCallbacks(callbacks ...mempool.OnEjection) {
	ir.seals.RegisterEjectionCallbacks(callbacks...)
}
