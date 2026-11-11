package main

// A selected domain list is a routing restriction even when it is empty
// or cannot be loaded. Falling back to a full tunnel would route unrelated traffic.
func fullTunnelForSelection(appCount, domainListCount int) bool {
	return appCount == 0 && domainListCount == 0
}
