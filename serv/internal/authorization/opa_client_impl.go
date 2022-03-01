package authorization

import (
	"fmt"
	"github.com/go-resty/resty/v2"
	"github.com/pkg/errors"
	"strings"
)

type HasAccessRequest struct {
	Token     string      `json:"token"`
	Args      interface{} `json:"args"`
	IPAddress string      `json:"ipAddress"`
}

type HasAccessResponse struct {
	Result bool `json:"result"`
}

func NewOPAClient(config *Config) *OpaClientImpl {
	client := resty.New()
	client.SetBaseURL(fmt.Sprintf("http://localhost:%s/v1/data", config.Port))
	return &OpaClientImpl{
		client: client,
	}
}

func parseToken(jwt string) string {
	parts := strings.Fields(jwt)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

func (s *OpaClientImpl) HasAccess(policy string, jwt string, ipAddress string, args interface{}) (bool, error) {
	req := HasAccessRequest{
		Token:     parseToken(jwt),
		Args:      args,
		IPAddress: ipAddress,
	}

	var res HasAccessResponse
	resp, err := s.client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(Request{Input: req}).
		SetResult(&res).
		Post(policy)

	if err != nil {
		return false, errors.Wrap(err, "Failed to get response from OPA")
	}

	if resp.IsError() {
		return false, fmt.Errorf("failed to get response from authorization service: %d", resp.StatusCode())
	}

	return res.Result, nil
}
