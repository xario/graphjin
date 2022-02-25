package authorization

import (
	"github.com/go-resty/resty/v2"
)

type OpaClient interface {
	HasAccess(policy string, jwt string, ipAddress string, args interface{}) (bool, error)
}

type OpaClientImpl struct {
	client *resty.Client
}

type Request struct {
	Input interface{} `json:"input"`
}
