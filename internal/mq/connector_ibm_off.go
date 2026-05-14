//go:build !ibmmq

package mq

// newIBM in the non-IBM build returns ErrUnsupported. Build with `-tags ibmmq`
// to include the IBM MQ client (requires CGO + IBM client distribution).
func newIBM(_ Config) (Connector, error) {
	return nil, ErrUnsupported
}
