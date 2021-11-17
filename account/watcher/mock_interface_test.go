// Code generated by MockGen. DO NOT EDIT.
// Source: account/watcher/interfaces.go

// Package watcher is a generated GoMock package.
package watcher

import (
	reflect "reflect"

	btcec "github.com/btcsuite/btcd/btcec"
	chainhash "github.com/btcsuite/btcd/chaincfg/chainhash"
	wire "github.com/btcsuite/btcd/wire"
	gomock "github.com/golang/mock/gomock"
	chainntnfs "github.com/lightningnetwork/lnd/chainntnfs"
)

// MockControllerInterface is a mock of ControllerInterface interface.
type MockControllerInterface struct {
	ctrl     *gomock.Controller
	recorder *MockControllerInterfaceMockRecorder
}

// MockControllerInterfaceMockRecorder is the mock recorder for MockControllerInterface.
type MockControllerInterfaceMockRecorder struct {
	mock *MockControllerInterface
}

// NewMockControllerInterface creates a new mock instance.
func NewMockControllerInterface(ctrl *gomock.Controller) *MockControllerInterface {
	mock := &MockControllerInterface{ctrl: ctrl}
	mock.recorder = &MockControllerInterfaceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockControllerInterface) EXPECT() *MockControllerInterfaceMockRecorder {
	return m.recorder
}

// CancelAccountConf mocks base method.
func (m *MockControllerInterface) CancelAccountConf(traderKey *btcec.PublicKey) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "CancelAccountConf", traderKey)
}

// CancelAccountConf indicates an expected call of CancelAccountConf.
func (mr *MockControllerInterfaceMockRecorder) CancelAccountConf(traderKey interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CancelAccountConf", reflect.TypeOf((*MockControllerInterface)(nil).CancelAccountConf), traderKey)
}

// CancelAccountSpend mocks base method.
func (m *MockControllerInterface) CancelAccountSpend(traderKey *btcec.PublicKey) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "CancelAccountSpend", traderKey)
}

// CancelAccountSpend indicates an expected call of CancelAccountSpend.
func (mr *MockControllerInterfaceMockRecorder) CancelAccountSpend(traderKey interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CancelAccountSpend", reflect.TypeOf((*MockControllerInterface)(nil).CancelAccountSpend), traderKey)
}

// Start mocks base method.
func (m *MockControllerInterface) Start() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Start")
	ret0, _ := ret[0].(error)
	return ret0
}

// Start indicates an expected call of Start.
func (mr *MockControllerInterfaceMockRecorder) Start() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Start", reflect.TypeOf((*MockControllerInterface)(nil).Start))
}

// Stop mocks base method.
func (m *MockControllerInterface) Stop() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Stop")
}

// Stop indicates an expected call of Stop.
func (mr *MockControllerInterfaceMockRecorder) Stop() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Stop", reflect.TypeOf((*MockControllerInterface)(nil).Stop))
}

// WatchAccountConf mocks base method.
func (m *MockControllerInterface) WatchAccountConf(traderKey *btcec.PublicKey, txHash chainhash.Hash, script []byte, numConfs, heightHint uint32) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "WatchAccountConf", traderKey, txHash, script, numConfs, heightHint)
	ret0, _ := ret[0].(error)
	return ret0
}

// WatchAccountConf indicates an expected call of WatchAccountConf.
func (mr *MockControllerInterfaceMockRecorder) WatchAccountConf(traderKey, txHash, script, numConfs, heightHint interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "WatchAccountConf", reflect.TypeOf((*MockControllerInterface)(nil).WatchAccountConf), traderKey, txHash, script, numConfs, heightHint)
}

// WatchAccountExpiration mocks base method.
func (m *MockControllerInterface) WatchAccountExpiration(traderKey *btcec.PublicKey, expiry uint32) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "WatchAccountExpiration", traderKey, expiry)
}

// WatchAccountExpiration indicates an expected call of WatchAccountExpiration.
func (mr *MockControllerInterfaceMockRecorder) WatchAccountExpiration(traderKey, expiry interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "WatchAccountExpiration", reflect.TypeOf((*MockControllerInterface)(nil).WatchAccountExpiration), traderKey, expiry)
}

// WatchAccountSpend mocks base method.
func (m *MockControllerInterface) WatchAccountSpend(traderKey *btcec.PublicKey, accountPoint wire.OutPoint, script []byte, heightHint uint32) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "WatchAccountSpend", traderKey, accountPoint, script, heightHint)
	ret0, _ := ret[0].(error)
	return ret0
}

