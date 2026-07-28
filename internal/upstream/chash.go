package upstream

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/cespare/xxhash/v2"
)

const continuumPointsPerWeight = 64

type continuum struct {
	hashes  []uint64
	indexes []uint32
	direct  uint32
}

func (c *continuum) selectIndex(sum uint64) uint32 {
	if len(c.hashes) == 0 {
		return c.direct
	}
	index := sort.Search(len(c.hashes), func(i int) bool {
		return c.hashes[i] >= sum
	})
	if index == len(c.hashes) {
		index = 0
	}
	return c.indexes[index]
}

func (c *continuum) selectNext(sum uint64, endpointCount int, selectable func(uint32) bool) (uint32, bool) {
	if len(c.hashes) == 0 {
		return c.direct, selectable(c.direct)
	}
	start := sort.Search(len(c.hashes), func(i int) bool {
		return c.hashes[i] >= sum
	})
	if start == len(c.hashes) {
		start = 0
	}
	first := c.indexes[start]
	if selectable(first) {
		return first, true
	}

	seen := make([]bool, endpointCount)
	if int(first) < len(seen) {
		seen[first] = true
	}
	distinct := 1
	for offset := 1; offset < len(c.hashes) && distinct < endpointCount; offset++ {
		ordinal := c.indexes[(start+offset)%len(c.hashes)]
		if int(ordinal) >= len(seen) || seen[ordinal] {
			continue
		}
		seen[ordinal] = true
		distinct++
		if selectable(ordinal) {
			return ordinal, true
		}
	}
	return 0, false
}

type continuumEndpoint struct {
	index     uint32
	identity  string
	weight    uint32
	assigned  uint32
	remainder uint64
}

type continuumPoint struct {
	hash          uint64
	identity      string
	virtualIndex  uint32
	endpointIndex uint32
}

func compileContinuum(endpoints []weightedEndpoint) (continuum, error) {
	active := make([]continuumEndpoint, 0, len(endpoints))
	for index, endpoint := range endpoints {
		if endpoint.weight == 0 {
			continue
		}
		active = append(active, continuumEndpoint{
			index:    uint32(index),
			identity: endpoint.identity,
			weight:   endpoint.weight,
		})
	}
	if len(active) == 0 {
		return continuum{}, fmt.Errorf("at least one positive endpoint weight is required")
	}
	if len(active) == 1 {
		return continuum{direct: active[0].index}, nil
	}
	if len(active) > MaxContinuumPoints {
		return continuum{}, fmt.Errorf("active endpoints exceed continuum point limit %d", MaxContinuumPoints)
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

	targetPoints := normalizedSum * continuumPointsPerWeight
	pointCount := int(targetPoints)
	if pointCount <= MaxContinuumPoints {
		for index := range active {
			active[index].assigned = active[index].weight * continuumPointsPerWeight
		}
	} else {
		pointCount = MaxContinuumPoints
		assignCappedContinuumPoints(active, normalizedSum, pointCount)
	}

	points := make([]continuumPoint, 0, pointCount)
	for _, endpoint := range active {
		for virtualIndex := uint32(0); virtualIndex < endpoint.assigned; virtualIndex++ {
			points = append(points, continuumPoint{
				hash:          continuumPointHash(endpoint.identity, uint64(virtualIndex)),
				identity:      endpoint.identity,
				virtualIndex:  virtualIndex,
				endpointIndex: endpoint.index,
			})
		}
	}
	sortContinuumPoints(points)

	compiled := continuum{
		hashes:  make([]uint64, len(points)),
		indexes: make([]uint32, len(points)),
	}
	for index, point := range points {
		compiled.hashes[index] = point.hash
		compiled.indexes[index] = point.endpointIndex
	}
	return compiled, nil
}

func assignCappedContinuumPoints(endpoints []continuumEndpoint, normalizedSum uint64, pointCount int) {
	remaining := uint64(pointCount - len(endpoints))
	allocated := len(endpoints)
	for index := range endpoints {
		numerator := uint64(endpoints[index].weight) * remaining
		extra := numerator / normalizedSum
		endpoints[index].assigned = 1 + uint32(extra)
		endpoints[index].remainder = numerator % normalizedSum
		allocated += int(extra)
	}

	order := make([]int, len(endpoints))
	for index := range order {
		order[index] = index
	}
	sort.Slice(order, func(i, j int) bool {
		left := endpoints[order[i]]
		right := endpoints[order[j]]
		if left.remainder != right.remainder {
			return left.remainder > right.remainder
		}
		if left.identity != right.identity {
			return left.identity < right.identity
		}
		return left.index < right.index
	})
	for _, index := range order[:pointCount-allocated] {
		endpoints[index].assigned++
	}
}

func continuumPointHash(identity string, virtualIndex uint64) uint64 {
	var digest xxhash.Digest
	digest.Reset()
	writeHashString(&digest, identity)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], virtualIndex)
	_, _ = digest.Write(encoded[:])
	return digest.Sum64()
}

func sortContinuumPoints(points []continuumPoint) {
	sort.Slice(points, func(i, j int) bool {
		if points[i].hash != points[j].hash {
			return points[i].hash < points[j].hash
		}
		if points[i].identity != points[j].identity {
			return points[i].identity < points[j].identity
		}
		return points[i].virtualIndex < points[j].virtualIndex
	})
}
