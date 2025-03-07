package requester

import (
	"context"
	"fmt"
	"time"

	"github.com/opentracing/opentracing-go"
	"github.com/rs/zerolog"
	"golang.org/x/exp/rand"

	"github.com/onflow/flow-go/engine"
	"github.com/onflow/flow-go/engine/verification/fetcher"
	"github.com/onflow/flow-go/model/flow"
	"github.com/onflow/flow-go/model/messages"
	"github.com/onflow/flow-go/model/verification"
	"github.com/onflow/flow-go/module"
	"github.com/onflow/flow-go/module/mempool"
	"github.com/onflow/flow-go/module/trace"
	"github.com/onflow/flow-go/network"
	"github.com/onflow/flow-go/state/protocol"
	"github.com/onflow/flow-go/utils/logging"
)

// Engine implements a ChunkDataPackRequester that is responsible of receiving chunk data pack requests,
// dispatching it to the execution nodes, receiving the requested chunk data pack from execution nodes,
// and passing it to the registered handler.
type Engine struct {
	// common
	log   zerolog.Logger
	unit  *engine.Unit
	state protocol.State  // used to check the last sealed height.
	con   network.Conduit // used to send the chunk data request, and receive the response.

	// monitoring
	tracer  module.Tracer
	metrics module.VerificationMetrics

	// output interfaces
	handler fetcher.ChunkDataPackHandler // contains callbacks for handling received chunk data packs.

	// internal logic
	retryInterval    time.Duration                          // determines time in milliseconds for retrying chunk data requests.
	requestTargets   uint                                   // maximum number of execution nodes being asked for a chunk data pack.
	pendingRequests  mempool.ChunkRequests                  // used to track requested chunks.
	reqQualifierFunc RequestQualifierFunc                   // used to decide whether to dispatch a request at a certain cycle.
	reqUpdaterFunc   mempool.ChunkRequestHistoryUpdaterFunc // used to atomically update chunk request info on mempool.
}

func New(log zerolog.Logger,
	state protocol.State,
	net module.Network,
	tracer module.Tracer,
	metrics module.VerificationMetrics,
	pendingRequests mempool.ChunkRequests,
	retryInterval time.Duration,
	reqQualifierFunc RequestQualifierFunc,
	reqUpdaterFunc mempool.ChunkRequestHistoryUpdaterFunc,
	requestTargets uint) (*Engine, error) {

	e := &Engine{
		log:              log.With().Str("engine", "requester").Logger(),
		unit:             engine.NewUnit(),
		state:            state,
		tracer:           tracer,
		metrics:          metrics,
		retryInterval:    retryInterval,
		requestTargets:   requestTargets,
		pendingRequests:  pendingRequests,
		reqUpdaterFunc:   reqUpdaterFunc,
		reqQualifierFunc: reqQualifierFunc,
	}

	con, err := net.Register(engine.RequestChunks, e)
	if err != nil {
		return nil, fmt.Errorf("could not register chunk data pack provider engine: %w", err)
	}
	e.con = con

	return e, nil
}

func (e *Engine) WithChunkDataPackHandler(handler fetcher.ChunkDataPackHandler) {
	e.handler = handler
}

// SubmitLocal submits an event originating on the local node.
func (e *Engine) SubmitLocal(event interface{}) {
	e.log.Fatal().Msg("engine is not supposed to be invoked on SubmitLocal")
}

// Submit submits the given event from the node with the given origin ID
// for processing in a non-blocking manner. It returns instantly and logs
// a potential processing error internally when done.
func (e *Engine) Submit(originID flow.Identifier, event interface{}) {
	e.unit.Launch(func() {
		err := e.Process(originID, event)
		if err != nil {
			engine.LogError(e.log, err)
		}
	})
}

// ProcessLocal processes an event originating on the local node.
func (e *Engine) ProcessLocal(event interface{}) error {
	return fmt.Errorf("should not invoke ProcessLocal of Match engine, use Process instead")
}

// Process processes the given event from the node with the given origin ID in
// a blocking manner. It returns the potential processing error when done.
func (e *Engine) Process(originID flow.Identifier, event interface{}) error {
	return e.unit.Do(func() error {
		return e.process(originID, event)
	})
}

// Ready initializes the engine and returns a channel that is closed when the initialization is done.
func (e *Engine) Ready() <-chan struct{} {
	if e.handler == nil {
		e.log.Fatal().Msg("could not start requester engine with missing chunk data pack handler")
	}

	delay := time.Duration(0)
	// run a periodic check to retry requesting chunk data packs.
	// if onTimer takes longer than retryInterval, the next call will be blocked until the previous
	// call has finished.
	// That being said, there won't be two onTimer running in parallel. See test cases for LaunchPeriodically.
	e.unit.LaunchPeriodically(e.onTimer, e.retryInterval, delay)
	return e.unit.Ready()
}

