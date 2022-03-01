package authorization

type OpaClientMock struct{}

func NewOPAClientMock() *OpaClientMock {
	return &OpaClientMock{}
}

func (s *OpaClientMock) HasAccess(policy string, jwt string, ipAddress string, args interface{}) (bool, error) {
	return true, nil
}
