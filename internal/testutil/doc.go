// Package testutil holds the test plumbing that go-discern's packages share:
// race-aware allocation guards, in-memory and recording HTTP fakes, fmt leak
// assertions, error extraction and JSON helpers.
//
// It is imported only by tests. It imports discern, testify and the standard
// library, and never an adapter or another internal package, so every test
// package in the module can use it, except internal tests of the root package,
// which discern itself would import back.
package testutil
