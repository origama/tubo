package cli

import "errors"

func errorWith(msg string) error { return errors.New(msg) }
