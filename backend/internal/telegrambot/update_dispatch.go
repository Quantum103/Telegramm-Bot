package telegrambot

import (
	"context"
	"sort"
)

type pendingUpdateResult struct {
	updateID int64
	result   <-chan bool
}

// dispatchUpdateBatch acknowledges callback UI events before queueing their
// business work. It advances offset only through the successful prefix of the
// batch, while still collecting every submitted result before the next poll.
func (b *Bot) dispatchUpdateBatch(
	ctx context.Context,
	pool *UpdateWorkerPool,
	updates []Update,
	offset int64,
) (int64, bool) {
	sort.SliceStable(updates, func(i, j int) bool {
		return updates[i].UpdateID < updates[j].UpdateID
	})

	pending := make([]pendingUpdateResult, 0, len(updates))
	retry := false
	for _, update := range updates {
		if ctx.Err() != nil {
			return offset, true
		}
		if update.UpdateID < offset {
			continue
		}

		b.acknowledgeUpdate(ctx, update)
		if ctx.Err() != nil {
			return offset, true
		}
		result, err := pool.Submit(ctx, update)
		if err != nil {
			retry = true
			if ctx.Err() == nil {
				b.logger.Warn(
					"не удалось поставить обновление Telegram в очередь; offset не будет продвинут дальше",
					"update_id", update.UpdateID,
					"error", err,
				)
			}
			break
		}
		pending = append(pending, pendingUpdateResult{
			updateID: update.UpdateID,
			result:   result,
		})
	}

	// Results are observed in update_id order, not completion order. Once an
	// update fails, later successes cannot move the Telegram offset past it.
	prefixSucceeded := true
	for _, item := range pending {
		select {
		case processed, ok := <-item.result:
			if !ok || !processed {
				prefixSucceeded = false
				retry = true
				continue
			}
			if prefixSucceeded {
				offset = item.updateID + 1
			}
		case <-ctx.Done():
			return offset, true
		}
	}

	return offset, retry
}
