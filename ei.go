package ei

import "sync/atomic"

type Entity struct {
	dead bool
}

func (e *Entity) Kill() { e.dead = true }

func (e *Entity) Alive() bool { return !e.dead }

type EntityAtomic struct {
	dead atomic.Bool
}

func (e *EntityAtomic) Kill() { e.dead.Store(true) }

func (e *EntityAtomic) Alive() bool { return !e.dead.Load() }

type Interface interface {
	Kill()
	Alive() bool
}

func Sweep[E Interface, S ~[]E](xs *S) {
	j := 0
	for _, x := range *xs {
		if x.Alive() {
			(*xs)[j] = x
			j++
		}
	}
	*xs = (*xs)[:j]
}

func SweepEach[E Interface, S ~[]E](xs *S, pred func(int, E)) {
	j := 0
	for i, x := range *xs {
		if x.Alive() {
			(*xs)[j] = x
			j++
			pred(i, x)
		}
	}
	*xs = (*xs)[:j]
}

func SweepMap[K comparable, V Interface, M ~map[K]V](m M) {
	for k, v := range m {
		if !v.Alive() {
			delete(m, k)
		}
	}
}

func SweepEachMap[K comparable, V Interface, M ~map[K]V](m M, pred func(K, V)) {
	for k, v := range m {
		if !v.Alive() {
			delete(m, k)
		} else {
			pred(k, v)
		}
	}
}
