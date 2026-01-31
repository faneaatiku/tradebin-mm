package internal

import "fmt"

func NewInvalidDependenciesErr(name string) error {
	return fmt.Errorf("invalid dependencies for: %s", name)
}
