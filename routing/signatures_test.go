package routing

import "testing"

var _ func(*DeliveryGraph, WaveSettlement, RoutingGeneration, ...bool) (WaveSettlementResult, error) = (*DeliveryGraph).SettleWave
var _ func(*DeliveryGraph, string, int, TaskFailure, RoutingGeneration, string, bool) (TaskFailureResult, error) = (*DeliveryGraph).RecordFailure

func TestFrozenSignatures(t *testing.T) {
	t.Parallel()
}
