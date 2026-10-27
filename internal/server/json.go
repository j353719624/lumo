package server

import (
	"encoding/json"
	"net/http"
)

func writeJSONBody(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}
