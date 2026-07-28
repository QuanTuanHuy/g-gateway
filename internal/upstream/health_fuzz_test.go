package upstream

import "testing"

func FuzzEndpointHealth(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4})
	f.Add([]byte{4, 4, 0, 0, 1, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		health := newEndpointHealth("users\x00http://a:80", thresholdTwoHealthPolicy(), 1)
		for _, value := range data {
			health.Observe(Observation{
				Source: ObservationSource(value % 2),
				Kind:   OutcomeKind(value % 5),
				Status: []int{200, 503, 302}[int(value)%3],
			})
			state := health.State()
			if state > HealthUnhealthy {
				t.Fatalf("illegal state %d", state)
			}
			if health.Selectable() == (state == HealthUnhealthy) {
				t.Fatalf("selectable mismatch for %v", state)
			}
		}
		if health.State() == HealthUnhealthy {
			health.Observe(Observation{Source: SourcePassive, Kind: OutcomeSuccess, Status: 200})
			health.Observe(Observation{Source: SourcePassive, Kind: OutcomeSuccess, Status: 200})
			if health.State() != HealthUnhealthy {
				t.Fatalf("passive recovered unhealthy endpoint")
			}
		}
	})
}
