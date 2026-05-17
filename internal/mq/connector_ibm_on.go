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

	// TLS / mTLS. IBM MQ doesn't speak PEMs directly — it wants a CMS
	// or PKCS#12 "key repository" stem (no extension; the client
	// appends .kdb / .sth / etc. at runtime). The operator pre-builds
	// the repository using `runmqakm` and points TLSCAFile at the
	// stem. TLSCertFile is optional — if set, we use it as the
	// certificate label (Domino-style certificate selection inside the
	// repository). TLSKeyFile is unused on IBM MQ; the private key
	// lives inside the same kdb. TLSInsecureSkipVerify has no IBM MQ
	// equivalent and is ignored with a warning at the API boundary.
	if c.cfg.TLS.Enabled() {
		sco := ibmmq.NewMQSCO()
		sco.KeyRepository = c.cfg.TLS.CAFile // path stem, no .kdb
		if c.cfg.TLS.CertFile != "" {
			sco.CertificateLabel = c.cfg.TLS.CertFile
		}
		cno.SSLConfig = sco
		// Force at least TLS 1.2. ANY_TLS12_OR_HIGHER is the modern
		// IBM-recommended pseudo-spec that lets the client + queue
		// manager negotiate the strongest available suite. Operators
		// can override per channel on the broker side.
		cd.SSLCipherSpec = "ANY_TLS12_OR_HIGHER"
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
	// MQGMO_SYNCPOINT holds the get inside a unit of work that we
	// commit (Cmit) in Commit() or roll back (Back) in Nack(). A
	// crash between Get and Commit causes IBM MQ to roll back the
	// uncommitted get on the queue manager's next sweep — true
	// at-least-once.
	gmo.Options = ibmmq.MQGMO_WAIT | ibmmq.MQGMO_SYNCPOINT
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

// Commit calls MQCMIT on the queue manager, persisting the
// most-recent syncpoint-scoped Get (and any other in-flight work on
// this thread/connection). Called by the executor after a successful
// downstream send or DLQ push.
func (c *IBMConnector) Commit(_ context.Context) error {
	c.mu.Lock()
	mgr := c.mgr
	c.mu.Unlock()
	if mgr == nil {
		return errors.New("ibm: not connected")
	}
	if err := mgr.Cmit(); err != nil {
		return fmt.Errorf("ibm Cmit: %w", err)
	}
	return nil
}

// Nack rolls back the in-flight syncpoint via MQBACK. The queue
// manager will redeliver the rolled-back message on the next Get.
// requeue is implicit: IBM MQ always redelivers a backed-out get
// (configure the queue's backout-threshold / BOQNAME for poison-
// message routing). requeue=false is the same behaviour at this
// layer — we don't have a per-message reject primitive.
func (c *IBMConnector) Nack(_ context.Context, _ bool) error {
	c.mu.Lock()
	mgr := c.mgr
	c.mu.Unlock()
	if mgr == nil {
		return errors.New("ibm: not connected")
	}
	if err := mgr.Back(); err != nil {
		return fmt.Errorf("ibm Back: %w", err)
	}
	return nil
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
