package helper

import "testing"

func TestFreePortPairKeepsBridgeAndGatewayDistinct(t *testing.T) {
	for i := 0; i < 20; i++ {
		bridge, gateway := freePortPair(t)
		if bridge <= 0 || gateway <= 0 || bridge == gateway {
			t.Fatalf("bridge port %d and gateway port %d must be distinct", bridge, gateway)
		}
	}
}
