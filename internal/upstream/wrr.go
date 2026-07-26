package upstream

import (
	"container/heap"
	"fmt"
	"sort"
	"sync/atomic"
)

type selectionState struct {
	sequence atomic.Uint64
}

type weightedEndpoint struct {
	identity string
	weight   uint32
}

type wrrSelector struct {
	state    *selectionState
	schedule []uint32
	direct   uint32
}

func compileWRR(endpoints []weightedEndpoint, state *selectionState) (wrrSelector, error) {
	if state == nil {
		return wrrSelector{}, fmt.Errorf("selection state is required")
	}
	active := make([]wrrSlot, 0, len(endpoints))
	for index, endpoint := range endpoints {
		if endpoint.weight == 0 {
			continue
		}
		active = append(active, wrrSlot{
			index:    uint32(index),
			identity: endpoint.identity,
			weight:   endpoint.weight,
		})
	}
	if len(active) == 0 {
		return wrrSelector{}, fmt.Errorf("at least one positive endpoint weight is required")
	}
	if len(active) == 1 {
		return wrrSelector{state: state, direct: active[0].index}, nil
	}
	if len(active) > MaxWRRSchedule {
		return wrrSelector{}, fmt.Errorf("active endpoints exceed WRR schedule limit %d", MaxWRRSchedule)
	}

	divisor := active[0].weight
	for index := 1; index < len(active); index++ {
		divisor = greatestCommonDivisor(divisor, active[index].weight)
	}
	var normalizedSum uint64
	for index := range active {
		active[index].weight /= divisor
		normalizedSum += uint64(active[index].weight)
	}

	scheduleLength := int(normalizedSum)
	if scheduleLength <= MaxWRRSchedule {
		for index := range active {
			active[index].assigned = active[index].weight
		}
	} else {
		scheduleLength = MaxWRRSchedule
		assignCappedWRRSlots(active, normalizedSum, scheduleLength)
	}

	schedule := make([]uint32, 0, scheduleLength)
	deadlines := wrrDeadlineHeap(active)
	heap.Init(&deadlines)
	for deadlines.Len() > 0 {
		slot := heap.Pop(&deadlines).(wrrSlot)
		schedule = append(schedule, slot.index)
		slot.emitted++
		if slot.emitted < slot.assigned {
			heap.Push(&deadlines, slot)
		}
	}
	return wrrSelector{
		state:    state,
		schedule: schedule,
	}, nil
}

func (s *wrrSelector) selectIndex() uint32 {
	if len(s.schedule) == 0 {
		return s.direct
	}
	next := s.state.sequence.Add(1) - 1
	return s.schedule[next%uint64(len(s.schedule))]
}

type wrrSlot struct {
	index     uint32
	identity  string
	weight    uint32
	assigned  uint32
	emitted   uint32
	remainder uint64
}

func assignCappedWRRSlots(slots []wrrSlot, normalizedSum uint64, scheduleLength int) {
	remaining := uint64(scheduleLength - len(slots))
	allocated := len(slots)
	for index := range slots {
		numerator := uint64(slots[index].weight) * remaining
		extra := numerator / normalizedSum
		slots[index].assigned = 1 + uint32(extra)
		slots[index].remainder = numerator % normalizedSum
		allocated += int(extra)
	}

	order := make([]int, len(slots))
	for index := range order {
		order[index] = index
	}
	sort.Slice(order, func(i, j int) bool {
		left := slots[order[i]]
		right := slots[order[j]]
		if left.remainder != right.remainder {
			return left.remainder > right.remainder
		}
		if left.identity != right.identity {
			return left.identity < right.identity
		}
		return left.index < right.index
	})
	for _, index := range order[:scheduleLength-allocated] {
		slots[index].assigned++
	}
}

type wrrDeadlineHeap []wrrSlot

func (h wrrDeadlineHeap) Len() int {
	return len(h)
}

func (h wrrDeadlineHeap) Less(i, j int) bool {
	left := uint64(h[i].emitted+1) * uint64(h[j].assigned)
	right := uint64(h[j].emitted+1) * uint64(h[i].assigned)
	if left != right {
		return left < right
	}
	if h[i].identity != h[j].identity {
		return h[i].identity < h[j].identity
	}
	return h[i].index < h[j].index
}

func (h wrrDeadlineHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *wrrDeadlineHeap) Push(value any) {
	*h = append(*h, value.(wrrSlot))
}

func (h *wrrDeadlineHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = wrrSlot{}
	*h = old[:last]
	return value
}

func greatestCommonDivisor(left, right uint32) uint32 {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}
