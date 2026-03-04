package core

import (
	"net/http"
	"time"
)

// HTTPClient is a shared HTTP client with a reasonable timeout for platform use.
var HTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}