// Done terminates the engine and returns a channel that is closed when the termination is done
func (e *Engine) Done() <-chan struct{} {
	return e.unit.Done()
}

// process receives and submits an event to the engine for processing.
// It returns an error so the engine will not propagate an event unless
// it is successfully processed by the engine.
// The origin ID indicates the node which originally submitted the event to
// the peer-to-peer network.
func (e *Engine) process(originID flow.Identifier, event interface{}) error {
	switch resource := event.(type) {
	case *messages.ChunkDataResponse:
		e.handleChunkDataPackWithTracing(originID, &resource.ChunkDataPack, &resource.Collection)
	default:
		return fmt.Errorf("invalid event type (%T)", event)
	}

	return nil
}

// handleChunkDataPackWithTracing encapsulates the logic of handling a chunk data pack with tracing enabled.
func (e *Engine) handleChunkDataPackWithTracing(originID flow.Identifier, chunkDataPack *flow.ChunkDataPack, collection *flow.Collection) {
	span, ok := e.tracer.GetSpan(chunkDataPack.ChunkID, trace.VERProcessChunkDataPackRequest)
	if !ok {
		span = e.tracer.StartSpan(chunkDataPack.ChunkID, trace.VERProcessChunkDataPackRequest)
		span.SetTag("chunk_id", chunkDataPack.ChunkID)
		defer span.Finish()
	}

	ctx := opentracing.ContextWithSpan(e.unit.Ctx(), span)
	e.tracer.WithSpanFromContext(ctx, trace.VERRequesterHandleChunkDataResponse, func() {
		e.handleChunkDataPack(originID, chunkDataPack, collection)
	})
}

// handleChunkDataPack sends the received chunk data pack and its collection to the registered handler, and cleans up its request status.
func (e *Engine) handleChunkDataPack(originID flow.Identifier, chunkDataPack *flow.ChunkDataPack, collection *flow.Collection) {
	chunkID := chunkDataPack.ChunkID
	collectionID := collection.ID()
	lg := e.log.With().
		Hex("chunk_id", logging.ID(chunkID)).
		Hex("collection_id", logging.ID(collectionID)).
		Logger()
	lg.Debug().Msg("chunk data pack received")

	e.metrics.OnChunkDataPackResponseReceivedFromNetwork()

	// makes sure we still need this chunk, and we will not process duplicate chunk data packs.
	removed := e.pendingRequests.Rem(chunkID)
	if !removed {
		lg.Debug().Msg("chunk request status not found in mempool to be removed, dropping chunk")
		return
	}

	e.handler.HandleChunkDataPack(originID, chunkDataPack, collection)

	e.metrics.OnChunkDataPackSentToFetcher()
	lg.Info().Msg("successfully sent the chunk data pack to the handler")
}

// Request receives a chunk data pack request and adds it into the pending requests mempool.
func (e *Engine) Request(request *verification.ChunkDataPackRequest) {
	span, ok := e.tracer.GetSpan(request.ChunkID, trace.VERProcessChunkDataPackRequest)
	if !ok {
		span = e.tracer.StartSpan(request.ChunkID, trace.VERProcessChunkDataPackRequest)
		span.SetTag("chunk_id", request.ChunkID)
		defer span.Finish()
	}

	ctx := opentracing.ContextWithSpan(e.unit.Ctx(), span)
	e.tracer.WithSpanFromContext(ctx, trace.VERRequesterHandleChunkDataRequest, func() {
		added := e.pendingRequests.Add(request)

		e.metrics.OnChunkDataPackRequestReceivedByRequester()

		e.log.Info().
			Hex("chunk_id", logging.ID(request.ChunkID)).
			Uint64("block_height", request.Height).
			Int("agree_executors", len(request.Agrees)).
			Int("disagree_executors", len(request.Disagrees)).
			Bool("added_to_pending_requests", added).
			Msg("chunk data pack request arrived")
	})
}

