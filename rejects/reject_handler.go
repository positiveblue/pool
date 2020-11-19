package rejects

import (
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/venue"
	"github.com/lightninglabs/subasta/venue/matching"
)

// Order holds the minimal information needed about an order in a match
type Order struct {
	// Nonce is the order nonce.
	Nonce orderT.Nonce

	// AcctKey is the account that made this order.
	AcctKey matching.AccountID

	// NodeKey is the node public key for this order.
	NodeKey [33]byte
}

// Match holds the ask and bid of a match.
type Match struct {
	// Ask is the ask that matched with the ask.
	Ask Order

	// Bid is the bid that matched with the bid.
	Bid Order
}

// FromBatch extracts the matches needed for reject handling from the given
// batch.
func FromBatch(batch *matching.OrderBatch) []Match {
	matches := make([]Match, len(batch.Orders))
	for i, o := range batch.Orders {
		ask := o.Details.Ask
		bid := o.Details.Bid

		matches[i] = Match{
			Ask: Order{
				AcctKey: ask.Details().AcctKey,
				NodeKey: ask.ServerDetails().NodeKey,
				Nonce:   ask.Nonce(),
			},
			Bid: Order{
				AcctKey: bid.Details().AcctKey,
				NodeKey: bid.ServerDetails().NodeKey,
				Nonce:   bid.Nonce(),
			},
		}
	}

	return matches
}

// RejectHandler encapsulates logic for handling reject messages sent from
// traders and filter out orders from the order book accrodingly.
type RejectHandler struct {
	// ReportConflict is a closure that will be called when we determine
	// the two given nodes should be matched.
	ReportConflict func(reporter, subject [33]byte, reason string)

	// RemoveIneligibleOrders is a closure that will be called for orders
	// we determined should be removed from match making.
	RemoveIneligibleOrders func(orders []orderT.Nonce)
}

// reportPartialReject handles a partial reject sent by a trader by making sure
// the order pair won't be matched again.
func (r *RejectHandler) reportPartialReject(reporter matching.AccountID,
	reporterOrder, subjectOrder *Order, reject *venue.Reject) {

	switch reject.Type {
	// The reporter node already has channels with the subject node
	// so we make sure we match them with another trader for this
	// batch (this preference will be cleared for the next batch).
	case venue.PartialRejectDuplicatePeer:
		r.ReportConflict(
			reporterOrder.NodeKey,
			subjectOrder.NodeKey,
			reject.Reason,
		)

	// The reporter node couldn't complete the channel funding
	// negotiation with the subject node. We won't match the two
	// nodes anymore in the future (this conflict will be tracked
	// across multiple batches but not across server restarts).
	case venue.PartialRejectFundingFailed:
		r.ReportConflict(
			reporterOrder.NodeKey,
			subjectOrder.NodeKey,
			reject.Reason,
		)

	default:
		// TODO(guggero): The trader sent an invalid reject.
		// This needs to be rate limited very aggressively. For
		// now we just remove the order to avoid getting stuck
		// in an execution loop.
		log.Warnf("Trader %x sent invalid reject type %v",
			reporter[:], reject)
		r.RemoveIneligibleOrders([]orderT.Nonce{
			reporterOrder.Nonce,
		})
	}
}

// reportFullReject handles a full reject reported by a trader by removing the
// order from consideration.
func (r *RejectHandler) reportFullReject(reporter matching.AccountID,
	reporterOrder orderT.Nonce, reject *venue.Reject) {

	switch reject.Type {
	// The trader rejected the full batch because of a more serious
	// problem. We can't really do more than log the error and
	// remove their orders.
	case venue.FullRejectBatchVersionMismatch,
		venue.FullRejectServerMisbehavior,
		venue.FullRejectUnknown:

		log.Warnf("Trader %x rejected the full batch, order=%v, "+
			"reject=%v", reporter[:], reporterOrder, reject)
		r.RemoveIneligibleOrders([]orderT.Nonce{
			reporterOrder,
		})

	default:
		// TODO(guggero): The trader sent an invalid reject.
		// This needs to be rate limited very aggressively. For
		// now we just remove the order to avoid getting stuck
		// in an execution loop.
		log.Warnf("Trader %x sent invalid reject type %v",
			reporter[:], reject)
		r.RemoveIneligibleOrders([]orderT.Nonce{
			reporterOrder,
		})
	}
}

