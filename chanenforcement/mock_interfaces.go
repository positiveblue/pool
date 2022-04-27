// Code generated by MockGen. DO NOT EDIT.
// Source: chanenforcement/interface.go

// Package chanenforcement is a generated GoMock package.
package chanenforcement

import (
	context "context"
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
)

// MockPackageSource is a mock of PackageSource interface.
type MockPackageSource struct {
	ctrl     *gomock.Controller
	recorder *MockPackageSourceMockRecorder
}

// MockPackageSourceMockRecorder is the mock recorder for MockPackageSource.
type MockPackageSourceMockRecorder struct {
	mock *MockPackageSource
}

// NewMockPackageSource creates a new mock instance.
func NewMockPackageSource(ctrl *gomock.Controller) *MockPackageSource {
	mock := &MockPackageSource{ctrl: ctrl}
	mock.recorder = &MockPackageSourceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockPackageSource) EXPECT() *MockPackageSourceMockRecorder {
	return m.recorder
}

// EnforceLifetimeViolation mocks base method.
func (m *MockPackageSource) EnforceLifetimeViolation(arg0 context.Context, arg1 *LifetimePackage, height uint32) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "EnforceLifetimeViolation", arg0, arg1, height)
	ret0, _ := ret[0].(error)
	return ret0
}

// EnforceLifetimeViolation indicates an expected call of EnforceLifetimeViolation.
func (mr *MockPackageSourceMockRecorder) EnforceLifetimeViolation(arg0, arg1, height interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "EnforceLifetimeViolation", reflect.TypeOf((*MockPackageSource)(nil).EnforceLifetimeViolation), arg0, arg1, height)
}

// LifetimePackages mocks base method.
func (m *MockPackageSource) LifetimePackages(arg0 context.Context) ([]*LifetimePackage, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifetimePackages", arg0)
	ret0, _ := ret[0].([]*LifetimePackage)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LifetimePackages indicates an expected call of LifetimePackages.
func (mr *MockPackageSourceMockRecorder) LifetimePackages(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifetimePackages", reflect.TypeOf((*MockPackageSource)(nil).LifetimePackages), arg0)
}

// PruneLifetimePackage mocks base method.
func (m *MockPackageSource) PruneLifetimePackage(arg0 context.Context, arg1 *LifetimePackage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PruneLifetimePackage", arg0, arg1)
	ret0, _ := ret[0].(error)
	return ret0
}

// PruneLifetimePackage indicates an expected call of PruneLifetimePackage.
func (mr *MockPackageSourceMockRecorder) PruneLifetimePackage(arg0, arg1 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PruneLifetimePackage", reflect.TypeOf((*MockPackageSource)(nil).PruneLifetimePackage), arg0, arg1)
}

// MockStore is a mock of Store interface.
type MockStore struct {
	ctrl     *gomock.Controller
	recorder *MockStoreMockRecorder
}

// MockStoreMockRecorder is the mock recorder for MockStore.
type MockStoreMockRecorder struct {
	mock *MockStore
}

// NewMockStore creates a new mock instance.
func NewMockStore(ctrl *gomock.Controller) *MockStore {
	mock := &MockStore{ctrl: ctrl}
	mock.recorder = &MockStoreMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockStore) EXPECT() *MockStoreMockRecorder {
	return m.recorder
}

// DeleteLifetimePackage mocks base method.
func (m *MockStore) DeleteLifetimePackage(ctx context.Context, pkg *LifetimePackage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteLifetimePackage", ctx, pkg)
	ret0, _ := ret[0].(error)
	return ret0
}

// DeleteLifetimePackage indicates an expected call of DeleteLifetimePackage.
func (mr *MockStoreMockRecorder) DeleteLifetimePackage(ctx, pkg interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteLifetimePackage", reflect.TypeOf((*MockStore)(nil).DeleteLifetimePackage), ctx, pkg)
}

// EnforceLifetimeViolation mocks base method.
func (m *MockStore) EnforceLifetimeViolation(ctx context.Context, pkg *LifetimePackage, height uint32) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "EnforceLifetimeViolation", ctx, pkg, height)
	ret0, _ := ret[0].(error)
	return ret0
}

// EnforceLifetimeViolation indicates an expected call of EnforceLifetimeViolation.
func (mr *MockStoreMockRecorder) EnforceLifetimeViolation(ctx, pkg, height interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "EnforceLifetimeViolation", reflect.TypeOf((*MockStore)(nil).EnforceLifetimeViolation), ctx, pkg, height)
}

// LifetimePackages mocks base method.
func (m *MockStore) LifetimePackages(ctx context.Context) ([]*LifetimePackage, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LifetimePackages", ctx)
	ret0, _ := ret[0].([]*LifetimePackage)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LifetimePackages indicates an expected call of LifetimePackages.
func (mr *MockStoreMockRecorder) LifetimePackages(ctx interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LifetimePackages", reflect.TypeOf((*MockStore)(nil).LifetimePackages), ctx)
}

// StoreLifetimePackage mocks base method.
func (m *MockStore) StoreLifetimePackage(ctx context.Context, pkg *LifetimePackage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "StoreLifetimePackage", ctx, pkg)
	ret0, _ := ret[0].(error)
	return ret0
}

// StoreLifetimePackage indicates an expected call of StoreLifetimePackage.
func (mr *MockStoreMockRecorder) StoreLifetimePackage(ctx, pkg interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "StoreLifetimePackage", reflect.TypeOf((*MockStore)(nil).StoreLifetimePackage), ctx, pkg)
}
