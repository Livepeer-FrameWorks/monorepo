package wireguard

import "fmt"

type darwinManager struct {
	interfaceName string
}

func newDarwinManager(interfaceName string) Manager {
	return &darwinManager{interfaceName: interfaceName}
}

func (m *darwinManager) Init() error {
	return fmt.Errorf("darwin support not implemented yet")
}

func (m *darwinManager) Apply(cfg Config) error {
	return fmt.Errorf("darwin support not implemented yet")
}

func (m *darwinManager) Close() error {
	return nil
}
