// Code generated by mockery v1.0.0. DO NOT EDIT.

package mock

import flow "github.com/dapperlabs/flow-go/model/flow"
import mock "github.com/stretchr/testify/mock"

// ComplianceMetrics is an autogenerated mock type for the ComplianceMetrics type
type ComplianceMetrics struct {
	mock.Mock
}

// BlockFinalized provides a mock function with given fields: _a0
func (_m *ComplianceMetrics) BlockFinalized(_a0 *flow.Block) {
	_m.Called(_a0)
}

// BlockSealed provides a mock function with given fields: _a0
func (_m *ComplianceMetrics) BlockSealed(_a0 *flow.Block) {
	_m.Called(_a0)
}

// FinalizedHeight provides a mock function with given fields: height
func (_m *ComplianceMetrics) FinalizedHeight(height uint64) {
	_m.Called(height)
}

// SealedHeight provides a mock function with given fields: height
func (_m *ComplianceMetrics) SealedHeight(height uint64) {
	_m.Called(height)
}
