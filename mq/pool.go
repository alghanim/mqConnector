package mq

import (
	"fmt"
	"sync"
)

// ConnectorPool manages reusable MQ connections keyed by a string ID.
type ConnectorPool struct {
	connectors sync.Map
}

type poolEntry struct {
	connector MQConnector
	mu        sync.Mutex
}

// Pool is the global connector pool singleton.
var Pool = &ConnectorPool{}

// Get returns an existing connected MQConnector or creates and connects a new one.
func (p *ConnectorPool) Get(id string, queueType QueueType, config map[string]string) (MQConnector, error) {
	// Try to load existing
	if val, ok := p.connectors.Load(id); ok {
		entry := val.(*poolEntry)
		return entry.connector, nil
	}

	// Create new
	connector, err := NewMQConnector(queueType, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connector for %s: %v", id, err)
	}

	if err := connector.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect for %s: %v", id, err)
	}

	entry := &poolEntry{connector: connector}
	// Store, but handle race — another goroutine might have stored in the meantime
	actual, loaded := p.connectors.LoadOrStore(id, entry)
	if loaded {
		// Someone else stored first, disconnect our new one and use theirs
		connector.Disconnect()
		return actual.(*poolEntry).connector, nil
	}

	return connector, nil
}

// Release disconnects and removes a connector from the pool.
func (p *ConnectorPool) Release(id string) {
	if val, ok := p.connectors.LoadAndDelete(id); ok {
		entry := val.(*poolEntry)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.connector.Disconnect()
	}
}

// ReleaseAll disconnects and removes all connectors.
func (p *ConnectorPool) ReleaseAll() {
	p.connectors.Range(func(key, value interface{}) bool {
		p.Release(key.(string))
		return true
	})
}
