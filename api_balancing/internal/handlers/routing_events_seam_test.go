package handlers

// Disable the fire-and-forget postBalancingEvent telemetry for the whole test
// binary. These events spawn a goroutine that reads the lb/state/geoip globals
// and outlives the test, racing withSeededBalancer's teardown under -race. No
// test asserts on the event (it is fire-and-forget to a nil Decklog client), so
// disabling it is lossless. Set once here (never restored) so the async readers
// never observe a concurrent write to the flag.
func init() {
	routingEventsDisabled = true
}
