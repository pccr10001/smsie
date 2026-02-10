//go:build !nouac

package calling

import "sync"

type int16Ring struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      []int16
	head     int
	tail     int
	count    int
	shutdown bool
}

func newInt16Ring(capacity int) *int16Ring {
	if capacity < 1 {
		capacity = 1
	}
	r := &int16Ring{buf: make([]int16, capacity)}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *int16Ring) Close() {
	r.mu.Lock()
	r.shutdown = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *int16Ring) Write(data []int16) {
	r.mu.Lock()
	defer func() {
		r.cond.Broadcast()
		r.mu.Unlock()
	}()

	for _, v := range data {
		if r.count == len(r.buf) {
			r.head = (r.head + 1) % len(r.buf)
			r.count--
		}
		r.buf[r.tail] = v
		r.tail = (r.tail + 1) % len(r.buf)
		r.count++
	}
}

func (r *int16Ring) ReadPartial(dst []int16) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shutdown {
		return 0, false
	}
	if len(dst) == 0 || r.count == 0 {
		return 0, true
	}
	n := len(dst)
	if n > r.count {
		n = r.count
	}
	for i := 0; i < n; i++ {
		dst[i] = r.buf[r.head]
		r.head = (r.head + 1) % len(r.buf)
		r.count--
	}
	return n, true
}
