package http

import (
	"encoding/json"
	"log"
	"net/http"
)

var httpClient = NewHTTPClientWrapper()

func RespondWithJSON(w http.ResponseWriter, code int, payload any) {
	response, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Error marshalling JSON response: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
		return
	}

	log.Printf("Returning %s", redactBodyForLog(response))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(response)
}

func GetHTTPClient() *http.Client {
	return httpClient.GetHTTPClient()
}

func GetHTTPClientWrapper() *HTTPClientWrapper {
	return httpClient
}
