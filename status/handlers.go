package status

import (
	"encoding/json"
	"io/ioutil"
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

// StatusHandler supports the GET and POST methods. GET will return information
// about the current status while POST will update it.
func (r *Reporter) StatusHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// GET: Return the current status.
		if req.Method == http.MethodGet {
			status := r.GetStatus()
			respBody, err := json.Marshal(status)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(respBody); err != nil {
				log.Info("unable to write resp body: %v", err)
			}
			return
		}

		// POST: Update the current status.
		if req.Method == http.MethodPost {
			var body struct {
				Status string
			}

			b, err := ioutil.ReadAll(req.Body)
			defer req.Body.Close()
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if err = json.Unmarshal(b, &body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			if err := r.SetStatus(body.Status); err != nil {
				log.Info("unable to set status: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	}
}
