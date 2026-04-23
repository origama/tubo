package controlplane

import "errors"

type Service struct{}

func (s *Service) RegisterService() error {
	return errors.New("not implemented")
}

func (s *Service) ResolveService() error {
	return errors.New("not implemented")
}
