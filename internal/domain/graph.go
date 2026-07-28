package domain

import (
	"errors"
	"fmt"
)

const DefaultMaxPipeHops = 8

type PipeLoader func(Ref) (PipeSpec, error)

type ServiceLoader func(Ref) (ServiceSpec, error)

type LogicalPath struct {
	Pipes    []Ref `json:"pipes"`
	Terminal Ref   `json:"terminal"`
}

func ResolveLogicalPath(start Ref, maxHops int, loadPipe PipeLoader, loadService ServiceLoader) (LogicalPath, error) {
	if err := start.RequireKind(KindPipe); err != nil {
		return LogicalPath{}, err
	}
	if maxHops <= 0 {
		maxHops = DefaultMaxPipeHops
	}
	if loadPipe == nil || loadService == nil {
		return LogicalPath{}, errors.New("pipe and service loaders are required")
	}

	path := LogicalPath{}
	visited := make(map[string]struct{}, maxHops)
	current := start
	for hop := 0; hop < maxHops; hop++ {
		if _, exists := visited[current.String()]; exists {
			return LogicalPath{}, fmt.Errorf("pipe graph cycle detected at %s", current)
		}
		visited[current.String()] = struct{}{}

		pipe, err := loadPipe(current)
		if err != nil {
			return LogicalPath{}, fmt.Errorf("load %s: %w", current, err)
		}
		if err := pipe.Validate(); err != nil {
			return LogicalPath{}, err
		}
		path.Pipes = append(path.Pipes, pipe.Ref)
		switch pipe.Target.Kind {
		case KindPipe:
			current = pipe.Target
		case KindService:
			service, err := loadService(pipe.Target)
			if err != nil {
				return LogicalPath{}, fmt.Errorf("load %s: %w", pipe.Target, err)
			}
			if err := service.Validate(); err != nil {
				return LogicalPath{}, err
			}
			path.Terminal = service.Ref
			return path, nil
		default:
			return LogicalPath{}, fmt.Errorf("pipe %s has invalid target %s", pipe.Ref, pipe.Target)
		}
	}
	return LogicalPath{}, fmt.Errorf("pipe path from %s exceeds maximum of %d hops", start, maxHops)
}
