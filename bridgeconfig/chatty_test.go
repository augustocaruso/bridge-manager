package bridgeconfig

import "testing"

func TestNativeOverlayIsNotSupportedBridge(t *testing.T) {
	if IsSupported("chatty") {
		t.Error("native overlay advertised as a bridge")
	}
	for _, name := range SupportedBridges {
		if name == "chatty" {
			t.Error("native overlay listed as a bridge")
		}
	}
	if _, err := Generate("chatty", Params{}); err != nil {
		t.Fatalf("native overlay cannot be rendered: %v", err)
	}
	if !IsSupported("instagram") || !IsSupported("whatsapp") {
		t.Fatal("upstream bridges must remain selectable")
	}
}