// HandleReject inspects all order rejects returned from the batch executor and
// creates the appropriate conflict reports or punishes traders if they exceeded
// their reject limit. Note that it is expected that if the reject map of a
// trader points to its own orders, it means it rejected the whole batch.
func (r *RejectHandler) HandleReject(orders []Match,
	rejectingTrader map[matching.AccountID]*venue.OrderRejectMap) {

	// Let's inspect the list of traders that rejected. We need to be aware
	// that messages are de-multiplexed for the venue and that we might have
	// entries that aren't valid. We make sure we only look at entries where
	// the reporting trader is part of the match with the rejected order.
	for reporter, rejectMap := range rejectingTrader {
		rejectMap := rejectMap
		reporter := reporter

		rejected := false
		removeAllOrders := func(rej *venue.Reject) {
			for _, reporterNonce := range rejectMap.OwnOrders {
				r.reportFullReject(
					reporter, reporterNonce, rej,
				)
			}

			rejected = true
		}

		// Remove the trader's own orders in case of a full reject.
		if rejectMap.FullReject != nil {
			log.Debugf("Full reject from %x, removing all orders",
				reporter[:])
			removeAllOrders(rejectMap.FullReject)
		}

		// For each partial reject, find the rejected order in the
		// batch and find out which order it was matched to.
		for rejectNonce, reject := range rejectMap.PartialRejects {
			var rejectedOrder, matchedOrder *Order
			for _, orderPair := range orders {
				ask := orderPair.Ask
				bid := orderPair.Bid

				if ask.Nonce == rejectNonce {
					rejectedOrder = &ask
					matchedOrder = &bid
					break
				}

				if bid.Nonce == rejectNonce {
					rejectedOrder = &bid
					matchedOrder = &ask
					break
				}
			}

			// We can't continue if the rejected nonce wasn't in the
			// batch.
			if rejectedOrder == nil || matchedOrder == nil {
				// TODO(guggero): The reporter reported a nonce
				// that wasn't in the batch. This could be an
				// attempt at interfering and should be
				// aggressively rate limited.
				log.Warnf("Trader %x sent invalid order in "+
					"reject: %v", reporter[:], rejectNonce)

				// Remove all orders for the trader.
				rej := &venue.Reject{
					Type:   venue.FullRejectUnknown,
					Reason: "invalid partial reject",
				}
				removeAllOrders(rej)
				continue
			}

			// Is the reporter on the other side of the rejected
			// order? If not, this could be the result of the
			// message de-multiplexing in the RPC server. Since we
			// found the order that was rejected (it was a valid
			// nonce), it is very unlikely that this is a deliberate
			// attempt to interfere, so we can safely skip this.
			// TODO(halseth): get rid of the RPC de-multiplexing to
			// avoid malicious nodes sending rejects for orders
			// they are not part of, since that can stall batch
			// execution.
			if matchedOrder.AcctKey != reporter {
				log.Warnf("Trader %x sent partial reject for "+
					"order %v it was not matched with",
					reporter[:], rejectNonce)
				continue
			}

			log.Warnf("Trader %x partially rejected order %v "+
				"with node %x", reporter[:], rejectNonce,
				rejectedOrder.NodeKey[:])

			// Report the conflicts now as we know both the
			// reporting trader's order and the subject's order.
			r.reportPartialReject(
				reporter, matchedOrder, rejectedOrder, reject,
			)
			rejected = true
		}

		// If the trader sent a reject message with nothing we could
		// act on, remove all its orders to ensure we converge on the
		// order book.
		if !rejected {
			log.Warnf("Trader %x sent reject with nothing to act "+
				"on, removing all orders", reporter[:])

			rej := &venue.Reject{
				Type:   venue.FullRejectUnknown,
				Reason: "invalid reject msg",
			}
			removeAllOrders(rej)
		}
	}
}
