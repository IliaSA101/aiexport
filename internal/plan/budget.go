package plan

import (
	"context"
	"sync"
)

// budget — семафор по суммарным байтам, а не по числу горутин. Ограничивать надо
// память: 8 воркеров по 16 МБ и 8 воркеров по 2 КБ — совершенно разные нагрузки.
type budget struct {
	mu   sync.Mutex
	cap  int64
	free int64
	wake chan struct{} // закрывается при каждом release: будит всех ждущих
}

func newBudget(capacity int64) *budget {
	return &budget{cap: capacity, free: capacity, wake: make(chan struct{})}
}

// acquire занимает n байт или ждёт. Проверка «хватает ли места» и само занятие
// выполняются под одним замком — иначе два воркера увидели бы один и тот же остаток.
// Запрос больше всего бюджета урезается до бюджета: иначе он ждал бы вечно.
func (b *budget) acquire(ctx context.Context, n int64) (int64, error) {
	n = max(1, min(n, b.cap))
	for {
		b.mu.Lock()
		if b.free >= n {
			b.free -= n
			b.mu.Unlock()
			return n, nil
		}
		ch := b.wake
		b.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (b *budget) release(n int64) {
	b.mu.Lock()
	b.free += n
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
}