// WatchAccountSpend indicates an expected call of WatchAccountSpend.
func (mr *MockControllerInterfaceMockRecorder) WatchAccountSpend(traderKey, accountPoint, script, heightHint interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "WatchAccountSpend", reflect.TypeOf((*MockControllerInterface)(nil).WatchAccountSpend), traderKey, accountPoint, script, heightHint)
}

// MockWatcherInterface is a mock of WatcherInterface interface.
type MockWatcherInterface struct {
	ctrl     *gomock.Controller
	recorder *MockWatcherInterfaceMockRecorder
}

// MockWatcherInterfaceMockRecorder is the mock recorder for MockWatcherInterface.
type MockWatcherInterfaceMockRecorder struct {
	mock *MockWatcherInterface
}

// NewMockWatcherInterface creates a new mock instance.
func NewMockWatcherInterface(ctrl *gomock.Controller) *MockWatcherInterface {
	mock := &MockWatcherInterface{ctrl: ctrl}
	mock.recorder = &MockWatcherInterfaceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockWatcherInterface) EXPECT() *MockWatcherInterfaceMockRecorder {
	return m.recorder
}

// AddAccountExpiration mocks base method.
func (m *MockWatcherInterface) AddAccountExpiration(traderKey *btcec.PublicKey, expiry uint32) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "AddAccountExpiration", traderKey, expiry)
}

// AddAccountExpiration indicates an expected call of AddAccountExpiration.
func (mr *MockWatcherInterfaceMockRecorder) AddAccountExpiration(traderKey, expiry interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddAccountExpiration", reflect.TypeOf((*MockWatcherInterface)(nil).AddAccountExpiration), traderKey, expiry)
}

// HandleAccountConf mocks base method.
func (m *MockWatcherInterface) HandleAccountConf(traderKey *btcec.PublicKey, confDetails *chainntnfs.TxConfirmation) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "HandleAccountConf", traderKey, confDetails)
	ret0, _ := ret[0].(error)
	return ret0
}

// HandleAccountConf indicates an expected call of HandleAccountConf.
func (mr *MockWatcherInterfaceMockRecorder) HandleAccountConf(traderKey, confDetails interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "HandleAccountConf", reflect.TypeOf((*MockWatcherInterface)(nil).HandleAccountConf), traderKey, confDetails)
}

// HandleAccountExpiry mocks base method.
func (m *MockWatcherInterface) HandleAccountExpiry(traderKey *btcec.PublicKey, height uint32) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "HandleAccountExpiry", traderKey, height)
	ret0, _ := ret[0].(error)
	return ret0
}

// HandleAccountExpiry indicates an expected call of HandleAccountExpiry.
func (mr *MockWatcherInterfaceMockRecorder) HandleAccountExpiry(traderKey, height interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "HandleAccountExpiry", reflect.TypeOf((*MockWatcherInterface)(nil).HandleAccountExpiry), traderKey, height)
}

// HandleAccountSpend mocks base method.
func (m *MockWatcherInterface) HandleAccountSpend(traderKey *btcec.PublicKey, spendDetails *chainntnfs.SpendDetail) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "HandleAccountSpend", traderKey, spendDetails)
	ret0, _ := ret[0].(error)
	return ret0
}

// HandleAccountSpend indicates an expected call of HandleAccountSpend.
func (mr *MockWatcherInterfaceMockRecorder) HandleAccountSpend(traderKey, spendDetails interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "HandleAccountSpend", reflect.TypeOf((*MockWatcherInterface)(nil).HandleAccountSpend), traderKey, spendDetails)
}

// NewBlock mocks base method.
func (m *MockWatcherInterface) NewBlock(bestHeight uint32) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "NewBlock", bestHeight)
}

// NewBlock indicates an expected call of NewBlock.
func (mr *MockWatcherInterfaceMockRecorder) NewBlock(bestHeight interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "NewBlock", reflect.TypeOf((*MockWatcherInterface)(nil).NewBlock), bestHeight)
}

// OverdueExpirations mocks base method.
func (m *MockWatcherInterface) OverdueExpirations(blockHeight uint32) {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "OverdueExpirations", blockHeight)
}

// OverdueExpirations indicates an expected call of OverdueExpirations.
func (mr *MockWatcherInterfaceMockRecorder) OverdueExpirations(blockHeight interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "OverdueExpirations", reflect.TypeOf((*MockWatcherInterface)(nil).OverdueExpirations), blockHeight)
}
