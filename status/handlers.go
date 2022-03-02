package status

import (
	"net/http"
)

// LivenessHandler is used by k8s to know when to restart a container.
func (r *Reporter) LivenessHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		status := r.GetStatus()
		if status.IsAlive {
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

// ReadinessHandler is used by k8s to know when to add the pod to the service
// load balancers.
func (r *Reporter) ReadinessHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		status := r.GetStatus()
		if status.IsReady {
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusServiceUnavailable)
	}
}
