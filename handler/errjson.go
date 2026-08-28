package handler

import (
	"encoding/json"
	"net/http"
)

func writeError(w http.ResponseWriter, status int, message, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]interface{}{"message": message, "type": errType, "code": status}})
}
