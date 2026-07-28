package upstream

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

type healthKey struct {
	endpointIdentity string
	fingerprint      [32]byte
}

type budgetKey struct {
	upstreamID  string
	fingerprint [32]byte
}

func makeHealthKey(_ string, endpointIdentity string, policy model.HealthPolicy) healthKey {
	digest := sha256.New()
	if policy.Active != nil {
		writeByte(digest, 1)
		writeString(digest, string(policy.Active.Type))
		writeUint64(digest, uint64(policy.Active.Timeout))
		writeUint64(digest, uint64(policy.Active.HealthyInterval))
		writeUint64(digest, uint64(policy.Active.UnhealthyInterval))
		writeByte(digest, policy.Active.HealthySuccesses)
		writeByte(digest, policy.Active.HTTPFailures)
		writeByte(digest, policy.Active.TransportFailures)
		writeByte(digest, policy.Active.Timeouts)
		writeStatuses(digest, policy.Active.HealthyStatuses)
		writeStatuses(digest, policy.Active.UnhealthyStatuses)
		writeString(digest, policy.Active.Path)
		writeString(digest, policy.Active.Host)
	} else {
		writeByte(digest, 0)
	}
	if policy.Passive != nil {
		writeByte(digest, 1)
		writeByte(digest, policy.Passive.HTTPFailures)
		writeByte(digest, policy.Passive.TransportFailures)
		writeByte(digest, policy.Passive.Timeouts)
		writeStatuses(digest, policy.Passive.UnhealthyStatuses)
	} else {
		writeByte(digest, 0)
	}
	var fingerprint [32]byte
	copy(fingerprint[:], digest.Sum(nil))
	return healthKey{endpointIdentity: endpointIdentity, fingerprint: fingerprint}
}

func makeBudgetKey(upstreamID string, policy model.RetryBudgetPolicy) budgetKey {
	digest := sha256.New()
	writeUint16(digest, policy.RatioPer1000)
	writeUint16(digest, policy.Burst)
	writeUint16(digest, policy.MaxInflight)
	var fingerprint [32]byte
	copy(fingerprint[:], digest.Sum(nil))
	return budgetKey{upstreamID: upstreamID, fingerprint: fingerprint}
}

func writeStatuses(dst hash.Hash, values []uint16) {
	writeUint64(dst, uint64(len(values)))
	for _, value := range values {
		writeUint16(dst, value)
	}
}

func writeString(dst hash.Hash, value string) {
	writeUint64(dst, uint64(len(value)))
	_, _ = dst.Write([]byte(value))
}

func writeByte(dst hash.Hash, value uint8) {
	_, _ = dst.Write([]byte{value})
}

func writeUint16(dst hash.Hash, value uint16) {
	var data [2]byte
	binary.BigEndian.PutUint16(data[:], value)
	_, _ = dst.Write(data[:])
}

func writeUint64(dst hash.Hash, value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	_, _ = dst.Write(data[:])
}
