package events

// SetBeforeActivate sets the hook that runs between a new subscription's
// registration and the read that gives it its filters, so that a test in
// package events_test can look at the subscription in that window. It is in a
// test file: nothing outside a test can set it.
func SetBeforeActivate(s *Service, hook func()) { s.beforeActivate = hook }
