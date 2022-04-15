package status

import (
	"encoding/json"
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

// StatusHandler returns information about the current status.
func (r *Reporter) StatusHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if req.Method == http.MethodGet {
			status := r.GetStatus()
			respBody, err := json.Marshal(status)
			if err != nil {
				log.Errorf("unable to marshal status: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(respBody); err != nil {
				log.Errorf("unable to write resp body: %v", err)
			}
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	}
}