// onTimer should run periodically, it goes through all pending requests, and requests their chunk data pack.
// It also retries the chunk data request if the data hasn't been received for a while.
func (e *Engine) onTimer() {
	pendingReqs := e.pendingRequests.All()

	e.log.Debug().
		Int("total", len(pendingReqs)).
		Msg("start processing all pending chunk data requests")

	lastSealed, err := e.state.Sealed().Head()
	if err != nil {
		e.log.Fatal().
			Err(err).
			Msg("could not determine whether block has been sealed")
	}

	for _, request := range pendingReqs {
		e.handleChunkDataPackRequestWithTracing(request, lastSealed.Height)
	}
}

// handleChunkDataPackRequestWithTracing encapsulates the logic of dispatching chunk data request in network with tracing enabled.
func (e *Engine) handleChunkDataPackRequestWithTracing(request *verification.ChunkDataPackRequest, lastSealedHeight uint64) {
	span, ok := e.tracer.GetSpan(request.ChunkID, trace.VERProcessChunkDataPackRequest)
	if !ok {
		span = e.tracer.StartSpan(request.ChunkID, trace.VERProcessChunkDataPackRequest)
		span.SetTag("chunk_id", request.ChunkID)
		defer span.Finish()
	}

	ctx := opentracing.ContextWithSpan(e.unit.Ctx(), span)
	e.tracer.WithSpanFromContext(ctx, trace.VERRequesterHandleChunkDataRequest, func() {
		e.handleChunkDataPackRequest(ctx, request, lastSealedHeight)
	})
}

// handleChunkDataPackRequest encapsulates the logic of dispatching the chunk data pack request to the network.
func (e *Engine) handleChunkDataPackRequest(ctx context.Context, request *verification.ChunkDataPackRequest, lastSealedHeight uint64) {
	lg := e.log.With().
		Hex("chunk_id", logging.ID(request.ID())).
		Uint64("block_height", request.Height).
		Logger()

	// if block has been sealed, then we can finish
	if request.Height <= lastSealedHeight {
		removed := e.pendingRequests.Rem(request.ID())
		e.handler.NotifyChunkDataPackSealed(request.ID())
		lg.Info().
			Bool("removed", removed).
			Msg("drops requesting chunk of a sealed block")
		return
	}

	qualified := e.canDispatchRequest(request.ChunkID)
	if !qualified {
		lg.Debug().Msg("chunk data pack request is not qualified for dispatching at this round")
		return
	}

	err := e.requestChunkDataPackWithTracing(ctx, request)
	if err != nil {
		lg.Error().Err(err).Msg("could not request chunk data pack")
		return
	}

	attempts, lastAttempt, retryAfter, updated := e.onRequestDispatched(request.ChunkID)
	lg.Info().
		Bool("pending_request_updated", updated).
		Uint64("attempts_made", attempts).
		Time("last_attempt", lastAttempt).
		Dur("retry_after", retryAfter).
		Msg("chunk data pack requested")
}

// requestChunkDataPack dispatches request for the chunk data pack to the execution nodes.
func (e *Engine) requestChunkDataPackWithTracing(ctx context.Context, request *verification.ChunkDataPackRequest) error {
	var err error
	e.tracer.WithSpanFromContext(ctx, trace.VERRequesterDispatchChunkDataRequest, func() {
		err = e.requestChunkDataPack(request)
	})
	return err
}

// requestChunkDataPack dispatches request for the chunk data pack to the execution nodes.
func (e *Engine) requestChunkDataPack(request *verification.ChunkDataPackRequest) error {
	req := &messages.ChunkDataRequest{
		ChunkID: request.ChunkID,
		Nonce:   rand.Uint64(), // prevent the request from being deduplicated by the receiver
	}

	// publishes the chunk data request to the network
	targetIDs := request.SampleTargets(int(e.requestTargets))
	err := e.con.Publish(req, targetIDs...)
	if err != nil {
		return fmt.Errorf("could not publish chunk data pack request for chunk (id=%s): %w", request.ChunkID, err)
	}

	return nil
}

// canDispatchRequest returns whether chunk data request for this chunk ID can be dispatched.
func (e *Engine) canDispatchRequest(chunkID flow.Identifier) bool {
	attempts, lastAttempt, retryAfter, exists := e.pendingRequests.RequestHistory(chunkID)
	if !exists {
		return false
	}

	return e.reqQualifierFunc(attempts, lastAttempt, retryAfter)
}

// onRequestDispatched encapsulates the logic of updating the chunk data request post a successful dispatch.
func (e *Engine) onRequestDispatched(chunkID flow.Identifier) (uint64, time.Time, time.Duration, bool) {
	e.metrics.OnChunkDataPackRequestDispatchedInNetwork()
	return e.pendingRequests.UpdateRequestHistory(chunkID, e.reqUpdaterFunc)
}
