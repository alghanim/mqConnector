//go:build ibmmq

package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ibm-messaging/mq-golang/v5/ibmmq"
)

// IBMConnector is a CGO-backed connector to IBM MQ. Only compiled when the
// `ibmmq` build tag is set.
type IBMConnector struct {
	cfg   Config
	mu    sync.Mutex
	mgr   *ibmmq.MQQueueManager
	queue *ibmmq.MQObject
}

func newIBM(cfg Config) (Connector, error) {
	return &IBMConnector{cfg: cfg}, nil
}

func (c *IBMConnector) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mgr != nil {
		return nil
	}

	cno := ibmmq.NewMQCNO()
	cd := ibmmq.NewMQCD()
	cd.ChannelName = c.cfg.Channel
	cd.ConnectionName = c.cfg.ConnName
	cno.ClientConn = cd

	if c.cfg.Username != "" {
		csp := ibmmq.NewMQCSP()
		csp.UserId = c.cfg.Username
		csp.Password = c.cfg.Password
		cno.SecurityParms = csp
	}

	mgr, err := ibmmq.Connx(c.cfg.QueueManager, cno)
	if err != nil {
		return fmt.Errorf("ibm Connx: %w", err)
	}
	c.mgr = &mgr

	od := ibmmq.NewMQOD()
	od.ObjectName = c.cfg.QueueName
	od.ObjectType = ibmmq.MQOT_Q
	openOpts := ibmmq.MQOO_OUTPUT | ibmmq.MQOO_INPUT_AS_Q_DEF

	q, err := c.mgr.Open(od, openOpts)
	if err != nil {
		_ = c.mgr.Disc()
		c.mgr = nil
		return fmt.Errorf("ibm Open: %w", err)
	}
	c.queue = &q
	return nil
}

func (c *IBMConnector) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	if c.queue != nil {
		if err := c.queue.Close(0); err != nil {
			firstErr = fmt.Errorf("ibm Close queue: %w", err)
		}
		c.queue = nil
	}
	if c.mgr != nil {
		if err := c.mgr.Disc(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ibm Disc: %w", err)
		}
		c.mgr = nil
	}
	return firstErr
}

func (c *IBMConnector) SendMessage(_ context.Context, message []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queue == nil {
		return errors.New("ibm: queue not opened")
	}
	md := ibmmq.NewMQMD()
	pmo := ibmmq.NewMQPMO()
	pmo.Options = ibmmq.MQPMO_NO_SYNCPOINT
	if err := c.queue.Put(md, pmo, message); err != nil {
		return fmt.Errorf("ibm Put: %w", err)
	}
	return nil
}

func (c *IBMConnector) ReceiveMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	queue := c.queue
	bufSize := c.cfg.IBMRecvBuffer
	c.mu.Unlock()

	if queue == nil {
		return nil, errors.New("ibm: queue not opened")
	}
	if bufSize <= 0 {
		bufSize = 4 * 1024 * 1024
	}

	md := ibmmq.NewMQMD()
	gmo := ibmmq.NewMQGMO()
	gmo.Options = ibmmq.MQGMO_WAIT
	gmo.WaitInterval = 1000 // 1s — short so we can re-check ctx

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		buf := make([]byte, bufSize)
		n, err := queue.Get(md, gmo, buf)
		if err != nil {
			mqret, ok := err.(*ibmmq.MQReturn)
			if ok && mqret.MQCC == ibmmq.MQCC_FAILED && mqret.MQRC == ibmmq.MQRC_NO_MSG_AVAILABLE {
				continue // wait interval elapsed; loop and re-check ctx
			}
			return nil, fmt.Errorf("ibm Get: %w", err)
		}
		return buf[:n], nil
	}
}

func (c *IBMConnector) Ping(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mgr == nil {
		return errors.New("ibm: not connected")
	}
	// IBM MQ has no cheap "ping" call. Inq() is on MQObject, not on the
	// queue-manager handle, so we'd need an open queue to actually probe
	// liveness — which is too expensive to run on every pool sweep tick.
	// Settle for a structural check: a non-nil manager handle that we
	// haven't explicitly disconnected. If the broker has died under us
	// the next real Send/Receive will surface the error and the pool
	// will evict the entry on the failed attempt.
	return nil
}
