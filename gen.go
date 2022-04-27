package subasta

// This program generates mocks. It can be invoked by running:
//
// make mock
//

//go:generate mockgen -source=chanenforcement/interface.go -package=chanenforcement -destination=chanenforcement/mock_interfaces.go
//go:generate mockgen -source=subastadb/interface.go -package=subastadb -destination=subastadb/mock_interfaces.go
