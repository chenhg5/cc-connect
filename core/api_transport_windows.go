package core

import "github.com/chenhg5/cc-connect/internal/localapi"

func listenLocalAPI(dataDir string) (apiEndpoint, error) {
	listener, err := localapi.Listen(dataDir)
	if err != nil {
		return apiEndpoint{}, err
	}
	return apiEndpoint{path: listener.EndpointPath(), listener: listener}, nil
}
