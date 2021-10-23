package poolgen

// This program generates mocks. It can be invoked by running:
//
// make mock
//

//go:generate mockgen -source=../account/watcher/watcher_controller.go -package=watcher -destination=../account/watcher/mock_watcher_controller.go
