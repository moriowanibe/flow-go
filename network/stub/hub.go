package stub

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dapperlabs/flow-go/model/flow"
)

// Hub is a value that stores mocked networks in order for them to send events directly
type Hub struct {
	networks map[flow.Identifier]*Network
	Buffer   *Buffer
}

// NewNetworkHub returns a MockHub value with empty network slice
func NewNetworkHub() *Hub {
	return &Hub{
		networks: make(map[flow.Identifier]*Network),
		Buffer:   NewBuffer(),
	}
}

func (hub *Hub) DeliverAll() {
	for _, network := range hub.networks {
		network.DeliverAll(false)
	}

}

func (hub *Hub) Eventually(t *testing.T, condition func() bool) {
	hub.EventuallyUntil(t, condition, time.Second*10, time.Millisecond*500)
}

func (hub *Hub) EventuallyUntil(t *testing.T, condition func() bool, waitFor time.Duration, tick time.Duration) {
	require.Eventually(t, func() bool {
		hub.DeliverAll()
		return condition()
	}, waitFor, tick)
}

// GetNetwork returns the Network by the network ID (or node ID)
func (hub *Hub) GetNetwork(nodeID flow.Identifier) (*Network, bool) {
	net, ok := hub.networks[nodeID]
	return net, ok
}

// Plug stores the reference of the network in the hub object, in order for networks to find
// other network to send events directly
func (hub *Hub) Plug(net *Network) {
	hub.networks[net.GetID()] = net
}

// GetIDs gets all node IDs from the network hub.
func (hub *Hub) GetIDs() []flow.Identifier {
	nodeIDs := make([]flow.Identifier, 0, len(hub.networks))
	for nodeID := range hub.networks {
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs
}
